package demo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestDemoUsesCanonicalOlistShowcase(t *testing.T) {
	root := filepath.Join("..", "..")
	projectPath := filepath.Join(root, "dashboards", "leapview.yaml")
	compiled, err := projectcompiler.CompileProject(
		projectPath,
		projectcompiler.Options{ServingStateID: workspace.ServingStateID("hosted-demo-test")},
	)
	require.NoError(t, err)
	require.Equal(t, "leapview-showcase", compiled.ID())

	paths, err := projectcompiler.SourceFiles(projectPath)
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	var source strings.Builder
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		source.Write(body)
		source.WriteByte('\n')
	}
	project := strings.ToLower(source.String())
	for _, required := range []string{
		"name: olist",
		"kind: managed",
		"name: visual-showcase",
		"name: executive-sales",
		"name: fulfillment-operations",
	} {
		require.Contains(t, project, required)
	}
	require.NotContains(t, project, "kind: quack")
}

func TestDemoSharedLoginIsDashboardOnly(t *testing.T) {
	root := filepath.Join("..", "..")
	compiled, err := projectcompiler.CompileProject(
		filepath.Join(root, "dashboards", "leapview.yaml"),
		projectcompiler.Options{ServingStateID: workspace.ServingStateID("hosted-demo-access-test")},
	)
	require.NoError(t, err)

	for _, workspaceID := range []string{"operations", "sales", "visuals"} {
		compiledWorkspace, ok := compiled.Workspace(workspaceID)
		require.True(t, ok, "compiled demo workspace %q", workspaceID)

		var privileges []string
		for _, grant := range compiledWorkspace.Manifest().Access.Grants {
			if grant.Subject.Email != "demo@leapview.dev" {
				continue
			}
			require.Equal(t, "principal", grant.Subject.Kind)
			require.Equal(t, "workspace", grant.Object.Type)
			require.Empty(t, grant.Object.ID)
			privileges = append(privileges, grant.Privilege)
		}
		require.ElementsMatch(t, []string{"USE_WORKSPACE", "VIEW_ITEM", "QUERY_DATA"}, privileges)

		for _, binding := range compiledWorkspace.Manifest().Access.RoleBindings {
			require.NotEqual(t, "demo@leapview.dev", binding.Subject.Email,
				"shared demo login must not inherit a role that enables chat or mutations")
		}
	}
}

func TestDemoDeploymentIsAutomaticAndDigestPinned(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := read(t, filepath.Join(root, ".github", "workflows", "demo-deploy.yml"))
	for _, required := range []string{
		"workflow_run:",
		"Main artifacts",
		"types: [completed]",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.head_branch == 'main'",
		"environment: leapview-demo",
		"id-token: write",
		"packages: read",
		"Infisical/secrets-action@",
		"scripts/deploy_demo.sh",
		"ghcr.io/flidai/leapview@sha256:",
	} {
		require.Contains(t, workflow, required)
	}

	script := read(t, filepath.Join(root, "scripts", "deploy_demo.sh"))
	for _, required := range []string{
		"dashboards/leapview.yaml",
		"bootstrapolist",
		"cd -P",
		"data sync",
		"dev --once",
		"publish",
		"approveDeployment",
		"activateDeployment",
		"getDeployment",
		"listProjectWorkspaces",
		"workspaceId == \"visuals\"",
		"leapviewctl upgrade",
		"StrictHostKeyChecking=yes",
		"ssh-keygen -lf",
		"grant_type=client_credentials",
		"DEMO_PUBLISHER_CLIENT_ID",
		"DEMO_RELEASE_CLIENT_ID",
		"'AUTHOR_PROJECT PUBLISH_RELEASE INGEST_DATA'",
		"'VIEW_ITEM APPROVE_DEPLOYMENT ACTIVATE_DEPLOYMENT MANAGE_PUBLICATIONS'",
	} {
		require.Contains(t, script, required)
	}
	for _, forbidden := range []string{
		"--token dev",
		"DEMO_PUBLISHER_TOKEN",
		"DEMO_RELEASE_TOKEN",
		"quack",
	} {
		require.NotContains(t, strings.ToLower(script), forbidden)
	}
	configGeneration := strings.Index(script, "go run ./internal/app/tools/configgen")
	olistBootstrap := strings.Index(script, "go run ./internal/app/tools/bootstrapolist")
	require.NotEqual(t, -1, configGeneration, "demo deployment must generate ignored config sources")
	require.NotEqual(t, -1, olistBootstrap, "demo deployment must bootstrap Olist")
	require.Less(t, configGeneration, olistBootstrap, "config generation must precede Olist compilation")
}

func TestDemoHumanCredentialsStayOutOfDeploymentAutomation(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := read(t, filepath.Join(root, ".github", "workflows", "demo-deploy.yml"))
	require.NotContains(t, workflow, "/demo/access")
	require.NotContains(t, workflow, "DEMO_ADMIN_PASSWORD")
	require.NotContains(t, workflow, "DEMO_VIEWER_PASSWORD")

	runbook := read(t, filepath.Join(root, "deploy", "demo", "README.md"))
	for _, required := range []string{
		"prod:/demo/access",
		"DEMO_ADMIN_EMAIL",
		"DEMO_ADMIN_PASSWORD",
		"DEMO_VIEWER_EMAIL",
		"DEMO_VIEWER_PASSWORD",
		"revoke every existing session",
	} {
		require.Contains(t, runbook, required)
	}
}

func TestDemoDeploymentRejectsMutableImagesBeforeChangingInfrastructure(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("bash", filepath.Join(root, "scripts", "deploy_demo.sh"))
	command.Env = append(os.Environ(),
		"DEMO_IMAGE=ghcr.io/flidai/leapview:latest",
		"DEMO_HOST=192.0.2.1",
		"DEMO_SOURCE_REVISION=0123456789012345678901234567890123456789",
		"DEMO_PUBLISHER_CLIENT_ID=publisher-client",
		"DEMO_PUBLISHER_CLIENT_SECRET=publisher-secret",
		"DEMO_RELEASE_CLIENT_ID=release-client",
		"DEMO_RELEASE_CLIENT_SECRET=release-secret",
		"DEMO_FIREWALL_ID=123",
		"HCLOUD_TOKEN=hcloud-test-token",
		"DEMO_RUNNER_IP=192.0.2.2",
	)
	output, err := command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "immutable sha256 digest")
}

func TestHetznerFirewallWaitsForEveryReturnedAction(t *testing.T) {
	root := filepath.Join("..", "..")
	helper := filepath.Join(root, "scripts", "lib", "hcloud_actions.sh")
	command := exec.Command("bash", "-c", `
set -euo pipefail
source "$1"
wait_hcloud_action() { printf '%s\n' "$1"; }
wait_hcloud_actions '{"actions":[{"id":101,"status":"success"},{"id":102,"status":"running"}]}'
`, "test-hcloud-actions", helper)

	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "101\n102\n", string(output))
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(body)
}
