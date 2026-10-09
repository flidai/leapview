{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.leapview;
  restrictedCIDR =
    cidr:
    let
      parts = builtins.match "([0-9a-fA-F:.]+)/([1-9][0-9]{0,2})" cidr;
      ipv6 = parts != null && lib.hasInfix ":" (builtins.elemAt parts 0);
      addressValid = builtins.tryEval (
        if ipv6 then
          builtins.deepSeq (lib.network.ipv6.fromString cidr) true
        else
          let
            octets = builtins.match "([0-9]{1,3})\\.([0-9]{1,3})\\.([0-9]{1,3})\\.([0-9]{1,3})" (
              builtins.elemAt parts 0
            );
          in
          octets != null && builtins.all (octet: lib.toInt octet <= 255) octets
      );
    in
    parts != null
    && lib.toInt (builtins.elemAt parts 1) <= (if ipv6 then 128 else 32)
    && addressValid.success
    && addressValid.value;
in
{
  options.leapview = {
    operatorKeys = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
    };
    operatorCIDRs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
    };
    publicInterface = lib.mkOption {
      type = lib.types.str;
      default = "enp1s0";
    };
    privateInterface = lib.mkOption {
      type = lib.types.str;
      default = "enp7s0";
    };
    privateAddress = lib.mkOption { type = lib.types.strMatching "[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+"; };
    diskDevice = lib.mkOption {
      type = lib.types.str;
      default = "/dev/sda";
    };
    bootMode = lib.mkOption {
      type = lib.types.enum [
        "bios"
        "uefi"
      ];
      default = "bios";
      description = "Firmware mode verified by the operator before initial disk installation.";
    };
  };
  config = {
    assertions = [
      {
        assertion = cfg.operatorKeys != [ ];
        message = "At least one operator public key is required.";
      }
      {
        assertion = cfg.operatorCIDRs != [ ] && builtins.all restrictedCIDR cfg.operatorCIDRs;
        message = "Use restricted operator CIDRs for SSH.";
      }
    ];
    networking.useDHCP = false;
    networking.useNetworkd = true;
    # Hetzner x86 virtio devices: confirm these matches during first-host qualification.
    systemd.network.networks."10-public" = {
      matchConfig.Name = cfg.publicInterface;
      networkConfig.DHCP = "ipv4";
      linkConfig.RequiredForOnline = "routable";
    };
    systemd.network.networks."20-private" = {
      matchConfig.Name = cfg.privateInterface;
      networkConfig.DHCP = "ipv4";
      linkConfig.RequiredForOnline = "routable";
    };
    services.openssh = {
      enable = true;
      openFirewall = false;
      settings = {
        PasswordAuthentication = false;
        KbdInteractiveAuthentication = false;
        PermitRootLogin = "prohibit-password";
      };
    };
    users.users.root.openssh.authorizedKeys.keys = cfg.operatorKeys;
    networking.firewall = {
      enable = true;
      # Tailscale membership still needs SSH keys and customer-scoped tailnet grants.
      interfaces.tailscale0.allowedTCPPorts = [ 22 ];
      extraCommands = lib.concatMapStringsSep "\n" (
        cidr:
        let
          iptables = if lib.hasInfix ":" cidr then "ip6tables" else "iptables";
        in
        "${iptables} -A nixos-fw -s ${cidr} -p tcp --dport 22 -j nixos-fw-accept"
      ) cfg.operatorCIDRs;
    };
    services.tailscale.enable = true; # Enrollment is an explicit operator step.
    services.journald.extraConfig = "SystemMaxUse=512M\nRuntimeMaxUse=128M";
    nix.settings.experimental-features = [
      "nix-command"
      "flakes"
    ];
    nix.gc = {
      automatic = false;
    }; # Explicit retention/GC after verified rollback points.
    system.autoUpgrade.enable = false; # Reviewed locked-input updates via deploy-rs.
    environment.systemPackages = [
      pkgs.curl
      pkgs.jq
    ];
    time.timeZone = "UTC";
    boot.initrd.availableKernelModules = [
      "virtio_pci"
      "virtio_scsi"
      "virtio_blk"
      "ahci"
      "sd_mod"
    ];
    boot.loader.grub = {
      enable = true;
      # BIOS receives its installation device from Disko's EF02 partition.
      # UEFI uses the portable ESP path, without altering firmware variables.
      devices = lib.mkIf (cfg.bootMode == "uefi") [ "nodev" ];
      efiSupport = cfg.bootMode == "uefi";
      efiInstallAsRemovable = cfg.bootMode == "uefi";
    };
    boot.loader.efi.canTouchEfiVariables = false;
  };
}
