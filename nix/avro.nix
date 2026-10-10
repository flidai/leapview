{ pkgs, http }:
let
  policy = builtins.fromJSON (builtins.readFile ./avro-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "avro";
  };
  command = "${pkgs.python3}/bin/python3 ${receipts.source}/scripts/nix_native_avro_receipt.py";
  features =
    name:
    pkgs.lib.mapAttrsToList pkgs.lib.cmakeBool policy.cmakeFeatures.${name}
    ++ [
      (pkgs.lib.cmakeBool "CMAKE_EXPORT_COMPILE_COMMANDS" true)
      (pkgs.lib.cmakeFeature "CMAKE_POLICY_VERSION_MINIMUM" "3.10")
    ];
  retain =
    name: package:
    package.overrideAttrs (old: {
      pname = "leapview-avro-${name}";
      src = package.src;
      env = (old.env or { }) // {
        NIX_CFLAGS_COMPILE = (old.env.NIX_CFLAGS_COMPILE or "") + " -fPIC";
      };
      postPatch = (old.postPatch or "") + ''
        mkdir -p "$TMPDIR/avro-evidence"
        ${command} source --repo ${receipts.source} --library ${name} --output-root "$PWD" \
          --destination "$TMPDIR/avro-evidence/${name}-source.json"
      '';
      postBuild =
        (old.postBuild or "")
        + ''
          { "$CC" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/avro-evidence/${name}-compiler.txt"
        ''
        + (
          if name == "xz" then
            ''
              ./config.status --config > "$TMPDIR/avro-evidence/xz-options.txt"
            ''
          else
            ''
              cp CMakeCache.txt "$TMPDIR/avro-evidence/${name}-cache.txt"
              cp compile_commands.json "$TMPDIR/avro-evidence/${name}-commands.json"
            ''
        )
        + pkgs.lib.optionalString (name == "avro") ''
          cp src/CMakeFiles/avrocat.dir/link.txt "$TMPDIR/avro-evidence/avro-link.txt"
        '';
      postCheck =
        (old.postCheck or "")
        + pkgs.lib.optionalString (name != "snappy") ''
          echo '${name} upstream tests passed' > "$TMPDIR/avro-evidence/${name}-checks.txt"
        '';
      postFixup = (old.postFixup or "") + ''
        mkdir -p "$out/share/leapview/avro-evidence"
        for file in "$TMPDIR/avro-evidence/"*; do
          base64 --wrap=0 "$file" > "$out/share/leapview/avro-evidence/$(basename "$file").b64"
        done
      '';
    });
  jansson = retain "jansson" (
    pkgs.jansson.overrideAttrs (_: {
      cmakeFlags = features "jansson";
      doCheck = true;
    })
  );
  snappy = retain "snappy" (
    (pkgs.snappy.override { static = true; }).overrideAttrs (_: {
      cmakeFlags = features "snappy";
    })
  );
  xz = retain "xz" (
    (pkgs.xz.override { enableStatic = true; }).overrideAttrs (old: {
      dontDisableStatic = true;
      configureFlags = (old.configureFlags or [ ]) ++ [
        "--enable-static"
        "--with-pic"
      ];
      buildPhase = ''
        runHook preBuild
        foundMakefile=1
        set -o pipefail
        make -j"$NIX_BUILD_CORES" V=1 SHELL="$SHELL" 2>&1 | \
          ${pkgs.python3}/bin/python3 ${receipts.source}/scripts/nix_native_build_receipt.py capture-cargo \
            --repo ${receipts.source} --platform ${receipts.platform} --destination "$TMPDIR/avro-evidence/xz-build.txt"
        runHook postBuild
      '';
    })
  );
  avro = retain "avro" (
    pkgs.stdenv.mkDerivation {
      pname = "duckdb-avro-c";
      version = policy.libraries.avro.version;
      src = pkgs.fetchFromGitHub {
        owner = "duckdb";
        repo = "duckdb-avro-c";
        rev = policy.libraries.avro.revision;
        hash = policy.libraries.avro.sourceNARHash;
      };
      patches = [
        ./avro-c-static-only.patch
        ./avro-c-available-tests.patch
      ];
      postPatch = "patchShebangs lang/c/version.sh\n";
      nativeBuildInputs = [ pkgs.cmake ];
      buildInputs = [
        jansson
        snappy
        xz
        http.libraries.zlib
      ];
      cmakeDir = "../lang/c";
      cmakeFlags = features "avro" ++ [
        # This fork uses pre-C23 unspecified callback parameter lists.
        (pkgs.lib.cmakeFeature "CMAKE_C_STANDARD" (toString policy.avroCStandard))
        (pkgs.lib.cmakeFeature "ZLIB_LIBRARY_RELEASE" "${http.libraries.zlib.out}/lib/libz.a")
        (pkgs.lib.cmakeFeature "ZLIB_INCLUDE_DIR" "${http.libraries.zlib.dev}/include")
        (pkgs.lib.cmakeFeature "LIBLZMA_LIBRARY_RELEASE" "${xz.out}/lib/liblzma.a")
        (pkgs.lib.cmakeFeature "LIBLZMA_INCLUDE_DIR" "${xz.dev}/include")
        (pkgs.lib.cmakeFeature "jansson_DIR" "${jansson.dev}/lib/cmake/jansson")
        (pkgs.lib.cmakeFeature "Snappy_DIR" "${snappy.dev}/lib/cmake/Snappy")
      ];
      doCheck = true;
      strictDeps = true;
    }
  );
  archives =
    pkgs.runCommand "leapview-avro-static-archives"
      {
        nativeBuildInputs = [
          pkgs.stdenv.cc
          pkgs.binutils
        ];
      }
      ''
        mkdir -p "$out/lib" "$out/share/leapview" "$TMPDIR/evidence"
        cp ${avro}/lib/libavro.a ${jansson.out}/lib/libjansson.a ${snappy.out}/lib/libsnappy.a ${xz.out}/lib/liblzma.a "$out/lib/"
        for package in ${jansson.out} ${snappy.out} ${xz.out} ${avro}; do
          for file in "$package/share/leapview/avro-evidence/"*.b64; do
            base64 --decode "$file" > "$TMPDIR/evidence/$(basename "$file" .b64)"
          done
        done
        cat "$TMPDIR/evidence/jansson-checks.txt" "$TMPDIR/evidence/xz-checks.txt" "$TMPDIR/evidence/avro-checks.txt" > "$TMPDIR/evidence/checks.txt"
        rm "$TMPDIR/evidence/"*-checks.txt
        ${pkgs.python3}/bin/python3 - "$TMPDIR/evidence/library-link.json" \
          ${avro}/lib/libavro.a ${jansson.out}/lib/libjansson.a ${snappy.out}/lib/libsnappy.a \
          ${xz.out}/lib/liblzma.a ${http.libraries.zlib.out}/lib/libz.a <<'PY'
        import hashlib, json, pathlib, sys
        pathlib.Path(sys.argv[1]).write_text(json.dumps({'lib/' + pathlib.Path(p).name: {'archive': p, 'sha256': hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()} for p in sys.argv[2:]}, sort_keys=True) + '\n')
        PY
        $CC -Wall -Wextra -Werror -I${avro}/include ${./check-avro-static-libraries.c} \
          "$out/lib/libavro.a" "$out/lib/libjansson.a" "$out/lib/libsnappy.a" "$out/lib/liblzma.a" \
          ${http.libraries.zlib.out}/lib/libz.a -lstdc++ -lm -lpthread -o "$TMPDIR/avro-library-consumer"
        cd "$TMPDIR"
        ./avro-library-consumer > "$TMPDIR/evidence/consumer.txt"
        readelf -d ./avro-library-consumer > "$TMPDIR/evidence/consumer-needed.txt"
        ${command} libraries --repo ${receipts.source} --platform ${receipts.platform} --evidence "$TMPDIR/evidence" \
          --output-root "$out" --zlib ${http.libraries.zlib.out}/lib/libz.a --destination "$TMPDIR/avro-library-proof.json"
        ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} --component avro \
          --evidence "$TMPDIR/evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
        mkdir -p "$out/share/leapview/avro-evidence"
        for file in "$TMPDIR/evidence/"* "$TMPDIR/avro-library-proof.json"; do
          base64 --wrap=0 "$file" > "$out/share/leapview/avro-evidence/$(basename "$file").b64"
        done
      '';
