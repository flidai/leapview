{ config, ... }:
{
  # Initial installation only. Disk formatting must never be part of host updates.
  disko.devices.disk.system = {
    type = "disk";
    device = config.leapview.diskDevice;
    content = {
      type = "gpt";
      partitions = {
        boot =
          if config.leapview.bootMode == "uefi" then
            {
              size = "512M";
              type = "EF00";
              content = {
                type = "filesystem";
                format = "vfat";
                mountpoint = "/boot";
                mountOptions = [ "umask=0077" ];
              };
            }
          else
            {
              size = "1M";
              type = "EF02";
            };
        root = {
          size = "100%";
          content = {
            type = "filesystem";
            format = "ext4";
            mountpoint = "/";
          };
        };
      };
    };
  };
}
