{ pkgs, ... }:

let
  # NixOS exposes systemPackages in the noninteractive system PATH used by
  # SSH and sudo. Keep this wrapper rooted at the standard host installation
  # path; the mutable controller and its state remain outside the Nix store.
  leapviewctl = pkgs.writeShellScriptBin "leapviewctl" ''
    export LEAPVIEWCTL_ROOT=/opt/leapview
    exec /opt/leapview/leapviewctl "$@"
  '';
in
{
  virtualisation.docker.enable = true;
  virtualisation.docker.enableOnBoot = true;

  environment.systemPackages = with pkgs; [
    leapviewctl
    python3
    openssl
    coreutils
    findutils
    util-linux
    gnutar
    gzip
    xz
  ];

  programs.nix-ld = {
    enable = true;
    libraries = [ pkgs.stdenv.cc.cc.lib ];
  };

  networking.firewall.allowedTCPPorts = [ 80 443 ];
  networking.firewall.allowedUDPPorts = [ 443 ];
}
