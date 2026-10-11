{ pkgs, modules }:
let
  sshKeys = import "${pkgs.path}/nixos/tests/ssh-keys.nix" pkgs;
  node = address: { lib, ... }: {
    imports = [ modules.database ];
    leapview = {
      operatorKeys = [ sshKeys.snakeOilPublicKey ];
      operatorCIDRs = [ "192.168.1.0/24" ];
      publicInterface = "eth0";
      privateInterface = "eth1";
      privateAddress = address;
      database = {
        appAddress = if address == "192.168.1.1" then "192.168.1.2" else "192.168.1.1";
        backupBucket = "component-only";
        backupEndpoint = "not-provisioned.invalid";
        backupRegion = "component-only";
        recoveryOwner = "recovery-fixture";
      };
    };
    users.users.recovery-fixture = {
      isSystemUser = true;
      group = "recovery-fixture";
    };
    users.groups.recovery-fixture = { };
    users.users.unrelated-fixture = {
      isSystemUser = true;
      group = "unrelated-fixture";
    };
    users.groups.unrelated-fixture = { };
    # This component uses the private guest network and pinned fixture SSH.
    # Do not contact an external access control plane without enrolled keys.
    services.tailscale.enable = lib.mkForce false;
    # This isolated POSIX repository is component evidence, not the production
    # object-store account, credential isolation, retention or recovery gate.
    services.pgbackrest.repos = lib.mkForce {
      localhost = {
        type = "posix";
        path = "/var/lib/promotion-component/repo";
        retention-full = 2;
      };
    };
    services.pgbackrest.stanzas.default.jobs = lib.mkForce { };
    systemd.tmpfiles.rules = [
      "d /var/lib/promotion-component 0700 postgres postgres -"
      "d /var/lib/promotion-component/repo 0700 postgres postgres -"
    ];
    systemd.services.postgresql.serviceConfig.ReadWritePaths = [ "/var/lib/promotion-component/repo" ];
    # Cold initdb exceeds the upstream 90-second startup limit under QEMU TCG.
    # Bound only this disposable fixture; retain the production service timeout.
    systemd.services.postgresql.serviceConfig.TimeoutStartSec = 300;
    systemd.network.networks."10-public".linkConfig.RequiredForOnline = lib.mkForce false;
    systemd.network.networks."20-private" = {
      networkConfig.DHCP = lib.mkForce "no";
      address = [ "${address}/24" ];
      linkConfig.RequiredForOnline = lib.mkForce "degraded";
    };
    environment.systemPackages = [
      pkgs.openssl
      pkgs.netcat-openbsd
    ];
    system.stateVersion = "26.05";
    virtualisation = {
      memorySize = 768;
      diskSize = 2048;
      graphics = false;
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "leapview-installed-postgresql-promotion-components";
  requiredFeatures.kvm = false;
  nodes = {
    replacement = node "192.168.1.1";
    source = node "192.168.1.2";
  };
  testScript = ''
    postgres_bin = "${pkgs.postgresql_18}/bin"
    pgbackrest = "${pkgs.pgbackrest}/bin/pgbackrest"
    fixture_ssh_key = "${sshKeys.snakeOilPrivateKey}"
    ${builtins.readFile ./postgres-promotion-journey.py}
  '';
}
