{ pkgs }:
let
  policy = builtins.fromJSON (builtins.readFile ./http-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "http";
  };
  registry = builtins.readFile ../internal/extension/builtin.go;
  # Each selected library keeps actual source, compiler, configuration and
  # successful make output before its installed archives enter the link group.
  retain =
    name: package:
    package.overrideAttrs (old: {
      pname = "leapview-http-${name}";
      env = (old.env or { }) // {
        NIX_CFLAGS_COMPILE = (old.env.NIX_CFLAGS_COMPILE or "") + " -fPIC";
      };
      postPatch = (old.postPatch or "") + ''
        mkdir -p "$TMPDIR/http-evidence"
        ${receipts.command} http-source --repo ${receipts.source} --platform ${receipts.platform} \
          --library ${name} --output-root "$PWD" --destination "$TMPDIR/http-evidence/${name}-source.json"
      '';
      buildPhase = ''
        runHook preBuild
        # stdenv's checkPhase uses this marker; instrumentation must not skip
        # the upstream OpenSSL, zlib and nghttp2 check targets.
        foundMakefile=1
        set -o pipefail
        local httpMakeFlags=(-j"$NIX_BUILD_CORES" V=1 SHELL="$SHELL")
        concatTo httpMakeFlags makeFlags makeFlagsArray buildFlags buildFlagsArray
        make "''${httpMakeFlags[@]}" 2>&1 | \
          ${receipts.command} capture-cargo --repo ${receipts.source} --platform ${receipts.platform} \
            --destination "$TMPDIR/http-evidence/${name}-build.txt"
        runHook postBuild
      '';
      postBuild =
        (old.postBuild or "")
        + ''
          { "$CC" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/http-evidence/${name}-compiler.txt"
        ''
        + {
          curl = ''
            cp lib/curl_config.h "$TMPDIR/http-evidence/curl-config.txt"
            ./config.status --config > "$TMPDIR/http-evidence/curl-options.txt"
          '';
          openssl = ''
            perl configdata.pm --dump > "$TMPDIR/http-evidence/openssl-config.txt"
          '';
          nghttp2 = ''
            cp config.h "$TMPDIR/http-evidence/nghttp2-config.txt"
            ./config.status --config > "$TMPDIR/http-evidence/nghttp2-options.txt"
          '';
          zlib = ''cp Makefile "$TMPDIR/http-evidence/zlib-config.txt"'';
        }
        .${name};
      postFixup = (old.postFixup or "") + ''
        mkdir -p "$out/share/leapview/http-evidence"
        for file in "$TMPDIR/http-evidence/"*; do
          base64 --wrap=0 "$file" > "$out/share/leapview/http-evidence/$(basename "$file").b64"
        done
      '';
    });
  openssl = retain "openssl" (pkgs.openssl.override { static = true; });
  zlib = retain "zlib" (
    pkgs.zlib.override {
      shared = false;
      splitStaticOutput = false;
    }
  );
  nghttp2 = retain "nghttp2" (
    (pkgs.nghttp2.override { enableApp = false; }).overrideAttrs (old: {
      configureFlags = old.configureFlags ++ [
        "--enable-static"
        "--disable-shared"
      ];
      dontDisableStatic = true;
    })
  );
  curl = retain "curl" (
    (pkgs.curlMinimal.override (policy.curlFeatures // { inherit openssl zlib nghttp2; })).overrideAttrs
      (old: {
        configureFlags = old.configureFlags ++ [
          "--enable-static"
          "--disable-shared"
          "--with-ca-bundle=/etc/ssl/certs/ca-certificates.crt"
          "--with-ca-path=/etc/ssl/certs"
        ];
        dontDisableStatic = true;
      })
  );
  libraries = {
    inherit
      curl
      openssl
      nghttp2
      zlib
      ;
  };
  unmodified = {
    curl = pkgs.curlMinimal;
    inherit (pkgs) openssl nghttp2 zlib;
  };
  checked = pkgs.lib.all (
    name:
    let
      expected = policy.libraries.${name};
      actual = unmodified.${name};
    in
    actual.version == expected.version
    &&
      builtins.convertHash {
        hash = actual.src.outputHash;
        hashAlgo = "sha256";
        toHashFormat = "base16";
      } == expected.archiveSHA256
    && map (p: builtins.hashFile "sha256" p) (actual.patches or [ ]) == expected.patchSHA256
  ) (builtins.attrNames libraries);
  source =
    name:
    pkgs.applyPatches {
      name = "leapview-${name}-source";
      postPatch = pkgs.lib.optionalString (name == "quack") ''
        ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/quack_extension.cpp src/include/quack_extension.hpp \
          ${pkgs.stdenv.cc}/bin/c++ QuackExtension EXT_VERSION_QUACK ${policy.wrappers.quack.revision}
      '';
      patches = [
        (if name == "httpfs" then ./httpfs-static-dependencies.patch else ./quack-engine-includes.patch)
      ];
      src = pkgs.fetchFromGitHub {
        owner = "duckdb";
        repo = "duckdb-${name}";
        rev = policy.wrappers.${name}.revision;
        hash = policy.wrappers.${name}.sourceNARHash;
      };
    };
  archives = pkgs.runCommand "leapview-http-static-archives" { } ''
    mkdir -p "$out/lib" "$out/share/leapview" "$TMPDIR/evidence"
    cp ${curl.out}/lib/libcurl.a ${openssl.out}/lib/lib{ssl,crypto}.a ${nghttp2.lib}/lib/libnghttp2.a ${zlib.out}/lib/libz.a "$out/lib/"
    ${pkgs.lib.concatMapStringsSep "\n" (p: ''
      for file in ${p.out}/share/leapview/http-evidence/*.b64; do
        base64 --decode "$file" > "$TMPDIR/evidence/$(basename "$file" .b64)"
      done
    '') (builtins.attrValues libraries)}
    ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} \
      --component http --evidence "$TMPDIR/evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
  '';
in
assert checked;
assert
  builtins.hashFile "sha256" ./httpfs-static-dependencies.patch == policy.wrappers.httpfs.patchSHA256;
assert
  builtins.hashFile "sha256" ./quack-engine-includes.patch == policy.wrappers.quack.patchSHA256;
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./http-source-lock.json) registry;
{
  inherit archives libraries;
  httpfs = {
    source = source "httpfs";
    inherit (policy.wrappers.httpfs) revision;
  };
  quack = {
    source = source "quack";
    inherit (policy.wrappers.quack) revision;
  };
}
