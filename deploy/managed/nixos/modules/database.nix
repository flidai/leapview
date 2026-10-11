{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.leapview.database;
  fenceRoot = "/var/lib/leapview-recovery";
  promotionRoot = "/var/lib/leapview-recovery-adoption";
  promotionGroup = "leapview-recovery-adoption";
  # Use the same rendering as the pinned upstream PostgreSQL module. The
  # resulting store file is the actual service's reviewed configuration, not
  # a restored config/include or an operator-provided command.
  toPGValue =
    value:
    if value == true then
      "yes"
    else if value == false then
      "no"
    else if builtins.isString value then
      "'${lib.replaceStrings [ "'" ] [ "''" ] value}'"
    else
      toString value;
  reviewedConfig = pkgs.writeTextDir "postgresql.conf" (
    lib.concatStringsSep "\n" (
      lib.mapAttrsToList (name: value: "${name} = ${toPGValue value}") (
        lib.filterAttrs (_: value: value != null) config.services.postgresql.settings
      )
    )
  );
  fence = pkgs.writeShellApplication {
    name = "leapview-postgres-fence";
    runtimeInputs = [ pkgs.python3 ];
    text = ''
      exec python3 ${./postgres_fence.py} \
        --state ${fenceRoot} \
        --data ${lib.escapeShellArg config.services.postgresql.dataDir} \
        --control ${config.services.postgresql.package}/bin/pg_controldata \
        --systemctl ${pkgs.systemd}/bin/systemctl "$@"
    '';
  };
  promote = pkgs.writeShellApplication {
    name = "leapview-postgres-promote";
    runtimeInputs = [ pkgs.python3 ];
    text = ''
      # This narrowly privileged entry point never accepts path/tool overrides.
      if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ "$1" != "--check" ]; }; then
        echo "Only the fixed PostgreSQL promotion action or --check is supported" >&2
        exit 1
      fi
      exec python3 ${./postgres_promote.py} \
        --state ${promotionRoot} \
        --data ${lib.escapeShellArg config.services.postgresql.dataDir} \
        --config ${reviewedConfig}/postgresql.conf \
        --bin ${config.services.postgresql.finalPackage}/bin \
        --systemctl ${pkgs.systemd}/bin/systemctl \
        --runuser ${pkgs.util-linux}/bin/runuser \
        --pgbackrest ${pkgs.pgbackrest}/bin/pgbackrest \
        --port ${toString config.services.postgresql.settings.port} "$@"
    '';
  };
  restore = pkgs.writeShellApplication {
    name = "leapview-postgres-restore";
    runtimeInputs = [ pkgs.python3 ];
    text = ''
      if [ "$#" -ne 1 ]; then
        echo "One fixed PostgreSQL restore action is required" >&2
        exit 1
      fi
      case "$1" in
        fresh|restore|check|start-readback|stop-readback) ;;
        *) echo "Unsupported PostgreSQL restore action" >&2; exit 1 ;;
      esac
      exec python3 ${./.}/postgres_restore.py \
        --state ${promotionRoot} \
        --data ${lib.escapeShellArg config.services.postgresql.dataDir} \
        --config ${reviewedConfig}/postgresql.conf \
        --bin ${config.services.postgresql.finalPackage}/bin \
        --systemctl ${pkgs.systemd}/bin/systemctl \
        --runuser ${pkgs.util-linux}/bin/runuser \
        --pgbackrest ${pkgs.pgbackrest}/bin/pgbackrest \
        --provider-config ${config.environment.etc."pgbackrest/pgbackrest.conf".source} \
        --credentials /var/lib/leapview-backup-secrets/pgbackrest.conf \
        --port ${toString config.services.postgresql.settings.port} --action "$1"
    '';
  };
