#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$ROOT/.tmp"
PID_FILE="$TMP_DIR/dev-server.pid"
PORT_FILE="$TMP_DIR/dev-server.port"
LOG_FILE="$TMP_DIR/dev-server.log"
PORT_START="${LEAPVIEW_DEV_PORT_START:-8100}"
PORT_COUNT="${LEAPVIEW_DEV_PORT_COUNT:-100}"

mkdir -p "$TMP_DIR"

usage() {
	echo "Usage: $0 start [project [connection source-root]]|once [project [connection source-root]]|publish [project [connection source-root]]|stop|status|logs"
}

is_alive() {
  local pid="${1:-}"
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

pid_command() {
  local pid="$1"
  ps -p "$pid" -o command= 2>/dev/null || true
}

pid_cwd() {
  local pid="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1
  elif [[ -e "/proc/$pid/cwd" ]]; then
    readlink "/proc/$pid/cwd" 2>/dev/null || true
  fi
}

port_pids() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | sort -u || true
  fi
}

stop_pid() {
  local pid="$1"
  local label="${2:-process}"
  if ! is_alive "$pid"; then
    return 0
  fi

  echo "Stopping $label (pid $pid)"
  kill "$pid" 2>/dev/null || true
  for _ in {1..30}; do
    if ! is_alive "$pid"; then
      return 0
    fi
    sleep 0.1
  done
  echo "Force stopping $label (pid $pid)"
  kill -KILL "$pid" 2>/dev/null || true
}

stop_recorded() {
  local port=""
  [[ -f "$PORT_FILE" ]] && port="$(cat "$PORT_FILE" 2>/dev/null || true)"

  if [[ ! -f "$PID_FILE" ]]; then
    stop_port "$port"
    return 0
  fi

  local pid
  pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  if is_alive "$pid"; then
    stop_pid "$pid" "LeapView dev server"
  fi
  rm -f "$PID_FILE"
  stop_port "$port"
}

stop_port() {
  local port="${1:-}"
  [[ -z "$port" ]] && return 0

  local pids
  pids="$(port_pids "$port")"
  [[ -z "$pids" ]] && return 0

  while read -r pid; do
    [[ -z "$pid" ]] && continue
    if same_worktree_pid "$pid"; then
      stop_pid "$pid" "LeapView dev server on port $port"
    fi
  done <<< "$pids"
}

worktree_port() {
  if [[ -n "${PORT:-}" ]]; then
    echo "$PORT"
    return 0
  fi

  if [[ -f "$PORT_FILE" ]]; then
    local saved
    saved="$(cat "$PORT_FILE" 2>/dev/null || true)"
    if [[ "$saved" =~ ^[0-9]+$ ]]; then
      echo "$saved"
      return 0
    fi
  fi

  local checksum
  checksum="$(printf '%s' "$ROOT" | cksum | awk '{print $1}')"
  echo $((PORT_START + checksum % PORT_COUNT))
}

port_is_free() {
  local port="$1"
  [[ -z "$(port_pids "$port")" ]]
}

same_worktree_pid() {
  local pid="$1"
  [[ "$(pid_cwd "$pid")" == "$ROOT" ]]
}

recorded_port() {
  [[ -f "$PORT_FILE" ]] && cat "$PORT_FILE" 2>/dev/null || true
}

same_worktree_port_pid() {
  local port="${1:-}"
  [[ -n "$port" ]] || return 1

  local pids
  pids="$(port_pids "$port")"
  [[ -n "$pids" ]] || return 1

  while read -r pid; do
    [[ -z "$pid" ]] && continue
    if same_worktree_pid "$pid"; then
      echo "$pid"
      return 0
    fi
  done <<< "$pids"
  return 1
}

running_server_pid() {
  local port
  port="$(recorded_port)"
  same_worktree_port_pid "$port"
}

ensure_port() {
  local candidate="$1"
  local end=$((PORT_START + PORT_COUNT - 1))
  local offset=0

  while (( offset < PORT_COUNT )); do
    local port=$((candidate + offset))
    if (( port > end )); then
      port=$((PORT_START + port - end - 1))
    fi

    local pids
    pids="$(port_pids "$port")"
    if [[ -z "$pids" ]]; then
      echo "$port"
      return 0
    fi

    local stopped=false
    local blocked=false
    while read -r pid; do
      [[ -z "$pid" ]] && continue
      if same_worktree_pid "$pid"; then
        stop_pid "$pid" "LeapView dev server on port $port"
        stopped=true
      else
        blocked=true
      fi
    done <<< "$pids"

    if [[ "$stopped" == true && "$blocked" == false ]] && port_is_free "$port"; then
      echo "$port"
      return 0
    fi

    offset=$((offset + 1))
  done

  echo "No free port found in ${PORT_START}-$end" >&2
  exit 1
}

