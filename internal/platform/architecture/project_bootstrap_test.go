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
	identity := strings.Index(publish, `project_id="$(jq -er '.projectUid | strings | select(length > 0)' <<<"$bootstrap_output")" || return 1`)
	for _, command := range []string{"go run ./cmd/leapview data sync", "go run ./cmd/leapview dev --once"} {
		operation := strings.Index(publish, command)
		if bootstrap < 0 || identity <= bootstrap || operation <= identity {
			t.Fatalf("%s must follow successful bootstrap and issuer identity resolution", command)
		}
	}
	if strings.Contains(publish, "project:leapview-showcase") {
		t.Fatal("development setup must not substitute a static Project UID for durable issuer state")
	}
	if !strings.Contains(publish, `bootstrap_args+=(--project-uid "$project_id")`) {
		t.Fatal("explicit externally issued development identity must reach bootstrap")
	}
}
