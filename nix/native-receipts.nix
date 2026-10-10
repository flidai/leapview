{ pkgs, component }:
let
  # Only receipt inputs enter this store source; retaining the complete worktree
  # would invalidate expensive native derivations for unrelated application edits.
  common = [
    "flake.lock"
    "nix/native-receipts.nix"
    "nix/native-component-lock.json"
    "internal/extension/builtin.go"
    "scripts/nix_native_build_receipt.py"
    "scripts/nix_native_database_receipt.py"
    "scripts/nix_native_excel_receipt.py"
    "scripts/nix_native_avro_receipt.py"
    "scripts/nix_native_delta_receipt.py"
    "scripts/nix_native_azure_receipt.py"
    "scripts/nix_native_vortex_receipt.py"
  ];
  lance = [
    "nix/lance.nix"
    "nix/lance-Cargo.lock"
    "nix/quick-xml-backport-lock.json"
    "nix/apply-quick-xml-backports.py"
    "nix/quick-xml/0.37.5-backport.patch"
    "nix/quick-xml/0.38.4-backport.patch"
  ];
  http = [
    "nix/http.nix"
    "nix/check-extension-version.py"
    "nix/http-source-lock.json"
    "nix/httpfs-static-dependencies.patch"
    "nix/quack-engine-includes.patch"
  ];
  database = [
    "nix/database-connectors.nix"
    "nix/check-extension-version.py"
    "nix/database-source-lock.json"
    "nix/libpq-static-openssl-refs.patch"
    "nix/postgres-static-dependencies.patch"
    "nix/mysql-static-dependencies.patch"
    "nix/check-mariadb-static-auth.c"
    "scripts/nix_native_database_receipt.py"
  ];
  excel = [
    "nix/check-extension-version.py"
    "nix/excel.nix"
    "nix/excel-source-lock.json"
    "nix/excel-static-dependencies.patch"
    "nix/check-excel-static-libraries.c"
    "scripts/nix_native_excel_receipt.py"
  ];
  avro = [
    "nix/check-extension-version.py"
    "nix/avro.nix"
    "nix/avro-source-lock.json"
    "nix/avro-static-dependencies.patch"
    "nix/avro-c-static-only.patch"
    "nix/avro-c-available-tests.patch"
    "nix/check-avro-static-libraries.c"
    "scripts/nix_native_avro_receipt.py"
  ];
  delta = [
    "nix/delta.nix"
    "nix/delta-source-lock.json"
    "nix/delta-Cargo.lock"
    "nix/delta-static-dependencies.patch"
    "nix/check-extension-version.py"
    "nix/apply-quick-xml-backports.py"
    "nix/delta-quick-xml-backport-lock.json"
    "nix/delta-quick-xml-smoke.rs"
    "nix/quick-xml/0.39.2-backport.patch"
    "nix/quick-xml/duplicate-attributes-upstream.patch"
    "nix/quick-xml/namespace-bounds-upstream.patch"
    "scripts/nix_native_delta_receipt.py"
  ];
  azure = [
    "nix/check-extension-version.py"
    "nix/azure.nix"
    "nix/azure-source-lock.json"
    "nix/azure-static-dependencies.patch"
    "nix/azure-core-selected-curl.patch"
    "nix/azure-identity-retain-fetch-option.patch"
    "nix/azure-storage-common-retain-fetch-option.patch"
    "nix/check-azure-static-libraries.cpp"
    "scripts/nix_native_azure_receipt.py"
  ];
  vortex = [
    "nix/vortex.nix"
    "nix/vortex-source-lock.json"
    "nix/vortex-Cargo.lock"
    "nix/vortex-static-dependencies.patch"
    "nix/vortex-selected-engine.patch"
    "nix/check-extension-version.py"
    "nix/vortex-quick-xml-backport-lock.json"
    "nix/apply-quick-xml-backports.py"
    "nix/delta-quick-xml-smoke.rs"
    "nix/quick-xml/0.39.2-backport.patch"
    "nix/quick-xml/duplicate-attributes-upstream.patch"
    "nix/quick-xml/namespace-bounds-upstream.patch"
    "scripts/nix_native_vortex_receipt.py"
    "scripts/nix_native_delta_receipt.py"
  ];
  duckdb = [
    "nix/duckdb.nix"
    "nix/sqlite.nix"
    "nix/ducklake.nix"
    "nix/ducklake-source-lock.json"
  ]
  ++ http
  ++ database
  ++ excel
  ++ avro
  ++ delta
  ++ azure
  ++ vortex;
  croaring = [
    "nix/ducklake.nix"
    "nix/ducklake-source-lock.json"
  ];
  paths =
    common
    ++ (
      if component == "lance" then
        lance
      else if component == "vortex" then
        vortex
      else if component == "delta" then
        delta
      else if component == "azure" then
        azure ++ http
      else if component == "avro" then
        avro ++ http
      else if component == "excel" then
        excel ++ http
      else if component == "database" then
        database ++ http
      else if component == "http" then
        http
      else if component == "croaring" then
        croaring
      else if component == "duckdb" then
        duckdb
      else
        lance
        ++ delta
        ++ duckdb
        ++ [
          "nix/application.nix"
          "nix/ca-root.sh"
          "nix/check-http-default-ca.sh"
          "nix/patched-runtime.nix"
          "nix/glibc-CVE-2026-19499.patch"
          "nix/portable.nix"
        ]
    );
  source = pkgs.lib.fileset.toSource {
    root = ../.;
    fileset = pkgs.lib.fileset.unions (map (path: ../. + "/${path}") paths);
  };
in
{
  inherit source;
  platform = if pkgs.stdenv.hostPlatform.isAarch64 then "linux/arm64" else "linux/amd64";
  command = "${pkgs.python3}/bin/python3 ${source}/scripts/nix_native_build_receipt.py";
}
