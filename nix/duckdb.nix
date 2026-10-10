{ pkgs }:
let
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "duckdb";
  };
  lance = import ./lance.nix { inherit pkgs; };
  sqlite = import ./sqlite.nix { inherit pkgs; };
  http = import ./http.nix { inherit pkgs; };
  database = import ./database-connectors.nix { inherit pkgs http; };
  excel = import ./excel.nix { inherit pkgs http; };
  avro = import ./avro.nix { inherit pkgs http; };
  delta = import ./delta.nix { inherit pkgs; };
  azure = import ./azure.nix { inherit pkgs http; };
  ducklake = import ./ducklake.nix { inherit pkgs; };
  revision = "08e34c447bae34eaee3723cac61f2878b6bdf787";
  registry = builtins.readFile ../internal/extension/builtin.go;
  source = pkgs.fetchFromGitHub {
    owner = "duckdb";
    repo = "duckdb";
    rev = revision;
    hash = "sha256-6xpKZKfH5/nwE2nU5kcpgITKFm3ilb1PYf9QEk+bKoM=";
  };
  extensions = pkgs.writeText "leapview-duckdb-extensions.cmake" ''
    duckdb_extension_load(postgres_scanner SOURCE_DIR ${database.postgres.source} EXTENSION_VERSION ${database.postgres.revision})
    duckdb_extension_load(mysql_scanner SOURCE_DIR ${database.mysql.source} EXTENSION_VERSION ${database.mysql.revision})
    duckdb_extension_load(httpfs SOURCE_DIR ${http.httpfs.source} EXTENSION_VERSION ${http.httpfs.revision})
    duckdb_extension_load(quack SOURCE_DIR ${http.quack.source} EXTENSION_VERSION ${http.quack.revision})
    duckdb_extension_load(excel SOURCE_DIR ${excel.source} INCLUDE_DIR ${excel.source}/src/excel/include EXTENSION_VERSION ${excel.revision})
    duckdb_extension_load(delta SOURCE_DIR ${delta.source} EXTENSION_VERSION ${delta.revision})
    duckdb_extension_load(avro SOURCE_DIR ${avro.source} EXTENSION_VERSION ${avro.revision})
    duckdb_extension_load(azure SOURCE_DIR ${azure.source} EXTENSION_VERSION ${azure.revision})
    duckdb_extension_load(icu)
    duckdb_extension_load(json)
    duckdb_extension_load(parquet)
    duckdb_extension_load(autocomplete)
    duckdb_extension_load(lance SOURCE_DIR ${lance.source})
    duckdb_extension_load(sqlite_scanner SOURCE_DIR ${sqlite.source})
    duckdb_extension_load(ducklake SOURCE_DIR ${ducklake.source} EXTENSION_VERSION ${ducklake.revision})
  '';
