#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
site_image="${LEAPVIEW_SITE_IMAGE:?Set LEAPVIEW_SITE_IMAGE to an immutable ghcr.io/flidai/leapview-site digest}"
site_host="${LEAPVIEW_SITE_HOST:-178.105.204.14}"
fingerprint_file="$repo_root/deploy/hetzner-site/ssh-host-key.sha256"
temporary_directory="$(mktemp -d)"
cleanup() {
  status=$?
  trap - EXIT
  if [[ "${remote_stage:-}" =~ ^/root/leapview-site-install\.[A-Za-z0-9]+$ ]]; then
    # The staging path is validated locally before expansion into the remote command.
    # shellcheck disable=SC2029
    if ! ssh "${ssh_options[@]}" "$remote" \
      "if test -f '$remote_stage/timer-was-active'; then test -f '$remote_stage/resume-safe' || exit 1; systemctl start leapview-site-reconcile.timer || exit 1; fi; rm -r -- '$remote_stage'"; then
      echo "Could not restore the site timer or remove staging at $remote_stage; operator recovery is required" >&2
      status=1
    fi
  fi
  rm -rf "$temporary_directory"
  exit "$status"
}
trap cleanup EXIT

identity_file="${LEAPVIEW_SITE_SSH_KEY:-}"
if [[ -z "$identity_file" && -n "${SITE_SSH_PRIVATE_KEY:-}" ]]; then
  identity_file="$temporary_directory/operator-identity"
  printf '%s\n' "$SITE_SSH_PRIVATE_KEY" >"$identity_file"
  chmod 0600 "$identity_file"
  unset SITE_SSH_PRIVATE_KEY
fi
identity_file="${identity_file:-${HOME}/.ssh/leapview-site-production}"

immutable_site_reference='^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$'
if [[ ! "$site_image" =~ $immutable_site_reference ]]; then
  echo "LEAPVIEW_SITE_IMAGE must use the canonical GHCR repository and an immutable sha256 digest" >&2
  exit 64
fi
if [[ ! "$site_host" =~ ^[0-9A-Fa-f:.]+$ ]]; then
  echo "LEAPVIEW_SITE_HOST must be an IP address" >&2
  exit 64
fi
if [[ ! -r "$identity_file" ]]; then
  echo "SSH identity is not readable: $identity_file" >&2
  exit 66
fi

for command in curl scp ssh ssh-keygen ssh-keyscan; do
  if ! command -v "$command" >/dev/null; then
    echo "required command is unavailable: $command" >&2
    exit 69
  fi
done

expected_fingerprint="$(tr -d '[:space:]' <"$fingerprint_file")"
scanned_keys="$temporary_directory/scanned-host-keys"
pinned_known_hosts="$temporary_directory/known-hosts"
if ! ssh-keyscan -T 10 "$site_host" >"$scanned_keys" 2>"$temporary_directory/ssh-keyscan.log"; then
  cat "$temporary_directory/ssh-keyscan.log" >&2
  exit 1
fi

