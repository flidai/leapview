package demo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/stretchr/testify/require"
)

func TestDemoUsesCanonicalOlistShowcase(t *testing.T) {
	root := filepath.Join("..", "..")
	sourceRoot := filepath.Join(root, "dashboards")
	_, err := projectcompiler.Compile(sourceRoot)
	require.NoError(t, err)

	paths, err := projectcompiler.SourceFiles(sourceRoot)
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
		"type: managed",
		"name: visual-showcase",
		"name: executive-sales",
		"name: fulfillment-operations",
	} {
		require.Contains(t, project, required)
	}
	require.NotContains(t, project, "kind: quack")
}

func TestDemoBundleDoesNotCarryControlPlaneAccessPolicy(t *testing.T) {
	root := filepath.Join("..", "..")
	compiled, err := projectcompiler.Compile(filepath.Join(root, "dashboards"))
	require.NoError(t, err)
	canonical := string(compiled.Canonical())
	require.NotContains(t, canonical, `"access"`)
	require.NotContains(t, canonical, `"publications"`)
}

func TestDemoDeploymentPublishesCanonicalProject(t *testing.T) {
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
		"Infisical/secrets-action@",
		"scripts/deploy_demo.sh",
		"Publish the selected showcase",
		"vars.DEMO_DATASET",
		"vars.DEMO_PROJECT_ID",
		"vars.DEMO_PUBLISHER_PRINCIPAL_ID",
		"vars.DEMO_RELEASE_PRINCIPAL_ID",
	} {
		require.Contains(t, workflow, required)
	}

	script := read(t, filepath.Join(root, "scripts", "deploy_demo.sh"))
	for _, required := range []string{
		"source_root=\"$repo_root/dashboards\"",
		"--source-root \"$source_root\"",
		"bootstrapolist",
		"cd -P",
		"data sync",
		"plan",
		"build",
		"publish",
		"getDeliveryCandidateStatus",
		"getCapabilities",
		"native_postgres",
		"buildRevision",
		".buildRevision == $source_revision",
		".buildDevelopment == true",
		"requestDeliveryPublicationApproval",
		"approveDeliveryPublicationApproval",
		"getDeliveryPublicationApproval",
		"getDeliveryPublicationEvidence",
		"getDeliveryGenerationStatus",
		"getProject",
		"browser entry did not redirect unauthenticated visitors to /login",
		"$demo_target/login",
		"DEMO_PROJECT_ID",
		"go build -o",
		"grant_type=client_credentials",
		"DEMO_PUBLISHER_CLIENT_ID",
		"DEMO_RELEASE_CLIENT_ID",
		"'RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH'",
		"'PROJECT_ADMIN'",
	} {
		require.Contains(t, script, required)
	}
	for _, forbidden := range []string{
		"demo_image",
		"leapviewctl upgrade",
		"stricthostkeychecking",
		"ssh-keygen",
		"ssh-host-key.sha256",
		"--token dev",
		"demo_publisher_token",
		"demo_release_token",
		"quack",
		"getdeployment",
		"approvedeployment",
		"activatedeployment",
		"project:leapview-showcase",
	} {
		require.NotContains(t, strings.ToLower(script), forbidden)
	}
	configGeneration := strings.Index(script, "go run ./internal/app/tools/configgen")
	datasetBootstrap := strings.Index(script, `go run "$bootstrap_tool"`)
	require.NotEqual(t, -1, configGeneration, "demo deployment must generate ignored config sources")
	require.NotEqual(t, -1, datasetBootstrap, "demo deployment must bootstrap the selected dataset")
	require.Less(t, configGeneration, datasetBootstrap, "config generation must precede dataset compilation")
	if _, err := os.Stat(filepath.Join(root, "deploy", "demo", "ssh-host-key.sha256")); !os.IsNotExist(err) {
		t.Fatalf("stale demo SSH identity remains tracked: %v", err)
	}
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

func TestHostedDemoRequiresPrivateAgentProviderConfiguration(t *testing.T) {
	root := filepath.Join("..", "..")
	runbook := read(t, filepath.Join(root, "deploy", "demo", "README.md"))
	workflow := read(t, filepath.Join(root, ".github", "workflows", "demo-deploy.yml"))
	rollout := read(t, filepath.Join(root, "scripts", "rollout_demo_runtime.sh"))
	runtime := read(t, filepath.Join(root, "scripts", "rollout_demo_runtime.py"))
	for _, required := range []string{
		"LEAPVIEW_AGENT_API_KEY",
		"LEAPVIEW_AGENT_BASE_URL",
		"LEAPVIEW_AGENT_MODEL",
		"runtime.env",
		"scripts/rollout_demo_runtime.sh",
		"never committed",
	} {
		require.Contains(t, runbook, required)
	}
	require.Contains(t, workflow, "secret-path: /demo/deployment")
	require.NotContains(t, workflow, "DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}")
	for _, required := range []string{
		"DEEPSEEK_API_KEY",
		"leapview-demo-agent-api-key",
		"unset agent_api_key DEEPSEEK_API_KEY",
	} {
		require.Contains(t, rollout, required)
	}
	cleanupArmed := strings.Index(rollout, "agent_key_uploaded=true")
	uploadAttempt := strings.Index(rollout, `printf '%s' "$agent_api_key"`)
	require.NotEqual(t, -1, cleanupArmed)
	require.NotEqual(t, -1, uploadAttempt)
	require.Less(t, cleanupArmed, uploadAttempt, "temporary-key cleanup must be armed before upload")
	for _, required := range []string{
		"LEAPVIEW_AGENT_API_KEY",
		"LEAPVIEW_AGENT_BASE_URL",
		"https://api.deepseek.com",
		"LEAPVIEW_AGENT_MODEL",
		"deepseek-v4-flash",
	} {
		require.Contains(t, runtime, required)
	}
	require.NotContains(t, runbook, "Keep the agent unconfigured on the shared demo instance.")
}