runner_name() {
  if command -v air >/dev/null 2>&1; then
    echo "air"
  else
    echo "binary"
  fi
}

ensure_dev_extension_supply() {
  local root="$TMP_DIR/dev-extension-supply"
  local manifest="$root/extension-supply.json"
  local digest_file="$manifest.sha256"
	local expected_digest
	local actual_digest
	echo "Preparing bounded reviewed DuckDB extension fixtures for development..."
	mkdir -p "$root"
	go run ./internal/app/tools/ducklakeprepare --supply-out "$manifest" >/dev/null
	expected_digest="$(awk 'NF {print $1; exit}' "$digest_file")"
	actual_digest="$(sha256sum "$manifest" | awk 'NF {print $1; exit}')"
	[[ "$expected_digest" =~ ^[0-9a-f]{64}$ && "$actual_digest" == "$expected_digest" ]] || {
		echo "Development extension supply digest is invalid" >&2
		return 1
	}
  export LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH="$manifest"
	export LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_SHA256="$expected_digest"
}

wait_ready() {
  local port="$1"
  local pid="$2"
  local attempts="${LEAPVIEW_DEV_READY_ATTEMPTS:-150}"
  local interval="${LEAPVIEW_DEV_READY_INTERVAL:-0.2}"

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if curl -fsS "http://localhost:$port/healthz" >/dev/null 2>&1; then
      return 0
    fi
    if ! is_alive "$pid"; then
      echo "LeapView dev server exited before it became ready" >&2
      return 1
    fi
    sleep "$interval"
  done

  echo "LeapView dev server did not become ready on http://localhost:$port" >&2
  return 1
}

mcp_call() {
  local port="$1"
  local body="$2"
  local token="${LEAPVIEW_DEV_API_TOKEN:-dev}"
  curl --fail --silent --show-error --max-time 15 \
    --config <(printf 'header = "Authorization: Bearer %s"\n' "$token") \
    --header 'Content-Type: application/json' \
    --header 'Accept: application/json, text/event-stream' \
    --header 'Mcp-Protocol-Version: 2025-11-25' \
    --data "$body" \
    "http://localhost:${port}/mcp"
}

mcp_smoke() {
  local port="$1"
  command -v jq >/dev/null 2>&1 || {
    echo "jq is required for the development MCP smoke check" >&2
    return 1
  }

  local listed
  listed="$(mcp_call "$port" '{"jsonrpc":"2.0","id":"dev-tools","method":"tools/list","params":{}}')" || return 1
  jq -e '
    (.error == null) and
    ([.result.tools[].name] | contains(["catalog_list", "catalog_search", "query_semantic_model"]))
  ' <<<"$listed" >/dev/null || {
    echo "Development MCP smoke check could not list the required tools" >&2
    return 1
  }

  local attempts="${LEAPVIEW_DEV_MCP_ATTEMPTS:-20}"
  local interval="${LEAPVIEW_DEV_MCP_INTERVAL:-0.5}"
  local catalog metric query_arguments
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    catalog="$(mcp_call "$port" '{"jsonrpc":"2.0","id":"dev-catalog","method":"tools/call","params":{"name":"catalog_list","arguments":{}}}')" || return 1
    if jq -e '(.error == null) and (.result.isError != true) and (.result.structuredContent.count > 0)' <<<"$catalog" >/dev/null; then
      break
    fi
    if (( attempt == attempts )); then
      echo "Development MCP smoke check found no accessible project resources after ${attempts} attempts" >&2
      printf '%s\n' "$catalog" >&2
      return 1
    fi
    sleep "$interval"
  done

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    metric="$(mcp_call "$port" '{"jsonrpc":"2.0","id":"dev-semantic-model","method":"tools/call","params":{"name":"catalog_search","arguments":{"query":"sales","kinds":["semantic_model"],"limit":1}}}')" || return 1
    query_arguments="$(jq -ce '
      .result.structuredContent.items[0] as $item |
      {model: $item.ref.id, metrics: [{field: "revenue"}], limit: 1}
    ' <<<"$metric" 2>/dev/null || true)"
    if [[ -n "$query_arguments" ]]; then
      break
    fi
    if (( attempt == attempts )); then
      echo "Development MCP smoke check could not resolve a semantic metric after ${attempts} attempts" >&2
      printf '%s\n' "$metric" >&2
      return 1
    fi
    sleep "$interval"
  done

  local query_body query
  query_body="$(jq -cn --argjson arguments "$query_arguments" '{jsonrpc:"2.0", id:"dev-query", method:"tools/call", params:{name:"query_semantic_model", arguments:$arguments}}')"
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    query="$(mcp_call "$port" "$query_body")" || return 1
    if jq -e '
      (.error == null) and (.result.isError != true) and
      (.result.structuredContent.queryId | type == "string" and length > 0) and
      (.result.structuredContent.rows | type == "array")
    ' <<<"$query" >/dev/null; then
      echo "Agent MCP smoke check passed"
      return 0
    fi
    sleep "$interval"
  done
  echo "Development MCP smoke check semantic query failed after ${attempts} attempts" >&2
  printf '%s\n' "$query" >&2
  return 1
}

