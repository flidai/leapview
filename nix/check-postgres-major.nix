{ pkgs }:
pkgs.runCommand "leapview-postgres-major-maintenance-rehearsal"
  {
    nativeBuildInputs = [ pkgs.python3 ];
    preferLocalBuild = true;
    allowSubstitutes = false;
  }
  ''
    python3 ${../scripts/rehearse_postgres_major.py} \
      --old-bin ${pkgs.postgresql_17}/bin \
      --new-bin ${pkgs.postgresql_18}/bin \
      --report "$out/report.json"
  ''