func TestHostedDemoComposeRolloutIsPinnedAndRollbackSafe(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := read(t, filepath.Join(root, ".github", "workflows", "demo-deploy.yml"))
	shell := read(t, filepath.Join(root, "scripts", "deploy_compose_demo_runtime.sh"))
	runtime := read(t, filepath.Join(root, "scripts", "deploy_compose_demo_runtime.py"))
	runbook := read(t, filepath.Join(root, "deploy", "demo", "README.md"))

	for _, required := range []string{
		"options: [publish, compose-deploy]",
		"inputs.action == 'compose-deploy'",
		"35894842492",
		"2caaf4d0c3e0ce637a22c376f63240c27dfaf20d",
		"ghcr.io/flidai/leapview@sha256:35d1207a312279cc7bcf3c64a9284a8410d1f21c30920f2cbf1935113553eb24",
		"SHA256:k3AZrVrLBF5tyItYzRUkcsVJEFVVOqxsHhBvQypTVWE",
		"scripts/deploy_compose_demo_runtime.sh",
	} {
		require.Contains(t, workflow, required)
	}
	require.NotContains(t, workflow, "DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}")

	for _, required := range []string{
		"StrictHostKeyChecking=yes",
		"DEMO_EXPECTED_SSH_FINGERPRINT",
		"set_firewall_rules",
		"demo server did not become reachable",
		"scripts/deploy_compose_demo_runtime.py",
	} {
		require.Contains(t, shell, required)
	}
	require.NotContains(t, shell, "DEEPSEEK_API_KEY")
	require.NotContains(t, shell, "leapview-demo-agent-api-key")

	for _, required := range []string{
		"PREDECESSOR_REVISION = '5a50b4c4d065278172c9779b98abb094215014c2'",
		"PREDECESSOR_IMAGE = 'ghcr.io/flidai/leapview@sha256:a24ef9fc224f4366b232938158f13c2665a069fc8f467f8856c6e183d76bf7eb'",
		"ROOT = Path('/opt/leapview')",
		"LEAPVIEW_AGENT_API_KEY",
		"LEAPVIEW_AGENT_BASE_URL",
		"LEAPVIEW_AGENT_MODEL",
		"org.opencontainers.image.revision",
		"rollout-success.json",
		"app_contents = APP_ENV.read_text()",
		"Compose rollout failed; reviewed predecessor configuration was restored",
	} {
		require.Contains(t, runtime, required)
	}
	require.NotContains(t, runtime, "write_private_atomic(APP_ENV")
	rollbackArmed := strings.Index(runtime, "changed = True")
	firstMutation := strings.Index(runtime, "write_private_atomic(DEPLOYMENT_ENV")
	startAttempt := strings.Index(runtime, "\n        start()")
	require.NotEqual(t, -1, rollbackArmed)
	require.NotEqual(t, -1, firstMutation)
	require.NotEqual(t, -1, startAttempt)
	require.Less(t, rollbackArmed, firstMutation, "rollback must be armed before the first durable mutation")
	require.Less(t, firstMutation, startAttempt, "configuration must be durable before the restart")

	for _, required := range []string{
		"exact reviewed predecessor",
		"complete predecessor state",
		"private application environment untouched",
		"revision-pinned `compose-deploy` action",
	} {
		require.Contains(t, runbook, required)
	}
}

func TestDemoDeploymentRequiresSourceRevisionBeforeChangingInfrastructure(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("bash", filepath.Join(root, "scripts", "deploy_demo.sh"))
	command.Env = append(os.Environ(),
		"DEMO_PUBLISHER_CLIENT_ID=publisher-client",
		"DEMO_PUBLISHER_CLIENT_SECRET=publisher-secret",
		"DEMO_RELEASE_CLIENT_ID=release-client",
		"DEMO_RELEASE_CLIENT_SECRET=release-secret",
	)
	output, err := command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "DEMO_SOURCE_REVISION")
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(body)
}

func TestDemoCFOProjectCompilesWithoutOlistResources(t *testing.T) {
	compiled, err := projectcompiler.Compile(filepath.Join("..", "..", "dashboards", "experiments", "cfo-demo"))
	require.NoError(t, err)
	canonical := string(compiled.Canonical())
	require.Contains(t, canonical, "dashboard:cfo-command-center")
	require.Contains(t, canonical, "semantic-model:finance")
	require.NotContains(t, canonical, "dashboard:executive-sales")
	require.NotContains(t, canonical, `"access"`)
}
