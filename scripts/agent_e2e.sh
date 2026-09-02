#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DEEPSEEK_KEY="${DEEPSEEK_API_TOKEN:-${DEEPSEEK_API_KEY:-}}"
if [[ -z "$DEEPSEEK_KEY" ]]; then
  echo "DEEPSEEK_API_TOKEN or DEEPSEEK_API_KEY is required" >&2
  exit 1
fi

REPORT_PATH="${AGENT_EVAL_REPORT:-$ROOT/.data/agent-e2e/report.json}"
TRANSCRIPT_PATH="${AGENT_EVAL_TRANSCRIPT:-${REPORT_PATH%.json}.jsonl}"
mkdir -p "$(dirname "$REPORT_PATH")" "$(dirname "$TRANSCRIPT_PATH")"
: > "$TRANSCRIPT_PATH"

go run ./internal/app/tools/bootstrapolist --out .data/olist
go run ./internal/app/tools/mapassets --shared-cache --out .data/map-assets
OLIST_ROOT="$(realpath .data/olist)"

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

EXTENSION_SUPPLY_DIR="$TMP_DIR/dev-extension-supply"
EXTENSION_SUPPLY_PATH="$EXTENSION_SUPPLY_DIR/extension-supply.json"
mkdir -p "$EXTENSION_SUPPLY_DIR"
go run ./internal/app/tools/ducklakeprepare --supply-out "$EXTENSION_SUPPLY_PATH" >/dev/null
export LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH="$EXTENSION_SUPPLY_PATH"
export LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_SHA256
LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_SHA256="$(awk 'NF {print $1; exit}' "$EXTENSION_SUPPLY_PATH.sha256")"

BIN="$TMP_DIR/leapview"
TOONJSON="$TMP_DIR/toonjson"
go build -tags=duckdb_arrow -o "$BIN" ./cmd/leapview
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
export LEAPVIEW_DEV_AUTH_BYPASS=true
export LEAPVIEW_ENVIRONMENT=dev
export LEAPVIEW_API_TOKEN_ONLY_AUTH=false
export LEAPVIEW_LOCAL_AUTH=false
export LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES=536870912
export LEAPVIEW_DEV_API_TOKEN="agent-e2e-dev-token"
export LEAPVIEW_CSRF_KEY="agent-e2e-csrf-key-agent-e2e-csrf-key"
export LEAPVIEW_METRICS_BEARER_TOKEN="agent-e2e-metrics-token-agent-e2e"
export LEAPVIEW_AGENT_API_KEY="$DEEPSEEK_KEY"
export LEAPVIEW_AGENT_BASE_URL="https://api.deepseek.com"
export LEAPVIEW_AGENT_MODEL="${LEAPVIEW_AGENT_MODEL:-deepseek-v4-flash}"

TOKEN="$LEAPVIEW_DEV_API_TOKEN"
"$BIN" serve > "$TMP_DIR/server.log" 2>&1 &
SERVER_PID="$!"

for _ in {1..120}; do
  if curl -fsS -H "Authorization: Bearer $TOKEN" "$TARGET/api/v1/projects?limit=1" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    cat "$TMP_DIR/server.log" >&2
    exit 1
  fi
  sleep 0.25
done

