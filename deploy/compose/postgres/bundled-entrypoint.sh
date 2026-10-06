#!/bin/sh
set -eu

source_dir=/run/leapview-postgres-secrets
pgdata="${PGDATA:?PostgreSQL image must set PGDATA}"
target_dir="${LEAPVIEW_POSTGRES_SECRET_DIR:?PostgreSQL image must set a persistent secret directory}"
expected_target_dir="$(dirname "$pgdata")/leapview-secrets"
data_directory="$(dirname "$pgdata")"

if [ "$target_dir" != "$expected_target_dir" ]; then
  printf 'PostgreSQL secret directory must be a persistent sibling of PGDATA\n' >&2
  exit 1
fi

if [ -L "$target_dir" ]; then
  printf 'PostgreSQL credential directory must not be a symlink\n' >&2
  exit 1
fi
if [ -L "$data_directory" ]; then
  printf 'PostgreSQL data parent must not be a symlink\n' >&2
  exit 1
fi
umask 077
mkdir -p "$data_directory"
# PostgreSQL must create PGDATA beneath this directory after dropping root.
chown root:root "$data_directory"
chmod 0750 "$data_directory"
chown postgres:postgres "$data_directory"
if [ -e "$target_dir" ] && [ ! -d "$target_dir" ]; then
  printf 'PostgreSQL credential path must be a directory\n' >&2
  exit 1
fi
mkdir -p "$target_dir"
# Take ownership before chmod so the wrapper needs CHOWN but not FOWNER.
# This also repairs a directory left root-owned by an interrupted first start.
chown root:root "$target_dir"
chmod 0700 "$target_dir"
chown postgres:postgres "$target_dir"
find "$target_dir" -maxdepth 1 -type f -name '.pending-*' -delete

copy_persistent_secret() {
  source_path="$source_dir/$1"
  target_path="$target_dir/$1"
  owner=postgres
  mode=0400
  case "$1" in
    server.crt|ca.crt) mode=0444 ;;
    server.key) mode=0600 ;;
  esac
  if [ -L "$target_path" ]; then
    printf 'persisted PostgreSQL secret %s must not be a symlink\n' "$1" >&2
    exit 1
  fi
  if [ -e "$target_path" ]; then
    actual_mode=$(stat -c '%a' "$target_path")
    expected_mode=$(printf '%s' "$mode" | sed 's/^0*//')
    [ -n "$expected_mode" ] || expected_mode=0
    actual_owner=$(stat -c '%u:%g' "$target_path")
    expected_owner="$(id -u postgres):$(id -g postgres)"
    if [ ! -f "$target_path" ] || [ "$actual_mode" != "$expected_mode" ] ||
      [ "$actual_owner" != "$expected_owner" ] || ! cmp -s "$source_path" "$target_path"; then
      printf 'persisted PostgreSQL secret %s differs from its host copy\n' "$1" >&2
      exit 1
    fi
  else
    temporary=$(mktemp "$target_dir/.pending-XXXXXX")
    cp "$source_path" "$temporary"
    chmod "$mode" "$temporary"
    chown "$owner:postgres" "$temporary"
    mv -f "$temporary" "$target_path"
  fi
}

for name in \
  bootstrap-password \
  control-runtime-password \
  control-migrator-password \
  control-maintenance-password \
  ducklake-runtime-password \
  ducklake-migrator-password \
  ducklake-maintenance-password \
  ca.crt server.crt server.key; do
  copy_persistent_secret "$name"
done
sync

export POSTGRES_PASSWORD_FILE="$target_dir/bootstrap-password"
export LEAPVIEW_POSTGRES_SECRET_DIR="$target_dir"
exec /usr/local/bin/docker-entrypoint.sh "$@" \
  -c ssl=on \
  -c "ssl_ca_file=$target_dir/ca.crt" \
  -c "ssl_cert_file=$target_dir/server.crt" \
  -c "ssl_key_file=$target_dir/server.key"
