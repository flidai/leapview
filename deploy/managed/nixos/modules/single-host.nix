{ lib, ... }:
{
  imports = [
    ./app.nix
    ./database.nix
  ];

  # V1 runs the application and PostgreSQL on one host. The database remains
  # reachable only through the fixed Kamal bridge, so a provider private NIC
  # is not part of this topology.
  leapview.privateNetwork.enable = lib.mkDefault false;
  leapview.database.appContainerNetwork.enable = true;
  leapview.database.backup.enable = lib.mkDefault false;
}
