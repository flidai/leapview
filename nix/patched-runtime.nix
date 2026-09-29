{ pkgs, application }:
let
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
  patchedApplication = pkgs.replaceDirectDependencies {
    drv = application;
    replacements = replacement ++ [
      {
        oldDependency = pkgs.stdenv.cc.cc.lib;
        newDependency = gccLib;
      }
    ];
  };
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
      ;
  };
  busybox = replace pkgs.busybox;
}
