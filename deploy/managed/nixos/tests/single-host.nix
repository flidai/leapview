{
  pkgs,
  module,
}:
let
  clientImage = pkgs.dockerTools.buildLayeredImage {
    name = "leapview-single-host-postgres-client";
    tag = "fixture";
    contents = [ pkgs.postgresql_18 ];
    config = {
      Entrypoint = [ "${pkgs.postgresql_18}/bin/psql" ];
      User = "0:0";
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "leapview-managed-single-host-database-network";
  requiredFeatures.kvm = false;
  defaults = {
    virtualisation.memorySize = pkgs.lib.mkDefault 1536;
    virtualisation.graphics = false;
    system.stateVersion = "26.05";
  };
  nodes = {
    single =
      { lib, ... }:
      {
        imports = [ module ];
        # TCG can exceed dockerd's internal containerd startup deadline. Start the
        # same daemon as a separate unit for this emulated test only.
        virtualisation.containerd.enable = true;
        virtualisation.docker.daemon.settings.containerd = "/run/containerd/containerd.sock";
        systemd.services.docker = {
          requires = [ "containerd.service" ];
          after = [ "containerd.service" ];
          serviceConfig.TimeoutStartSec = 300;
        };
        systemd.services.containerd.serviceConfig.TimeoutStartSec = 300;
        services.tailscale.enable = lib.mkForce false;
        leapview = {
          operatorKeys = [ "ssh-ed25519 test-only" ];
          operatorCIDRs = [ "192.168.2.0/24" ];
          publicInterface = "eth1";
        };
        virtualisation.vlans = [ 2 ];
        virtualisation.additionalPaths = [ clientImage ];
        systemd.network.networks."10-public" = {
          networkConfig.DHCP = lib.mkForce "no";
          address = [ "192.168.2.1/24" ];
          linkConfig.RequiredForOnline = lib.mkForce "degraded";
        };
        systemd.services.leapview-test-postgresql-tls = {
          description = "Disposable TLS material for the single-host PostgreSQL test";
          wantedBy = [ "postgresql.service" ];
          before = [ "postgresql.service" ];
          path = [
            pkgs.coreutils
            pkgs.openssl
            pkgs.util-linux
          ];
          serviceConfig = {
            Type = "oneshot";
            RemainAfterExit = true;
          };
          script = ''
            install -d -m 0700 -o postgres -g postgres /var/lib/leapview-postgres-tls
            ${pkgs.openssl}/bin/openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
              -subj /CN=172.30.0.1 -addext subjectAltName=IP:172.30.0.1 \
              -keyout /var/lib/leapview-postgres-tls/server.key \
              -out /var/lib/leapview-postgres-tls/server.crt
            chown postgres:postgres /var/lib/leapview-postgres-tls/server.*
            chmod 0400 /var/lib/leapview-postgres-tls/server.key
            chmod 0444 /var/lib/leapview-postgres-tls/server.crt
          '';
        };
        systemd.services.postgresql = {
          requires = [ "leapview-test-postgresql-tls.service" ];
          after = [ "leapview-test-postgresql-tls.service" ];
          serviceConfig.TimeoutStartSec = 300;
        };
        environment.systemPackages = [
          pkgs.iproute2
          pkgs.netcat-openbsd
          pkgs.postgresql_18
        ];
      };
    outsider = {
      virtualisation.vlans = [ 2 ];
      environment.systemPackages = [ pkgs.netcat-openbsd ];
    };
  };
  testScript = ''
    single.start(allow_reboot=True)
    outsider.start()
    single.wait_for_unit("docker.service", timeout=600)
    single.wait_for_unit("leapview-kamal-network.service", timeout=120)
    single.wait_for_unit("postgresql.service", timeout=300)
    outsider.wait_for_unit("network.target", timeout=120)
    single.wait_until_succeeds("ip -4 -o addr show dev eth1 | grep -Fq '192.168.2.1/24'")
    single.succeed("docker load -i ${clientImage}")
    single.succeed("sudo -u postgres psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE probe LOGIN PASSWORD 'disposable-test-only'\"")
    single.succeed("sudo -u postgres createdb --owner=probe leapview_control")
    connection = "postgresql://probe:disposable-test-only@172.30.0.1/leapview_control?sslmode=verify-full&sslrootcert=/var/lib/leapview-postgres-tls/server.crt&connect_timeout=5"
    client = "docker run --rm --network kamal -v /var/lib/leapview-postgres-tls/server.crt:/run/postgres-ca.crt:ro leapview-single-host-postgres-client:fixture"
    connection = connection.replace("/var/lib/leapview-postgres-tls/server.crt", "/run/postgres-ca.crt")
    single.succeed(client + " -XAt -d '" + connection + "' -c 'SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()' | grep -qx t")
    single.succeed(client + " -XAt -d '" + connection + "' -c \"CREATE TABLE retained (value text NOT NULL); INSERT INTO retained VALUES ('kept')\"")
    single.fail(client + " -XAt -d '" + connection.replace("sslmode=verify-full", "sslmode=disable") + "' -c 'SELECT 1'")
    # Restarting Docker exercises the production lifecycle: PostgreSQL and the
    # managed bridge must restart with Docker, with the bridge validated first.
    single.succeed("systemctl restart docker.service")
    single.wait_for_unit("docker.service", timeout=600)
    single.wait_for_unit("leapview-kamal-network.service", timeout=120)
    single.wait_for_unit("postgresql.service", timeout=300)
    single.succeed("docker network inspect --format '{{(index .IPAM.Config 0).Subnet}} {{(index .IPAM.Config 0).Gateway}}' kamal | grep -qx '172.30.0.0/24 172.30.0.1'")
    single.succeed("ip -o link show dev br-lv-kamal0")
    single.succeed(client + " -XAt -d '" + connection + "' -c 'SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()' | grep -qx t")
    single.succeed("ss -lnt | grep -Fq '172.30.0.1:5432'")
    single.fail("ss -lnt | grep -Fq '0.0.0.0:5432'")
    outsider.fail("nc -z -w 3 192.168.2.1 5432")

    # A different bridge cannot reach the same PostgreSQL listener.
    single.succeed("docker network create single-host-outsider >/dev/null")
    other_client = client.replace("--network kamal", "--network single-host-outsider")
    single.fail(other_client + " -XAt -d '" + connection + "' -c 'SELECT 1'")
    single.succeed("docker network rm single-host-outsider >/dev/null")

    single.reboot()
    single.wait_for_unit("docker.service", timeout=600)
    single.wait_for_unit("leapview-kamal-network.service", timeout=120)
    single.wait_for_unit("postgresql.service", timeout=300)
    single.succeed(client + " -XAt -d '" + connection + "' -c 'SELECT value FROM retained' | grep -qx kept")
    outsider.fail("nc -z -w 3 192.168.2.1 5432")

    # A conflicting pre-existing network must fail closed and remain untouched.
    single.succeed("systemctl stop leapview-kamal-network.service")
    single.succeed("docker network rm kamal >/dev/null")
    single.succeed("docker network create --driver bridge --subnet 172.31.0.0/24 --gateway 172.31.0.1 kamal >/dev/null")
    single.fail("systemctl start leapview-kamal-network.service")
    single.succeed("docker network inspect --format '{{(index .IPAM.Config 0).Subnet}}' kamal | grep -qx '172.31.0.0/24'")
  '';
}