in
assert pkgs.lib.hasInfix ''EngineRevision:"${revision}"'' (
  builtins.replaceStrings [ " " "\t" ] [ "" "" ] registry
);
assert pkgs.lib.hasInfix ''DuckDBVersion: "v1.5.4"'' registry;
pkgs.duckdb.overrideAttrs (old: {
  pname = "leapview-duckdb";
  version = "1.5.4";
  rev = revision;
  src = source;
  buildInputs = (old.buildInputs or [ ]) ++ [
    ducklake.croaring
    database.libpq
    database.mariadb
    avro.avro
    excel.expat
    excel.minizip
    http.libraries.curl
    http.libraries.openssl
  ];
  cmakeFlags = [
    (pkgs.lib.cmakeFeature "DUCKDB_EXTENSION_CONFIGS" "${extensions}")
    (pkgs.lib.cmakeFeature "OVERRIDE_GIT_DESCRIBE" "v1.5.4-0-g${revision}")
    (pkgs.lib.cmakeFeature "DUCKDB_EXPLICIT_PLATFORM" (
      if pkgs.stdenv.hostPlatform.isAarch64 then "linux_arm64" else "linux_amd64"
    ))
    (pkgs.lib.cmakeBool "BUILD_UNITTESTS" false)
    (pkgs.lib.cmakeBool "BUILD_SHELL" true)
    (pkgs.lib.cmakeBool "BUILD_EXTENSIONS_ONLY" false)
    (pkgs.lib.cmakeBool "CMAKE_EXPORT_COMPILE_COMMANDS" true)
    (pkgs.lib.cmakeFeature "CURL_LIBRARY_RELEASE" "${http.archives}/lib/libcurl.a")
    (pkgs.lib.cmakeFeature "CURL_INCLUDE_DIR" "${http.libraries.curl.dev}/include")
    (pkgs.lib.cmakeFeature "OPENSSL_SSL_LIBRARY" "${http.archives}/lib/libssl.a")
    (pkgs.lib.cmakeFeature "OPENSSL_CRYPTO_LIBRARY" "${http.archives}/lib/libcrypto.a")
    (pkgs.lib.cmakeFeature "OPENSSL_INCLUDE_DIR" "${http.libraries.openssl.dev}/include")
    (pkgs.lib.cmakeFeature "HTTPFS_STATIC_LIBRARIES" "${http.archives}/lib/libssl.a;${http.archives}/lib/libcrypto.a;${http.archives}/lib/libnghttp2.a;${http.archives}/lib/libz.a")
    (pkgs.lib.cmakeFeature "PostgreSQL_LIBRARY_RELEASE" "${database.archives}/lib/libpq.a")
    (pkgs.lib.cmakeFeature "PostgreSQL_INCLUDE_DIR" "${database.libpq.dev}/include")
    (pkgs.lib.cmakeFeature "MYSQL_LIBRARIES" "${database.archives}/lib/libmariadbclient.a")
    (pkgs.lib.cmakeFeature "MYSQL_INCLUDE_DIR" "${database.mariadb.dev}/include/mariadb")
    (pkgs.lib.cmakeFeature "DATABASE_STATIC_LIBRARIES" "${database.archives}/lib/libpgcommon.a;${database.archives}/lib/libpgport.a;${http.archives}/lib/libssl.a;${http.archives}/lib/libcrypto.a;${http.archives}/lib/libz.a")
    (pkgs.lib.cmakeFeature "EXPAT_LIBRARY" "${excel.archives}/lib/libexpat.a")
    (pkgs.lib.cmakeFeature "EXPAT_LIBRARY_RELEASE" "${excel.archives}/lib/libexpat.a")
    (pkgs.lib.cmakeFeature "EXPAT_INCLUDE_DIR" "${excel.expat.dev}/include")
    (pkgs.lib.cmakeFeature "MINIZIP_LIBRARY" "${excel.archives}/lib/libminizip-ng.a")
    (pkgs.lib.cmakeFeature "MINIZIP_INCLUDE_DIR" "${excel.minizip}/include")
    (pkgs.lib.cmakeFeature "DELTA_KERNEL_LIBRARY" "${delta.rust}/lib/libdelta_kernel_ffi.a")
    (pkgs.lib.cmakeFeature "DELTA_KERNEL_INCLUDE_DIR" "${delta.rust}/include")
    (pkgs.lib.cmakeFeature "AVRO_LIBRARY" "${avro.archives}/lib/libavro.a")
    (pkgs.lib.cmakeFeature "JANSSON_LIBRARY" "${avro.archives}/lib/libjansson.a")
    (pkgs.lib.cmakeFeature "SNAPPY_LIBRARY" "${avro.archives}/lib/libsnappy.a")
    (pkgs.lib.cmakeFeature "LZMA_LIBRARY" "${avro.archives}/lib/liblzma.a")
    (pkgs.lib.cmakeFeature "AVRO_INCLUDE_DIR" "${avro.avro}/include")
    (pkgs.lib.cmakeFeature "ZLIB_LIBRARY" "${http.archives}/lib/libz.a")
    (pkgs.lib.cmakeFeature "ZLIB_LIBRARY_RELEASE" "${http.archives}/lib/libz.a")
    (pkgs.lib.cmakeFeature "ZLIB_INCLUDE_DIR" "${http.libraries.zlib.dev}/include")
    (pkgs.lib.cmakeFeature "AZURE_LIBRARIES" (
      pkgs.lib.concatStringsSep ";" (
        map (name: "${azure.archives}/lib/${name}") [
          "libazure-core.a"
          "libazure-identity.a"
          "libazure-storage-blobs.a"
          "libazure-storage-common.a"
          "libazure-storage-files-datalake.a"
          "libxml2.a"
        ]
      )
    ))
    (pkgs.lib.cmakeFeature "AZURE_HTTP_LIBRARIES" "${http.archives}/lib/libcrypto.a;${http.archives}/lib/libcurl.a;${http.archives}/lib/libnghttp2.a;${http.archives}/lib/libssl.a;${http.archives}/lib/libz.a")
    (pkgs.lib.cmakeFeature "AZURE_INCLUDE_DIRS" (
      pkgs.lib.concatStringsSep ";" (
        map (name: "${azure.libraries.${name}.dev}/include") (builtins.attrNames azure.libraries)
      )
    ))
    (pkgs.lib.cmakeFeature "roaring_DIR" "${ducklake.croaring}/lib/cmake/roaring")
  ];
  postBuild = (old.postBuild or "") + ''
    mkdir -p "$TMPDIR/native-evidence"
    { "$CC" --version; "$CXX" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/native-evidence/compiler.txt"
    cp CMakeCache.txt "$TMPDIR/native-evidence/cmake-cache.txt"
    cp compile_commands.json "$TMPDIR/native-evidence/compile-commands.json"
    ${pkgs.python3}/bin/python3 - ${http.archives} "$TMPDIR/native-evidence/http-link.json" <<'PY'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps({str(p.relative_to(root)): {'archive': str(p), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted((root / 'lib').glob('*.a'))}, sort_keys=True) + '\n')
    PY
    ${pkgs.python3}/bin/python3 - ${database.archives} "$TMPDIR/native-evidence/database-link.json" <<'PYDB'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps({str(p.relative_to(root)): {'archive': str(p), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted((root / 'lib').glob('*.a'))}, sort_keys=True) + '\n')
    PYDB
    ${pkgs.python3}/bin/python3 - ${excel.archives} "$TMPDIR/native-evidence/excel-link.json" <<'PYEXCEL'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps({str(p.relative_to(root)): {'archive': str(p), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted((root / 'lib').glob('*.a'))}, sort_keys=True) + '\n')
    PYEXCEL
    ${pkgs.python3}/bin/python3 ${receipts.source}/scripts/nix_native_delta_receipt.py binding --repo ${receipts.source} \
      --output-root ${delta.rust} --destination "$TMPDIR/native-evidence/delta-link.json"
    ${pkgs.python3}/bin/python3 - ${avro.archives} "$TMPDIR/native-evidence/avro-link.json" <<'PYAVRO'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps({str(p.relative_to(root)): {'archive': str(p), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted((root / 'lib').glob('*.a'))}, sort_keys=True) + '\n')
    PYAVRO
    ${pkgs.python3}/bin/python3 - ${azure.archives} "$TMPDIR/native-evidence/azure-link.json" <<'PYAZURE'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps({str(p.relative_to(root)): {'archive': str(p), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted((root / 'lib').glob('*.a'))}, sort_keys=True) + '\n')
    PYAZURE
    cp ${extensions} "$TMPDIR/native-evidence/extensions.cmake"
    ${pkgs.python3}/bin/python3 - ${sqlite.source} "$TMPDIR/native-evidence/sqlite-source.json" <<'PY'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1]) / 'src/sqlite'
    pathlib.Path(sys.argv[2]).write_text(json.dumps({name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in ('sqlite3.c', 'sqlite3.h')}, sort_keys=True) + '\n')
    PY
    ${pkgs.python3}/bin/python3 - ${ducklake.croaring}/lib/libroaring.a "$TMPDIR/native-evidence/croaring-link.json" <<'PY'
    import hashlib, json, pathlib, sys
    archive = pathlib.Path(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps({'archive': str(archive), 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest()}, sort_keys=True) + '\n')
    PY
  '';
  postFixup = (old.postFixup or "") + ''
    mkdir -p "$lib/share/leapview"
    ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} \
      --component duckdb --evidence "$TMPDIR/native-evidence" --output-root "$lib" \
      --destination "$lib/share/leapview/native-build" > /dev/null
  '';
  doInstallCheck = true;
  installCheckPhase = ''
    "$out/bin/duckdb" -c "SELECT extension_name, loaded, installed FROM duckdb_extensions() WHERE extension_name = 'lance';" > "$TMPDIR/lance-status"
    grep -q lance "$TMPDIR/lance-status"
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'lance' AND loaded AND installed;" | grep -q 1
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'excel' AND installed AND install_mode = 'STATICALLY_LINKED' AND extension_version = '${excel.revision}';" | grep -q 1
    "$out/bin/duckdb" -c "COPY (SELECT 42 AS retained_value) TO '$TMPDIR/native-excel.xlsx' (FORMAT XLSX, HEADER true); SELECT retained_value FROM read_xlsx('$TMPDIR/native-excel.xlsx', header=true);" | grep -q 42
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'delta' AND installed AND install_mode = 'STATICALLY_LINKED' AND extension_version = '${delta.revision}';" | grep -q 1
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'avro' AND installed AND install_mode = 'STATICALLY_LINKED' AND extension_version = '${avro.revision}';" | grep -q 1
    "$out/bin/duckdb" -c "COPY (SELECT 42 AS retained_value) TO '$TMPDIR/native.avro' (FORMAT AVRO); SELECT retained_value FROM read_avro('$TMPDIR/native.avro');" | grep -q 42
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'azure' AND installed AND install_mode = 'STATICALLY_LINKED' AND extension_version = '${azure.revision}';" | grep -q 1
    "$out/bin/duckdb" -c "SELECT version();" | grep -q v1.5.4
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'sqlite_scanner' AND installed AND install_mode = 'STATICALLY_LINKED' AND install_path = '(BUILT-IN)';" | grep -q 1
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'ducklake' AND installed AND install_mode = 'STATICALLY_LINKED' AND install_path = '(BUILT-IN)';" | grep -q 1
  '';
  passthru = {
    inherit
      lance
      sqlite
      ducklake
      http
      database
      excel
      avro
      delta
      azure
      revision
      ;
  };
})
