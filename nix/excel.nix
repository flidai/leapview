{ pkgs, http }:
let
  policy = builtins.fromJSON (builtins.readFile ./excel-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "excel";
  };
  proofSource = receipts.source;
  command = "${pkgs.python3}/bin/python3 ${proofSource}/scripts/nix_native_excel_receipt.py";
  platform = if pkgs.stdenv.hostPlatform.isAarch64 then "linux/arm64" else "linux/amd64";
  retain =
    name: package:
    package.overrideAttrs (old: {
      pname = "leapview-excel-${name}";
      # Keep finalAttrs-based source URLs bound to the original package name.
      src = package.src;
      env = (old.env or { }) // {
        NIX_CFLAGS_COMPILE = (old.env.NIX_CFLAGS_COMPILE or "") + " -fPIC";
      };
      postPatch = (old.postPatch or "") + ''
        mkdir -p "$TMPDIR/excel-evidence"
        ${command} source --repo ${proofSource} --library ${name} --output-root "$PWD" \
          --destination "$TMPDIR/excel-evidence/${name}-source.json"
      '';
      postBuild =
        (old.postBuild or "")
        + ''
          { "$CC" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/excel-evidence/${name}-compiler.txt"
        ''
        + {
          expat = ''
            cp expat_config.h "$TMPDIR/excel-evidence/expat-config.txt"
            ./config.status --config > "$TMPDIR/excel-evidence/expat-options.txt"
          '';
          minizip = ''
            cp CMakeCache.txt "$TMPDIR/excel-evidence/minizip-cache.txt"
            cp compile_commands.json "$TMPDIR/excel-evidence/minizip-commands.json"
          '';
        }
        .${name};
      postFixup = (old.postFixup or "") + ''
        mkdir -p "$out/share/leapview/excel-evidence"
        for file in "$TMPDIR/excel-evidence/"*; do
          base64 --wrap=0 "$file" > "$out/share/leapview/excel-evidence/$(basename "$file").b64"
        done
      '';
    });
  expat = retain "expat" (
    pkgs.expat.overrideAttrs (old: {
      configureFlags = (old.configureFlags or [ ]) ++ [
        "--enable-static"
        "--disable-shared"
        "--with-pic"
      ];
      dontDisableStatic = true;
      buildPhase = ''
        runHook preBuild
        foundMakefile=1
        set -o pipefail
        make -j"$NIX_BUILD_CORES" V=1 SHELL="$SHELL" 2>&1 | \
          ${pkgs.python3}/bin/python3 ${proofSource}/scripts/nix_native_build_receipt.py capture-cargo --repo ${proofSource} --platform ${platform} \
            --destination "$TMPDIR/excel-evidence/expat-build.txt"
        runHook postBuild
      '';
    })
  );
  minizip = retain "minizip" (
    (pkgs.minizip-ng.override { zlib = http.libraries.zlib; }).overrideAttrs (old: {
      # Upstream Excel requests only zlib; ambient crypto/codec discovery changes
      # that contract and needlessly enlarges the selected native dependency set.
      buildInputs = [ http.libraries.zlib ];
      cmakeFlags = pkgs.lib.mapAttrsToList pkgs.lib.cmakeBool policy.minizipFeatures ++ [
        (pkgs.lib.cmakeBool "CMAKE_EXPORT_COMPILE_COMMANDS" true)
        (pkgs.lib.cmakeBool "MZ_BUILD_TESTS" old.doCheck)
        (pkgs.lib.cmakeBool "MZ_BUILD_UNIT_TESTS" old.doCheck)
        (pkgs.lib.cmakeFeature "MZ_ZLIB_FLAVOR" "zlib")
        (pkgs.lib.cmakeFeature "ZLIB_LIBRARY_RELEASE" "${http.libraries.zlib.out}/lib/libz.a")
        (pkgs.lib.cmakeFeature "ZLIB_INCLUDE_DIR" "${http.libraries.zlib.dev}/include")
      ];
    })
  );
  archives =
    pkgs.runCommand "leapview-excel-static-archives"
      {
        nativeBuildInputs = [
          pkgs.stdenv.cc
          pkgs.binutils
        ];
      }
      ''
        mkdir -p "$out/lib" "$out/share/leapview" "$TMPDIR/evidence"
        cp ${expat.out}/lib/libexpat.a ${minizip}/lib/libminizip-ng.a "$out/lib/"
        for package in ${expat.out} ${minizip}; do
          for file in "$package/share/leapview/excel-evidence/"*.b64; do
            base64 --decode "$file" > "$TMPDIR/evidence/$(basename "$file" .b64)"
          done
        done
        ${pkgs.python3}/bin/python3 - ${http.libraries.zlib.out}/lib/libz.a "$TMPDIR/evidence/zlib-link.json" <<'PY'
        import hashlib, json, pathlib, sys
        archive = pathlib.Path(sys.argv[1])
        pathlib.Path(sys.argv[2]).write_text(json.dumps({'archive': str(archive), 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest()}, sort_keys=True) + '\n')
        PY
        $CC -Wall -Wextra -Werror -I${expat.dev}/include -I${minizip}/include \
          ${./check-excel-static-libraries.c} "$out/lib/libminizip-ng.a" "$out/lib/libexpat.a" \
          ${http.libraries.zlib.out}/lib/libz.a -o "$TMPDIR/excel-library-consumer"
        "$TMPDIR/excel-library-consumer" > "$TMPDIR/evidence/consumer.txt"
        readelf -d "$TMPDIR/excel-library-consumer" > "$TMPDIR/evidence/consumer-needed.txt"
        if grep -E 'NEEDED.*(libexpat|libminizip|libz\.so)' "$TMPDIR/evidence/consumer-needed.txt"; then
          echo 'Excel consumer used dynamic parser/compression libraries' >&2
          exit 1
        fi
        ${command} libraries --repo ${proofSource} --platform ${platform} --evidence "$TMPDIR/evidence" \
          --output-root "$out" --zlib ${http.libraries.zlib.out}/lib/libz.a \
          --destination "$TMPDIR/excel-library-proof.json"
        ${receipts.command} component --repo ${proofSource} --platform ${platform} --component excel \
          --evidence "$TMPDIR/evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
        mkdir -p "$out/share/leapview/excel-evidence"
        for file in "$TMPDIR/evidence/"* "$TMPDIR/excel-library-proof.json"; do
          base64 --wrap=0 "$file" > "$out/share/leapview/excel-evidence/$(basename "$file").b64"
        done
      '';
