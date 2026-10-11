{ pkgs, modules }:
let
  sshKeys = import "${pkgs.path}/nixos/tests/ssh-keys.nix" pkgs;
  common = address: { lib, ... }: {
    imports = [ ../modules/common.nix ];
    leapview = {
      operatorKeys = [ sshKeys.snakeOilPublicKey ];
      operatorCIDRs = [ "192.168.1.0/24" ];
      publicInterface = "eth0";
      privateInterface = "eth1";
      privateAddress = address;
    };
    services.tailscale.enable = lib.mkForce false;
    # This disposable VM has a fixed virtio PCI root disk. Load its transport
    # and block driver before waiting for the filesystem label in the initrd.
    boot.initrd.kernelModules = lib.mkAfter [
      "virtio_pci"
      "virtio_blk"
    ];
    # The disposable test driver's root shell requires /dev/hvc0 after switch
    # root. Load the console driver explicitly instead of relying on PCI udev.
    boot.kernelModules = lib.mkAfter [ "virtio_console" ];
    systemd.network.networks."10-public".linkConfig.RequiredForOnline = lib.mkForce false;
    systemd.network.networks."20-private" = {
      networkConfig.DHCP = lib.mkForce "no";
      address = [ "${address}/24" ];
      linkConfig.RequiredForOnline = lib.mkForce "degraded";
    };
    environment.systemPackages = [
      pkgs.openssl
      pkgs.python3
      pkgs.restic
      pkgs.openssh
    ];
    users.users.recovery-fixture = {
      isSystemUser = true;
      group = "recovery-fixture";
      home = "/home/recovery-fixture";
      createHome = true;
    };
    users.groups.recovery-fixture = { };
    systemd.tmpfiles.rules = [ "d /var/lib/coordinator-component 0700 root root -" ];
    system.stateVersion = "26.05";
    virtualisation = {
      memorySize = 1024;
      diskSize = 4096;
      graphics = false;
      cores = 1;
    };
  };
  database = address: replacement: { lib, ... }: {
    imports = [
      modules.database
      (common address)
    ];
    leapview.database = {
      appAddress = if replacement then "192.168.1.2" else "192.168.1.1";
      backupBucket = "component-only";
      backupEndpoint = "not-provisioned.invalid";
      backupRegion = "component-only";
      recoveryOwner = "recovery-fixture";
      recoveryRestore = replacement;
    };
    # Fencing checks every installed/retained generation. Direct kernel boot
    # creates no system profile and cannot exercise that production boundary.
    virtualisation = {
      writableStore = true;
      useBootLoader = true;
      directBoot.enable = false;
      diskSize = lib.mkForce 8192;
      fileSystems."/".autoResize = true;
    };
    # The installed image starts at its closure size; grow its partition and
    # filesystem to the declared disk before importing tools and recovery data.
    boot.growPartition = true;
    boot.loader.timeout = 1;
    # Private POSIX transport tests the installed coordinator mechanics only.
    # Customer object-store credentials and protected artifacts remain gates.
    services.pgbackrest.repos = lib.mkForce {
      localhost = {
        type = "posix";
        path = "/var/lib/coordinator-repository";
        retention-full = 2;
      };
    };
    services.pgbackrest.stanzas.default.jobs = lib.mkForce { };
    systemd.tmpfiles.rules = [ "d /var/lib/coordinator-repository 0700 postgres postgres -" ];
    systemd.services.postgresql = {
      unitConfig.ConditionPathExists = [ "/var/lib/leapview-postgres-tls/server.key" ];
      serviceConfig = {
        ReadWritePaths = [ "/var/lib/coordinator-repository" ];
        # Disposable TCG cold init/restore allowance; production is unchanged.
        TimeoutStartSec = 300;
      };
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "leapview-installed-managed-coordinator-component";
  requiredFeatures.kvm = false;
  nodes = {
    source = database "192.168.1.2" false;
    replacement = database "192.168.1.1" true;
    authority = { lib, ... }: {
      imports = [ (common "192.168.1.3") ];
      services.postgresql = {
        enable = true;
        package = pkgs.postgresql_18;
        settings = {
          listen_addresses = lib.mkForce "127.0.0.1,192.168.1.3";
          ssl = true;
          ssl_cert_file = "/var/lib/coordinator-tls/server.crt";
          ssl_key_file = "/var/lib/coordinator-tls/server.key";
          password_encryption = "scram-sha-256";
        };
        authentication = lib.mkForce ''
          local all postgres peer
          local all all reject
          hostssl managed_recovery_authority all 192.168.1.0/24 scram-sha-256
          hostssl managed_recovery_authority all 127.0.0.1/32 scram-sha-256
          host all all 0.0.0.0/0 reject
          host all all ::0/0 reject
        '';
      };
      systemd.services.postgresql = {
        unitConfig.ConditionPathExists = [ "/var/lib/coordinator-tls/server.key" ];
        serviceConfig.TimeoutStartSec = 300;
      };
      systemd.tmpfiles.rules = [ "d /var/lib/coordinator-tls 0700 postgres postgres -" ];
      networking.firewall.interfaces.eth1.allowedTCPPorts = [ 5432 ];
    };
  };
  testScript = ''
    postgres_bin = "${pkgs.postgresql_18}/bin"
    pgbackrest = "${pkgs.pgbackrest}/bin/pgbackrest"
    restic = "${pkgs.restic}/bin/restic"
    ssh = "${pkgs.openssh}/bin/ssh"
    fixture_helpers = "${./managed-coordinator-fixture.py}"
    artifact_reference = "${./managed-coordinator-artifact.json}"
    fixture_ssh_key = "${sshKeys.snakeOilPrivateKey}"
    ${builtins.readFile ./managed-coordinator-journey.py}
  '';
}
