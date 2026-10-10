{ pkgs }:
let
  policy = builtins.fromJSON (builtins.readFile ./spatial-geos-source-lock.json);
in
pkgs.stdenv.mkDerivation {
  pname = "leapview-spatial-geos";
  version = policy.version;
  src = pkgs.fetchFromGitHub {
    owner = "libgeos";
    repo = "geos";
    rev = policy.revision;
    hash = policy.sourceNARHash;
  };
  nativeBuildInputs = [
    pkgs.cmake
    pkgs.python3
  ];
  cmakeFlags = pkgs.lib.mapAttrsToList pkgs.lib.cmakeBool policy.features ++ [
    (pkgs.lib.cmakeBool "CMAKE_EXPORT_COMPILE_COMMANDS" true)
  ];
  postPatch = ''
    mkdir -p "$TMPDIR/spatial-geos-evidence"
    python3 - ${./spatial-geos-source-lock.json} "$TMPDIR/spatial-geos-evidence/source.json" <<'PY'
    import hashlib, json, pathlib, sys
    policy = json.loads(pathlib.Path(sys.argv[1]).read_text())
    actual = {name: hashlib.sha256(pathlib.Path(name).read_bytes()).hexdigest() for name in policy['selectedFiles']}
    if actual != policy['selectedFiles']:
        raise SystemExit('selected GEOS source changed')
    pathlib.Path(sys.argv[2]).write_text(json.dumps(actual, sort_keys=True) + '\n')
    PY
  '';
  postBuild = ''
    cp CMakeCache.txt "$TMPDIR/spatial-geos-evidence/cache.txt"
    cp compile_commands.json "$TMPDIR/spatial-geos-evidence/commands.json"
    { "$CXX" --version; printf 'compiler-target: '; "$CXX" -dumpmachine; } > "$TMPDIR/spatial-geos-evidence/compiler.txt"
  '';
  doCheck = true;
  checkPhase = ''
    runHook preCheck
    ctest --no-tests=error --output-on-failure --parallel "$NIX_BUILD_CORES"
    printf 'selected GEOS upstream checks passed\n' > "$TMPDIR/spatial-geos-evidence/checks.txt"
    runHook postCheck
  '';
  postFixup = ''
    test -f "$out/lib/libgeos.a"
    test -f "$out/lib/libgeos_c.a"
    test -z "$(find "$out" -name '*.so*' -print -quit)"
    mkdir -p "$out/share/leapview/spatial-geos-evidence"
    for file in "$TMPDIR/spatial-geos-evidence/"*; do
      base64 --wrap=0 "$file" > "$out/share/leapview/spatial-geos-evidence/$(basename "$file").b64"
    done
  '';
}