in
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./excel-source-lock.json) (
  builtins.readFile ../internal/extension/builtin.go
);
assert pkgs.expat.version == policy.libraries.expat.version;
assert pkgs.minizip-ng.version == policy.libraries.minizip.version;
assert pkgs.minizip-ng.src.outputHash == policy.libraries.minizip.sourceNARHash;
assert
  builtins.convertHash {
    hash = pkgs.expat.src.outputHash;
    hashAlgo = "sha256";
    toHashFormat = "base16";
  } == policy.libraries.expat.archiveSHA256;
assert
  map (p: builtins.hashFile "sha256" p) (pkgs.expat.patches or [ ])
  == policy.libraries.expat.patchSHA256;
assert
  map (p: builtins.hashFile "sha256" p) (pkgs.minizip-ng.patches or [ ])
  == policy.libraries.minizip.patchSHA256;
assert builtins.hashFile "sha256" ./excel-static-dependencies.patch == policy.wrapper.patchSHA256;
{
  inherit archives expat minizip;
  inherit (policy.wrapper) revision;
  source = pkgs.applyPatches {
    name = "leapview-excel-source";
    patches = [ ./excel-static-dependencies.patch ];
    postPatch = ''
      ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/excel/excel_extension.cpp src/excel/include/excel_extension.hpp \
        ${pkgs.stdenv.cc}/bin/c++ ExcelExtension EXT_VERSION_EXCEL ${policy.wrapper.revision}
    '';
    src = pkgs.fetchFromGitHub {
      owner = "duckdb";
      repo = "duckdb-excel";
      rev = policy.wrapper.revision;
      hash = policy.wrapper.sourceNARHash;
    };
  };
}