in
{
  imports = [ ./common.nix ];
  options.leapview.database = {
    appAddress = lib.mkOption { type = lib.types.str; };
    backupBucket = lib.mkOption { type = lib.types.str; };
    backupEndpoint = lib.mkOption { type = lib.types.str; };
    backupRegion = lib.mkOption { type = lib.types.str; };
    recoveryOwner = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Explicit provisioned unprivileged recovery owner authorized only for the fixed promotion helper.";
    };
    recoveryRestore = lib.mkEnableOption "fixed module-owned recovery restore with PostgreSQL automatic initialization disabled";
  };
  config = {
    assertions = [
      {
        assertion = !cfg.recoveryRestore || cfg.recoveryOwner != null;
        message = "Module-owned restore requires an explicit provisioned recovery owner.";
      }
      {
        assertion = builtins.match "[0-9.]+" cfg.appAddress != null;
        message = "Use the application's private IPv4 address.";
      }
      {
        assertion =
          cfg.recoveryOwner == null
          || (cfg.recoveryOwner != "root" && builtins.match "[a-z_][a-z0-9_-]*" cfg.recoveryOwner != null);
        message = "Use an explicit provisioned unprivileged recovery owner.";
      }
      {
        assertion =
          cfg.recoveryOwner == null
          || config.users.users.${cfg.recoveryOwner}.isNormalUser
          || config.users.users.${cfg.recoveryOwner}.isSystemUser;
        message = "Provision the explicit recovery owner account before granting fixed recovery helper access.";
      }
    ];
    users.groups.${promotionGroup} = { };
    users.users = lib.optionalAttrs (cfg.recoveryOwner != null) {
      ${cfg.recoveryOwner}.extraGroups = [ promotionGroup ];
    };
    security.sudo.extraRules = lib.mkIf (cfg.recoveryOwner != null) [
      {
        users = [ cfg.recoveryOwner ];
        commands = [
          {
            command = "/run/current-system/sw/bin/leapview-postgres-promote";
            options = [ "NOPASSWD" ];
          }
        ]
        ++ lib.optionals cfg.recoveryRestore [
          {
            command = "/run/current-system/sw/bin/leapview-postgres-restore";
            options = [ "NOPASSWD" ];
          }
        ];
      }
    ];
    # PostgreSQL binds the private IP; wait for that interface on cold boot.
    systemd.services.postgresql = {
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      # A physical restore must never reactivate the original writer on reboot
      # or during deploy-rs rollback. Only a separately reviewed operator action
      # may remove this recovery fence after replacement is abandoned/fenced.
      unitConfig.ConditionPathExists =
        if cfg.recoveryRestore then
          [
            "!${fenceRoot}/postgresql-fence.json"
            "${promotionRoot}/postgresql-restore-materialized"
          ]
        else
          "!${fenceRoot}/postgresql-fence.json";
      # Recovery is explicitly driven by the fixed helper. Boot or a deploy
      # cannot create an unrelated fresh cluster or reopen a paused readback.
      wantedBy = lib.mkIf cfg.recoveryRestore (lib.mkForce [ ]);
    };
    systemd.targets.postgresql.wantedBy = lib.mkIf cfg.recoveryRestore (lib.mkForce [ ]);
    environment.systemPackages = [
      fence
      promote
    ]
    ++ lib.optionals cfg.recoveryRestore [ restore ];
    services.postgresql = {
      enable = true;
      package = pkgs.postgresql_18;
      enableTCPIP = true;
      settings = {
        listen_addresses = lib.mkForce "127.0.0.1,${config.leapview.privateAddress}";
        ssl = true;
        ssl_cert_file = "/var/lib/leapview-postgres-tls/server.crt";
        ssl_key_file = "/var/lib/leapview-postgres-tls/server.key";
        ssl_min_protocol_version = "TLSv1.2";
        password_encryption = "scram-sha-256";
        # Size memory/connection settings from measured workload; no hardcoded 16GB tuning.
      };
      identMap = "postgres postgres postgres";
      authentication = lib.mkForce ''
        local all postgres peer map=postgres
        local all all reject
        # Promotion/adoption stays loopback-only. Retained runtime and explicit
        # maintenance accounts still require password authentication over TLS.
        hostssl leapview_control,leapview_ducklake all 127.0.0.1/32 scram-sha-256
        hostssl leapview_control,leapview_ducklake all ${cfg.appAddress}/32 scram-sha-256
        host all all 0.0.0.0/0 reject
        host all all ::/0 reject
      '';
    };
    networking.firewall.extraCommands = lib.mkAfter ''
      iptables -A nixos-fw -s ${cfg.appAddress}/32 -d ${config.leapview.privateAddress}/32 -p tcp --dport 5432 -j nixos-fw-accept
    '';
    services.pgbackrest = {
      enable = true;
      repos.customer = {
        type = "s3";
        path = "/postgres";
        s3-bucket = cfg.backupBucket;
        s3-endpoint = cfg.backupEndpoint;
        s3-region = cfg.backupRegion;
        cipher-type = "aes-256-cbc";
        retention-full = 4;
      };
      stanzas.default.jobs =
        if cfg.recoveryRestore then
          lib.mkForce { }
        else
          {
            weekly = {
              schedule = "Sun *-*-* 02:00:00";
              type = "full";
            };
            daily = {
              schedule = "Mon..Sat *-*-* 02:00:00";
              type = "diff";
            };
          };
    };
    # External runtime file; no credential values are evaluated into the Nix store.
    # pgBackRest reads *.conf from its default /etc/pgbackrest/conf.d directory.
    environment.etc."pgbackrest/conf.d/credentials.conf".source =
      "/var/lib/leapview-backup-secrets/pgbackrest.conf";
    systemd.tmpfiles.rules = [
      "d ${fenceRoot} 0700 root root -"
      "d ${promotionRoot} 0750 root ${promotionGroup} -"
      "d /var/lib/leapview-postgres-tls 0700 postgres postgres -"
      "d /var/lib/leapview-backup-secrets 0750 root pgbackrest -"
    ];
    # Application roles/schema/passwords must use the qualified lifecycle adapter;
    # do not run the development init.sh or create permissive default users here.
  };
}
