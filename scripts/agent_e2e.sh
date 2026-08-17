#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$HOME/.zshrc.secrets" ]]; then
  # shellcheck disable=SC1090
  source "$HOME/.zshrc.secrets"
fi

DEEPSEEK_KEY="${DEEPSEEK_API_TOKEN:-${DEEPSEEK_API_KEY:-}}"
if [[ -z "$DEEPSEEK_KEY" ]]; then
  echo "DEEPSEEK_API_TOKEN or DEEPSEEK_API_KEY is required in ~/.zshrc.secrets" >&2
  exit 1
fi

go run ./internal/tools/bootstrapolist --out .data/olist

TMP_DIR="$(mktemp -d)"
cleanup() {
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  chmod -R u+w "$TMP_DIR" 2>/dev/null || true
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

BIN="$TMP_DIR/leapview"
go build -tags=duckdb_arrow -o "$BIN" ./cmd/leapview
TOONJSON="$TMP_DIR/toonjson"
go build -o "$TOONJSON" ./internal/app/tools/toonjson

PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
TARGET="http://127.0.0.1:$PORT"

export LEAPVIEW_HOME="$TMP_DIR/home"
export LEAPVIEW_ADDR="127.0.0.1:$PORT"
export LEAPVIEW_PRODUCTION=false
export LEAPVIEW_ENVIRONMENT=dev
export LEAPVIEW_API_TOKEN_ONLY_AUTH=false
export LEAPVIEW_LOCAL_AUTH=false
export LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES=536870912
export LEAPVIEW_DEV_API_TOKEN="agent-e2e-dev-token"
export LEAPVIEW_CSRF_KEY="agent-e2e-csrf-key-agent-e2e-csrf-key"
export LEAPVIEW_METRICS_BEARER_TOKEN="agent-e2e-metrics-token-agent-e2e"
export LEAPVIEW_AGENT_API_KEY="$DEEPSEEK_KEY"
export LEAPVIEW_AGENT_BASE_URL="https://api.deepseek.com"
export LEAPVIEW_AGENT_MODEL="deepseek-v4-flash"

TOKEN="$LEAPVIEW_DEV_API_TOKEN"
"$BIN" serve > "$TMP_DIR/server.log" 2>&1 &
SERVER_PID="$!"

for _ in {1..80}; do
  if curl -fsS -H "Authorization: Bearer $TOKEN" "$TARGET/api/v1/projects?limit=1" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    cat "$TMP_DIR/server.log" >&2
    exit 1
  fi
  sleep 0.25
done

SYNC_OUTPUT="$("$BIN" data sync --project dashboards/leapview.yaml --connection olist --from .data/olist --target "$TARGET" --token "$TOKEN")"
echo "$SYNC_OUTPUT"
REVISION="$(awk '$1 == "staged" { print $2 }' <<<"$SYNC_OUTPUT")"
[[ "$REVISION" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "managed data sync did not return a canonical revision" >&2
  exit 1
}
"$BIN" dev --once --no-browser --target "$TARGET" --token "$TOKEN" --project dashboards/leapview.yaml
"$BIN" publish --target "$TARGET" --token "$TOKEN" --project dashboards/leapview.yaml

ALLOWED_TOOLS='catalog_search,catalog_list,catalog_get,query_semantic_model,query_dashboard_visual,query_visual,docs_search,docs_read'

run_agent_scenario() {
  local label="$1"
  local expected_tools="$2"
  local validator="$3"
  local question="$4"
  local output conversation stop_reason messages

  output="$("$BIN" agent ask "$question" --target "$TARGET" --token "$TOKEN" --json)"
  echo "$label: $output"
  conversation="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["conversationId"])' <<<"$output")"
  stop_reason="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["run"]["stopReason"])' <<<"$output")"
  if [[ "$stop_reason" != "completed" ]]; then
    echo "$label: agent run stopped with $stop_reason" >&2
    exit 1
  fi
  messages="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$TARGET/api/v1/agent/conversations/$conversation/messages?limit=200")"
  python3 -c '
import json
import sys

import subprocess

label, expected_csv, allowed_csv, validator, toonjson = sys.argv[1:6]
payload = json.load(sys.stdin)
items = payload.get("items", [])
names = [item.get("toolName", "") for item in items if item.get("toolName")]
allowed = set(allowed_csv.split(","))
legacy = sorted(set(names) - allowed)
if legacy:
    raise SystemExit(f"{label}: transcript used non-curated tools: {legacy}")
expected = [name for name in expected_csv.split(",") if name]
missing = [name for name in expected if name not in names]
if missing:
    raise SystemExit(f"{label}: transcript missed expected tools {missing}; used {names}")
successful = {
    item.get("toolName", "")
    for item in payload.get("items", [])
    if item.get("toolName") and not item.get("isError", False)
}
failed = [name for name in expected if name not in successful]
if failed:
    raise SystemExit(f"{label}: expected tools never succeeded {failed}; used {names}")

def results(name):
    decoded = []
    for item in items:
        if item.get("toolName") != name or item.get("isError", False):
            continue
        try:
            converted = subprocess.run(
                [toonjson],
                input=item.get("contentText", ""),
                text=True,
                capture_output=True,
                check=True,
            )
            value = json.loads(converted.stdout)
        except (subprocess.CalledProcessError, json.JSONDecodeError) as exc:
            raise SystemExit(f"{label}: could not decode {name} model content: {exc}") from exc
        if not isinstance(value, dict):
            raise SystemExit(f"{label}: {name} returned non-object model content: {value!r}")
        decoded.append(value)
    return decoded

answers = [
    item.get("contentText", "")
    for item in items
    if item.get("role") == "assistant" and not item.get("toolName")
]
answer = answers[-1] if answers else ""
if not answer.strip():
    raise SystemExit(f"{label}: transcript has no final assistant answer")

if validator == "project":
    pages = results("catalog_list")
    if not any(any(entry.get("ref", {}).get("kind") == "project" for entry in page.get("items", [])) for page in pages):
        raise SystemExit(f"{label}: catalog_list returned no project refs")
elif validator == "dashboard_search":
    pages = results("catalog_search")
    matches = [entry for page in pages for entry in page.get("items", [])]
    if not any(entry.get("ref", {}).get("type") == "dashboard" and "executive" in entry.get("name", "").lower() and entry.get("href") for entry in matches):
        raise SystemExit(f"{label}: search did not return the Executive Sales dashboard with an href")
elif validator == "page_definition":
    definitions = results("catalog_get")
    if not any(value.get("details", {}).get("type") == "page" and value.get("details", {}).get("components") for value in definitions):
        raise SystemExit(f"{label}: catalog_get did not return a populated page definition")
elif validator == "semantic_query":
    queries = results("query_semantic_model")
    if not queries:
        raise SystemExit(f"{label}: semantic query produced no result")
    value = queries[-1]
    columns = value.get("columns", [])
    if not value.get("queryId") or not value.get("servingSnapshot") or not value.get("rows"):
        raise SystemExit(f"{label}: semantic result lacks rows or provenance: {value}")
    if not any(column.get("fieldRef", {}).get("id", "").endswith(".revenue") and column.get("format") == "currency" for column in columns):
        raise SystemExit(f"{label}: semantic result did not preserve the revenue currency format: {columns}")
    if value.get("completeness", {}).get("returnedRows") != len(value.get("rows", [])):
        raise SystemExit(f"{label}: semantic completeness does not match emitted rows")
elif validator == "semantic_pagination":
    queries = results("query_semantic_model")
    if len(queries) < 2:
        raise SystemExit(f"{label}: expected at least two semantic query pages, got {len(queries)}")
    if not queries[0].get("hasMore") or not queries[0].get("nextCursor"):
        raise SystemExit(f"{label}: first semantic page did not expose continuation")
    emitted = sum(len(value.get("rows", [])) for value in queries)
    if emitted < 60:
        raise SystemExit(f"{label}: paginated semantic query emitted only {emitted} rows")
    if any(value.get("completeness", {}).get("returnedRows") != len(value.get("rows", [])) for value in queries):
        raise SystemExit(f"{label}: a semantic page cursor skipped model-visible rows")
elif validator == "dashboard_visual":
    queries = results("query_dashboard_visual")
    if not queries:
        raise SystemExit(f"{label}: dashboard visual query produced no result")
    value = queries[-1]
    if value.get("visualId") != "revenue_kpi" or value.get("type") != "kpi" or not value.get("rows"):
        raise SystemExit(f"{label}: dashboard visual result is not the requested KPI: {value}")
    if not value.get("queryId") or not value.get("servingSnapshot") or "completeness" not in value:
        raise SystemExit(f"{label}: dashboard visual lacks provenance or completeness")
elif validator == "generated_visual":
    queries = results("query_visual")
    if not queries:
        raise SystemExit(f"{label}: generated visual produced no result")
    value = queries[-1]
    if value.get("type") != "bar" or value.get("status", {}).get("kind") not in {"ready", "no_data"}:
        raise SystemExit(f"{label}: generated visual has unexpected type/status: {value}")
    if not any(filter_value.get("operator") == "equals" and "delivered" in filter_value.get("values", []) for filter_value in value.get("filters", [])):
        raise SystemExit(f"{label}: generated visual did not preserve the delivered filter: {value.get('filters')}")
    if not any(field.get("role") == "measure" and field.get("format") == "currency" for field in value.get("fields", [])):
        raise SystemExit(f"{label}: generated visual did not preserve measure formatting: {value.get('fields')}")
    if not value.get("queryId") or not value.get("servingSnapshot") or "completeness" not in value:
        raise SystemExit(f"{label}: generated visual lacks provenance or completeness")
elif validator == "documentation":
    reads = results("docs_read")
    if not reads:
        raise SystemExit(f"{label}: docs_read produced no result")
    if not any(len(value.get("content", "")) > 2000 and value.get("lineEnd", 0) >= value.get("lineStart", 1) for value in reads):
        raise SystemExit(f"{label}: docs_read did not deliver content beyond the former 2,000-character harness limit")
    if "relationship" not in answer.lower():
        raise SystemExit(f"{label}: final answer does not summarize semantic relationships")
' "$label" "$expected_tools" "$ALLOWED_TOOLS" "$validator" "$TOONJSON" <<<"$messages"
}

run_agent_scenario \
  "project discovery" \
  "catalog_list" \
  "project" \
  "Use catalog browsing to list the project resources I can access."

run_agent_scenario \
  "global dashboard search" \
  "catalog_search" \
  "dashboard_search" \
  "Use global catalog search to find the executive sales dashboard in the project catalog."

run_agent_scenario \
  "dashboard and page browsing" \
  "catalog_search,catalog_list,catalog_get" \
  "page_definition" \
  "Find the executive sales dashboard with catalog search, browse its pages with catalog list, then inspect the overview page definition with catalog get."

run_agent_scenario \
  "semantic query" \
  "catalog_search,query_semantic_model" \
  "semantic_query" \
  "Use catalog_search with semantic_model kind to find the Sales semantic model. Then call query_semantic_model with the returned model resource, measure sales.revenue, and limit 1. Report the returned format and freshness."

run_agent_scenario \
  "semantic pagination" \
  "catalog_search,query_semantic_model" \
  "semantic_pagination" \
  "Use catalog_search to confirm the Sales semantic model. Call query_semantic_model with its returned model resource, dimension sales.orders.order_id, and limit 50. Then pass its nextCursor back as pageToken in a second query_semantic_model call so at least 60 distinct order IDs are returned without skipping model-visible rows."

run_agent_scenario \
  "dashboard visual query" \
  "catalog_search,query_dashboard_visual" \
  "dashboard_visual" \
  "Find the revenue_kpi visual on the Executive Sales overview page, then query that exact dashboard visual using its returned ref and location."

run_agent_scenario \
  "generated visualization" \
  "catalog_search,query_visual" \
  "generated_visual" \
  "Use catalog_search to find the Sales semantic model. Then call query_visual with its returned model resource, dataset sales_orders, a bar chart grouped by sales_orders.status with measure sales.revenue, and a governed equals filter on sales_orders.status for delivered."

run_agent_scenario \
  "documentation" \
  "docs_search,docs_read" \
  "documentation" \
  "Search the LeapView product documentation for semantic relationships, read at least 80 lines from the relevant document in one docs_read window, and summarize the documented relationship behavior."
