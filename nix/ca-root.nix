{ pkgs }:
pkgs.runCommand "leapview-ca-root" { } ''
  bash ${./ca-root.sh} ${pkgs.cacert} "$out"
''
