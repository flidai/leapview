{
  pkgs,
  modules,
  deployRs,
}:
let
  system = "x86_64-linux";
  sshKeys = import "${pkgs.path}/nixos/tests/ssh-keys.nix" pkgs;
  portProbe = import ./port-probe.nix { inherit pkgs; };
  markerModule = generation: {
    environment.etc."leapview-managed-generation".text = pkgs.lib.mkForce generation;
  };
  variants = nodes: {
    appUpdate = nodes.app.specialisation.update.configuration;
    databaseUpdate = nodes.database.specialisation.update.configuration;
    appFailedActivation = nodes.app.specialisation.failed-activation.configuration;
    databaseUnconfirmed = nodes.database.specialisation.unconfirmed.configuration;
  };
  activate = config: deployRs.lib.${system}.activate.nixos { inherit config; };
  mkDeploymentNode = hostname: activation: {
    inherit hostname;
    sshUser = "root";
    autoRollback = true;
    magicRollback = true;
    confirmTimeout = 5;
    profiles.system = {
      path = builtins.toString activation;
      drvPath = activation.drvPath;
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "leapview-managed-host-update-recovery";
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
        operatorKeys = [ sshKeys.snakeOilPublicKey ];
        operatorCIDRs = [ "192.168.1.0/24" ];
        publicInterface = "eth2";
        privateInterface = "eth1";
        privateAddress = "192.168.1.1";
      };
      environment.etc."leapview-managed-generation".text = "installed";
      specialisation = {
        update.configuration = markerModule "updated";
        "failed-activation".configuration = {
          imports = [ (markerModule "failed-activation") ];
          system.activationScripts.managedUpdateFailure = {
            deps = [ "etc" ];
            text = ''
              install -d -m 0755 /var/lib/leapview
              echo candidate-activation-started > /var/lib/leapview/managed-update-attempt
              exit 1
            '';
          };
        };
      };
      virtualisation.writableStore = true;
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
        operatorKeys = [ sshKeys.snakeOilPublicKey ];
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
      environment.etc."leapview-managed-generation".text = "installed";
      specialisation = {
        update.configuration = markerModule "updated";
        unconfirmed.configuration = {
          imports = [ (markerModule "unconfirmed") ];
          system.activationScripts.managedUpdateAttempt = {
            deps = [ "etc" ];
            text = ''
              install -d -m 0755 /var/lib/leapview-update-test
              echo candidate-activation-started > /var/lib/leapview-update-test/attempt
            '';
          };
        };
      };
      virtualisation.writableStore = true;
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
    operator =
      { nodes, ... }:
      let
        targets = variants nodes;
      in
      {
        virtualisation.memorySize = 512;
        virtualisation.vlans = [ 1 ];
        # Keep this node after database in the harness's name ordering so the
        # database retains its explicitly configured 192.168.1.2 address.
        virtualisation.writableStore = true;
        # deploy-rs builds from drvPath even when the activation output is
        # prebuilt. Register derivations and their inputs for offline guest Nix.
        virtualisation.additionalPaths =
          pkgs.lib.concatMap
            (
              config:
              let
                activation = activate config;
              in
              [
                activation
                activation.drvPath
              ]
            )
            [
              targets.appUpdate
              targets.databaseUpdate
              targets.appFailedActivation
              targets.databaseUnconfirmed
            ];
        nix.settings = {
          experimental-features = [
            "nix-command"
            "flakes"
          ];
          substituters = pkgs.lib.mkForce [ ];
        };
        environment.systemPackages = [
          deployRs.packages.${system}.default
          pkgs.openssh
        ];
      };
  };
  testScript =
    { nodes }:
    let
      targets = variants nodes;
      appActivation = activate targets.appUpdate;
      databaseActivation = activate targets.databaseUpdate;
      failedAppActivation = activate targets.appFailedActivation;
      unconfirmedDatabaseActivation = activate targets.databaseUnconfirmed;
      deploymentData = {
        deploy.nodes = {
          # These RFC 1918 addresses exist only on this disposable NixOS test VLAN.
          # Never import the example inventory's real-host settings into this target list.
          app = mkDeploymentNode "192.168.1.1" appActivation;
          database = mkDeploymentNode "192.168.1.2" databaseActivation;
          "app-failed-activation" = mkDeploymentNode "192.168.1.1" failedAppActivation;
          database-unconfirmed = mkDeploymentNode "192.168.1.2" unconfirmedDatabaseActivation;
        };
      };
      deploymentFile = pkgs.writeTextDir "default.nix" (
        "builtins.fromJSON ${builtins.toJSON (builtins.toJSON deploymentData)}"
      );
    in
    ''
      app.start(allow_reboot=True)
      database.start(allow_reboot=True)
      outsider.start()
      operator.start()
      app.wait_for_unit("docker.service", timeout=600)
      app.wait_for_unit("sshd.service")
      database.wait_for_unit("sshd.service", timeout=600)
      outsider.wait_for_unit("multi-user.target", timeout=600)
      operator.wait_for_unit("network.target")
      operator.succeed("ip -4 -o addr show dev eth1 | grep -Fq '192.168.1.3/24'")

      operator.succeed("install -d -m 700 /root/.ssh")
      operator.succeed("cp --no-preserve=mode ${sshKeys.snakeOilPrivateKey} /root/.ssh/id_ed25519")
      operator.succeed("chmod 600 /root/.ssh/id_ed25519")
      app_host_key = app.succeed("awk '{print $1, $2}' /etc/ssh/ssh_host_ed25519_key.pub").strip()
      database_host_key = database.succeed("awk '{print $1, $2}' /etc/ssh/ssh_host_ed25519_key.pub").strip()
      operator.succeed(
          "cat > /root/.ssh/known_hosts <<'HOSTS'\n"
          + "192.168.1.1 " + app_host_key + "\n"
          + "192.168.1.2 " + database_host_key + "\n"
          + "HOSTS\n"
      )
      operator.succeed("chmod 600 /root/.ssh/known_hosts")
      operator.succeed(
          "cat > /root/.ssh/config <<'SSH'\n"
          "Host 192.168.1.1 192.168.1.2\n"
          "  IdentityFile /root/.ssh/id_ed25519\n"
          "  UserKnownHostsFile /root/.ssh/known_hosts\n"
          "  StrictHostKeyChecking yes\n"
          "  ConnectTimeout 5\n"
          "SSH\n"
      )
      operator.succeed("chmod 600 /root/.ssh/config")
      operator.succeed("ssh root@192.168.1.1 true")
      operator.succeed("ssh root@192.168.1.2 true")

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

      # This QEMU harness reboots from its test image. Post-update assertions below
      # cover the active system generation and rollback, not bootloader selection.
      with subtest("deploy-rs updates installed app and database hosts"):
          app.succeed("echo app-state > /var/lib/leapview/managed-update-state")
          database.succeed("sudo -u postgres psql -d leapview_control -v ON_ERROR_STOP=1 -c 'CREATE TABLE managed_update_state (value text NOT NULL); GRANT SELECT ON managed_update_state TO probe'")
          database.succeed("sudo -u postgres psql -d leapview_control -v ON_ERROR_STOP=1 -c \"INSERT INTO managed_update_state VALUES ('database-state')\"")
          installed_app = app.succeed("readlink -f /run/current-system").strip()
          installed_database = database.succeed("readlink -f /run/current-system").strip()

          operator.succeed("deploy --file ${deploymentFile} --targets app")
          operator.succeed("deploy --file ${deploymentFile} --targets database")
          assert installed_app != "${targets.appUpdate.system.build.toplevel}"
          assert installed_database != "${targets.databaseUpdate.system.build.toplevel}"
          assert app.succeed("readlink -f /run/current-system").strip() == "${targets.appUpdate.system.build.toplevel}"
          assert database.succeed("readlink -f /run/current-system").strip() == "${targets.databaseUpdate.system.build.toplevel}"
          app.succeed("grep -qx updated /etc/leapview-managed-generation")
          database.succeed("grep -qx updated /etc/leapview-managed-generation")
          assert app.succeed("cat /var/lib/leapview/managed-update-state").strip() == "app-state"
          assert database.succeed("sudo -u postgres psql -d leapview_control -XAt -c 'SELECT value FROM managed_update_state'").strip() == "database-state"
          operator.succeed("ssh root@192.168.1.1 true")
          operator.succeed("ssh root@192.168.1.2 true")
          assert app.succeed("readlink -f /nix/var/nix/profiles/system").strip() == "${appActivation}"
          assert database.succeed("readlink -f /nix/var/nix/profiles/system").strip() == "${databaseActivation}"
          app.wait_until_succeeds("curl -s -o /dev/null http://127.0.0.1:8080")
          outsider.succeed("nc -z -w 3 192.168.2.1 80")
          outsider.fail("nc -z -w 3 192.168.1.1 8080")
          outsider.fail("nc -z -w 3 192.168.1.2 5432")
          assert app.succeed("PGPASSWORD=disposable-test-only psql -XAt \"" + connection + "\" -c 'SELECT value FROM managed_update_state'").strip() == "database-state"

      with subtest("deploy-rs autoRollback restores a failed activation"):
          app.succeed("rm -f /var/lib/leapview/managed-update-attempt")
          previous = app.succeed("readlink -f /run/current-system").strip()
          previous_profile = app.succeed("readlink -f /nix/var/nix/profiles/system").strip()
          assert previous == "${targets.appUpdate.system.build.toplevel}"
          out = operator.fail("deploy --file ${deploymentFile} --targets app-failed-activation", timeout=120)
          assert "candidate-activation-started" in app.succeed("cat /var/lib/leapview/managed-update-attempt"), out
          assert app.succeed("readlink -f /run/current-system").strip() == previous, out
          assert app.succeed("readlink -f /nix/var/nix/profiles/system").strip() == previous_profile, out
          app.succeed("grep -qx updated /etc/leapview-managed-generation")
          assert app.succeed("cat /var/lib/leapview/managed-update-state").strip() == "app-state"
          app.wait_for_unit("docker.service", timeout=120)
          operator.succeed("ssh root@192.168.1.1 true")
          app.wait_until_succeeds("curl -s -o /dev/null http://127.0.0.1:8080")
          outsider.succeed("nc -z -w 3 192.168.2.1 443")
          outsider.fail("nc -z -w 3 192.168.1.1 9090")

      with subtest("deploy-rs magicRollback restores an unconfirmed activation"):
          previous = database.succeed("readlink -f /run/current-system").strip()
          previous_profile = database.succeed("readlink -f /nix/var/nix/profiles/system").strip()
          assert previous == "${targets.databaseUpdate.system.build.toplevel}"
          operator.succeed("nix copy --to ssh://root@192.168.1.2 ${unconfirmedDatabaseActivation} --no-check-sigs", timeout=120)
          out = operator.fail(
              "ssh root@192.168.1.2 ${unconfirmedDatabaseActivation}/activate-rs activate ${unconfirmedDatabaseActivation} --profile-path /nix/var/nix/profiles/system --temp-path /tmp/deploy-rs-managed-update-test --confirm-timeout 2 --magic-rollback --auto-rollback",
              timeout=60,
          )
          assert "Timeout elapsed for confirmation" in out, out
          assert "candidate-activation-started" in database.succeed("cat /var/lib/leapview-update-test/attempt"), out
          assert database.succeed("readlink -f /run/current-system").strip() == previous, out
          assert database.succeed("readlink -f /nix/var/nix/profiles/system").strip() == previous_profile, out
          database.succeed("grep -qx updated /etc/leapview-managed-generation")
          assert database.succeed("sudo -u postgres psql -d leapview_control -XAt -c 'SELECT value FROM managed_update_state'").strip() == "database-state"
          database.wait_for_unit("postgresql.service", timeout=120)
          operator.succeed("ssh root@192.168.1.2 true")
          outsider.fail("nc -z -w 3 192.168.1.2 5432")
          assert app.succeed("PGPASSWORD=disposable-test-only psql -XAt \"" + connection + "\" -c 'SELECT value FROM managed_update_state'").strip() == "database-state"
    '';
}
