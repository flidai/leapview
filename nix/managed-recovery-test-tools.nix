{
  application,
  revision,
  dirty,
}:
# Public test executable only. Private exports are created after the build and
# copied to disposable guests by the driver; they never enter this derivation.
application.overrideAttrs (old: {
  pname = "leapview-managed-recovery-test-tools";
  outputs = [ "out" ];
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
    mkdir -p "$out/bin"
    go test -tags=duckdb_arrow -c -trimpath -buildvcs=false \
      -ldflags="-X github.com/flidai/leapview/internal/platform/buildinfo.revision=${revision} -X github.com/flidai/leapview/internal/platform/buildinfo.dirty=${
        if dirty then "true" else "false"
      }" \
      -o "$out/bin/managed-recovery-fixture" ./internal/app
    runHook postBuild
  '';
  installPhase = ''
    mkdir -p "$out/share/leapview/internal/app"
    cp -R static schemas dashboards evaluation "$out/share/leapview/"
    cp go.mod "$out/share/leapview/go.mod"
  '';
  doInstallCheck = false;
  meta.mainProgram = "managed-recovery-fixture";
})
