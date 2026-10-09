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
  rejectedOperatorCIDRs =
    builtins.all
      (
        cidr:
        let
          invalid =
            (hosts.example-app.extendModules {
              modules = [ ({ lib, ... }: { leapview.operatorCIDRs = lib.mkForce [ cidr ]; }) ];
            }).config;
        in
        !(builtins.all (item: item.assertion) invalid.assertions)
      )
      [
        "0.0.0.0/0"
        "0.0.0.0/00"
        "::/0"
        "::/000"
        "203.0.113.10/33"
        "2001:db8::1/129"
        "203.0.113.10"
        "999.0.113.10/32"
        "203.0.113/24"
        "203.000.113.10/32"
        ":::1/128"
      ];
  validIPv6Operator =
    (hosts.example-app.extendModules {
      modules = [ ({ lib, ... }: { leapview.operatorCIDRs = lib.mkForce [ "2001:db8::42/128" ]; }) ];
    }).config;
  uefiApp =
    (hosts.example-app.extendModules {
      modules = [ { leapview.bootMode = "uefi"; } ];
    }).config;
  uefiDatabase =
    (hosts.example-database.extendModules {
      modules = [ { leapview.bootMode = "uefi"; } ];
    }).config;
  checks = [
    (app.leapview.bootMode == "bios")
    (app.disko.devices.disk.system.content.partitions.boot.type == "EF02")
    (!app.boot.loader.grub.efiSupport)
    (uefiApp.disko.devices.disk.system.content.partitions.boot.type == "EF00")
    (uefiApp.fileSystems."/boot".fsType == "vfat")
    (uefiApp.boot.loader.grub.devices == [ "nodev" ])
    uefiApp.boot.loader.grub.efiSupport
    uefiApp.boot.loader.grub.efiInstallAsRemovable
    (!uefiApp.boot.loader.efi.canTouchEfiVariables)
    uefiDatabase.boot.loader.grub.efiSupport
    (uefiDatabase.fileSystems."/boot".fsType == "vfat")
    (!(builtins.all (item: item.assertion) noOperator.assertions))
    rejectedOperatorCIDRs
    (builtins.all (item: item.assertion) validIPv6Operator.assertions)
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
