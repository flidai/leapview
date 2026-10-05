{ pkgs, toolchain }:
let
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
''
  cp -R ${toolchain.go}/share/go "$TMPDIR/host-go"
  chmod -R u+w "$TMPDIR/host-go"
  (cd "$TMPDIR/host-go"
    ${pkgs.lib.concatMapStringsSep "\n" (patch: "patch --reverse -p1 < ${patch}") hostDataPatches}
  )
  export GOROOT="$TMPDIR/host-go"
''
