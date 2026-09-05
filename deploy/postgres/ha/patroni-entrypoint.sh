#!/usr/bin/env bash
set -euo pipefail

: "${PATRONI_NAME:?PATRONI_NAME is required}"
: "${PATRONI_SCOPE:?PATRONI_SCOPE is required}"
: "${PATRONI_NAMESPACE:?PATRONI_NAMESPACE is required}"
: "${PATRONI_ETCD3_HOSTS:?PATRONI_ETCD3_HOSTS is required}"
: "${PATRONI_SUPERUSER_PASSWORD:?PATRONI_SUPERUSER_PASSWORD is required}"
: "${PATRONI_REPLICATION_PASSWORD:?PATRONI_REPLICATION_PASSWORD is required}"
: "${PATRONI_REWIND_PASSWORD:?PATRONI_REWIND_PASSWORD is required}"
: "${PGDATA:=/var/lib/postgresql/18/docker}"

# The official postgres entrypoint is intentionally bypassed: Patroni must own
# the postmaster lifecycle and must bootstrap/clone an empty member directory
# itself. Manually running initdb here would give both members different system
# identifiers and prevent the standby from cloning the first primary.
install -d -o postgres -g postgres -m 0700 "${PGDATA}"

patroni_config="$(mktemp /tmp/patroni.XXXXXX)"
cat >"${patroni_config}" <<EOF
scope: ${PATRONI_SCOPE}
namespace: ${PATRONI_NAMESPACE}
name: ${PATRONI_NAME}

restapi:
  listen: 0.0.0.0:8008
  connect_address: ${PATRONI_NAME}:8008

etcd3:
  hosts: ${PATRONI_ETCD3_HOSTS}
  protocol: http

bootstrap:
  dcs:
    ttl: 30
    loop_wait: 5
    retry_timeout: 10
    maximum_lag_on_failover: 1048576
    postgresql:
      use_pg_rewind: true
      use_slots: true
      parameters:
        wal_level: replica
        hot_standby: 'on'
        wal_log_hints: 'on'
        max_wal_senders: 10
        max_replication_slots: 10
  initdb:
    - encoding: UTF8
    - data-checksums
  pg_hba:
    - local all all trust
    - host all all 0.0.0.0/0 scram-sha-256
    - host replication replicator 0.0.0.0/0 scram-sha-256
    - host replication rewind 0.0.0.0/0 scram-sha-256
postgresql:
  listen: 0.0.0.0:5432
  connect_address: ${PATRONI_NAME}:5432
  data_dir: ${PGDATA}
  bin_dir: /usr/local/bin
  pgpass: /tmp/pgpass
  authentication:
    superuser:
      username: postgres
      password: ${PATRONI_SUPERUSER_PASSWORD}
    replication:
      username: replicator
      password: ${PATRONI_REPLICATION_PASSWORD}
    rewind:
      username: rewind
      password: ${PATRONI_REWIND_PASSWORD}
  parameters:
    unix_socket_directories: '/var/run/postgresql'
EOF
chown postgres:postgres "${patroni_config}"
chmod 0600 "${patroni_config}"

exec gosu postgres /opt/patroni/bin/patroni "${patroni_config}"
