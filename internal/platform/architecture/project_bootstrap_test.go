package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentPublishingBootstrapsIssuerIdentityBeforeProjectWork(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	start := strings.Index(script, "publish_project() {")
	if start < 0 {
		t.Fatal("development publication entrypoint is missing")
	}
	publish := script[start:]
	bootstrap := strings.Index(publish, `bootstrap_output="$(go run ./cmd/leapview "${bootstrap_args[@]}")" || return 1`)
	handoff := strings.Index(publish, `persist_development_claim_publisher "$bootstrap_output" || return 1`)
	identity := strings.Index(publish, `project_id="$(jq -er '.claimedProjectUid | strings | select(length > 0)' "$CREDENTIAL_FILE")" || return 1`)
	acknowledge := strings.Index(publish, `acknowledge_development_claim_publisher "http://localhost:${port}" || return 1`)
	publisher := strings.Index(publish, `token="$(jq -er '.publisherToken | strings | select(startswith("lv_pat_"))' "$CREDENTIAL_FILE")" || return 1`)
	for _, command := range []string{"go run ./cmd/leapview data sync", `go run ./cmd/leapview "${dev_args[@]}"`} {
		operation := strings.Index(publish, command)
		if bootstrap < 0 || handoff <= bootstrap || identity <= handoff || acknowledge <= identity || publisher <= acknowledge || operation <= publisher {
			t.Fatalf("%s must follow successful bootstrap, publisher handoff, claim acknowledgement, and issuer identity resolution", command)
		}
	}
	if devArgs := strings.Index(publish, "local dev_args=(dev --once"); devArgs <= identity {
		t.Fatal("one-shot development command must be constructed after issuer identity resolution")
	}
	if strings.Contains(publish, "project:leapview-showcase") {
		t.Fatal("development setup must not substitute a static Project UID for durable issuer state")
	}
	if !strings.Contains(publish, `bootstrap_args+=(--project-uid "$project_id")`) {
		t.Fatal("explicit externally issued development identity must reach bootstrap")
	}
}

func TestDevelopmentServerTracksCompiledFallbackProcess(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		`go build -tags=duckdb_arrow -o "$TMP_DIR/leapview-dev" ./cmd/leapview`,
		`"$TMP_DIR/leapview-dev" >> "$LOG_FILE" 2>&1 &`,
		`LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES="${LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES:-67108864}"`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server script missing tracked binary fragment %q", want)
		}
	}
	if strings.Contains(serverText, `go run ./cmd/leapview >> "$LOG_FILE" 2>&1 &`) {
		t.Fatal("development server must not track the go run wrapper as the server process")
	}

	qa, err := os.ReadFile(filepath.Join(root, "scripts", "qa_ui_framework.ts"))
	if err != nil {
		t.Fatalf("read UI framework QA script: %v", err)
	}
	qaText := string(qa)
	if !strings.Contains(qaText, "const managedServerReadyAttempts = 1800") ||
		!strings.Contains(qaText, "attempt < managedServerReadyAttempts") {
		t.Fatal("UI framework QA must allow a cold Go build before checking server readiness")
	}
	for _, want := range []string{
		"LEAPVIEW_MANAGED_DATA_DIR: `${qaHome}/managed-data`",
		"['chmod', '-R', 'u+w', qaHome]",
	} {
		if !strings.Contains(qaText, want) {
			t.Fatalf("UI framework QA must isolate and clean managed-data state: missing %q", want)
		}
	}
}

func TestDevelopmentServerDefaultsAgentToAmbientDeepSeekCredential(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		`if [[ -z "${LEAPVIEW_AGENT_API_KEY:-}" && -n "${DEEPSEEK_API_KEY:-}" ]]; then`,
		`LEAPVIEW_AGENT_API_KEY="$DEEPSEEK_API_KEY"`,
		`LEAPVIEW_AGENT_BASE_URL="${LEAPVIEW_AGENT_BASE_URL:-https://api.deepseek.com}"`,
		`LEAPVIEW_AGENT_MODEL="${LEAPVIEW_AGENT_MODEL:-deepseek-v4-flash}"`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server must configure the default DeepSeek agent without overriding explicit agent credentials: missing %q", want)
		}
	}
}

func TestDevelopmentServerRunsAgentMCPSmokeCheckAfterPublishing(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		"mcp_smoke()",
		`"method":"tools/list"`,
		`"name":"catalog_list"`,
		`name:"query_semantic_model"`,
		`mcp_smoke "$port"`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server must smoke-test the live MCP tool surface after publishing: missing %q", want)
		}
	}
	if strings.Index(serverText, `go run ./cmd/leapview publish`) > strings.Index(serverText, `mcp_smoke "$port"`) {
		t.Fatal("development MCP smoke check must run after candidate publication")
	}
}

func TestDevelopmentPublishingCanonicalizesSharedDatasetRoots(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		"canonical_source_root()",
		`local token="${LEAPVIEW_DEV_API_TOKEN:-dev}"`,
		`from="$(canonical_source_root "$from")"`,
		`candidate_id="$(awk '$1 == "candidate" { print $2; exit }' <<<"$dev_output")"`,
		`go run ./cmd/leapview publish "$candidate_id" --token "$token"`,
		`publish) publish_running "$@" ;;`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server must safely resolve shared dataset roots: missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"go run ./cmd/leapview publish --project",
		"go run ./cmd/leapview publish --target",
	} {
		if strings.Contains(serverText, forbidden) {
			t.Fatalf("development server still uses retired publish selector %q", forbidden)
		}
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	if !strings.Contains(string(taskfile), "./scripts/dev-server.sh publish") {
		t.Fatal("dev:publish must delegate to the canonical development server publication path")
	}
}