SYNC_OUTPUT="$("$BIN" data sync --project dashboards/leapview.yaml --connection olist --from "$OLIST_ROOT" --target "$TARGET" --token "$TOKEN")"
REVISION="$(awk '$1 == "staged" { print $2 }' <<<"$SYNC_OUTPUT")"
[[ "$REVISION" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "managed data sync did not return a canonical revision" >&2
  exit 1
}
DEV_OUTPUT="$("$BIN" dev --once --no-browser --target "$TARGET" --token "$TOKEN" --project dashboards/leapview.yaml)"
CANDIDATE_ID="$(awk '$1 == "candidate" { print $2; exit }' <<<"$DEV_OUTPUT")"
[[ "$CANDIDATE_ID" =~ ^cand_[A-Za-z0-9_-]+$ ]] || {
  echo "development candidate publication did not return a canonical candidate ID" >&2
  exit 1
}
"$BIN" publish "$CANDIDATE_ID" --token "$TOKEN" >/dev/null

ALLOWED_TOOLS='add_dashboard_page,add_dashboard_visual,assign_dashboard_field,catalog_get,catalog_list,catalog_search,create_dashboard_draft,docs_read,docs_search,edit_dashboard_source,execute_dashboard_command,export_dashboard_yaml,fork_dashboard,get_dashboard,get_dashboard_draft,list_dashboards,preview_dashboard_draft,query_dashboard_visual,query_semantic_model,query_visual,read_dashboard_source,set_dashboard_visibility'

run_agent_scenario() {
  local label="$1"
  local expected_tools="$2"
  local validator="$3"
  local question="$4"
  local output conversation stop_reason messages

  if [[ -n "${AGENT_EVAL_SCENARIO:-}" && ",${AGENT_EVAL_SCENARIO}," != *",${label},"* ]]; then
    return 0
  fi

  if ! output="$("$BIN" agent ask "$question" --target "$TARGET" --token "$TOKEN" --json)"; then
    python3 -c 'import json,sys; open(sys.argv[2], "a").write(json.dumps({"scenario":sys.argv[1],"runFailed":True,"stopReason":"command_failed","expected":sys.argv[3].split(","),"calls":[],"validationErrors":["agent command failed"]},separators=(",",":"))+"\n")' "$label" "$TRANSCRIPT_PATH" "$expected_tools"
    echo "$label: agent command failed" >&2
    return 0
  fi
  conversation="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["conversationId"])' <<<"$output")"
  stop_reason="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["run"]["stopReason"])' <<<"$output")"
  messages="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$TARGET/api/v1/agent/conversations/$conversation/messages?limit=200")"

  python3 -c '
import json
import subprocess
import sys

label, expected_csv, allowed_csv, validator, toonjson, report, stop_reason, conversation_id = sys.argv[1:9]
items = json.load(sys.stdin).get("items", [])
arguments = {}
for item in items:
    if item.get("role") != "assistant":
        continue
    content = item.get("content") or {}
    for call in content.get("tool_calls") or []:
        arguments[call.get("id", "")] = call.get("arguments")

calls = []
decoded_results = {}
for item in items:
    name = item.get("toolName", "")
    if not name:
        continue
    error_code = ""
    error_detail = ""
    decoded = None
    try:
        converted = subprocess.run([toonjson], input=item.get("contentText", ""), text=True, capture_output=True, check=True)
        decoded = json.loads(converted.stdout)
        if isinstance(decoded, dict) and isinstance(decoded.get("error"), dict):
            error_code = str(decoded["error"].get("code", ""))
            error_detail = str(decoded["error"].get("message", ""))
    except (subprocess.CalledProcessError, json.JSONDecodeError):
        pass
    if not item.get("isError", False) and isinstance(decoded, dict):
        decoded_results.setdefault(name, []).append(decoded)
    calls.append({
        "name": name,
        "callId": item.get("toolCallId", ""),
        "arguments": arguments.get(item.get("toolCallId", "")),
        "error": bool(item.get("isError", False)),
        "errorCode": error_code,
        "errorDetail": error_detail,
    })

expected = [name for name in expected_csv.split(",") if name]
allowed = set(name for name in allowed_csv.split(",") if name)
seen = [call["name"] for call in calls]
successful = {call["name"] for call in calls if not call["error"]}
validation_errors = []
if stop_reason != "completed":
    validation_errors.append(f"run stopped with {stop_reason}")
unknown = sorted(set(seen) - allowed)
if unknown:
    validation_errors.append(f"unknown tools: {unknown}")
missing = [name for name in expected if name not in seen]
never_succeeded = [name for name in expected if name not in successful]
if missing:
    validation_errors.append(f"missing tools: {missing}")
if never_succeeded:
    validation_errors.append(f"tools never succeeded: {never_succeeded}")

if validator == "semantic_query":
    values = decoded_results.get("query_semantic_model", [])
    if not values or not values[-1].get("rows") or not values[-1].get("queryId"):
        validation_errors.append("semantic query lacks rows or provenance")
elif validator == "semantic_pagination":
    values = decoded_results.get("query_semantic_model", [])
    if len(values) < 2 or not values[0].get("nextCursor"):
        validation_errors.append("semantic pagination lacks two pages or continuation")
elif validator == "dashboard_visual":
    values = decoded_results.get("query_dashboard_visual", [])
    if not values or values[-1].get("visualId") != "revenue_kpi" or not values[-1].get("rows"):
        validation_errors.append("dashboard visual result is incomplete")
elif validator == "generated_visual":
    values = decoded_results.get("query_visual", [])
    if not values or values[-1].get("type") not in {"bar", "column"} or values[-1].get("status", {}).get("kind") not in {"ready", "no_data"}:
        validation_errors.append("generated visual result is incomplete")
elif validator == "documentation":
    values = decoded_results.get("docs_read", [])
    if not values or not any(len(value.get("content", "")) > 2000 for value in values):
        validation_errors.append("documentation read window is incomplete")
elif validator == "dashboard_source_edit":
    reads = decoded_results.get("read_dashboard_source", [])
    edits = decoded_results.get("edit_dashboard_source", [])
    if len(reads) < 2:
        validation_errors.append("dashboard source edit lacks before and after reads")
    if not edits:
        validation_errors.append("dashboard source edit result is missing")
    else:
        edited = edits[-1]
        if edited.get("changedBlocks") != 1:
            validation_errors.append("dashboard source edit did not change exactly one block")
        if "displayName: Agent Eval Sales Edited" not in edited.get("yaml", "") or "displayName: Agent Eval Sales Edited" not in edited.get("diff", ""):
            validation_errors.append("dashboard source edit lacks canonical YAML or diff evidence")
    if len(reads) >= 2:
        before, after = reads[0], reads[-1]
        if "displayName: Agent Eval Sales" not in before.get("yaml", ""):
            validation_errors.append("dashboard source before-read lacks the original title")
        if "displayName: Agent Eval Sales Edited" not in after.get("yaml", ""):
            validation_errors.append("dashboard source after-read lacks the edited title")
        if after.get("revision", {}).get("number", 0) <= before.get("revision", {}).get("number", 0):
            validation_errors.append("dashboard source revision did not advance")

record = {
    "scenario": label,
    "conversationId": conversation_id,
    "stopReason": stop_reason,
    "runFailed": False,
    "expected": expected,
    "missing": missing,
    "neverSucceeded": never_succeeded,
    "extra": sorted(set(seen) - set(expected)),
    "calls": calls,
    "validationErrors": validation_errors,
}
with open(report, "a") as target:
    target.write(json.dumps(record, separators=(",", ":")) + "\n")
print("{}: calls={} errors={} validation_errors={}".format(label, len(calls), sum(call["error"] for call in calls), validation_errors))
' "$label" "$expected_tools" "$ALLOWED_TOOLS" "$validator" "$TOONJSON" "$TRANSCRIPT_PATH" "$stop_reason" "$conversation" <<<"$messages"
}

