{ pkgs, hosts }:
let
  app = hosts.example-app.config;
  db = hosts.example-database.config;
  restic =
    (hosts.example-app.extendModules {
      modules = [
        {
          leapview.fileBackup = {
            enable = true;
            paths = [ "/var/lib/leapview/uploads" ];
            repository = "s3:https://example.invalid/files";
          };
        }
      ];
    }).config;
  noOperator =
    (hosts.example-app.extendModules {
      modules = [ ({ lib, ... }: { leapview.operatorKeys = lib.mkForce [ ]; }) ];
    }).config;
  checks = [
    (!(builtins.all (item: item.assertion) noOperator.assertions))
    (
      restic.services.restic.backups.retained-files.passwordFile
      == "/var/lib/leapview-secrets/restic-password"
    )
    (!restic.services.restic.backups.retained-files.initialize)

    app.virtualisation.docker.enable
    (app.virtualisation.docker.daemon.settings."firewall-backend" == "iptables")
    (!app.virtualisation.docker.daemon.settings."userland-proxy")
    (builtins.elem "firewall.service" app.systemd.services.docker.requires)
    (builtins.elem "firewall.service" app.systemd.services.docker.after)
    (builtins.elem "firewall.service" app.systemd.services.docker.partOf)
    (app.networking.firewall.allowedTCPPorts == [ ])
    (
      app.networking.firewall.interfaces.${app.leapview.publicInterface}.allowedTCPPorts == [
        80
        443
      ]
    )
    (!db.virtualisation.docker.enable)
    (app.virtualisation.oci-containers.containers == { })
    (db.services.postgresql.package.psqlSchema == "18")
    (builtins.elem "network-online.target" db.systemd.services.postgresql.after)
    (db.services.postgresql.settings.listen_addresses == "127.0.0.1,10.42.0.20")
    db.services.postgresql.settings.ssl
    (db.services.postgresql.settings.archive_mode == "on")
    (!(builtins.elem 5432 db.networking.firewall.allowedTCPPorts))
    (!app.system.autoUpgrade.enable)
    (!db.system.autoUpgrade.enable)
    (!app.virtualisation.docker.autoPrune.enable)
    (app.services.restic.backups == { })
    (builtins.length (builtins.attrNames db.services.pgbackrest.stanzas.default.jobs) == 2)
    (
      db.environment.etc."pgbackrest/conf.d/credentials.conf".source
      == "/var/lib/leapview-backup-secrets/pgbackrest.conf"
    )
  ];
in
assert builtins.all (check: check) checks;
pkgs.runCommand "leapview-host-contracts" { } ''touch "$out"''
