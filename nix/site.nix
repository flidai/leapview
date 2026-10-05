{
  pkgs,
  toolchain,
  src,
  revision,
  dirty,
}:
let
  canonicalVersion = pkgs.lib.trim (builtins.readFile ../VERSION);
  version = canonicalVersion;
  dependencies = import ./dependencies.nix { inherit pkgs toolchain src; };
  portableGoSDK = import ./portable-go-sdk.nix { inherit pkgs toolchain; };
  sourcegen = pkgs.stdenv.mkDerivation {
    pname = "leapview-site-source";
    inherit version src;
    outputs = [
      "out"
      "tools"
    ];
    nativeBuildInputs = [
      toolchain.go
      toolchain.sqlcGo
      toolchain.bun
      pkgs.nodejs_24
      pkgs.autoPatchelfHook
      pkgs.binutils
    ];
    buildInputs = [ pkgs.stdenv.cc.cc.lib ];
    GOTOOLCHAIN = "local";
    GOSUMDB = "off";
    GOPROXY = "file://${dependencies.go}/download";
    GOFLAGS = "-mod=readonly";
    CGO_ENABLED = "1"; # The source-generation phase runs the tagged DuckDB tools.
    PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
    BUN_FEATURE_FLAG_NO_ORPHANS = "1";
    dontConfigure = true;
    dontPatchShebangs = true;
    dontFixup = true;
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
      go run ./internal/app/tools/clidocgen
      go run ./internal/app/tools/schemadocgen
      go run ./internal/app/tools/openapidocgen
      go run ./internal/app/tools/docsitegen
      mkdir -p "$tools/bin"
      CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$tools/bin/mapassets" ./internal/app/tools/mapassets
      go build -tags=duckdb_arrow -trimpath -buildvcs=false -o "$tools/bin/extensionsupply" ./internal/app/tools/extensionsupply
      runHook postBuild
    '';
    installPhase = ''
      rm -rf node_modules pkg/apigen/typespec/node_modules
      mkdir -p "$out"
      cp -R ./. "$out/"
    '';
  };
  assets = import ./runtime-assets.nix {
    inherit pkgs toolchain src;
    application = {
      tools = sourcegen.tools;
    };
  };
  package = pkgs.stdenv.mkDerivation {
    pname = "leapview-site";
    inherit version;
    src = sourcegen;
    nativeBuildInputs = [
      toolchain.go
      toolchain.bun
      pkgs.nodejs_24
      pkgs.autoPatchelfHook
      pkgs.binutils
    ];
    buildInputs = [ pkgs.stdenv.cc.cc.lib ];
    GOTOOLCHAIN = "local";
    GOSUMDB = "off";
    GOPROXY = "file://${dependencies.go}/download";
    GOFLAGS = "-mod=readonly";
    CGO_ENABLED = "0";
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
      chmod -R u+w node_modules
      ${dependencies.prunePlatformPackages} "$PWD/node_modules" "${dependencies.nativeCPU}"
      autoPatchelf node_modules
      patchShebangs node_modules
    '';
    buildPhase = ''
      runHook preBuild
      # Source generation consumes the signed, fixed-output extension supply.
      # Documentation tools load these exact local files without installation.
      export DUCKDB_EXTENSION_DIRECTORY="$TMPDIR/site-extensions"
      mkdir -m 0700 "$DUCKDB_EXTENSION_DIRECTORY"
      for name in ducklake spatial postgres_scanner; do
        artifacts=( ${assets.extensions}/artifacts/$name-*.duckdb_extension )
        test "''${#artifacts[@]}" -eq 1
        cp "''${artifacts[0]}" "$DUCKDB_EXTENSION_DIRECTORY/$name.duckdb_extension"
      done
      CGO_ENABLED=1 go run -tags=duckdb_arrow ./internal/app/tools/visualdocgen
      go run ./internal/app/tools/docsitegen
      bun scripts/generate_visualization_validator.ts
      bun run build:site
      ${portableGoSDK}
      # Preserve Go function symbols for exact binary vulnerability analysis.
      go build -trimpath -buildvcs=false \
        -ldflags="-w -X main.buildRevision=${revision}" \
        -o "$TMPDIR/leapview-site" ./cmd/leapview-site
      runHook postBuild
    '';
    installPhase = ''
      mkdir -p "$out/bin" "$out/.data/map-assets"
      cp "$TMPDIR/leapview-site" "$out/bin/leapview-site"
      cp -R ${assets.maps}/. "$out/.data/map-assets/"
      chmod 0555 "$out/bin/leapview-site"
      find "$out/.data" -type d -exec chmod 0555 {} +
      find "$out/.data" -type f -exec chmod 0444 {} +
    '';
    meta.mainProgram = "leapview-site";
  };
in
assert !dirty;
assert revision != "unknown";
{
  inherit package version;
  tools = sourcegen.tools;
  maps = assets.maps;
}