canonical_source_root() {
	local source_root="$1"
	if [[ "$source_root" != /* ]]; then
		source_root="$ROOT/$source_root"
	fi
	if [[ ! -d "$source_root" ]]; then
		echo "Managed data source root does not exist or is not a directory: $source_root" >&2
		return 1
	fi
	(
		cd -- "$source_root"
		pwd -P
	)
}

publish_project() {
	local port="$1"
	local project="${2:-${LEAPVIEW_DEV_PROJECT:-dashboards/leapview.yaml}}"
	local connection="${3:-}"
	local token="${LEAPVIEW_DEV_API_TOKEN:-dev}"
	local from="${4:-}"
	if [[ "${LEAPVIEW_DEV_SKIP_PUBLISH:-}" == "1" ]]; then
    echo "Skipping dev candidate publication"
    return 0
  fi
	if [[ "$project" == "dashboards/leapview.yaml" ]]; then
		connection="${connection:-olist}"
		from="${from:-.data/olist}"
	fi
	if [[ -n "$connection" ]]; then
		[[ -n "$from" ]] || {
			echo "source-root is required when a managed data connection is provided." >&2
			return 1
		}
		from="$(canonical_source_root "$from")" || return 1
		local sync_output revision
		sync_output="$(go run ./cmd/leapview data sync --project "$project" --connection "$connection" --from "$from" --target "http://localhost:${port}" --token "$token")" || return 1
    printf '%s\n' "$sync_output"
    revision="$(printf '%s\n' "$sync_output" | awk '$1 == "staged" { print $2 }')"
    [[ "$revision" =~ ^sha256:[0-9a-f]{64}$ ]] || {
      echo "Managed data sync did not return a canonical revision." >&2
      return 1
    }
	fi
	local dev_output candidate_id
	dev_output="$(go run ./cmd/leapview dev --once --no-browser --project "$project" --target "http://localhost:${port}" --token "$token")" || return 1
	printf '%s\n' "$dev_output"
	candidate_id="$(awk '$1 == "candidate" { print $2; exit }' <<<"$dev_output")"
	[[ "$candidate_id" =~ ^cand_[A-Za-z0-9_-]+$ ]] || {
		echo "Development candidate publication did not return a canonical candidate ID." >&2
		return 1
	}
	go run ./cmd/leapview publish "$candidate_id" --token "$token" || return 1
	mcp_smoke "$port"
}

publish_running() {
	local project="${1:-${LEAPVIEW_DEV_PROJECT:-dashboards/leapview.yaml}}"
	local connection="${2:-}"
	local from="${3:-}"
	local port
	port="$(recorded_port)"
	[[ -n "$port" ]] || {
		echo "Dev server port file missing. Run task dev first." >&2
		return 1
	}
  curl -fsS "http://localhost:${port}/" >/dev/null || {
		echo "Dev server is not running on http://localhost:${port}. Run task dev first." >&2
		return 1
	}
	cd "$ROOT"
	publish_project "$port" "$project" "$connection" "$from"
}

attach_server() {
  local pid="$1"
  local port="$2"
  local tail_pid=""

  touch "$LOG_FILE"
  tail -n "${LEAPVIEW_DEV_LOG_LINES:-120}" -f "$LOG_FILE" &
  tail_pid="$!"

  cleanup_attach() {
    [[ -n "$tail_pid" ]] && kill "$tail_pid" 2>/dev/null || true
    if is_alive "$pid"; then
      stop_pid "$pid" "LeapView dev server"
    fi
    stop_port "$port"
  }
  trap cleanup_attach INT TERM

  while is_alive "$pid"; do
    sleep 1
  done
  [[ -n "$tail_pid" ]] && kill "$tail_pid" 2>/dev/null || true
  stop_port "$port"
  trap - INT TERM
}

start() {
	local project="${1:-${LEAPVIEW_DEV_PROJECT:-dashboards/leapview.yaml}}"
	local connection="${2:-}"
	local from="${3:-}"
  if [[ "${LEAPVIEW_DEV_RESTART:-}" != "1" ]]; then
    local existing_pid
    existing_pid="$(running_server_pid || true)"
    if [[ -n "$existing_pid" ]]; then
      local existing_port
      existing_port="$(recorded_port)"
      echo "LeapView dev server already running"
      echo "PID: $existing_pid"
      echo "URL: http://localhost:$existing_port"
      echo "Logs: $LOG_FILE"
      echo "Publishing project candidate to existing target..."
			publish_project "$existing_port" "$project" "$connection" "$from"
      if [[ "${LEAPVIEW_DEV_ONCE:-}" == "1" ]]; then
        echo "One-shot candidate publication completed on the existing target"
        return 0
      fi
      echo "Attached to LeapView logs. Press Ctrl-C to stop."
      attach_server "$existing_pid" "$existing_port"
      return 0
    fi
  fi

  stop_recorded

  local preferred
  preferred="$(worktree_port)"
  local port
  port="$(ensure_port "$preferred")"
  echo "$port" > "$PORT_FILE"
  rm -f "$PID_FILE"

  local runner
  runner="$(runner_name)"
  echo "Starting LeapView on http://localhost:$port"
  if [[ "$runner" == "air" ]]; then
    echo "Runner: air"
  else
    echo "Runner: local binary (install air for hot reload)"
  fi
  if [[ "${LEAPVIEW_DEV_SKIP_PUBLISH:-}" == "1" ]]; then
    echo "Candidate publication disabled. Press Ctrl-C to stop."
  else
    echo "Publishing project candidate after startup. Press Ctrl-C to stop."
  fi

  cd "$ROOT"
  ensure_dev_extension_supply
  export PORT="$port"
  export LEAPVIEW_ADDR=":$port"
  export LEAPVIEW_DEV_WORKTREE="$ROOT"
  export LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES="${LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES:-67108864}"
  if [[ -z "${LEAPVIEW_AGENT_API_KEY:-}" && -n "${DEEPSEEK_API_KEY:-}" ]]; then
    export LEAPVIEW_AGENT_API_KEY="$DEEPSEEK_API_KEY"
    export LEAPVIEW_AGENT_BASE_URL="${LEAPVIEW_AGENT_BASE_URL:-https://api.deepseek.com}"
    export LEAPVIEW_AGENT_MODEL="${LEAPVIEW_AGENT_MODEL:-deepseek-v4-flash}"
  fi

  : > "$LOG_FILE"
  if [[ "$runner" == "air" ]]; then
    air -c .air.toml >> "$LOG_FILE" 2>&1 &
  else
    go build -tags=duckdb_arrow -o "$TMP_DIR/leapview-dev" ./cmd/leapview
    "$TMP_DIR/leapview-dev" >> "$LOG_FILE" 2>&1 &
  fi
  local pid="$!"
  echo "$pid" > "$PID_FILE"

  if ! wait_ready "$port" "$pid"; then
    stop_pid "$pid" "LeapView dev server"
    exit 1
  fi

	if ! publish_project "$port" "$project" "$connection" "$from"; then
    stop_pid "$pid" "LeapView dev server"
    exit 1
  fi

  if [[ "${LEAPVIEW_DEV_ONCE:-}" == "1" ]]; then
    stop_pid "$pid" "LeapView one-shot dev server"
    rm -f "$PID_FILE"
    stop_port "$port"
    echo "One-shot candidate publication completed"
    return 0
  fi

  echo "LeapView listening at http://localhost:$port"
  echo "Attached to LeapView logs. Press Ctrl-C to stop."
  attach_server "$pid" "$port"
}

stop() {
  stop_recorded
  echo "LeapView dev server stopped"
}

status() {
  local port=""
  [[ -f "$PORT_FILE" ]] && port="$(cat "$PORT_FILE" 2>/dev/null || true)"
  local pid=""
  [[ -f "$PID_FILE" ]] && pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  local port_pid=""
  if [[ -n "$port" ]]; then
    port_pid="$(port_pids "$port" | head -n 1)"
  fi

  if is_alive "$pid"; then
    echo "LeapView dev server running"
    echo "PID: $pid"
    [[ -n "$port" ]] && echo "URL: http://localhost:$port"
    echo "Command: $(pid_command "$pid")"
    echo "Logs: $LOG_FILE"
    return 0
  fi

  if is_alive "$port_pid" && same_worktree_pid "$port_pid"; then
    echo "LeapView dev server running"
    echo "PID: $port_pid"
    [[ -n "$port" ]] && echo "URL: http://localhost:$port"
    echo "Command: $(pid_command "$port_pid")"
    echo "Logs: $LOG_FILE"
    return 0
  fi

  echo "LeapView dev server not running"
  [[ -n "$port" ]] && echo "Last port: $port"
  echo "Logs: $LOG_FILE"
}

logs() {
  touch "$LOG_FILE"
  tail -n "${LEAPVIEW_DEV_LOG_LINES:-120}" -f "$LOG_FILE"
}

action="${1:-}"
shift || true
case "$action" in
  start) start "$@" ;;
  once) LEAPVIEW_DEV_ONCE=1 start "$@" ;;
  publish) publish_running "$@" ;;
  stop) stop ;;
  status) status ;;
  logs) logs ;;
  *) usage >&2; exit 2 ;;
esac
