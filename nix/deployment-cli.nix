{
  pkgs,
  toolchain,
  src,
  revision,
  dirty,
  buildTime,
  purpose,
}:
let
  dependencies = import ./dependencies.nix { inherit pkgs toolchain src; };
  canonicalVersion = pkgs.lib.trim (builtins.readFile ../VERSION);
  buildVersion =
    if purpose == "compose" then
      canonicalVersion
    else if dirty then
      "development"
    else
      "${canonicalVersion}+nix.${builtins.substring 0 12 revision}";
  buildInfo = "github.com/flidai/leapview/internal/platform/buildinfo";
  portableGoSDK = import ./portable-go-sdk.nix { inherit pkgs toolchain; };
in
assert builtins.elem purpose [
  "development"
  "compose"
];
assert purpose != "compose" || !dirty;
pkgs.stdenv.mkDerivation {
  pname = "leapviewctl-linux";
  version = buildVersion;
  inherit src;
  outputs = [
    "out"
    "arm64"
  ];
  nativeBuildInputs = [
    toolchain.go
    toolchain.sqlcGo
    toolchain.bun
    pkgs.nodejs_24
    pkgs.autoPatchelfHook
    pkgs.python3
  ];
  buildInputs = [ pkgs.stdenv.cc.cc.lib ];
  GOTOOLCHAIN = "local";
  GOSUMDB = "off";
  GOPROXY = "file://${dependencies.go}/download";
  GOFLAGS = "-mod=readonly";
  CGO_ENABLED = "1"; # Native source generators; published controllers below use 0.
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
  BUN_FEATURE_FLAG_NO_ORPHANS = "1";
  dontConfigure = true;
  dontPatchShebangs = true;
  dontFixup = true;
  allowedReferences = [ ];
  preBuild = ''
    export GOPATH="$TMPDIR/go" GOCACHE="$TMPDIR/go-cache"
    export npm_config_cache="$TMPDIR/npm-cache"
    cp -R ${dependencies.javascript}/app/node_modules ./node_modules
    cp -R ${dependencies.javascript}/typespec/${dependencies.nativeCPU}/node_modules pkg/apigen/typespec/node_modules
    chmod -R u+w node_modules pkg/apigen/typespec/node_modules
    ${dependencies.prunePlatformPackages} "$PWD/node_modules" "${dependencies.nativeCPU}"
    ${dependencies.prunePlatformPackages} "$PWD/pkg/apigen/typespec/node_modules" "${dependencies.nativeCPU}"
    autoPatchelf node_modules pkg/apigen/typespec/node_modules
    patchShebangs node_modules pkg/apigen/typespec/node_modules
    export APIGEN_TYPESPEC_PACKAGE_DIR="$PWD/pkg/apigen/typespec"
    npm --prefix pkg/apigen/typespec run build
  '';
  buildPhase = ''
    runHook preBuild
    ./scripts/generate_build_sources.sh
    ${portableGoSDK}
    # Retain function symbols for exact binary-mode vulnerability analysis.
    # A Compose candidate must match the image's canonical release metadata.
    # Metadata grants no archive publication or adoption authority.
    flags="-w -X ${buildInfo}.version=${buildVersion} -X ${buildInfo}.revision=${revision} -X ${buildInfo}.buildTime=${buildTime} -X ${buildInfo}.dirty=${
      if dirty then "true" else "false"
    } -X ${buildInfo}.release=${if purpose == "compose" then "true" else "false"}"
    for arch in amd64 arm64; do
      if [ "$arch" = amd64 ]; then destination="$out"; else destination="$arm64"; fi
      mkdir -p "$destination/bin"
      CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0 go build -trimpath -buildvcs=false \
        -ldflags="$flags" -o "$destination/bin/leapviewctl" ./cmd/leapviewctl
      # The controller is a candidate input; bundle assembly requires an exact
      # matching immutable image and still needs its own qualification/signing.
      tar --sort=name --mtime=@1 --owner=0 --group=0 --numeric-owner \
        -C "$destination/bin" -cf - leapviewctl | gzip -n > "$destination/leapviewctl-linux-$arch.tar.gz"
      printf '{"platform":"linux/%s","version":"%s","sourceRevision":"%s"}\n' \
        "$arch" '${buildVersion}' '${revision}' > "$destination/archive-identity.json"
      python3 scripts/check_nix_cli_compatibility.py "$destination/bin/leapviewctl" \
        --arch "$arch" --output "$destination/static-compatibility.json"
      ${pkgs.lib.optionalString (purpose == "compose") ''
            python3 - "$destination/bin/leapviewctl" "$destination/controller-build-identity.json" "$arch" <<'PY'
        import hashlib, json, pathlib, sys
        binary, output, arch = sys.argv[1:]
        receipt = {
            'schemaVersion': 1, 'platform': 'linux/' + arch,
            'binarySHA256': 'sha256:' + hashlib.sha256(pathlib.Path(binary).read_bytes()).hexdigest(),
            'version': '${buildVersion}', 'revision': '${revision}',
            'buildTime': '${buildTime}', 'dirty': False, 'development': False,
        }
        pathlib.Path(output).write_text(json.dumps(receipt, indent=2) + '\n')
        PY
      ''}
    done
    "$out/bin/leapviewctl" version --format json > "$TMPDIR/runtime-identity.json"
    python3 - "$TMPDIR/runtime-identity.json" <<'PY'
    import json, pathlib, sys
    expected = {
        'product': 'leapviewctl', 'version': '${buildVersion}', 'revision': '${revision}',
        'buildTime': '${buildTime}', 'dirty': ${if dirty then "True" else "False"},
        'development': ${if purpose == "compose" then "False" else "True"},
    }
    if json.loads(pathlib.Path(sys.argv[1]).read_text()) != expected:
        raise SystemExit('native controller runtime disagrees with build identity')
    PY
    "$out/bin/leapviewctl" --help >/dev/null
    runHook postBuild
  '';
  installPhase = "true";
  meta.mainProgram = "leapviewctl";
}