in
assert builtins.hashFile "sha256" ./avro-static-dependencies.patch == policy.wrapper.patchSHA256;
assert builtins.hashFile "sha256" ./avro-c-static-only.patch == policy.libraries.avro.patchSHA256;
assert
  builtins.hashFile "sha256" ./avro-c-available-tests.patch == policy.libraries.avro.testPatchSHA256;
assert builtins.all
  (
    name:
    pkgs.${name}.version == policy.libraries.${name}.version
    && pkgs.${name}.src.outputHash == policy.libraries.${name}.sourceHash
    &&
      map (p: builtins.hashFile "sha256" p) (pkgs.${name}.patches or [ ])
      == policy.libraries.${name}.patchSHA256
  )
  [
    "jansson"
    "snappy"
    "xz"
  ];
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./avro-source-lock.json) (
  builtins.readFile ../internal/extension/builtin.go
);
{
  inherit
    archives
    avro
    jansson
    snappy
    xz
    ;
  inherit (policy.wrapper) revision;
  source = pkgs.applyPatches {
    name = "leapview-avro-source";
    src = pkgs.fetchFromGitHub {
      owner = "duckdb";
      repo = "duckdb-avro";
      rev = policy.wrapper.revision;
      hash = policy.wrapper.sourceNARHash;
    };
    patches = [ ./avro-static-dependencies.patch ];
    postPatch = ''
      ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/avro_extension.cpp src/include/avro_extension.hpp \
        ${pkgs.stdenv.cc}/bin/c++ AvroExtension EXT_VERSION_AVRO ${policy.wrapper.revision}
    '';
  };
}
