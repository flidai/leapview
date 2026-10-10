{
  pkgs,
  toolchain,
  application,
  src,
}:
let
  hashes = builtins.fromJSON (builtins.readFile ./build-hashes.json);
  extensionHash =
    hashes.extensions.${pkgs.stdenv.hostPlatform.system}
      or (throw "no native extension-supply hash is pinned for ${pkgs.stdenv.hostPlatform.system}");
  fixed =
    name: hash: script:
    pkgs.stdenvNoCC.mkDerivation {
      inherit name src;
      nativeBuildInputs = [
        toolchain.go
        pkgs.cacert
      ];
      GOTOOLCHAIN = "local";
      CGO_ENABLED = "0";
      SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
      outputHashMode = "recursive";
      outputHashAlgo = "sha256";
      outputHash = hash;
      dontConfigure = true;
      dontFixup = true;
      buildPhase = ''
        export GOPATH="$TMPDIR/go" GOCACHE="$TMPDIR/go-cache"
        ${script}
        # Normalize permissions before hashing; no timestamps enter the NAR hash.
        find "$out" -type d -exec chmod 0755 {} +
        find "$out" -type f -exec chmod 0644 {} +
      '';
      installPhase = "true";
    };
in
{
  maps = fixed "leapview-map-assets" hashes.maps ''
    ${application.tools}/bin/mapassets --out "$out"
  '';
  extensions = fixed "leapview-extension-supply" extensionHash ''
    # The publisher verifies closed compiled source descriptors and actual
    # static registration; remaining extensions retain official exact-file LOADs.
    # The Nix hash pins the complete resulting supply across rebuilds.
    ${application.tools}/bin/extensionsupply --out "$TMPDIR/supply"
    cp -R "$TMPDIR/supply" "$out"
  '';
}
