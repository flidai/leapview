{
  pkgs,
  toolchain,
  src,
  revision,
  dirty,
  buildTime,
}:
let
  manifest = builtins.fromJSON (builtins.readFile ../package.json);
  dependencies = import ./dependencies.nix { inherit pkgs toolchain src; };
  buildVersion =
    if dirty then "development" else "${manifest.version}+nix.${builtins.substring 0 12 revision}";
  buildInfo = "github.com/flidai/leapview/internal/platform/buildinfo";
in
pkgs.stdenv.mkDerivation {
  pname = "leapview";
  version = buildVersion;
  inherit src;
  outputs = [
    "out"
    "tools"
  ];
  nativeBuildInputs = [
    toolchain.go
    toolchain.sqlcGo
    toolchain.bun
    pkgs.nodejs_24
    pkgs.pkg-config
    pkgs.autoPatchelfHook
    pkgs.binutils
  ];
  buildInputs = [ pkgs.stdenv.cc.cc.lib ];
  GOTOOLCHAIN = "local";
  GOSUMDB = "off";
  GOPROXY = "file://${dependencies.go}/download";
  GOFLAGS = "-mod=readonly";
  CGO_ENABLED = "1";
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
  BUN_FEATURE_FLAG_NO_ORPHANS = "1";
  dontConfigure = true;
  # Deployment scripts leave the image and must retain portable interpreters.
  # Only build-time npm helpers are patched explicitly below.
  dontPatchShebangs = true;
  # patchelf's default header sorting can move an empty GNU_STACK segment
  # ahead of LOAD segments, hiding Go build info from Go/SBOM scanners.
  # The native compiler already supplies the Nix loader and runtime RPATHs.
  dontPatchELF = true;
  dontAutoPatchelf = true;
  dontStrip = true;
  preBuild = ''
    export GOPATH="$TMPDIR/go" GOCACHE="$TMPDIR/go-cache"
    export npm_config_cache="$TMPDIR/npm-cache"
    cp -R ${dependencies.javascript}/app/node_modules ./node_modules
    cp -R ${dependencies.javascript}/typespec/node_modules pkg/apigen/typespec/node_modules
    chmod -R u+w node_modules pkg/apigen/typespec/node_modules
    # Bun retains both libc variants of these optional packages. This output
    # supports glibc x86_64 Linux; do not patch the unused musl alternatives.
    rm -rf node_modules/lightningcss-linux-x64-musl node_modules/@parcel/watcher-linux-x64-musl
    # Native npm helpers (Tailwind, TypeScript, esbuild) need Nix interpreters.
    autoPatchelf node_modules pkg/apigen/typespec/node_modules
    patchShebangs node_modules pkg/apigen/typespec/node_modules
    export APIGEN_TYPESPEC_PACKAGE_DIR="$PWD/pkg/apigen/typespec"
    npm --prefix pkg/apigen/typespec run build
  '';
  buildPhase = ''
    runHook preBuild
    ./scripts/generate_build_sources.sh
    go run ./internal/app/tools/clidocgen
    go run ./internal/app/tools/schemadocgen
    go run ./internal/app/tools/openapidocgen
    go run ./internal/app/tools/docsitegen
    bun scripts/generate_lucide_icon_catalog.ts
    bun scripts/generate_visualization_validator.ts
    bun run build
    mkdir -p "$out/bin" "$tools/bin"
    # Keep Go function symbols so govulncheck can identify shipped vulnerable
    # functions. Removing them forces conservative module-level analysis.
    flags="-w -X ${buildInfo}.version=${buildVersion} -X ${buildInfo}.revision=${revision} -X ${buildInfo}.buildTime=${buildTime} -X ${buildInfo}.dirty=${
      if dirty then "true" else "false"
    } -X ${buildInfo}.release=false"
    go build -tags=duckdb_arrow -trimpath -buildvcs=false -ldflags="$flags" -o "$out/bin/leapview" ./cmd/leapview
    go build -tags=duckdb_arrow -trimpath -buildvcs=false -ldflags="$flags" -o "$out/bin/leapviewctl" ./cmd/leapviewctl
    go build -tags=duckdb_arrow -trimpath -buildvcs=false -o "$tools/bin/extensionsupply" ./internal/app/tools/extensionsupply
    go build -trimpath -buildvcs=false -o "$tools/bin/mapassets" ./internal/app/tools/mapassets
    runHook postBuild
  '';
  installPhase = ''
    mkdir -p "$out/share/leapview"
    cp -R static schemas dashboards evaluation deploy "$out/share/leapview/"
    # Runtime resources must not become a second test tree through a result link.
    find "$out/share/leapview" -type f \( -name '*_test.go' -o -name '*.test.ts' -o -name '*.test.mjs' \) -delete
    # Nix candidates export Linux binaries with a glibc 2.38 ABI baseline.
    # Keep the standard Dockerfile candidate's Debian client fixture unchanged.
    substituteInPlace "$out/share/leapview/deploy/compose/qualification/Dockerfile.authoring-client" \
      --replace-fail 'FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251' \
      'FROM ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3'

  '';
  doInstallCheck = true;
  installCheckPhase = ''
    if grep -R -l '/nix/store/' "$out/share/leapview/deploy/host/files"; then
      echo "exported host scripts contain a Nix-store dependency" >&2
      exit 1
    fi
    for binary in "$out/bin/"*; do
      go version -m "$binary" > "$TMPDIR/build-info"
      grep -Fq 'github.com/flidai/leapview' "$TMPDIR/build-info"
      grep -Fq 'github.com/jackc/pgx/v5' "$TMPDIR/build-info"
    done
    "$out/bin/leapview" version
    "$out/bin/leapviewctl" --help >/dev/null
  '';
  passthru = { inherit dependencies; };
  meta.mainProgram = "leapview";
}
