#!/usr/bin/env bash
# shellcheck disable=SC2016
set -euo pipefail

# This controller is intentionally small and disposable. It never writes
# credentials to evidence and it tears down exactly its own Compose project,
# named volumes, and network on every exit (including an interrupted run).

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$ROOT/compose.yaml"
EVIDENCE_DIR="${LEAPVIEW_POSTGRES_HA_EVIDENCE_DIR:-$ROOT/../../../.tmp/postgres-ha-qualification}"
WAIT_SECONDS="${LEAPVIEW_POSTGRES_HA_WAIT_SECONDS:-120}"
WORKTREE_DIGEST="$(printf '%s' "$ROOT" | cksum | awk '{print $1}')"
RUN_ID="${WORKTREE_DIGEST}-$(date -u +%Y%m%d%H%M%S)-$$-${RANDOM}"
PROJECT_NAME="leapview-postgres-ha-${RUN_ID}"
PATRONI_IMAGE="${PROJECT_NAME}-patroni:4.1.5"

SUPERUSER_PASSWORD="${LEAPVIEW_POSTGRES_HA_SUPERUSER_PASSWORD:-leapview-ha-superuser}"
REPLICATION_PASSWORD="${LEAPVIEW_POSTGRES_HA_REPLICATION_PASSWORD:-leapview-ha-replication}"
REWIND_PASSWORD="${LEAPVIEW_POSTGRES_HA_REWIND_PASSWORD:-leapview-ha-rewind}"

if [[ ! "$WAIT_SECONDS" =~ ^[0-9]+$ ]] || ((WAIT_SECONDS < 10 || WAIT_SECONDS > 600)); then
    echo "LEAPVIEW_POSTGRES_HA_WAIT_SECONDS must be between 10 and 600" >&2
    exit 1
