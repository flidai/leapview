{ pkgs, http }:
let
  policy = builtins.fromJSON (builtins.readFile ./azure-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "azure";
  };
  proofSource = receipts.source;
  command = "${pkgs.python3}/bin/python3 ${proofSource}/scripts/nix_native_azure_receipt.py";
  platform = if pkgs.stdenv.hostPlatform.isAarch64 then "linux/arm64" else "linux/amd64";
  retain =
    name: package:
    package.overrideAttrs (old: {
      pname = "leapview-azure-${name}";
      src = package.src;
      env = (old.env or { }) // {
        NIX_CFLAGS_COMPILE = (old.env.NIX_CFLAGS_COMPILE or "") + " -fPIC";
      };
      postPatch = (old.postPatch or "") + ''
        mkdir -p "$TMPDIR/azure-evidence"
        ${command} source --policy ${proofSource}/nix/azure-source-lock.json --library ${name} --root "$PWD" \
          --destination "$TMPDIR/azure-evidence/${name}-source.json"
      '';
      postBuild =
        (old.postBuild or "")
        + ''
          { "$CC" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/azure-evidence/${name}-compiler.txt"
        ''
        + (
          if name == "libxml2" then
            ''
              ./config.status --config > "$TMPDIR/azure-evidence/libxml2-options.txt"
              $CC -E -dM -Iinclude -include include/libxml/xmlversion.h -xc /dev/null > "$TMPDIR/azure-evidence/libxml2-config.txt"
            ''
          else
            ''
              cp CMakeCache.txt "$TMPDIR/azure-evidence/${name}-cache.txt"
              cp compile_commands.json "$TMPDIR/azure-evidence/${name}-commands.json"
            ''
        );
      postFixup = (old.postFixup or "") + ''
        mkdir -p "$out/share/leapview/azure-evidence"
        for file in "$TMPDIR/azure-evidence/"*; do
          base64 --wrap=0 "$file" > "$out/share/leapview/azure-evidence/$(basename "$file").b64"
        done
      '';
    });
  libxml2 = retain "libxml2" (
    (pkgs.libxml2.override {
      enableStatic = true;
      enableShared = false;
      icuSupport = false;
      zlibSupport = false;
      pythonSupport = false;
      enableHttp = false;
    }).overrideAttrs
      (old: {
        dontDisableStatic = true;
        buildPhase = ''
          runHook preBuild
          set -o pipefail
          make -j"$NIX_BUILD_CORES" V=1 SHELL="$SHELL" 2>&1 | tee "$TMPDIR/azure-evidence/libxml2-build.txt"
          runHook postBuild
        '';
        checkPhase = ''
          runHook preCheck
          set -o pipefail
          make -j"$NIX_BUILD_CORES" check SHELL="$SHELL"
          printf 'libxml2 upstream checks passed\n' > "$TMPDIR/azure-evidence/libxml2-checks.txt"
          runHook postCheck
        '';
        configureFlags = (old.configureFlags or [ ]) ++ [
          "--with-pic"
          "--without-modules"
        ];
      })
  );
  sdkFlags = pkgs.lib.mapAttrsToList pkgs.lib.cmakeBool policy.sdkFeatures ++ [
    (pkgs.lib.cmakeFeature "CURL_LIBRARY_RELEASE" "${http.archives}/lib/libcurl.a")
    (pkgs.lib.cmakeFeature "CURL_INCLUDE_DIR" "${http.libraries.curl.dev}/include")
    (pkgs.lib.cmakeBool "CURL_USE_STATIC_LIBS" true)
    (pkgs.lib.cmakeFeature "OPENSSL_SSL_LIBRARY" "${http.archives}/lib/libssl.a")
    (pkgs.lib.cmakeFeature "OPENSSL_CRYPTO_LIBRARY" "${http.archives}/lib/libcrypto.a")
    (pkgs.lib.cmakeFeature "OPENSSL_INCLUDE_DIR" "${http.libraries.openssl.dev}/include")
    (pkgs.lib.cmakeBool "OPENSSL_USE_STATIC_LIBS" true)
    (pkgs.lib.cmakeFeature "LIBXML2_LIBRARY" "${libxml2.out}/lib/libxml2.a")
    (pkgs.lib.cmakeFeature "LIBXML2_INCLUDE_DIR" "${libxml2.dev}/include/libxml2")
    (pkgs.lib.cmakeBool "WARNINGS_AS_ERRORS" false)
  ];
  sdk =
    name: package:
    retain name (
      package.overrideAttrs (old: {
        cmakeFlags = sdkFlags;
        patches =
          (old.patches or [ ])
          ++ pkgs.lib.optional (builtins.hasAttr name policy.cacheRetentionPatches) (
            ./. + "/azure-${name}-retain-fetch-option.patch"
          );
      })
    );
  core = sdk "core" (
    (pkgs.azure-sdk-for-cpp.core.override {
      curl = http.libraries.curl;
      inherit libxml2;
    }).overrideAttrs
      (old: {
        patches = (old.patches or [ ]) ++ [ ./azure-core-selected-curl.patch ];
      })
  );
  identity = sdk "identity" (
    pkgs.azure-sdk-for-cpp.identity.override {
      inherit core;
      openssl = http.libraries.openssl;
    }
  );
  common = sdk "storage-common" (
    pkgs.azure-sdk-for-cpp.storage-common.override {
      inherit core libxml2;
      openssl = http.libraries.openssl;
    }
  );
  blobs = sdk "storage-blobs" (
    pkgs.azure-sdk-for-cpp.storage-blobs.override { storage-common = common; }
  );
  datalake = sdk "storage-files-datalake" (
    pkgs.azure-sdk-for-cpp.storage-files-datalake.override {
      storage-common = common;
      storage-blobs = blobs;
    }
  );
  libraries = {
    inherit core identity libxml2;
    storage-common = common;
    storage-blobs = blobs;
    storage-files-datalake = datalake;
  };
  originals = {
    inherit (pkgs) libxml2;
    inherit (pkgs.azure-sdk-for-cpp)
      core
      identity
      storage-common
      storage-blobs
      storage-files-datalake
      ;
  };
  checked = pkgs.lib.all (
    name:
    let
      expected = policy.libraries.${name};
      actual = originals.${name};
    in
    actual.version == expected.version
    && actual.src.outputHash == expected.sourceNARHash
    && map (p: builtins.hashFile "sha256" p) (actual.patches or [ ]) == expected.patchSHA256
  ) (builtins.attrNames libraries);
  archives =
    pkgs.runCommand "leapview-azure-static-archives"
      {
        nativeBuildInputs = [
          pkgs.stdenv.cc
          pkgs.binutils
        ];
      }
      ''
        mkdir -p "$out/lib" "$out/share/leapview/azure-evidence" "$TMPDIR/evidence"
        ${pkgs.lib.concatMapStringsSep "\n" (name: ''
          cp ${libraries.${name}.out}/lib/*.a "$out/lib/"
          for file in ${libraries.${name}.out}/share/leapview/azure-evidence/*.b64; do
            base64 --decode "$file" > "$TMPDIR/evidence/$(basename "$file" .b64)"
          done
        '') (builtins.attrNames libraries)}
        ${pkgs.python3}/bin/python3 - ${http.archives} ${libxml2.out}/lib/libxml2.a "$TMPDIR/evidence/native-link.json" <<'PYCODE'
        import hashlib,json,pathlib,sys
        paths=list((pathlib.Path(sys.argv[1])/'lib').glob('*.a'))+[pathlib.Path(sys.argv[2])]
        pathlib.Path(sys.argv[3]).write_text(json.dumps({'lib/'+p.name:{'archive':str(p),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in paths},sort_keys=True)+'\n')
        PYCODE
        $CXX -std=c++17 -Wall -Wextra -Werror \
          ${
            pkgs.lib.concatMapStringsSep " " (name: "-I${libraries.${name}.dev}/include") (
              builtins.attrNames libraries
            )
          } \
          ${./check-azure-static-libraries.cpp} -Wl,--start-group "$out/lib/"*.a ${http.archives}/lib/*.a -Wl,--end-group \
          -lpthread -ldl -lm -o "$TMPDIR/azure-library-consumer"
        "$TMPDIR/azure-library-consumer" > "$TMPDIR/evidence/consumer.txt"
        readelf -d "$TMPDIR/azure-library-consumer" > "$TMPDIR/evidence/consumer-needed.txt"
        ${command} libraries --policy ${proofSource}/nix/azure-source-lock.json --platform ${platform} \
          --root "$out" --evidence "$TMPDIR/evidence" --destination "$TMPDIR/azure-library-proof.json"
        ${receipts.command} component --repo ${proofSource} --platform ${platform} --component azure \
          --evidence "$TMPDIR/evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
        for file in "$TMPDIR/evidence/"* "$TMPDIR/azure-library-proof.json"; do
          base64 --wrap=0 "$file" > "$out/share/leapview/azure-evidence/$(basename "$file").b64"
        done
      '';

in
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./azure-source-lock.json) (
  builtins.readFile ../internal/extension/builtin.go
);
assert checked;
assert pkgs.lib.all (
  name:
  builtins.hashFile "sha256" (./. + "/azure-${name}-retain-fetch-option.patch")
  == policy.cacheRetentionPatches.${name}
) (builtins.attrNames policy.cacheRetentionPatches);
assert
  builtins.hashFile "sha256" ./azure-core-selected-curl.patch == policy.coreDependencyPatchSHA256;
assert builtins.hashFile "sha256" ./azure-static-dependencies.patch == policy.wrapper.patchSHA256;
{
  inherit archives libraries platform;
  inherit (policy.wrapper) revision;
  source = pkgs.applyPatches {
    name = "leapview-azure-source";
    patches = [ ./azure-static-dependencies.patch ];
    postPatch = ''
      ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/azure_extension.cpp src/include/azure_extension.hpp \
        ${pkgs.stdenv.cc}/bin/c++ AzureExtension EXT_VERSION_AZURE ${policy.wrapper.revision}
    '';
    src = pkgs.fetchFromGitHub {
      owner = "duckdb";
      repo = "duckdb-azure";
      rev = policy.wrapper.revision;
      hash = policy.wrapper.sourceNARHash;
    };
  };
}
