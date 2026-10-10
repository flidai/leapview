{ pkgs }:
let
  revision = "494e9feed54c20b6bbfb665baf26864bc7e3b517";
  registry = builtins.readFile ../internal/extension/builtin.go;
  wrapper = pkgs.fetchFromGitHub {
    owner = "duckdb";
    repo = "duckdb-sqlite";
    rev = revision;
    hash = "sha256-KGN/HbL3S0W8885CEarSUcTA6haSCFq5ElWA9Fzxnlg=";
  };
  amalgamation = pkgs.fetchurl {
    url = "https://www.sqlite.org/2026/sqlite-amalgamation-3530400.zip";
    hash = "sha256-HnHd+ThJxqbs9YuCfAaSBz0t1+5AGWFYBo97KfQi6H0=";
  };
  source =
    pkgs.runCommand "leapview-sqlite-${builtins.substring 0 12 revision}-3.53.4-source"
      {
        nativeBuildInputs = [
          pkgs.unzip
          pkgs.openssl
        ];
      }
      ''
        unzip -q ${amalgamation}
        cd sqlite-amalgamation-3530400
        echo 'b1dd5d74ec7f29055a6684fa06fb3c2f6821c87dd38f9a458dfd2e8a1db28189  sqlite3.c' | sha256sum -c -
        echo '919e7f2e8ed1d8f56ac17b412b8971c76aa5d1a879752cc6058f75e7d5910e1d  sqlite3.h' | sha256sum -c -
        openssl dgst -sha3-256 sqlite3.c | grep -q '67f423e9ebbbdc473cbc4772c872ee6b89f31fde4ed0279a5c25d5f65c043a16$'
        grep -Fq '#define SQLITE_VERSION        "3.53.4"' sqlite3.h
        grep -Fq '2026-07-24 19:02:57 bf7c7f30031888f4e796e429ab3978879485813aaca6f641c7b33e4e09459bcc' sqlite3.h
        cp -R ${wrapper} "$out"
        chmod -R u+w "$out"
        cp sqlite3.c sqlite3.h "$out/src/sqlite/"
      '';
  smoke = pkgs.stdenv.mkDerivation {
    pname = "leapview-sqlite-source-smoke";
    version = "3.53.4";
    src = source;
    dontConfigure = true;
    buildPhase = ''
      $CC -O2 -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_FTS4 -DSQLITE_ENABLE_FTS3_PARENTHESIS -DSQLITE_ENABLE_RTREE \
        -I src/sqlite src/sqlite/sqlite3.c ${./sqlite-smoke.c} -lm -lpthread -ldl -o sqlite-smoke
    '';
    doCheck = true;
    checkPhase = ''
      ./sqlite-smoke "$TMPDIR/sqlite-smoke.db"
    '';
    installPhase = ''
      mkdir -p "$out/bin"
      cp sqlite-smoke "$out/bin/"
    '';
  };
in
assert pkgs.lib.hasInfix revision registry;
assert pkgs.lib.hasInfix "1e71ddf93849c6a6ecf58b827c0692073d2dd7ee40196158068f7b29f422e87d"
  registry;
assert pkgs.lib.hasInfix "67f423e9ebbbdc473cbc4772c872ee6b89f31fde4ed0279a5c25d5f65c043a16"
  registry;
{
  inherit revision source smoke;
}