fi
if [[ ! "$SUPERUSER_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$REPLICATION_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$REWIND_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ ]]; then
    echo "PostgreSQL HA fixture passwords must contain only URL-safe characters" >&2
    exit 1
fi

export COMPOSE_PROJECT_NAME
# The fixture has no buildx dependency; classic Docker builds keep it usable
# on the minimal Docker/Compose installations used for local qualification.
export DOCKER_BUILDKIT=0
export LEAPVIEW_POSTGRES_HA_SUPERUSER_PASSWORD="$SUPERUSER_PASSWORD"
export LEAPVIEW_POSTGRES_HA_REPLICATION_PASSWORD="$REPLICATION_PASSWORD"
export LEAPVIEW_POSTGRES_HA_REWIND_PASSWORD="$REWIND_PASSWORD"

umask 077
mkdir -p "$EVIDENCE_DIR"
EVIDENCE_FILE="$EVIDENCE_DIR/evidence.json"

write_evidence() {
    local filter="${!#}"
    local jq_args=("${@:1:$#-1}")
    local tmp
    tmp="$(mktemp "$EVIDENCE_DIR/.evidence.XXXXXX")"
    jq "${jq_args[@]}" "$filter" "$EVIDENCE_FILE" >"$tmp"
    mv -f "$tmp" "$EVIDENCE_FILE"
}

cat >"$EVIDENCE_FILE" <<'JSON'
{
  "schemaVersion": 1,
  "evidenceKind": "postgres-ha-qualification",
  "runId": null,
  "fixture": "same-host-patroni-postgresql-18",
  "topology": {
    "postgresMembers": 2,
    "dcsMembers": 3,
    "leaderEndpoint": "haproxy",
    "postgresMajor": 18,
    "patroniVersion": "4.1.5",
    "etcdVersion": "3.5.15",
    "haproxyVersion": "3.0.8"
  },
  "runtimeVersions": {},
  "measurements": {
    "failoverToWritableMilliseconds": null,
    "rejoinMilliseconds": null
  },
  "finalState": {
    "leader": null,
    "replica": null
  },
  "limitations": [
    "All containers run on one host and share one Docker daemon.",
    "This validates Patroni and endpoint behavior, not independent-host or provider-level failure domains.",
    "This evidence is separate from the application --multi-node-process qualification."
  ],
  "events": [],
  "diagnostics": [],
  "result": "running",
  "cleanup": {
    "attempted": false,
    "composeProject": "isolated"
  }
}
JSON
write_evidence --arg runId "$RUN_ID" --arg project "$PROJECT_NAME" \
    '.runId=$runId | .cleanup.composeProject=$project'

bootstrap_marker="${RUN_ID}-bootstrap-write"
failover_marker="${RUN_ID}-failover-write"
dcs_loss_marker="${RUN_ID}-dcs-member-loss-write"
dcs_recovery_marker="${RUN_ID}-dcs-member-recovery-write"
final_marker="${RUN_ID}-final-convergence"

compose() {
    docker compose --project-name "$PROJECT_NAME" --file "$COMPOSE_FILE" "$@"
}

record_event() {
    local name="$1"
    local status="$2"
    write_evidence --arg name "$name" --arg status "$status" '.events += [{"name":$name,"status":$status}]'
}

record_result() {
    local result="$1"
    write_evidence --arg result "$result" '.result=$result'
}

record_diagnostics() {
    local context="$1"
    local compose_ps
    local compose_logs
    compose_ps="$(compose ps --all --format json 2>/dev/null | LC_ALL=C head -c 65536 || true)"
    compose_logs="$(compose logs --no-color --tail=40 pg1 pg2 etcd1 etcd2 etcd3 haproxy 2>/dev/null | LC_ALL=C head -c 65536 || true)"
    compose_ps="${compose_ps//"$SUPERUSER_PASSWORD"/[redacted]}"
    compose_ps="${compose_ps//"$REPLICATION_PASSWORD"/[redacted]}"
    compose_ps="${compose_ps//"$REWIND_PASSWORD"/[redacted]}"
    compose_logs="${compose_logs//"$SUPERUSER_PASSWORD"/[redacted]}"
    compose_logs="${compose_logs//"$REPLICATION_PASSWORD"/[redacted]}"
    compose_logs="${compose_logs//"$REWIND_PASSWORD"/[redacted]}"
    write_evidence --arg context "$context" --arg composePs "$compose_ps" --arg logs "$compose_logs" \
        '.diagnostics += [{"context":$context,"composePs":$composePs,"logs":$logs}]'
}

deadline=""
until_deadline() {
    deadline="$(( $(date +%s) + WAIT_SECONDS ))"
}

within_deadline() {
    (( $(date +%s) < deadline ))
}

now_milliseconds() {
    printf '%s\n' "$(( $(date +%s%N) / 1000000 ))"
}

psql_member() {
    local member="$1"
    local sql="$2"
    compose exec -T \
        --env "PGPASSWORD=$SUPERUSER_PASSWORD" \
        "$member" psql --no-psqlrc --set ON_ERROR_STOP=1 --tuples-only --no-align \
        --host 127.0.0.1 --username postgres --dbname postgres --command "$sql" \
        >/dev/null 2>&1
}

psql_endpoint() {
    local runner="$1"
    local sql="$2"
    compose exec -T \
        --env "PGPASSWORD=$SUPERUSER_PASSWORD" \
        "$runner" psql --no-psqlrc --set ON_ERROR_STOP=1 --tuples-only --no-align \
        --host haproxy --username postgres --dbname postgres --command "$sql" \
        >/dev/null 2>&1
}

member_has_marker() {
    local member="$1"
    local marker="$2"
    local value
    value="$(compose exec -T \
        --env "PGPASSWORD=$SUPERUSER_PASSWORD" \
        "$member" psql --no-psqlrc --set ON_ERROR_STOP=1 --tuples-only --no-align \
        --host 127.0.0.1 --username postgres --dbname postgres \
        --command "SELECT marker FROM ha_qualification WHERE id = 1" 2>/dev/null || true)"
    [[ "$value" == "$marker" ]]
}

wait_for_member() {
    local member="$1"
    until_deadline
    while within_deadline; do
        if compose exec -T "$member" wget -qO /dev/null http://127.0.0.1:8008/health >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

wait_for_dcs_member() {
    local member="$1"
    until_deadline
    while within_deadline; do
        if compose exec -T "$member" etcdctl \
            --endpoints=http://127.0.0.1:2379 endpoint health >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

wait_for_dcs_quorum_write() {
    until_deadline
    while within_deadline; do
        if compose exec -T etcd1 etcdctl \
            --endpoints=http://etcd1:2379,http://etcd2:2379 \
            put "/qualification/${RUN_ID}" "$RUN_ID" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

dcs_cluster_converged() {
    local members
    if ! compose exec -T etcd1 etcdctl \
        --endpoints=http://etcd1:2379,http://etcd2:2379,http://etcd3:2379 \
        endpoint health >/dev/null 2>&1; then
        return 1
    fi
    members="$(compose exec -T etcd1 etcdctl \
        --endpoints=http://etcd1:2379 member list --write-out=json 2>/dev/null || true)"
    jq -e '.members | length == 3' <<<"$members" >/dev/null
}

leader() {
    local member
    for member in pg1 pg2; do
        if compose exec -T "$member" wget -qO /dev/null http://127.0.0.1:8008/primary >/dev/null 2>&1; then
            printf '%s\n' "$member"
            return 0
        fi
    done
    return 1
}

wait_for_leader() {
    local current
    until_deadline
    while within_deadline; do
        if current="$(leader)"; then
            printf '%s\n' "$current"
            return 0
        fi
        sleep 1
    done
    return 1
}

wait_for_marker() {
    local member="$1"
    local marker="$2"
    until_deadline
    while within_deadline; do
        if member_has_marker "$member" "$marker"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

setup_schema() {
    local runner="$1"
    psql_endpoint "$runner" \
        "CREATE TABLE IF NOT EXISTS ha_qualification (id integer PRIMARY KEY, marker text NOT NULL);"
}

write_marker() {
    local runner="$1"
    local marker="$2"
    psql_endpoint "$runner" \
        "INSERT INTO ha_qualification (id, marker) VALUES (1, '$marker') ON CONFLICT (id) DO UPDATE SET marker = EXCLUDED.marker;"
}

wait_for_endpoint_write() {
    local runner="$1"
    local marker="$2"
    until_deadline
    while within_deadline; do
        if write_marker "$runner" "$marker"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

check_replication() {
    local replica="$1"
    local marker="$2"
    wait_for_marker "$replica" "$marker"
}

cleanup() {
    local exit_code=$?
    local cleanup_status="failed"
    local image_status="not-created"
    set +e
    if compose down --volumes --remove-orphans >/dev/null 2>&1; then
        cleanup_status="passed"
    fi
    if docker image inspect "$PATRONI_IMAGE" >/dev/null 2>&1; then
        image_status="failed"
        if docker image rm "$PATRONI_IMAGE" >/dev/null 2>&1; then
            image_status="removed"
        else
            cleanup_status="failed"
        fi
    fi
    write_evidence --arg status "$cleanup_status" --arg imageStatus "$image_status" --argjson attempted true \
        '.cleanup.attempted=$attempted | .cleanup.status=$status | .cleanup.derivedImage=$imageStatus'
    if [[ "$exit_code" -eq 0 && "$cleanup_status" != "passed" ]]; then
        exit_code=1
        write_evidence --arg result failed '.result=$result'
    fi
    exit "$exit_code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! compose config --quiet >/dev/null 2>&1; then
    record_event topology-validation failed
    record_diagnostics topology-validation
    record_result failed
    exit 1
fi
record_event topology-validation passed

if ! compose up --build --detach --wait >/dev/null 2>&1; then
    record_event bootstrap failed
    record_diagnostics bootstrap
    record_result failed
    exit 1
fi
record_event bootstrap passed

postgres_runtime="$(compose exec -T pg1 postgres --version 2>/dev/null | tr -d '\r')"
patroni_runtime="$(compose exec -T pg1 /opt/patroni/bin/patroni --version 2>/dev/null | tr -d '\r')"
etcd_runtime="$(compose exec -T etcd1 etcd --version 2>/dev/null | sed -n '1p' | tr -d '\r')"
haproxy_runtime="$(compose exec -T haproxy haproxy -v 2>/dev/null | sed -n '1p' | tr -d '\r')"
write_evidence \
    --arg postgres "$postgres_runtime" \
    --arg patroni "$patroni_runtime" \
    --arg etcd "$etcd_runtime" \
    --arg haproxy "$haproxy_runtime" \
    '.runtimeVersions={postgresql:$postgres,patroni:$patroni,etcd:$etcd,haproxy:$haproxy}'
if [[ "$postgres_runtime" != *"(PostgreSQL) 18."* || "$patroni_runtime" != "patroni 4.1.5" || "$etcd_runtime" != "etcd Version: 3.5.15" || "$haproxy_runtime" != HAProxy\ version\ 3.0.8-* ]]; then
    record_event runtime-version-validation failed
    record_diagnostics runtime-version-validation
    record_result failed
    exit 1
fi
record_event runtime-version-validation passed

if ! wait_for_member pg1 || ! wait_for_member pg2; then
    record_event member-readiness failed
    record_diagnostics member-readiness
    record_result failed
    exit 1
fi
record_event member-readiness passed

if ! current_leader="$(wait_for_leader)"; then
    record_event initial-leader-election failed
    record_diagnostics initial-leader-election
    record_result failed
    exit 1
fi
if ! setup_schema "$current_leader" || ! write_marker "$current_leader" "$bootstrap_marker"; then
    record_event write-and-replication failed
    record_diagnostics write-and-replication
    record_result failed
    exit 1
fi
current_replica="pg1"
[[ "$current_leader" == "pg1" ]] && current_replica="pg2"
if ! check_replication "$current_replica" "$bootstrap_marker"; then
    record_event write-and-replication failed
    record_diagnostics write-and-replication
    record_result failed
    exit 1
fi
record_event write-and-replication passed

lost_primary="$current_leader"
failover_started_at="$(now_milliseconds)"
if ! compose kill -s SIGKILL "$lost_primary" >/dev/null 2>&1; then
    record_event abrupt-primary-loss failed
    record_diagnostics abrupt-primary-loss
    record_result failed
    exit 1
fi
record_event abrupt-primary-loss passed
if ! current_leader="$(wait_for_leader)"; then
    record_event failover-election failed
    record_diagnostics failover-election
    record_result failed
    exit 1
fi
record_event failover-election passed
if ! wait_for_endpoint_write "$current_leader" "$failover_marker"; then
    record_event failover-write failed
    record_diagnostics failover-write
    record_result failed
    exit 1
fi
failover_finished_at="$(now_milliseconds)"
failover_elapsed_ms=$((failover_finished_at - failover_started_at))
write_evidence --argjson elapsed "$failover_elapsed_ms" \
    '.measurements.failoverToWritableMilliseconds=$elapsed'
record_event failover-write passed

rejoin_started_at="$(now_milliseconds)"
if ! compose up --detach "$lost_primary" >/dev/null 2>&1 || ! wait_for_member "$lost_primary" || ! wait_for_marker "$lost_primary" "$failover_marker"; then
    record_event primary-recovery failed
    record_diagnostics primary-recovery
    record_result failed
    exit 1
fi
rejoin_finished_at="$(now_milliseconds)"
rejoin_elapsed_ms=$((rejoin_finished_at - rejoin_started_at))
write_evidence --argjson elapsed "$rejoin_elapsed_ms" \
    '.measurements.rejoinMilliseconds=$elapsed'
record_event primary-recovery passed

if ! compose kill -s SIGKILL etcd3 >/dev/null 2>&1; then
    record_event dcs-member-loss failed
    record_diagnostics dcs-member-loss
    record_result failed
    exit 1
fi
if compose ps --status running --services | grep -Fxq etcd3 || ! wait_for_dcs_quorum_write || ! wait_for_endpoint_write "$current_leader" "$dcs_loss_marker"; then
    record_event dcs-member-loss failed
    record_diagnostics dcs-member-loss
    record_result failed
    exit 1
fi
record_event dcs-member-loss passed

if ! compose up --detach etcd3 >/dev/null 2>&1 || ! wait_for_dcs_member etcd3 || ! dcs_cluster_converged; then
    record_event dcs-member-recovery failed
    record_diagnostics dcs-member-recovery
    record_result failed
    exit 1
fi
if ! wait_for_endpoint_write "$current_leader" "$dcs_recovery_marker"; then
    record_event dcs-member-recovery failed
    record_diagnostics dcs-member-recovery
    record_result failed
    exit 1
fi
record_event dcs-member-recovery passed

for member in pg1 pg2; do
    rolling_marker="${RUN_ID}-rolling-restart-${member}"
    if ! compose restart "$member" >/dev/null 2>&1 || ! wait_for_member "$member" || ! current_leader="$(wait_for_leader)" || ! wait_for_endpoint_write "$current_leader" "$rolling_marker"; then
        record_event rolling-restart failed
        record_diagnostics rolling-restart
        record_diagnostics rolling-restart
        record_result failed
        exit 1
    fi
done
record_event rolling-restart passed

if ! current_leader="$(wait_for_leader)" || ! wait_for_endpoint_write "$current_leader" "$final_marker"; then
    record_event final-convergence failed
    record_diagnostics final-convergence
    record_result failed
    exit 1
fi
current_replica="pg1"
[[ "$current_leader" == "pg1" ]] && current_replica="pg2"
if ! check_replication "$current_replica" "$final_marker"; then
    record_event final-convergence failed
    record_diagnostics final-convergence
    record_result failed
    exit 1
fi
write_evidence --arg leader "$current_leader" --arg replica "$current_replica" \
    '.finalState.leader=$leader | .finalState.replica=$replica'
record_event final-convergence passed
record_result passed
