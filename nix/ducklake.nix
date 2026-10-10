{ pkgs }:
let
  policy = builtins.fromJSON (builtins.readFile ./ducklake-source-lock.json);
  registry = builtins.readFile ../internal/extension/builtin.go;
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "croaring";
  };
  croaring = pkgs.croaring.overrideAttrs (old: {
    pname = "leapview-croaring";
    cmakeFlags =
      (old.cmakeFlags or [ ])
      ++ (pkgs.lib.mapAttrsToList pkgs.lib.cmakeBool policy.croaring.cmake)
      ++ [
        (pkgs.lib.cmakeBool "CMAKE_EXPORT_COMPILE_COMMANDS" true)
      ];
    postPatch = (old.postPatch or "") + ''
      mkdir -p "$TMPDIR/native-evidence"
      ${receipts.command} croaring-source --repo ${receipts.source} --platform ${receipts.platform} \
        --output-root "$PWD" --destination "$TMPDIR/native-evidence/source.json"
    '';
    postBuild = (old.postBuild or "") + ''
      { "$CC" --version; "$CXX" --version; printf 'compiler-target: '; "$CC" -dumpmachine; } > "$TMPDIR/native-evidence/compiler.txt"
      cp CMakeCache.txt "$TMPDIR/native-evidence/cmake-cache.txt"
      cp compile_commands.json "$TMPDIR/native-evidence/compile-commands.json"
    '';
    postFixup = (old.postFixup or "") + ''
      mkdir -p "$out/share/leapview"
      ${receipts.command} component --repo ${receipts.source} --platform ${receipts.platform} \
        --component croaring --evidence "$TMPDIR/native-evidence" --output-root "$out" \
        --destination "$out/share/leapview/native-build" > /dev/null
    '';
  });
in
assert pkgs.croaring.version == policy.croaring.version;
assert pkgs.croaring.src.outputHash == policy.croaring.sourceNARHash;
assert builtins.length pkgs.croaring.patches == 1;
assert
  builtins.hashFile "sha256" (builtins.head pkgs.croaring.patches)
  == policy.croaring.pkgConfigPatchSHA256;
assert pkgs.lib.hasInfix policy.ducklake.revision registry;
assert pkgs.lib.hasInfix (builtins.hashFile "sha256" ./ducklake-source-lock.json) registry;
{
  inherit croaring;
  revision = policy.ducklake.revision;
  source = pkgs.fetchFromGitHub {
    owner = "duckdb";
    repo = "ducklake";
    rev = policy.ducklake.revision;
    hash = policy.ducklake.sourceNARHash;
  };
}
