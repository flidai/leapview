{ pkgs, revision ? "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }:
let
  server = pkgs.runCommand "managed-kamal-transport-protocol" {
    nativeBuildInputs = [ pkgs.go ];
  } ''
    export HOME="$TMPDIR" GOCACHE="$TMPDIR/go-cache" CGO_ENABLED=0
    mkdir -p "$out/bin"
    go build -trimpath -ldflags '-s -w -X main.revision=${revision}' \
      -o "$out/bin/managed-transport-protocol" ${./kamal_transport_protocol.go}
  '';
in pkgs.dockerTools.buildLayeredImage {
  name = "leapview-managed-transport";
  tag = revision;
  contents = [ server ];
  config = {
    Entrypoint = [ "${server}/bin/managed-transport-protocol" ];
    ExposedPorts."8080/tcp" = { };
    Labels."org.opencontainers.image.revision" = revision;
  };
}
