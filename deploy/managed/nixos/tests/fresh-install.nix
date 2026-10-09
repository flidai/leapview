{
  pkgs,
  disko,
  host,
  bootMode,
}:
let
  lib = pkgs.lib;
  selected =
    (host.extendModules {
      modules = [ { leapview.bootMode = bootMode; } ];
    }).config;
  sshKeys = import "${pkgs.path}/nixos/tests/ssh-keys.nix" pkgs;
  efi = bootMode == "uefi";
  test = disko.lib.testLib.makeDiskoTest {
    inherit pkgs efi;
    name = "leapview-common-fresh-${bootMode}";
    # The locked upstream harness formats a blank second disk, mounts it,
    # installs the real bootloader with nixos-enter, stops the installer, and
    # boots that installed disk in a new BIOS/OVMF machine.
    disko-config = {
      disko.devices = selected.disko.devices;
    };
    extraSystemConfig = {
      imports = [ ../modules/common.nix ];
      leapview = {
        inherit bootMode;
        operatorKeys = [ sshKeys.snakeOilPublicKey ];
        operatorCIDRs = [ "192.168.1.0/24" ];
        privateAddress = "192.168.1.1";
      };
      # Disable the upstream harness's EFI systemd-boot default: production
      # common hosts use GRUB in both modes.
      boot.loader.systemd-boot.enable = lib.mkForce false;
      nix.settings.substituters = lib.mkForce [ ];
      system.stateVersion = "26.05";
      environment.systemPackages = [ pkgs.util-linux ];
      environment.etc."leapview-common-install-scope".text = "common-os-only";
    };
    postDisko = ''
      machine.succeed("test $(findmnt -n -o FSTYPE /mnt) = ext4")
      machine.succeed("test -e /mnt/home/testfile")
    '';
    extraTestScript = ''
      machine.wait_for_unit("multi-user.target")
      machine.succeed("test $(findmnt -n -o FSTYPE /) = ext4")
      machine.succeed("test $(cat /etc/leapview-common-install-scope) = common-os-only")
      machine.succeed("test -e /home/testfile")
      machine.succeed("test -L /nix/var/nix/profiles/system")
      machine.succeed("nix show-config | grep -x 'substituters = '")
      ${lib.optionalString efi ''
        machine.succeed("test -d /sys/firmware/efi")
        machine.succeed("test $(findmnt -n -o FSTYPE /boot) = vfat")
        machine.succeed("test -f /boot/EFI/BOOT/BOOTX64.EFI")
        machine.succeed("test $(lsblk -n -o PARTTYPE /dev/vda1) = c12a7328-f81f-11d2-ba4b-00a0c93ec93b")
      ''}
      ${lib.optionalString (!efi) ''
        machine.succeed("test ! -d /sys/firmware/efi")
        machine.succeed("test $(lsblk -n -o PARTTYPE /dev/vda1) = 21686148-6449-6e6f-744e-656564454649")
      ''}
      # Retained local closures permit boot without public caches. This
      # harness mounts the test store; it is not a self-contained backup test.
      before = machine.succeed("readlink /nix/var/nix/profiles/system").strip()
      machine.succeed("printf preserved > /var/lib/leapview-common-install-retained")
      machine.shutdown()
      machine.start()
      machine.wait_for_unit("multi-user.target")
      assert machine.succeed("readlink /nix/var/nix/profiles/system").strip() == before
      machine.succeed("test $(cat /var/lib/leapview-common-install-retained) = preserved")
      machine.succeed("test -e /home/testfile")
    '';
  };
in
test.overrideTestDerivation (old: {
  # The upstream QEMU command explicitly supports kvm:tcg fallback.
  requiredSystemFeatures = builtins.filter (feature: feature != "kvm") (
    old.requiredSystemFeatures or [ ]
  );
})
