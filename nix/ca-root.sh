set -euo pipefail

# Keep the Nix/Go bundle name and the conventional Debian/CURL name backed by
# identical pinned bytes. HTTPFS searches fixed host paths, not SSL_CERT_FILE.
mkdir -p "$2/etc/ssl"
cp -R "$1/etc/ssl/." "$2/etc/ssl/"
chmod u+w "$2/etc/ssl/certs"
ln -s ca-bundle.crt "$2/etc/ssl/certs/ca-certificates.crt"
cmp "$2/etc/ssl/certs/ca-certificates.crt" "$1/etc/ssl/certs/ca-bundle.crt"
