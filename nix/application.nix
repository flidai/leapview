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
  manifest = builtins.fromJSON (builtins.readFile ../package.json);
  canonicalVersion = pkgs.lib.trim (builtins.readFile ../VERSION);
  dependencies = import ./dependencies.nix { inherit pkgs toolchain src; };
  duckdb = import ./duckdb.nix { inherit pkgs; };
  buildVersion =
    if purpose == "compose" then
      canonicalVersion
    else if dirty then
      "development"
    else
      "${manifest.version}+nix.${builtins.substring 0 12 revision}";
  buildInfo = "github.com/flidai/leapview/internal/platform/buildinfo";
in
assert builtins.elem purpose [
  "development"
  "compose"
];
assert purpose != "compose" || !dirty;
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
  CGO_CFLAGS = "-I${duckdb.dev}/include";
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
    cp -R ${dependencies.javascript}/typespec/${dependencies.nativeCPU}/node_modules pkg/apigen/typespec/node_modules
    chmod -R u+w node_modules pkg/apigen/typespec/node_modules
    # The fixed dependency tree contains every Linux CPU variant; retain only
    # this native glibc target before patching executable helpers.
    ${dependencies.prunePlatformPackages} "$PWD/node_modules" "${dependencies.nativeCPU}"
    ${dependencies.prunePlatformPackages} "$PWD/pkg/apigen/typespec/node_modules" "${dependencies.nativeCPU}"
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
    } -X ${buildInfo}.release=${if purpose == "compose" then "true" else "false"}"
    # Use source-built DuckDB with patched, statically linked Lance and SQLite.
    # A link group retains dependency resolution without unsigned extension
    # loading or a new extension signing-key custody requirement.
    nativeLibraries=$(find ${duckdb.lib}/lib -maxdepth 1 -name '*.a' ! -name 'libdummy_static_extension_loader.a' -type f | LC_ALL=C sort)
    export CGO_LDFLAGS="-Wl,--start-group $nativeLibraries ${duckdb.lance.rust}/lib/liblance_duckdb_ffi.a -Wl,--end-group -lstdc++ -ldl -lm"
    tags=duckdb_arrow,duckdb_use_static_lib,leapview_static_lance,leapview_static_sqlite
    go build -tags="$tags" -trimpath -buildvcs=false -ldflags="$flags" -o "$out/bin/leapview" ./cmd/leapview
    go build -tags="$tags" -trimpath -buildvcs=false -ldflags="$flags" -o "$out/bin/leapviewctl" ./cmd/leapviewctl
    go build -tags="$tags" -trimpath -buildvcs=false -o "$tools/bin/extensionsupply" ./internal/app/tools/extensionsupply
    go build -trimpath -buildvcs=false -o "$tools/bin/mapassets" ./internal/app/tools/mapassets
    runHook postBuild
  '';
  doCheck = true;
  checkPhase = ''
    runHook preCheck
    # Test the same engine archives and compile-time registry as the shipped
    # binaries, including independent-session readback and linked SQLite ID.
    go test -count=1 -tags="$tags" ./internal/extension \
      ./internal/deployment/extensionsupply ./internal/analytics/duckdbsession
    go test -count=1 -tags="$tags" -run '^TestAdmittedExtensionLoad' \
      ./internal/analytics/ducklake
    runHook postCheck
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
  passthru = { inherit dependencies duckdb; };
  meta.mainProgram = "leapview";
}