run_agent_scenario "project discovery" "catalog_list" "basic" \
  "Use catalog_list to list the project resources I can access."
run_agent_scenario "global dashboard search" "catalog_search" "basic" \
  "Use catalog_search to find the Executive Sales dashboard."
run_agent_scenario "dashboard metadata" "catalog_search,catalog_get" "basic" \
  "Find Executive Sales with catalog_search, then inspect that exact dashboard resource with catalog_get."
run_agent_scenario "semantic query" "catalog_search,query_semantic_model" "semantic_query" \
  "Use catalog_search with semantic_model kind to find Sales. Then call query_semantic_model with the returned model, metric revenue, and limit 1."
run_agent_scenario "semantic pagination" "catalog_search,query_semantic_model" "semantic_pagination" \
  "Find the Sales semantic model. Query dimension purchase_date and metric order_count with limit 50, then use nextCursor as pageToken for a second page."
run_agent_scenario "dashboard visual query" "catalog_search,query_dashboard_visual" "dashboard_visual" \
  "Find revenue_kpi on the Executive Sales overview page, then query that exact dashboard visual using its returned ref and location."
run_agent_scenario "generated visualization" "catalog_search,query_visual" "generated_visual" \
  "Find the Sales semantic model, then call query_visual for a column chart grouped by category with metric revenue using governed semantic fields. Set both the aggregate query limit and dataBudget maxRows to 50."
