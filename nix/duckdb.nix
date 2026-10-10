{ pkgs }:
let
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "duckdb";
  };
  lance = import ./lance.nix { inherit pkgs; };
  sqlite = import ./sqlite.nix { inherit pkgs; };
  revision = "08e34c447bae34eaee3723cac61f2878b6bdf787";
  registry = builtins.readFile ../internal/extension/builtin.go;
  source = pkgs.fetchFromGitHub {
    owner = "duckdb";
    repo = "duckdb";
    rev = revision;
    hash = "sha256-6xpKZKfH5/nwE2nU5kcpgITKFm3ilb1PYf9QEk+bKoM=";
  };
  extensions = pkgs.writeText "leapview-duckdb-extensions.cmake" ''
    duckdb_extension_load(icu)
    duckdb_extension_load(json)
    duckdb_extension_load(parquet)
    duckdb_extension_load(autocomplete)
    duckdb_extension_load(lance SOURCE_DIR ${lance.source})
    duckdb_extension_load(sqlite_scanner SOURCE_DIR ${sqlite.source})
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
  ];
  postBuild = (old.postBuild or "") + ''
    mkdir -p "$TMPDIR/native-evidence"
    { "$CC" --version; "$CXX" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/native-evidence/compiler.txt"
    cp CMakeCache.txt "$TMPDIR/native-evidence/cmake-cache.txt"
    cp compile_commands.json "$TMPDIR/native-evidence/compile-commands.json"
    cp ${extensions} "$TMPDIR/native-evidence/extensions.cmake"
    ${pkgs.python3}/bin/python3 - ${sqlite.source} "$TMPDIR/native-evidence/sqlite-source.json" <<'PY'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1]) / 'src/sqlite'
    pathlib.Path(sys.argv[2]).write_text(json.dumps({name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in ('sqlite3.c', 'sqlite3.h')}, sort_keys=True) + '\n')
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
    "$out/bin/duckdb" -c "SELECT version();" | grep -q v1.5.4
    "$out/bin/duckdb" -c "SELECT count(*) FROM duckdb_extensions() WHERE extension_name = 'sqlite_scanner' AND installed AND install_mode = 'STATICALLY_LINKED' AND install_path = '(BUILT-IN)';" | grep -q 1
  '';
  passthru = { inherit lance sqlite revision; };
})
