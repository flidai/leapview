{
  pkgs,
  toolchain,
  src,
  revision,
  dirty,
  buildTime,
}:
let
  dependencies = import ./dependencies.nix { inherit pkgs toolchain src; };
  canonicalVersion = pkgs.lib.trim (builtins.readFile ../VERSION);
  buildVersion =
    if dirty then "development" else "${canonicalVersion}+nix.${builtins.substring 0 12 revision}";
  buildInfo = "github.com/flidai/leapview/internal/platform/buildinfo";
  # The exported controller uses the conventional host's data files. Reverse
  # only these locked NixOS data-path patches in a private SDK copy; retain
  # compiler/linker fixes and the existing compiler/source hashes.
  hostDataPatches = builtins.filter (
    patch:
    builtins.any (name: pkgs.lib.hasSuffix name (toString patch)) [
      "iana-etc-1.25.patch"
      "mailcap-1.17.patch"
      "tzdata-1.19.patch"
    ]
  ) toolchain.go.patches;
in
assert builtins.length hostDataPatches == 3;
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
    cp -R ${dependencies.javascript}/typespec/node_modules pkg/apigen/typespec/node_modules
    chmod -R u+w node_modules pkg/apigen/typespec/node_modules
    rm -rf node_modules/lightningcss-linux-x64-musl node_modules/@parcel/watcher-linux-x64-musl
    autoPatchelf node_modules pkg/apigen/typespec/node_modules
    patchShebangs node_modules pkg/apigen/typespec/node_modules
    export APIGEN_TYPESPEC_PACKAGE_DIR="$PWD/pkg/apigen/typespec"
    npm --prefix pkg/apigen/typespec run build
  '';
  buildPhase = ''
    runHook preBuild
    ./scripts/generate_build_sources.sh
    cp -R ${toolchain.go}/share/go "$TMPDIR/host-go"
    chmod -R u+w "$TMPDIR/host-go"
    (cd "$TMPDIR/host-go"
      ${pkgs.lib.concatMapStringsSep "\n" (patch: "patch --reverse -p1 < ${patch}") hostDataPatches}
    )
    export GOROOT="$TMPDIR/host-go"
    # Retain function symbols for exact binary-mode vulnerability analysis.
    flags="-w -X ${buildInfo}.version=${buildVersion} -X ${buildInfo}.revision=${revision} -X ${buildInfo}.buildTime=${buildTime} -X ${buildInfo}.dirty=${
      if dirty then "true" else "false"
    } -X ${buildInfo}.release=false"
    for arch in amd64 arm64; do
      if [ "$arch" = amd64 ]; then destination="$out"; else destination="$arm64"; fi
      mkdir -p "$destination/bin"
      CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0 go build -trimpath -buildvcs=false \
        -ldflags="$flags" -o "$destination/bin/leapviewctl" ./cmd/leapviewctl
      # A standalone controller candidate, not an installation bundle.
      tar --sort=name --mtime=@1 --owner=0 --group=0 --numeric-owner \
        -C "$destination/bin" -cf - leapviewctl | gzip -n > "$destination/leapviewctl-linux-$arch.tar.gz"
      printf '{"platform":"linux/%s","version":"%s","sourceRevision":"%s"}\n' \
        "$arch" '${buildVersion}' '${revision}' > "$destination/archive-identity.json"
      python3 scripts/check_nix_cli_compatibility.py "$destination/bin/leapviewctl" \
        --arch "$arch" --output "$destination/static-compatibility.json"
    done
    "$out/bin/leapviewctl" version --json
    "$out/bin/leapviewctl" --help >/dev/null
    runHook postBuild
  '';
  installPhase = "true";
  meta.mainProgram = "leapviewctl";
}
