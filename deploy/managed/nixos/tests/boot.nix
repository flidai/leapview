{ pkgs, modules }:
let
  portProbe = import ./port-probe.nix { inherit pkgs; };
in
pkgs.testers.runNixOSTest {
  name = "leapview-managed-host-boot";
  requiredFeatures.kvm = false; # Also runnable with QEMU software emulation.
  defaults = {
    virtualisation.memorySize = pkgs.lib.mkDefault 1024;
    virtualisation.graphics = false;
    system.stateVersion = "26.05";
  };
  nodes = {
    app = { lib, ... }: {
      imports = [ modules.app ];
      leapview = {
        operatorKeys = [ "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnlyNotAnOperatorKey" ];
        operatorCIDRs = [ "192.168.1.0/24" ];
        publicInterface = "eth2";
        privateInterface = "eth1";
        privateAddress = "192.168.1.1";
      };
      boot.loader.grub.enable = lib.mkForce false;
      # The test network supplies a static private interface, not Hetzner DHCP.
      virtualisation.vlans = [
        1
        2
      ];
      systemd.network.networks."10-public" = {
        networkConfig.DHCP = lib.mkForce "no";
        address = [ "192.168.2.1/24" ];
        linkConfig.RequiredForOnline = lib.mkForce "degraded";
      };
      systemd.network.networks."20-private" = {
        networkConfig.DHCP = lib.mkForce "no";
        address = [ "192.168.1.1/24" ];
        linkConfig.RequiredForOnline = lib.mkForce "degraded";
      };
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
      environment.systemPackages = [ pkgs.postgresql_18 ];
      virtualisation.additionalPaths = [ portProbe ];
    };
    database = { lib, ... }: {
      imports = [ modules.database ];
      leapview = {
        operatorKeys = [ "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnlyNotAnOperatorKey" ];
        operatorCIDRs = [ "192.168.1.0/24" ];
        publicInterface = "eth0";
        privateInterface = "eth1";
        privateAddress = "192.168.1.2";
        database = {
          appAddress = "192.168.1.1";
          backupBucket = "test-only";
          backupEndpoint = "not-provisioned.invalid";
          backupRegion = "test-only";
        };
      };
      boot.loader.grub.enable = lib.mkForce false;
      # The test network supplies a static private interface, not Hetzner DHCP.
      systemd.network.networks."10-public".linkConfig.RequiredForOnline = lib.mkForce false;
      systemd.network.networks."20-private" = {
        networkConfig.DHCP = lib.mkForce "no";
        address = [ "192.168.1.2/24" ];
        linkConfig.RequiredForOnline = lib.mkForce "degraded";
      };
      environment.systemPackages = [ pkgs.openssl ];
    };
    outsider = {
      virtualisation.memorySize = 256;
      virtualisation.vlans = [
        1
        2
      ];
      environment.systemPackages = [
        pkgs.netcat-openbsd
        pkgs.curl
      ];
    };
  };
  testScript = ''
    app.start(allow_reboot=True)
    database.start(allow_reboot=True)
    outsider.start()
    app.wait_for_unit("docker.service", timeout=600)
    app.wait_for_unit("sshd.service")
    database.wait_for_unit("sshd.service", timeout=600)
    outsider.wait_for_unit("multi-user.target", timeout=600)

    with subtest("Docker publications cannot bypass ingress policy"):
        app.succeed("docker load -i ${portProbe}")
        app.succeed("docker network create probe")
        # Test translated ports: public 80/443 reach container 8080, while
        # publishing a container's port 80 as 9090 must still be denied.
        app.succeed("docker run -d --name port-probe --restart always --network probe -p 80:8080 -p 443:8080 -p 8080:8080 -p 9090:80 managed-port-probe:fixture")
        app.wait_until_succeeds("curl -s -o /dev/null http://127.0.0.1:8080")
        outsider.succeed("nc -z -w 3 192.168.2.1 80")
        outsider.succeed("nc -z -w 3 192.168.2.1 443")
        outsider.fail("nc -z -w 3 192.168.2.1 8080")
        outsider.fail("nc -z -w 3 192.168.2.1 9090")
        outsider.fail("nc -z -w 3 192.168.1.1 80")
        outsider.fail("nc -z -w 3 192.168.1.1 443")
        outsider.fail("nc -z -w 3 192.168.1.1 8080")
        outsider.fail("nc -z -w 3 192.168.1.1 9090")
        app.succeed("systemctl reload firewall")
        outsider.succeed("nc -z -w 3 192.168.2.1 80")
        outsider.fail("nc -z -w 3 192.168.1.1 8080")
        app.succeed("systemctl restart firewall")
        app.wait_for_unit("docker.service", timeout=600)
        app.wait_until_succeeds("curl -s -o /dev/null http://127.0.0.1:8080")
        outsider.succeed("nc -z -w 3 192.168.2.1 80")
        outsider.fail("nc -z -w 3 192.168.1.1 9090")
        app.succeed("systemctl restart docker")
        app.wait_until_succeeds("curl -s -o /dev/null http://127.0.0.1:8080")
        outsider.succeed("nc -z -w 3 192.168.2.1 443")
        outsider.fail("nc -z -w 3 192.168.1.1 9090")

    with subtest("runtime TLS material is required and stays outside the store"):
        database.fail("test -f /var/lib/leapview-postgres-tls/server.key")
        database.succeed("install -d -m 700 -o postgres -g postgres /var/lib/leapview-postgres-tls")
        database.succeed("openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=database -addext subjectAltName=IP:192.168.1.2 -keyout /var/lib/leapview-postgres-tls/server.key -out /var/lib/leapview-postgres-tls/server.crt")
        database.succeed("chown postgres:postgres /var/lib/leapview-postgres-tls/server.*; chmod 400 /var/lib/leapview-postgres-tls/server.key")
        database.succeed("systemctl reset-failed; systemctl start postgresql")
        database.wait_for_unit("postgresql.service")
        database.succeed("sudo -u postgres psql -XAt -c 'select 1' | grep -qx 1")

    with subtest("application address can connect with verified TLS"):
        database.succeed("sudo -u postgres psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE probe LOGIN PASSWORD 'disposable-test-only'\"")
        database.succeed("sudo -u postgres psql -v ON_ERROR_STOP=1 -c 'CREATE DATABASE leapview_control OWNER probe'")
        ca = database.succeed("cat /var/lib/leapview-postgres-tls/server.crt")
        app.succeed("cat > /tmp/database-ca.crt <<'CERT'\n" + ca + "CERT\n")
        connection = "host=192.168.1.2 dbname=leapview_control user=probe sslmode=verify-full sslrootcert=/tmp/database-ca.crt connect_timeout=5"
        assert app.succeed("PGPASSWORD=disposable-test-only psql -XAt \"" + connection + "\" -c 'select ssl from pg_stat_ssl where pid=pg_backend_pid()'").strip() == "t"
        app.fail("PGPASSWORD=disposable-test-only psql -XAt \"" + connection.replace("verify-full", "disable") + "\" -c 'select 1'")
        outsider.fail("nc -z -w 3 192.168.1.2 5432")

    with subtest("services and database survive a guest reboot"):
        database.reboot()
        database.wait_for_unit("postgresql.service", timeout=600)
        database.succeed("sudo -u postgres psql -XAt -c \"select count(*) from pg_roles where rolname='probe'\" | grep -qx 1")
        app.reboot()
        app.wait_for_unit("docker.service", timeout=600)
        app.wait_for_unit("sshd.service")
        app.wait_until_succeeds("curl -s -o /dev/null http://127.0.0.1:8080")
        outsider.succeed("nc -z -w 3 192.168.2.1 80")
        outsider.fail("nc -z -w 3 192.168.1.1 8080")
  '';
}
