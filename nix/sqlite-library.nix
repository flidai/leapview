{ pkgs, source }:
let
  policy = builtins.fromJSON (builtins.readFile ./sqlite-library-source-lock.json);
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "sqlite";
  };
in
pkgs.stdenv.mkDerivation {
  pname = "leapview-selected-sqlite";
  version = policy.version;
  src = source;
  nativeBuildInputs = [
    pkgs.python3
    pkgs.binutils
  ];
  dontConfigure = true;
  buildPhase = ''
    runHook preBuild
    mkdir -p "$TMPDIR/sqlite-evidence"
    { "$CC" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/sqlite-evidence/compiler.txt"
    python3 - "$TMPDIR/sqlite-evidence/source.json" <<'PY'
    import hashlib, json, pathlib, sys
    expected = {'sqlite3.c': 'b1dd5d74ec7f29055a6684fa06fb3c2f6821c87dd38f9a458dfd2e8a1db28189',
                'sqlite3.h': '919e7f2e8ed1d8f56ac17b412b8971c76aa5d1a879752cc6058f75e7d5910e1d',
                'sqlite3ext.h': 'ac9645e5c9ff0cf176efdd6e75cb5e98f46295d38e02db5c4d208826a39ab4be'}
    actual = {name: hashlib.sha256(pathlib.Path('src/sqlite', name).read_bytes()).hexdigest() for name in expected}
    if actual != expected:
        raise SystemExit('selected SQLite amalgamation changed')
    pathlib.Path(sys.argv[1]).write_text(json.dumps(actual, sort_keys=True) + '\n')
    PY
    set -o pipefail
    ( set -x
    $CC -O2 -fPIC -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_FTS4 -DSQLITE_ENABLE_FTS3_PARENTHESIS \
      -DSQLITE_ENABLE_RTREE -I src/sqlite -c src/sqlite/sqlite3.c -o sqlite3.o
    ) 2> "$TMPDIR/sqlite-evidence/build.txt"
    $AR rcs libsqlite3.a sqlite3.o
    $CC -O2 -I src/sqlite ${./sqlite-smoke.c} libsqlite3.a -lm -lpthread -ldl -o sqlite-smoke
    runHook postBuild
  '';
  doCheck = true;
  checkPhase = ''
    runHook preCheck
    ./sqlite-smoke "$TMPDIR/sqlite-shared-archive.db" > "$TMPDIR/sqlite-evidence/checks.txt"
    readelf -d ./sqlite-smoke > "$TMPDIR/sqlite-evidence/consumer-needed.txt"
    # A valid query returning zero rows must fail the functional RTREE proof.
    sed 's/INSERT INTO bounds VALUES(30,0,4,0,4); COMMIT;/COMMIT;/' ${./sqlite-smoke.c} > missing-rtree.c
    $CC -O2 -I src/sqlite missing-rtree.c libsqlite3.a -lm -lpthread -ldl -o missing-rtree
    if ./missing-rtree "$TMPDIR/sqlite-missing-rtree.db"; then
      echo "SQLite consumer accepted a missing RTREE row" >&2
      exit 1
    else
      test "$?" -eq 8
    fi
    runHook postCheck
  '';
  postFixup = ''
    # Retain consumer proof against the final stripped archive selected by all
    # downstream users, not merely the intermediate build-tree object.
    $CC -O2 -I"$out/include" ${./sqlite-smoke.c} "$out/lib/libsqlite3.a" -lm -lpthread -ldl -o "$TMPDIR/sqlite-final-consumer"
    "$TMPDIR/sqlite-final-consumer" "$TMPDIR/sqlite-final-archive.db" > "$TMPDIR/sqlite-evidence/checks.txt"
    readelf -d "$TMPDIR/sqlite-final-consumer" > "$TMPDIR/sqlite-evidence/consumer-needed.txt"
    ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} --component sqlite \
      --evidence "$TMPDIR/sqlite-evidence" --output-root "$out" --destination "$out/share/leapview/native-build" > /dev/null
  '';
  installPhase = ''
    runHook preInstall
    mkdir -p "$out/lib" "$out/include" "$out/share/leapview/sqlite-evidence"
    cp libsqlite3.a "$out/lib/"
    cp src/sqlite/sqlite3.h src/sqlite/sqlite3ext.h "$out/include/"
    for file in "$TMPDIR/sqlite-evidence/"*; do
      base64 --wrap=0 "$file" > "$out/share/leapview/sqlite-evidence/$(basename "$file").b64"
    done
    runHook postInstall
  '';
}
