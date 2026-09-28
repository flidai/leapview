# Evaluation-only inventory. Keep real customer inventory in private operations.
{
  common = {
    leapview.operatorKeys = [ "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleReplaceBeforeInstallation" ];
    leapview.operatorCIDRs = [ "203.0.113.10/32" ];
    leapview.diskDevice = "/dev/sda"; # Verify disk identity in rescue mode first.
    system.stateVersion = "26.05";
  };
  app = {
    networking.hostName = "example-app";
    leapview.privateAddress = "10.42.0.10";
  };
  database = {
    networking.hostName = "example-database";
    leapview.privateAddress = "10.42.0.20";
    leapview.database.appAddress = "10.42.0.10";
    leapview.database.backupBucket = "replace-with-customer-backup-bucket";
    leapview.database.backupEndpoint = "fsn1.your-objectstorage.com";
    leapview.database.backupRegion = "fsn1";
  };
}