run_agent_scenario "documentation" "docs_search,docs_read" "documentation" \
  "Search LeapView documentation for semantic relationships, read at least 80 lines from the relevant document in one docs_read call, and summarize it."
run_agent_scenario "authoring catalog and export" "list_dashboards,get_dashboard,export_dashboard_yaml" "basic" \
  "Use list_dashboards to find Executive Sales. Use its exact dashboard ID with get_dashboard, then export its canonical project YAML with export_dashboard_yaml and sourceKind project."
run_agent_scenario "authoring create foundation" "create_dashboard_draft,add_dashboard_page" "basic" \
  "Create a private dashboard draft with dashboardId dashboard:agent-eval-sales, title Agent Eval Sales, slug agent-eval-sales, and semantic model semantic-model:sales. Read that draft, add page details titled Details using its exact revision, then stop."
run_agent_scenario "authoring source edit" "read_dashboard_source,edit_dashboard_source" "dashboard_source_edit" \
  "Use read_dashboard_source with the exact dashboard ID dashboard:agent-eval-sales, including its dashboard: prefix. Change only displayName from Agent Eval Sales to Agent Eval Sales Edited with edit_dashboard_source, using the exact draft ID and revision returned by the read and an exact oldText/newText replacement. Then call read_dashboard_source again with dashboard:agent-eval-sales to verify the saved displayName and revision, and stop."
run_agent_scenario "authoring visual fields" "get_dashboard_draft,add_dashboard_visual,assign_dashboard_field" "basic" \
  "Read dashboard:agent-eval-sales. Add bar visual revenue-by-category to page details. Assign category as its dimension and revenue as its metric, always using the latest returned revision token, then stop."
run_agent_scenario "authoring visibility preview" "get_dashboard_draft,set_dashboard_visibility,preview_dashboard_draft" "basic" \
  "Read dashboard:agent-eval-sales, set its visibility to organization using the exact current revision, then preview page details using the latest returned revision and stop."
run_agent_scenario "authoring fork" "fork_dashboard" "basic" \
  "Fork project dashboard dashboard:executive-sales into a private draft titled Agent Eval Fork with slug agent-eval-fork, then stop."
run_agent_scenario "authoring publish and archive" "list_dashboards,get_dashboard_draft,execute_dashboard_command" "basic" \
  "Use list_dashboards to find the exact instance dashboard titled Agent Eval Fork. Read its draft, publish that exact revision with execute_dashboard_command, then archive the exact published revision with a second execute_dashboard_command call and stop."

python3 -c '
import collections
import json
import os
import sys

transcript, report, allowed_csv = sys.argv[1:4]
records = [json.loads(line) for line in open(transcript) if line.strip()]
allowed = [name for name in allowed_csv.split(",") if name]
selected_scenarios = bool(os.environ.get("AGENT_EVAL_SCENARIO"))
required = sorted({name for record in records for name in record.get("expected", [])}) if selected_scenarios else allowed
by_tool = {name: {"calls": 0, "errors": 0, "successes": 0, "errorCodes": collections.Counter()} for name in allowed}
all_calls = []
unexpected = 0
argument_failures = 0
model_mistakes = 0
recovery_calls = 0
backend_failures = collections.Counter()
model_failure_codes = collections.Counter()
platform_failure_codes = collections.Counter()
model_error_codes = {"invalid_arguments", "invalid_tool_arguments", "unknown_tool", "not_found", "resource_not_found", "catalog_not_found", "stale_revision"}
for record in records:
    expected = set(record.get("expected", []))
    failed_tools = set()
    for call in record.get("calls", []):
        all_calls.append(call)
        name = call.get("name", "")
        if name not in expected:
            unexpected += 1
        if name in failed_tools:
            recovery_calls += 1
        stats = by_tool.setdefault(name, {"calls": 0, "errors": 0, "successes": 0, "errorCodes": collections.Counter()})
        stats["calls"] += 1
        if call.get("error"):
            failed_tools.add(name)
            stats["errors"] += 1
            code = call.get("errorCode") or "unclassified"
            stats["errorCodes"][code] += 1
            detail = (call.get("errorDetail") or "").lower()
            if code in {"invalid_arguments", "invalid_tool_arguments", "unknown_tool"}:
                argument_failures += 1
            if code not in {"invalid_arguments", "invalid_tool_arguments", "unknown_tool"}:
                backend_failures[code] += 1
            attributable_to_model = code in model_error_codes or any(fragment in detail for fragment in (
                "row budget", "invalid governed field", "does not exist", "stale revision", "does not match current",
            ))
            if attributable_to_model:
                model_mistakes += 1
                model_failure_codes[code] += 1
            else:
                platform_failure_codes[code] += 1
        else:
            stats["successes"] += 1
