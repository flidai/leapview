{ pkgs, geos }:
pkgs.runCommand "leapview-spatial-geos-consumer"
  {
    nativeBuildInputs = [
      pkgs.stdenv.cc
      pkgs.binutils
      pkgs.python3
    ];
  }
  ''
    mkdir -p "$out"
    $CC -Wall -Wextra -Werror -I${geos}/include ${./check-spatial-geos.c} \
      ${geos}/lib/libgeos_c.a ${geos}/lib/libgeos.a -lstdc++ -lm -o consumer
    ./consumer > "$out/consumer.txt"
    readelf -d consumer > "$out/consumer-needed.txt"
    if grep -E 'NEEDED.*libgeos' "$out/consumer-needed.txt"; then
      echo 'GEOS consumer used an unselected shared library' >&2
      exit 1
    fi
    python3 - ${geos} "$out/archives.json" <<'PY'
    import hashlib, json, pathlib, sys
    root = pathlib.Path(sys.argv[1])
    value = {name: hashlib.sha256((root / 'lib' / name).read_bytes()).hexdigest() for name in ('libgeos.a', 'libgeos_c.a')}
    pathlib.Path(sys.argv[2]).write_text(json.dumps(value, sort_keys=True) + '\n')
    PY
  ''
