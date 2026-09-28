{
  config,
  lib,
  pkgs,
  ...
}:
{
  imports = [ ./common.nix ];
  options.leapview.fileBackup = {
    enable = lib.mkEnableOption "Restic for explicitly inventoried irreplaceable files";
    paths = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
    };
    repository = lib.mkOption {
      type = lib.types.str;
      default = "";
    };
  };
  config = lib.mkMerge [
    {
      virtualisation.docker = {
        enable = true;
        autoPrune.enable = false; # Keep images required by the qualified rollback window.
        daemon.settings = {
          "log-driver" = "local";
          "log-opts" = {
            "max-size" = "20m";
            "max-file" = "5";
          };
        };
      };
      networking.firewall.allowedTCPPorts = [
        80
        443
      ];
      # UID/GID are the public release image's persistent-volume contract.
      systemd.tmpfiles.rules = [
        "d /var/lib/leapview 0750 999 999 -"
        "d /var/lib/leapview-secrets 0700 root root -"
        "d /var/lib/leapview-trust 0755 root root -"
      ];
      environment.systemPackages = [ pkgs.restic ];
      # Kamal owns application/proxy containers; do not declare OCI units here.
    }
    (lib.mkIf config.leapview.fileBackup.enable {
      assertions = [
        {
          assertion = config.leapview.fileBackup.paths != [ ] && config.leapview.fileBackup.repository != "";
          message = "Restic requires explicit retained-file paths and a repository.";
        }
      ];
      services.restic.backups.retained-files = {
        paths = config.leapview.fileBackup.paths;
        repository = config.leapview.fileBackup.repository;
        passwordFile = "/var/lib/leapview-secrets/restic-password";
        environmentFile = "/var/lib/leapview-secrets/restic.env";
        initialize = false; # Repository creation/retention protection is an operator decision.
        timerConfig = {
          OnCalendar = "daily";
          Persistent = true;
          RandomizedDelaySec = "15m";
        };
        # No automatic prune until retention/Object Lock behavior is qualified.
      };
    })
  ];
}
