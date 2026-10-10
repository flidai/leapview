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
  ];
  lance = [
    "nix/lance.nix"
    "nix/lance-Cargo.lock"
    "nix/quick-xml-backport-lock.json"
    "nix/apply-quick-xml-backports.py"
    "nix/quick-xml/0.37.5-backport.patch"
    "nix/quick-xml/0.38.4-backport.patch"
  ];
  duckdb = [
    "nix/duckdb.nix"
    "nix/sqlite.nix"
  ];
  paths =
    common
    ++ (
      if component == "lance" then
        lance
      else if component == "duckdb" then
        duckdb
      else
        lance
        ++ duckdb
        ++ [
          "nix/application.nix"
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
