package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPublicDocsAndScriptsDoNotAdvertiseRemovedCaCSurfaces(t *testing.T) {
	root := projectRoot(t)
	files := []string{
		"README.md",
		"ui-spec.md",
		filepath.Join("scripts", "agent_e2e.sh"),
	}
	for _, name := range files {
		name := name
		t.Run(name, func(t *testing.T) {
			body := readRepoFile(t, root, name)
			for _, forbidden := range []string{
				"dashboards/catalog.yaml",
				"GET /dashboards",
				"`/dashboards/",
				"('/dashboards/",
				"\"/dashboards/",
				"/workspaces/{workspace}/updates",
				"/chat/updates",
				"/data/updates",
				"/admin/storage/updates",
				"/admin/queries/updates",
			} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s still advertises removed surface %q", name, forbidden)
				}
			}
			if regexp.MustCompile(`(?m)^\s*/chat(?:\s|$)`).MatchString(body) {
				t.Fatalf("%s still advertises unscoped /chat route", name)
			}
		})
	}

	script := readRepoFile(t, root, filepath.Join("scripts", "agent_e2e.sh"))
	for _, want := range []string{
		"--project dashboards/leapview.yaml",
		`"$BIN" dev --once`,
		`"$BIN" publish`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("scripts/agent_e2e.sh missing current deploy/agent argument %q", want)
		}
	}
	if regexp.MustCompile(`agent ask[^\n]*--workspace`).MatchString(script) {
		t.Fatal("scripts/agent_e2e.sh still passes the removed agent --workspace flag")
	}
}

func TestPublicDocsAdvertiseExactCandidatePublishing(t *testing.T) {
	root := projectRoot(t)
	authoringDocs := []string{
		filepath.Join("docs", "guides", "cli", "validate-deploy.md"),
		filepath.Join("docs", "guides", "cli", "automation.md"),
	}
	for _, name := range authoringDocs {
		body := readRepoFile(t, root, name)
		for _, want := range []string{
			"leapview dev",
			"leapview publish",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing exact candidate workflow %q", name, want)
			}
		}
		for _, forbidden := range []string{"leapview deploy", "--auto-approve"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s advertises retired publish workflow %q", name, forbidden)
			}
		}
	}

	revisionsGuide := readRepoFile(t, root, filepath.Join("docs", "articles", "data", "revisions.md"))
	for _, want := range []string{
		"leapview data plan",
		"leapview data sync",
		"leapview dev --once",
		"leapview publish",
	} {
		if !strings.Contains(revisionsGuide, want) {
			t.Fatalf("docs/articles/data/revisions.md missing current command surface %q", want)
		}
	}
}

func TestDeveloperWorkflowsUseExactCandidatePublishing(t *testing.T) {
	root := projectRoot(t)
	files := map[string][]string{
		"Taskfile.yml": {
			"dev:publish:",
			"./scripts/dev-server.sh publish",
		},
		filepath.Join("scripts", "dev-server.sh"): {
			"go run ./cmd/leapview dev --once",
			"go run ./cmd/leapview publish",
		},
		filepath.Join("scripts", "agent_e2e.sh"): {
			`"$BIN" dev --once`,
			`"$BIN" publish`,
		},
		filepath.Join("internal", "app", "cli", "composectl", "qualification_client.go"): {
			`"dev",`,
			`"publish",`,
		},
		filepath.Join("internal", "app", "cli", "composectl", "qualification_recovery.go"): {
			`"leapview", "dev", "--once", "--no-browser"`,
			`"leapview", "publish"`,
		},
		filepath.Join("deploy", "hetzner", "README.md"): {
			"leapview dev --once",
			"leapview publish",
		},
	}
	for name, required := range files {
		body := readRepoFile(t, root, name)
		for _, forbidden := range []string{
			"data deploy",
			"leapview deploy",
			`"$BIN" deploy`,
			"--auto-approve",
			"deploy:dev:",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s still invokes removed command surface %q", name, forbidden)
			}
		}
		for _, want := range required {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing project deploy contract %q", name, want)
			}
		}
	}
}

func TestEnvExampleDoesNotEnablePlaceholderIdentityProviders(t *testing.T) {
	envExample := readRepoFile(t, projectRoot(t), ".env.example")
	for _, name := range []string{
		"LEAPVIEW_AZURE_CLIENT_ID",
		"LEAPVIEW_AZURE_CLIENT_SECRET",
		"LEAPVIEW_AZURE_CALLBACK_URL",
		"LEAPVIEW_AZURE_TENANT",
		"LEAPVIEW_OIDC_PROVIDER_ID",
		"LEAPVIEW_OIDC_ISSUER_URL",
		"LEAPVIEW_OIDC_CLIENT_ID",
		"LEAPVIEW_OIDC_CLIENT_SECRET",
		"LEAPVIEW_OIDC_CALLBACK_URL",
		"LEAPVIEW_OIDC_SCOPES",
	} {
		if regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=`).MatchString(envExample) {
			t.Fatalf(".env.example enables optional provider variable %s by default", name)
		}
	}
}

func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}
