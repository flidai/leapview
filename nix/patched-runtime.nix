{ pkgs, application }:
let
  receipts = import ./native-receipts.nix {
    inherit pkgs;
    component = "application";
  };
  # Keep the upstream package name and layout: replaceDirectDependencies
  # rewrites same-length Nix store references in shipped runtime outputs.
  glibc = pkgs.glibc.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./glibc-CVE-2026-19499.patch ];
  });
  replacement = [
    {
      oldDependency = pkgs.glibc;
      newDependency = glibc;
    }
  ];
  replace =
    drv:
    pkgs.replaceDirectDependencies {
      inherit drv;
      replacements = replacement;
    };
  gccLib = replace pkgs.stdenv.cc.cc.lib;
  applicationReplacements = replacement ++ [
    {
      oldDependency = pkgs.stdenv.cc.cc.lib;
      newDependency = gccLib;
    }
  ];
  replacementEvidence = pkgs.writeText "leapview-runtime-replacements.json" (
    builtins.toJSON (
      map (pair: {
        old = toString pair.oldDependency;
        new = toString pair.newDependency;
      }) applicationReplacements
    )
  );
  patchedApplication =
    (pkgs.replaceDirectDependencies {
      drv = application;
      replacements = applicationReplacements;
    }).overrideAttrs
      (old: {
        buildCommand = old.buildCommand + ''
          chmod u+w "$out/share/leapview/native-build"
          ${receipts.command} runtime --repo ${receipts.source} --platform ${receipts.platform} \
            --revision ${application.revision} --input-receipt ${application}/share/leapview/native-build \
            --binaries ${application}/bin --output-root "$out/bin" --replacements ${replacementEvidence} \
            --destination "$out/share/leapview/native-build" > /dev/null
        '';
      });
in
{
  inherit glibc gccLib;
  application = patchedApplication // {
    inherit (application)
      version
      dependencies
      tools
      meta
      pname
      revision
      ;
  };
  busybox = replace pkgs.busybox;
}