for stats in by_tool.values():
    stats["errorCodes"] = dict(stats["errorCodes"])
covered = sorted(name for name, stats in by_tool.items() if stats["calls"])
successful = sorted(name for name, stats in by_tool.items() if stats["successes"])
hard_failures = [error for record in records for error in record.get("validationErrors", [])]
summary = {
    "scenarios": len(records),
    "completedScenarios": sum(not record.get("validationErrors") for record in records),
    "scenarioCompletionRate": (sum(not record.get("validationErrors") for record in records) / len(records)) if records else 0,
    "totalCalls": len(all_calls),
    "failedCalls": sum(bool(call.get("error")) for call in all_calls),
    "rawErrorRate": (sum(bool(call.get("error")) for call in all_calls) / len(all_calls)) if all_calls else 0,
    "argumentValidationFailures": argument_failures,
    "modelAttributableMistakes": model_mistakes,
    "modelAttributableErrorRate": (model_mistakes / len(all_calls)) if all_calls else 0,
    "unexpectedSelectionCalls": unexpected,
    "recoveryCalls": recovery_calls,
    "backendExecutionFailuresByErrorCode": dict(backend_failures),
    "modelAttributableErrorsByCode": dict(model_failure_codes),
    "platformFailuresByErrorCode": dict(platform_failure_codes),
    "coveredTools": covered,
    "toolCoverageRate": len(set(covered) & set(required)) / len(required) if required else 0,
    "missingTools": sorted(set(required) - set(covered)),
    "successfulTools": successful,
    "successfulToolCoverageRate": len(set(successful) & set(required)) / len(required) if required else 0,
    "neverSuccessfulTools": sorted(set(required) - set(successful)),
    "hardFailures": hard_failures,
    "byTool": by_tool,
    "scenariosDetail": records,
}
os.makedirs(os.path.dirname(report) or ".", exist_ok=True)
with open(report, "w") as target:
    json.dump(summary, target, indent=2, sort_keys=True)
print("AGENT_TOOL_EVAL=" + json.dumps(summary, separators=(",", ":")))
github_summary = os.environ.get("GITHUB_STEP_SUMMARY")
if github_summary:
    with open(github_summary, "a") as target:
        target.write("## Agent tool evaluation\n\n")
        target.write(f"- Required coverage: {len(set(covered) & set(required))}/{len(required)} tools; successful: {len(set(successful) & set(required))}/{len(required)}\n")
        target.write("- Calls: {}; raw errors: {}; argument failures: {}; unexpected selections: {}\n".format(len(all_calls), summary["failedCalls"], argument_failures, unexpected))
        target.write("- Completed scenarios: {}/{}\n".format(summary["completedScenarios"], len(records)))
if summary["missingTools"] or summary["neverSuccessfulTools"] or argument_failures or hard_failures:
    raise SystemExit(1)
' "$TRANSCRIPT_PATH" "$REPORT_PATH" "$ALLOWED_TOOLS"

echo "agent evaluation report: $REPORT_PATH"
echo "agent evaluation transcript: $TRANSCRIPT_PATH"
