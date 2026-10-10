set -euo pipefail

# Inputs come from the same trusted native tools derivation. The manual native
# lane runs this private chroot as root; host trust and mounts are never changed.
test "$EUID" -eq 0
test "$#" -eq 2
umask 077
probe=$1
layout=$2
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/source/etc/ssl/certs" "$fixture/root/etc" "$fixture/root/tmp"
cp "$probe" "$fixture/root/tmp/probe"
# ldd executes only this trusted, locally built ELF. Copy its resolved loader
# and shared objects at their exact store paths, with no full-store bind mount.
ldd "$probe" > "$fixture/ldd.txt"
if grep -F 'not found' "$fixture/ldd.txt"; then exit 1; fi
awk '{for(i=1;i<=NF;i++) if($i ~ /^\//) print $i}' "$fixture/ldd.txt" | sort -u > "$fixture/libraries"
test "$(wc -l < "$fixture/libraries")" -ge 1
test "$(wc -l < "$fixture/libraries")" -le 32
while read -r library; do
  case "$library" in /nix/store/*) ;; *) echo 'unexpected non-store native test library' >&2; exit 1 ;; esac
  cp --parents "$library" "$fixture/root"
done < "$fixture/libraries"
LEAPVIEW_TEST_HTTP_CA_MODE=export \
  LEAPVIEW_TEST_HTTP_CA_OUTPUT="$fixture/source/etc/ssl/certs/ca-bundle.crt" \
  "$probe" -test.run '^TestCompiledHTTPFSDefaultTrust$' -test.v

verify() {
  LEAPVIEW_TEST_HTTP_CA_MODE=verify SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt TMPDIR=/tmp \
    chroot "$fixture/root" /tmp/probe \
    -test.run '^TestCompiledHTTPFSDefaultTrust$' -test.v
}

# Preserve the old broken image layout as a negative control: Go's environment
# points at a real CA file, but HTTPFS/CURL must not silently bypass verification.
cp -R "$fixture/source/etc/ssl" "$fixture/root/etc/ssl"
if verify > "$fixture/rejected.log" 2>&1; then
  echo 'default HTTPS unexpectedly accepted the incompatible CA layout' >&2
  exit 1
fi
grep -F 'default HTTPS trust read=' "$fixture/rejected.log"
rm -rf "$fixture/root/etc/ssl"
bash "$layout" "$fixture/source" "$fixture/root"
cmp "$fixture/root/etc/ssl/certs/ca-certificates.crt" "$fixture/source/etc/ssl/certs/ca-bundle.crt"
verify
