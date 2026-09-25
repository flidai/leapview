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

func TestHostedDemoPreservesPrivateRuntimeConfiguration(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := read(t, filepath.Join(root, ".github", "workflows", "demo-deploy.yml"))
	runtime := read(t, filepath.Join(root, "scripts", "demo_compose_runtime.py"))
	require.Contains(t, workflow, "secret-path: /demo/deployment")
	require.NotContains(t, workflow, "secret-path: /demo/access")
	require.NotContains(t, workflow, "/hetzner-qualification/infrastructure")
	require.NotContains(t, workflow, "scripts/stage_demo_runtime.sh")
	require.Contains(t, runtime, "Runtime configuration changed")
	require.NotContains(t, runtime, "DEEPSEEK_API_KEY")
}

func TestDemoDeploymentBehavior(t *testing.T) {
	command := exec.Command("python3", "-m", "unittest", "discover", "-s", "scripts/tests", "-p", "test_demo*.py")
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
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