matched=false
scanned_key_file="$temporary_directory/scanned-host-key"
while IFS= read -r scanned_key; do
  if [[ -z "$scanned_key" || "$scanned_key" == \#* ]]; then
    continue
  fi
  printf '%s\n' "$scanned_key" >"$scanned_key_file"
  if ! fingerprint_output="$(ssh-keygen -lf "$scanned_key_file" 2>/dev/null)"; then
    continue
  fi
  actual_fingerprint="$(printf '%s\n' "$fingerprint_output" | awk '{print $2}')"
  if [[ "$actual_fingerprint" == "$expected_fingerprint" ]]; then
    printf '%s\n' "$scanned_key" >"$pinned_known_hosts"
    matched=true
    break
  fi
done <"$scanned_keys"
if [[ "$matched" != true ]]; then
  echo "server host key did not match the reviewed fingerprint $expected_fingerprint" >&2
  exit 1
fi

ssh_options=(
  -i "$identity_file"
  -o BatchMode=yes
  -o ConnectTimeout=10
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$pinned_known_hosts"
)
remote="root@$site_host"

remote_stage="$(ssh "${ssh_options[@]}" "$remote" 'mktemp -d /root/leapview-site-install.XXXXXX')"
if [[ ! "$remote_stage" =~ ^/root/leapview-site-install\.[A-Za-z0-9]+$ ]]; then
  echo "unexpected remote staging directory" >&2
  remote_stage=""
  exit 1
fi
bundle="$temporary_directory/bundle"
mkdir "$bundle"
for file in compose.yaml deploy.sh provision.sh reconcile.sh site_image_retention.py \
  leapview-site-reconcile.service leapview-site-reconcile.timer; do
  cp "$repo_root/deploy/hetzner-site/files/$file" "$bundle/$file"
done
(cd "$bundle" && sha256sum ./* > SHA256SUMS)
scp "${ssh_options[@]}" "$bundle/"* "$remote:$remote_stage/"

# Install one coherent script set without interrupting the serving containers.
ssh "${ssh_options[@]}" "$remote" bash -s -- "$remote_stage" <<'REMOTE_INSTALL'
set -euo pipefail
stage="${1:?missing staging directory}"
(cd "$stage" && sha256sum --check SHA256SUMS)
command -v python3 >/dev/null
python3 -c 'import ast,sys; ast.parse(open(sys.argv[1]).read())' "$stage/site_image_retention.py"
for script in deploy provision reconcile; do bash -n "$stage/$script.sh"; done

# Also take the legacy locks to safely upgrade a host running the old scripts.
exec 8>/opt/leapview-site/reconcile.lock
flock -w 60 8
exec 9>/opt/leapview-site/deploy.lock
flock -w 60 9
exec 7>/opt/leapview-site/site-mutation.lock
flock -w 60 7
was_active=false
if systemctl is-active --quiet leapview-site-reconcile.timer; then
  was_active=true
  touch "$stage/timer-was-active"
fi
backup=$(mktemp -d /opt/leapview-site/operator-install.XXXXXX)
paths=(/opt/leapview-site/compose.yaml /opt/leapview-site/deploy.sh
  /opt/leapview-site/provision.sh /opt/leapview-site/reconcile.sh
  /opt/leapview-site/site_image_retention.py
  /etc/systemd/system/leapview-site-reconcile.service
  /etc/systemd/system/leapview-site-reconcile.timer)
for i in "${!paths[@]}"; do
  if [[ -e "${paths[$i]}" ]]; then cp -p "${paths[$i]}" "$backup/$i"; fi
done
finish_install() {
  status=$?
  trap - EXIT
  if [[ "$status" -ne 0 ]]; then
    for i in "${!paths[@]}"; do
      if [[ -e "$backup/$i" ]]; then
        cp -p "$backup/$i" "${paths[$i]}" || exit 1
      else
        rm -f "${paths[$i]}" || exit 1
      fi
    done
    systemctl daemon-reload || exit 1
  fi
  touch "$stage/resume-safe" || exit 1
  if [[ "$status" -ne 0 && "$was_active" == true ]]; then systemctl start leapview-site-reconcile.timer || exit 1; fi
  rm -r "$backup"
  exit "$status"
}
trap finish_install EXIT
systemctl stop leapview-site-reconcile.timer
install -o root -g root -m 0700 "$stage/site_image_retention.py" /opt/leapview-site/site_image_retention.py
install -o root -g root -m 0644 "$stage/compose.yaml" /opt/leapview-site/compose.yaml
for script in deploy provision reconcile; do
  install -o root -g root -m 0700 "$stage/$script.sh" "/opt/leapview-site/$script.sh"
done
install -o root -g root -m 0644 "$stage/leapview-site-reconcile.service" /etc/systemd/system/leapview-site-reconcile.service
install -o root -g root -m 0644 "$stage/leapview-site-reconcile.timer" /etc/systemd/system/leapview-site-reconcile.timer
systemctl daemon-reload
REMOTE_INSTALL
# The value is deliberately expanded locally after the strict digest validation above.
# shellcheck disable=SC2029
ssh "${ssh_options[@]}" "$remote" "/opt/leapview-site/deploy.sh '$site_image'"
ssh "${ssh_options[@]}" "$remote" 'systemctl enable --now leapview-site-reconcile.timer'

deployed_image="$(ssh "${ssh_options[@]}" "$remote" 'cat /opt/leapview-site/deployed-image')"
if [[ "$deployed_image" != "$site_image" ]]; then
  echo "server reports $deployed_image after deploying $site_image" >&2
  exit 1
fi

curl --fail --silent --show-error --max-time 15 https://leapview.dev/healthz >/dev/null
curl --fail --silent --show-error --max-time 15 https://leapview.dev/readyz >/dev/null
www_headers="$(curl --head --silent --show-error --max-time 15 https://www.leapview.dev/)"
if ! printf '%s\n' "$www_headers" | tr -d '\r' | grep -Eqi '^location: https://leapview\.dev/$'; then
  echo "www.leapview.dev did not redirect to the canonical origin" >&2
  exit 1
fi

printf 'deployed and qualified %s on https://leapview.dev\n' "$site_image"
