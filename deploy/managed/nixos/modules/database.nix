{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.leapview.database;
  fenceRoot = "/var/lib/leapview-recovery";
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
in
{
  imports = [ ./common.nix ];
  options.leapview.database = {
    appAddress = lib.mkOption { type = lib.types.str; };
    backupBucket = lib.mkOption { type = lib.types.str; };
    backupEndpoint = lib.mkOption { type = lib.types.str; };
    backupRegion = lib.mkOption { type = lib.types.str; };
  };
  config = {
    assertions = [
      {
        assertion = builtins.match "[0-9.]+" cfg.appAddress != null;
        message = "Use the application's private IPv4 address.";
      }
    ];
    # PostgreSQL binds the private IP; wait for that interface on cold boot.
    systemd.services.postgresql = {
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      # A physical restore must never reactivate the original writer on reboot
      # or during deploy-rs rollback. Only a separately reviewed operator action
      # may remove this recovery fence after replacement is abandoned/fenced.
      unitConfig.ConditionPathExists = "!${fenceRoot}/postgresql-fence.json";
    };
    environment.systemPackages = [ fence ];
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
      stanzas.default.jobs = {
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
      "d /var/lib/leapview-postgres-tls 0700 postgres postgres -"
      "d /var/lib/leapview-backup-secrets 0750 root pgbackrest -"
    ];
    # Application roles/schema/passwords must use the qualified lifecycle adapter;
    # do not run the development init.sh or create permissive default users here.
  };
}
