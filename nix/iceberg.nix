{ pkgs, http }:
let
  policy = builtins.fromJSON (builtins.readFile ./iceberg-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "iceberg";
  };
  command = "${pkgs.python3}/bin/python3 ${../scripts/nix_native_iceberg_receipt.py}";
  platform = if pkgs.stdenv.hostPlatform.isAarch64 then "linux/arm64" else "linux/amd64";
  names = builtins.attrNames policy.libraries;
  httpPaths = [
    "${http.libraries.curl.out}/lib/libcurl.a"
    "${http.libraries.openssl.out}/lib/libssl.a"
    "${http.libraries.openssl.out}/lib/libcrypto.a"
    "${http.libraries.zlib.out}/lib/libz.a"
    "${http.libraries.nghttp2.lib}/lib/libnghttp2.a"
  ];
  libraries = pkgs.lib.makeScope pkgs.newScope (
    self:
    {
      inherit (http.libraries) openssl curl zlib;
    }
    // builtins.listToAttrs (
      map (name: {
        inherit name;
        value =
          let
            args = pkgs.lib.optionalAttrs (name == "aws-sdk-cpp") {
              apis = policy.sdkAPIs;
              requiredSystemFeatures = [ ];
            };
            path = pkgs.path + "/pkgs/by-name/${builtins.substring 0 2 name}/${name}/package.nix";
            original = self.callPackage path args;
            expected = policy.libraries.${name};
          in
          assert original.version == expected.version;
          assert original.src.outputHash == expected.sourceNARHash;
          assert map (p: builtins.hashFile "sha256" p) (original.patches or [ ]) == expected.patchSHA256;
          original.overrideAttrs (old: {
            pname = "leapview-iceberg-${name}";
            dontDisableStatic = true;
            patches =
              (old.patches or [ ])
              ++ pkgs.lib.optionals (name == "aws-sdk-cpp") [ ./iceberg-selected-curl.patch ];
            cmakeFlags =
              builtins.filter (flag: !(pkgs.lib.hasPrefix "-DBUILD_SHARED_LIBS" flag)) (old.cmakeFlags or [ ])
              ++ pkgs.lib.mapAttrsToList pkgs.lib.cmakeBool policy.features
              ++ pkgs.lib.optionals (name == "s2n-tls" || name == "aws-c-cal" || name == "aws-sdk-cpp") [
                "-DOPENSSL_USE_STATIC_LIBS=TRUE"
                "-DOPENSSL_SSL_LIBRARY=${http.libraries.openssl.out}/lib/libssl.a"
                "-DOPENSSL_CRYPTO_LIBRARY=${http.libraries.openssl.out}/lib/libcrypto.a"
                "-DOPENSSL_INCLUDE_DIR=${http.libraries.openssl.dev}/include"
              ]
              ++ pkgs.lib.optionals (name == "aws-sdk-cpp") [
                "-DICEBERG_HTTP_STATIC_LIBRARIES=${pkgs.lib.concatStringsSep ";" httpPaths}"
                "-DCURL_LIBRARY_RELEASE=${http.libraries.curl.out}/lib/libcurl.a"
                "-DCURL_INCLUDE_DIR=${http.libraries.curl.dev}/include"
                "-DZLIB_LIBRARY=${http.libraries.zlib.out}/lib/libz.a"
                "-DZLIB_INCLUDE_DIR=${http.libraries.zlib.dev}/include"
              ];
            postUnpack = (old.postUnpack or "") + ''
              mkdir -p "$TMPDIR/iceberg-evidence"
              ${command} source --policy ${./iceberg-source-lock.json} --library ${name} --root "$sourceRoot" \
                --destination "$TMPDIR/iceberg-evidence/${name}-source.json"
            '';
            postConfigure = (old.postConfigure or "") + ''
              cp CMakeCache.txt "$TMPDIR/iceberg-evidence/${name}-cache.txt"
              cp compile_commands.json "$TMPDIR/iceberg-evidence/${name}-commands.json"
              { "$CC" --version; "$CXX" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } \
                > "$TMPDIR/iceberg-evidence/${name}-compiler.txt"
            '';
            postFixup = (old.postFixup or "") + ''
              mkdir -p "$out/share/leapview/iceberg-evidence"
              for file in "$TMPDIR/iceberg-evidence/"*; do
                base64 --wrap=0 "$file" > "$out/share/leapview/iceberg-evidence/$(basename "$file").b64"
              done
            '';
          });
      }) names
    )
  );
  archiveNames = pkgs.lib.sort builtins.lessThan (
    pkgs.lib.concatMap (
      name:
      if name == "aws-sdk-cpp" then
        [
          "libaws-cpp-sdk-core.a"
          "libaws-cpp-sdk-sso.a"
          "libaws-cpp-sdk-sts.a"
        ]
      else if name == "s2n-tls" then
        [ "libs2n.a" ]
      else
        [ "lib${name}.a" ]
    ) names
  );
  includeDirectories = map (name: "${pkgs.lib.getDev libraries.${name}}/include") names;
  archives =
    pkgs.runCommand "leapview-iceberg-static-archives"
      {
        nativeBuildInputs = [
          pkgs.stdenv.cc
          pkgs.binutils
        ];
      }
      ''
        mkdir -p "$out/lib" "$out/share/leapview/iceberg-evidence" "$TMPDIR/evidence"
        ${pkgs.lib.concatMapStringsSep "\n" (name: ''
          cp ${libraries.${name}.out}/lib/*.a "$out/lib/"
          for file in ${libraries.${name}.out}/share/leapview/iceberg-evidence/*.b64; do
            base64 --decode "$file" > "$TMPDIR/evidence/$(basename "$file" .b64)"
          done
        '') names}
        ${pkgs.python3}/bin/python3 - "$TMPDIR/evidence/http-link.json" ${pkgs.lib.concatStringsSep " " httpPaths} <<'PY'
        import hashlib,json,pathlib,sys
        paths=map(pathlib.Path,sys.argv[2:])
        pathlib.Path(sys.argv[1]).write_text(json.dumps({'lib/'+p.name:{'archive':str(p),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in paths},sort_keys=True)+'\n')
        PY
        $CXX -std=c++17 -Wall -Wextra -Werror ${
          pkgs.lib.concatMapStringsSep " " (path: "-I${path}") includeDirectories
        } \
          ${./check-iceberg-static-libraries.cpp} -Wl,--start-group "$out/lib/"*.a \
          ${pkgs.lib.concatStringsSep " " httpPaths} -Wl,--end-group -lpthread -ldl -lm -o "$TMPDIR/consumer"
        AWS_EC2_METADATA_DISABLED=true AWS_CONFIG_FILE=/nonexistent AWS_SHARED_CREDENTIALS_FILE=/nonexistent \
          "$TMPDIR/consumer" > "$TMPDIR/evidence/consumer.txt"
        readelf -d "$TMPDIR/consumer" > "$TMPDIR/evidence/consumer-needed.txt"
        ${command} libraries --policy ${./iceberg-source-lock.json} --platform ${platform} \
          --root "$out" --evidence "$TMPDIR/evidence" --destination "$TMPDIR/library-proof.json"
        ${receipts.command} component --repo ${receipts.source} --platform ${platform} --component iceberg \
          --evidence "$TMPDIR/evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
        for file in "$TMPDIR/evidence/"* "$TMPDIR/library-proof.json"; do
          base64 --wrap=0 "$file" > "$out/share/leapview/iceberg-evidence/$(basename "$file").b64"
        done
      '';
  source = pkgs.applyPatches {
    name = "leapview-iceberg-source";
    src = pkgs.fetchFromGitHub {
      owner = "duckdb";
      repo = "duckdb-iceberg";
      rev = policy.wrapper.revision;
      hash = policy.wrapper.sourceNARHash;
    };
    patches = [ ./iceberg-static-dependencies.patch ];
    postPatch = ''
      ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/iceberg_extension.cpp src/include/iceberg_extension.hpp \
        ${pkgs.stdenv.cc}/bin/c++ IcebergExtension EXT_VERSION_ICEBERG ${policy.wrapper.revision}
    '';
  };
in
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./iceberg-source-lock.json) (
  builtins.readFile ../internal/extension/builtin.go
);
assert builtins.hashFile "sha256" ./iceberg-selected-curl.patch == policy.sdkDependencyPatchSHA256;
assert builtins.hashFile "sha256" ./iceberg-static-dependencies.patch == policy.wrapper.patchSHA256;
{
  inherit
    libraries
    includeDirectories
    archiveNames
    archives
    source
    platform
    ;
  inherit (policy.wrapper) revision;
}
