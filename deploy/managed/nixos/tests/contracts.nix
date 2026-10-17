{ pkgs, hosts }:
let
  app = hosts.example-app.config;
  db = hosts.example-database.config;
  singleHost = hosts.example-single-host.config;
  withoutBackups =
    (hosts.example-database.extendModules {
      modules = [
        ({ lib, ... }: {
          leapview.database = {
            backup.enable = false;
            backupBucket = lib.mkForce null;
            backupEndpoint = lib.mkForce null;
            backupRegion = lib.mkForce null;
          };
        })
      ];
    }).config;
  restoreWithoutBackups =
    (hosts.example-database.extendModules {
      modules = [
        {
          leapview.database = {
            backup.enable = false;
            recoveryRestore = true;
            recoveryOwner = "recovery-fixture";
          };
          users.users.recovery-fixture = {
            isSystemUser = true;
            group = "recovery-fixture";
          };
          users.groups.recovery-fixture = { };
        }
      ];
    }).config;
  missingBackupDestination =
    (hosts.example-database.extendModules {
      modules = [ ({ lib, ... }: { leapview.database.backupBucket = lib.mkForce null; }) ];
    }).config;
  restore =
    (hosts.example-database.extendModules {
      modules = [
        {
          leapview.database = {
            recoveryRestore = true;
            recoveryOwner = "recovery-fixture";
          };
          users.users.recovery-fixture = {
            isSystemUser = true;
            group = "recovery-fixture";
          };
          users.groups.recovery-fixture = { };
        }
      ];
    }).config;
  restoreWithoutOwner =
    (hosts.example-database.extendModules {
      modules = [ { leapview.database.recoveryRestore = true; } ];
    }).config;
  singleHostWithApplicationAddress =
    (hosts.example-single-host.extendModules {
      modules = [ ({ lib, ... }: { leapview.database.appAddress = lib.mkForce "10.42.0.10"; }) ];
    }).config;
  singleHostWithoutDocker =
    (hosts.example-single-host.extendModules {
      modules = [ ({ lib, ... }: { virtualisation.docker.enable = lib.mkForce false; }) ];
    }).config;
  singleHostWithoutPrivateAddress =
    (hosts.example-single-host.extendModules {
      modules = [ ({ lib, ... }: { leapview.privateNetwork.enable = lib.mkForce true; }) ];
    }).config;
  singleHostBackupWithoutDestination =
    (hosts.example-single-host.extendModules {
      modules = [ ({ lib, ... }: { leapview.database.backup.enable = lib.mkForce true; }) ];
    }).config;
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
    (!(builtins.all (item: item.assertion) missingBackupDestination.assertions))
    (builtins.all (item: item.assertion) withoutBackups.assertions)
    (!(builtins.all (item: item.assertion) restoreWithoutBackups.assertions))
    withoutBackups.services.postgresql.enable
    withoutBackups.services.postgresql.settings.ssl
    (
      withoutBackups.services.postgresql.settings.listen_addresses
      == db.services.postgresql.settings.listen_addresses
    )
    (withoutBackups.services.postgresql.authentication == db.services.postgresql.authentication)
    (!withoutBackups.services.pgbackrest.enable)
    ((withoutBackups.services.postgresql.settings.archive_mode or "off") == "off")
    ((withoutBackups.services.postgresql.settings.archive_command or "") == "")
    (!(withoutBackups.environment.etc ? "pgbackrest/conf.d/credentials.conf"))
    (!(withoutBackups.systemd.services ? "pgbackrest-default-daily"))
    (!(withoutBackups.systemd.services ? "pgbackrest-default-weekly"))
    (!(withoutBackups.systemd.timers ? "pgbackrest-default-daily"))
    (!(withoutBackups.systemd.timers ? "pgbackrest-default-weekly"))
    (
      !(builtins.any (
        package: (package.name or "") == "leapview-postgres-promote"
      ) withoutBackups.environment.systemPackages)
    )
    (
      withoutBackups.systemd.services.postgresql.unitConfig.ConditionPathExists
      == db.systemd.services.postgresql.unitConfig.ConditionPathExists
    )
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
    (!(builtins.all (item: item.assertion) restoreWithoutOwner.assertions))
    (builtins.all (item: item.assertion) restore.assertions)
    (!(builtins.all (item: item.assertion) singleHostWithApplicationAddress.assertions))
    (!(builtins.all (item: item.assertion) singleHostWithoutDocker.assertions))
    (!(builtins.all (item: item.assertion) singleHostWithoutPrivateAddress.assertions))
    (!(builtins.all (item: item.assertion) singleHostBackupWithoutDestination.assertions))
    (restore.systemd.services.postgresql.wantedBy == [ ])
    (restore.systemd.targets.postgresql.wantedBy == [ ])
    (restore.services.pgbackrest.stanzas.default.jobs == { })
    (
      restore.systemd.services.postgresql.unitConfig.ConditionPathExists == [
        "!/var/lib/leapview-recovery/postgresql-fence.json"
        "/var/lib/leapview-recovery-adoption/postgresql-restore-materialized"
      ]
    )
    (restore.users.users.recovery-fixture.extraGroups == [ "leapview-recovery-adoption" ])
    (builtins.any (
      rule:
      builtins.elem "recovery-fixture" rule.users
      && builtins.elem {
        command = "/run/current-system/sw/bin/leapview-postgres-restore";
        options = [ "NOPASSWD" ];
      } rule.commands
    ) restore.security.sudo.extraRules)
    (app.virtualisation.oci-containers.containers == { })
    (db.services.postgresql.package.psqlSchema == "18")
    (db.systemd.services.postgresql.serviceConfig.StateDirectoryMode == "0750")
    (db.systemd.services.postgresql.serviceConfig.User == "postgres")
    (db.systemd.services.postgresql.serviceConfig.Group == "postgres")
    (builtins.elem "network-online.target" db.systemd.services.postgresql.after)
    (
      db.systemd.services.postgresql.unitConfig.ConditionPathExists
      == "!/var/lib/leapview-recovery/postgresql-fence.json"
    )
    (db.services.postgresql.settings.listen_addresses == "127.0.0.1,10.42.0.20")
    (pkgs.lib.hasInfix "-s 10.42.0.10/32 -d 10.42.0.20/32" db.networking.firewall.extraCommands)
    (!(pkgs.lib.hasInfix "-i br-lv-kamal0" db.networking.firewall.extraCommands))
    (singleHost.networking.hostName == "example-single-host")
    (!singleHost.leapview.privateNetwork.enable)
    (singleHost.leapview.privateAddress == null)
    (!(singleHost.systemd.network.networks ? "20-private"))
    (singleHost.virtualisation.docker.enable)
    (singleHost.services.postgresql.enable)
    (singleHost.services.postgresql.settings.listen_addresses == "127.0.0.1,172.30.0.1")
    (pkgs.lib.hasInfix "172.30.0.0/24" singleHost.services.postgresql.authentication)
    (pkgs.lib.hasInfix "172.30.0.0/24" singleHost.networking.firewall.extraCommands)
    (pkgs.lib.hasInfix "-i br-lv-kamal0" singleHost.networking.firewall.extraCommands)
    (builtins.elem "leapview-kamal-network.service" singleHost.systemd.services.postgresql.requires)
    (builtins.elem "docker.service" singleHost.systemd.services.postgresql.partOf)
    (singleHost.systemd.services.leapview-kamal-network.serviceConfig.Type == "oneshot")
    (singleHost.systemd.services.leapview-kamal-network.serviceConfig.RemainAfterExit)
    (pkgs.lib.hasInfix "--subnet 172.30.0.0/24" singleHost.systemd.services.leapview-kamal-network.script)
    (pkgs.lib.hasInfix "172.30.0.1" singleHost.systemd.services.leapview-kamal-network.script)
    (pkgs.lib.hasInfix "com.flidai.leapview.managed" singleHost.systemd.services.leapview-kamal-network.script)
    (!singleHost.services.pgbackrest.enable)
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
