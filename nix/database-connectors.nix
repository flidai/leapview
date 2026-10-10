{ pkgs, http }:
let
  policy = builtins.fromJSON (builtins.readFile ./database-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "database";
  };
  registry = builtins.readFile ../internal/extension/builtin.go;
  captureSource = name: ''
    mkdir -p "$TMPDIR/database-evidence"
    ${receipts.command} database-source --repo ${receipts.source} --platform ${receipts.platform} \
      --library ${name} --output-root "$PWD" --destination "$TMPDIR/database-evidence/${name}-source.json"
  '';
  compiler = name: ''
    { "$CC" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/database-evidence/${name}-compiler.txt"
  '';
  retain = ''
    mkdir -p "$out/share/leapview/database-evidence"
    for file in "$TMPDIR/database-evidence/"*; do
      base64 --wrap=0 "$file" > "$out/share/leapview/database-evidence/$(basename "$file").b64"
    done
  '';
  libpq =
    (pkgs.libpq.override {
      inherit (http.libraries) openssl zlib;
      # The wrapper supplies its bearer-token hook. Automatic OAuth acquisition
      # and Kerberos are optional upstream profiles, not selected dependencies.
      curlSupport = false;
      gssSupport = false;
      nlsSupport = false;
    }).overrideAttrs
      (old: {
        pname = "leapview-static-libpq";
        # Preserve the upstream process-exit check while allowing exactly the
        # pthread_exit symbol contributed by the selected static OpenSSL.
        patches = old.patches ++ [ ./libpq-static-openssl-refs.patch ];
        dontDisableStatic = true;
        env = old.env // {
          NIX_CFLAGS_COMPILE = old.env.NIX_CFLAGS_COMPILE + " -fPIC";
        };
        configureFlags = old.configureFlags ++ [
          "--without-libcurl"
          "--without-gssapi"
          "--without-lz4"
          "--without-zstd"
        ];
        postPatch = old.postPatch + captureSource "libpq";
        buildPhase = ''
              runHook preBuild
              foundMakefile=1
              set -o pipefail
          if ! make submake-libpgport submake-libpq 2>&1 | \
            ${receipts.command} capture-cargo --repo ${receipts.source} --platform ${receipts.platform} \
              --destination "$TMPDIR/database-evidence/libpq-build.txt"; then
            tail -n 80 "$TMPDIR/database-evidence/libpq-build.txt" >&2
            exit 1
          fi
              runHook postBuild
        '';
        postBuild =
          (old.postBuild or "")
          + compiler "libpq"
          + ''
            cp src/include/pg_config.h "$TMPDIR/database-evidence/libpq-config.txt"
            ./config.status --config > "$TMPDIR/database-evidence/libpq-options.txt"
          '';
        postFixup = (old.postFixup or "") + retain;
      });
  mariadb = pkgs.stdenv.mkDerivation {
    pname = "leapview-static-mariadb";
    inherit (policy.libraries.mariadb) version;
    src = pkgs.fetchFromGitHub {
      owner = "mariadb-corporation";
      repo = "mariadb-connector-c";
      rev = policy.libraries.mariadb.revision;
      hash = policy.libraries.mariadb.sourceNARHash;
    };
    outputs = [
      "out"
      "dev"
    ];
    nativeBuildInputs = [ pkgs.cmake ];
    buildInputs = [
      http.libraries.openssl
      http.libraries.zlib
    ];
    postPatch = captureSource "mariadb";
    cmakeFlags = [
      "-DWITH_SSL=OPENSSL"
      "-DWITH_EXTERNAL_ZLIB=ON"
      "-DWITH_CURL=OFF"
      "-DWITH_ICONV=OFF"
      "-DWITH_UNIT_TESTS=OFF"
      "-DCMAKE_EXPORT_COMPILE_COMMANDS=ON"
      "-DAUTH_GSSAPI_PLUGIN_TYPE=OFF"
      "-DREMOTEIO_PLUGIN_TYPE=OFF"
      "-DZLIB_LIBRARY=${http.libraries.zlib.out}/lib/libz.a"
      "-DZLIB_INCLUDE_DIR=${http.libraries.zlib.dev}/include"
      "-DOPENSSL_SSL_LIBRARY=${http.libraries.openssl.out}/lib/libssl.a"
      "-DOPENSSL_CRYPTO_LIBRARY=${http.libraries.openssl.out}/lib/libcrypto.a"
      "-DOPENSSL_INCLUDE_DIR=${http.libraries.openssl.dev}/include"
    ]
    ++ pkgs.lib.mapAttrsToList (name: value: "-DCLIENT_PLUGIN_${name}=${value}") policy.mariadbPlugins;
    # Build only the real upstream static target, including its selected plugin
    # objects; no renamed shared object or dynamic-plugin runtime dependency.
    buildFlags = [ "mariadbclient" ];
    enableParallelBuilding = true;
    postBuild = compiler "mariadb" + ''
      cp CMakeCache.txt "$TMPDIR/database-evidence/mariadb-cache.txt"
      cp compile_commands.json "$TMPDIR/database-evidence/mariadb-commands.json"
      cp libmariadb/ma_client_plugin.c "$TMPDIR/database-evidence/mariadb-plugins.c"
    '';
    doCheck = true;
    checkPhase = ''
      runHook preCheck
      "$CC" -I../include -Iinclude ${./check-mariadb-static-auth.c} libmariadb/libmariadbclient.a \
        ${http.libraries.openssl.out}/lib/libssl.a ${http.libraries.openssl.out}/lib/libcrypto.a \
        ${http.libraries.zlib.out}/lib/libz.a -ldl -lm -lpthread -o auth-probe
      ./auth-probe
      runHook postCheck
    '';
    installPhase = ''
      runHook preInstall
      mkdir -p "$out/lib" "$dev/include/mariadb"
      cp libmariadb/libmariadbclient.a "$out/lib/"
      cp -R ../include/. "$dev/include/mariadb/"
      cp include/*.h "$dev/include/mariadb/"
      runHook postInstall
    '';
    postFixup = retain;
  };
  connector = pkgs.fetchFromGitHub {
    owner = "duckdb";
    repo = "database-connector";
    rev = policy.databaseConnector.revision;
    hash = policy.databaseConnector.sourceNARHash;
  };
  source =
    name:
    pkgs.applyPatches {
      name = "leapview-${name}-source";
      src = pkgs.fetchFromGitHub {
        owner = "duckdb";
        repo = "duckdb-${name}";
        rev = policy.wrappers.${name}.revision;
        hash = policy.wrappers.${name}.sourceNARHash;
      };
      patches = [
        (
          if name == "postgres" then
            ./postgres-static-dependencies.patch
          else
            ./mysql-static-dependencies.patch
        )
      ];
      postPatch = ''
        test -z "$(ls -A database-connector)"
        cp -R ${connector}/. database-connector/
        ${pkgs.python3}/bin/python3 ${./check-extension-version.py} src/${name}_extension.cpp src/include/${name}_scanner_extension.hpp \
          ${pkgs.stdenv.cc}/bin/c++ ${if name == "postgres" then "Postgres" else "Mysql"}ScannerExtension \
          EXT_VERSION_${pkgs.lib.toUpper name}_SCANNER ${policy.wrappers.${name}.revision}
      '';
    };
  archives = pkgs.runCommand "leapview-database-static-archives" { } ''
    mkdir -p "$out/lib" "$out/share/leapview" "$TMPDIR/evidence"
    cp ${libpq.dev}/lib/lib{pq,pgcommon,pgport}.a ${mariadb.out}/lib/libmariadbclient.a "$out/lib/"
    for directory in ${libpq.out} ${mariadb.out}; do
      for file in "$directory/share/leapview/database-evidence/"*.b64; do
        base64 --decode "$file" > "$TMPDIR/evidence/$(basename "$file" .b64)"
      done
    done
    ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} \
      --component database --evidence "$TMPDIR/evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
  '';
in
assert pkgs.libpq.version == policy.libraries.libpq.version;
assert pkgs.libpq.src.outputHash == policy.libraries.libpq.sourceNARHash;
assert
  map (p: builtins.hashFile "sha256" p) pkgs.libpq.patches == policy.libraries.libpq.patchSHA256;
assert
  builtins.hashFile "sha256" ./libpq-static-openssl-refs.patch
  == policy.libraries.libpq.staticOpenSSLRefsPatchSHA256;
assert
  builtins.hashFile "sha256" ./postgres-static-dependencies.patch
  == policy.wrappers.postgres.patchSHA256;
assert
  builtins.hashFile "sha256" ./mysql-static-dependencies.patch == policy.wrappers.mysql.patchSHA256;
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./database-source-lock.json) registry;
{
  inherit libpq mariadb archives;
  postgres = {
    source = source "postgres";
    inherit (policy.wrappers.postgres) revision;
  };
  mysql = {
    source = source "mysql";
    inherit (policy.wrappers.mysql) revision;
  };
}
