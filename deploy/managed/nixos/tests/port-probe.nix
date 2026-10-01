{ pkgs }:
pkgs.dockerTools.buildLayeredImage {
  name = "managed-port-probe";
  tag = "fixture";
  contents = [ pkgs.busybox ];
  config.Cmd = [
    "${pkgs.busybox}/bin/sh"
    "-c"
    "httpd -p 80; exec httpd -f -p 8080"
  ];
}
