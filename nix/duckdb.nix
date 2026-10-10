{ pkgs }:
let
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
pkgs.duckdb.overrideAttrs (_: {
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
  ];
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
