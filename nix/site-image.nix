{
  pkgs,
  site,
  revision,
  dirty,
  buildTime,
}:
let
  version = site.version;
  root = pkgs.runCommand "leapview-site-image-root" { allowedReferences = [ ]; } ''
    mkdir -p "$out/.data/map-assets" "$out/etc/ssl/certs"
    cp ${site.package}/bin/leapview-site "$out/leapview-site"
    cp -R ${site.maps}/. "$out/.data/map-assets/"
    cp ${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt "$out/etc/ssl/certs/ca-certificates.crt"
    chmod 0555 "$out" "$out/leapview-site"
    find "$out/.data" -type d -exec chmod 0555 {} +
    find "$out/.data" -type f -exec chmod 0444 {} +
    find "$out/etc" -type d -exec chmod 0555 {} +
    find "$out/etc" -type f -exec chmod 0444 {} +
  '';
in
assert !dirty;
pkgs.dockerTools.buildLayeredImage {
  name = "leapview-site";
  tag = builtins.substring 0 12 revision;
  created = buildTime;
  contents = [ ];
  extraCommands = ''
    cp -a ${root}/. .
    chmod 0555 .
  '';
  config = {
    User = "65532:65532";
    WorkingDir = "/";
    Entrypoint = [ "/leapview-site" ];
    Cmd = [ "-addr=:8081" ];
    Env = [
      "LEAPVIEW_SITE_BASE_URL="
      "LEAPVIEW_SITE_SHOWCASE_EMBED_URL="
    ];
    ExposedPorts."8081/tcp" = { };
    Labels = {
      service = "leapview-site";
      "org.opencontainers.image.title" = "LeapView documentation site";
      "org.opencontainers.image.description" = "LeapView public website and documentation portal";
      "org.opencontainers.image.source" = "https://github.com/flidai/leapview";
      "org.opencontainers.image.licenses" = "Apache-2.0";
      "org.opencontainers.image.version" = version;
      "org.opencontainers.image.revision" = revision;
      "org.opencontainers.image.created" = buildTime;
      "dev.leapview.build.dirty" = "false";
      "dev.leapview.build.release" = "false";
      "dev.leapview.build.kind" = "site-image";
    };
  };
}
