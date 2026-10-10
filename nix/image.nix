{
  pkgs,
  application,
  assets,
  portable,
  patchedRuntime,
  revision,
  dirty,
  buildTime,
  purpose,
}:
let
  loaderDir = pkgs.stdenv.hostPlatform.libDir;
  loader = builtins.baseNameOf pkgs.stdenv.cc.bintools.dynamicLinker;
  runtime = pkgs.runCommand "leapview-image-root" { } ''
    mkdir -p "$out/app" "$out/bin" "$out/sbin" "$out/${loaderDir}" "$out/etc" "$out/usr/bin" "$out/usr/local/bin" "$out/usr/local/libexec" "$out/usr/local/share/leapview" "$out/var/lib/leapview/home" "$out/tmp"
    cp ${portable}/bin/leapview "$out/usr/local/bin/leapview"
    cp ${portable}/bin/leapviewctl "$out/usr/local/libexec/leapviewctl"
    cp -R ${portable}/share/leapview/native-build "$out/usr/local/share/leapview/native-build"
    ln -s ${patchedRuntime.glibc}/lib/${loader} "$out/${loaderDir}/${loader}"
    ln -s ${patchedRuntime.busybox}/bin "$out/busybox"
    ln -s ${patchedRuntime.busybox}/bin/sh "$out/bin/sh"
    ln -s ${patchedRuntime.busybox}/bin/env "$out/usr/bin/env"
    ln -s ${patchedRuntime.busybox}/bin/nologin "$out/sbin/nologin"
    ln -s ${pkgs.cacert}/etc/ssl "$out/etc/ssl"
    printf 'root:x:0:0:root:/root:/bin/sh\nleapview:x:999:999::/var/lib/leapview:/sbin/nologin\n' > "$out/etc/passwd"
    printf 'root:x:0:\nleapview:x:999:\n' > "$out/etc/group"
    cp -R ${application}/share/leapview/{static,schemas,dashboards,evaluation} "$out/app/"
    mkdir -p "$out/app/.data"
    ln -s ${assets.maps} "$out/app/.data/map-assets"
    cp -R ${assets.extensions} "$out/usr/local/share/leapview/extensions"
    mkdir -p "$out/usr/local/share/leapview/deployment"
    cp -R ${application}/share/leapview/deploy/compose/{compose.yaml,compose.postgres.yaml,compose.https.yaml,compose.first-install-bootstrap.yaml,Caddyfile,Caddyfile.first-install-bootstrap,first-install.env,deployment.env.example,leapview.env.example,README.md,QUALIFICATION.md,qualification,postgres} "$out/usr/local/share/leapview/deployment/"
    cp -R ${application}/share/leapview/deploy/host/files/. "$out/usr/local/share/leapview/deployment/"
    cp ${portable}/bin/leapviewctl "$out/usr/local/share/leapview/deployment/leapviewctl"
    chmod -R u+w "$out/usr/local/share/leapview/deployment"
    chmod 0500 "$out/usr/local/share/leapview/deployment/"{leapviewctl,leapviewctl-wrapper}
    chmod 0400 "$out/usr/local/share/leapview/deployment/"{compose.yaml,compose.postgres.yaml,compose.https.yaml,compose.first-install-bootstrap.yaml,Caddyfile,Caddyfile.first-install-bootstrap,first-install.env,deployment.env.example,leapview.env.example,README.md,QUALIFICATION.md,qualification/*}
    chmod 0444 "$out/usr/local/share/leapview/deployment/postgres/"*.sh
    find "$out/usr/local/share/leapview/extensions" -type d -exec chmod 0555 {} +
    find "$out/usr/local/share/leapview/extensions" -type f -exec chmod 0444 {} +
  '';
in
assert builtins.elem purpose [
  "development"
  "compose"
];
assert purpose != "compose" || !dirty;
pkgs.dockerTools.buildLayeredImage {
  name = "leapview-nix";
  tag = "${builtins.substring 0 12 revision}${if dirty then "-dirty" else ""}";
  created = buildTime;
  # Materialize the public paths: deployment qualification copies these binaries
  # and files into non-Nix hosts/containers. symlinkJoin's store links are unsuitable.
  contents = [ ];
  extraCommands = ''
    cp -a ${runtime}/. .
    chmod -R u+w .
  '';
  enableFakechroot = true;
  fakeRootCommands = ''
    chown -hR 0:0 ./app ./bin ./${loaderDir} ./sbin ./etc ./usr ./var ./tmp
    chown -h 0:0 ./busybox
    chmod 0500 ./usr/local/share/leapview/deployment/{leapviewctl,leapviewctl-wrapper}
    chmod 0400 ./usr/local/share/leapview/deployment/{compose.yaml,compose.postgres.yaml,compose.https.yaml,compose.first-install-bootstrap.yaml,Caddyfile,Caddyfile.first-install-bootstrap,first-install.env,deployment.env.example,leapview.env.example,README.md,QUALIFICATION.md,qualification/*}
    chmod 0444 ./usr/local/share/leapview/deployment/postgres/*.sh
    find ./usr/local/share/leapview/extensions -type d -exec chmod 0555 {} +
    find ./usr/local/share/leapview/extensions -type f -exec chmod 0444 {} +
    chown -R 999:999 ./var/lib/leapview ./app
    chmod 1777 ./tmp
  '';
  config = {
    User = "leapview:leapview";
    WorkingDir = "/app";
    Entrypoint = [ "/usr/local/bin/leapview" ];
    Cmd = [
      "serve"
      "--production"
    ];
    Env = [
      "PATH=/usr/local/bin:${patchedRuntime.busybox}/bin"
      "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
      "LD_LIBRARY_PATH=${
        pkgs.lib.makeLibraryPath [
          patchedRuntime.glibc
          patchedRuntime.gccLib
        ]
      }"
      "TZDIR=${pkgs.tzdata}/share/zoneinfo"
      "LEAPVIEW_ADDR=:8080"
      "LEAPVIEW_ENVIRONMENT=prod"
      "LEAPVIEW_HOME=/var/lib/leapview/home"
      "LEAPVIEW_MAP_ASSET_DIR=/app/.data/map-assets"
      "LEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data"
      "LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH=/usr/local/share/leapview/extensions/extension-supply.json"
      "LEAPVIEW_PRODUCTION=1"
    ];
    ExposedPorts."8080/tcp" = { };
    Volumes."/var/lib/leapview" = { };
    Healthcheck = {
      Test = [
        "CMD"
        "/usr/local/bin/leapview"
        "healthcheck"
      ];
      Interval = 30000000000;
      Timeout = 5000000000;
      StartPeriod = 10000000000;
      Retries = 3;
    };
    Labels = {
      "org.opencontainers.image.title" = "LeapView";
      "org.opencontainers.image.description" = "LeapView business intelligence server";
      "org.opencontainers.image.source" = "https://github.com/flidai/leapview";
      "org.opencontainers.image.licenses" = "Apache-2.0";
      "org.opencontainers.image.version" = application.version;
      "org.opencontainers.image.revision" = revision;
      "org.opencontainers.image.created" = buildTime;
      "dev.leapview.build.dirty" = if dirty then "true" else "false";
      "dev.leapview.build.release" = if purpose == "compose" then "true" else "false";
      "dev.leapview.build.kind" = "application-image";
    };
  };
}
