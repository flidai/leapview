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
	projectPath := filepath.Join(root, "dashboards", "leapview.yaml")
	compiled, err := projectcompiler.CompileProject(projectPath)
	require.NoError(t, err)
	require.Equal(t, "project:leapview-showcase", compiled.ProjectID().String())

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
		"type: managed",
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
	compiled, err := projectcompiler.CompileProject(filepath.Join(root, "dashboards", "leapview.yaml"))
	require.NoError(t, err)

	var capabilities []string
	var objects []string
	const demoPrincipalID = "email_2a7d2952c0d423cf3ea7b39428fb9420"
	for _, grant := range compiled.Manifest().Access.Grants {
		if grant.Subject.PrincipalID != demoPrincipalID {
			continue
		}
		require.Equal(t, "principal", grant.Subject.Kind)
		require.Contains(t, []string{"semantic_model", "dashboard"}, grant.Object.Kind)
		require.NotEmpty(t, grant.Object.ID)
		capabilities = append(capabilities, grant.Capability)
		objects = append(objects, grant.Object.ID)
	}
	require.ElementsMatch(t, []string{
		"RESOURCE_USE", "RESOURCE_USE",
		"RESOURCE_READ", "RESOURCE_READ", "RESOURCE_READ", "RESOURCE_READ", "RESOURCE_READ",
	}, capabilities)
	require.ElementsMatch(t, []string{
		"semantic-model:sales", "semantic-model:sales",
		"semantic-model:operations", "semantic-model:operations",
		"dashboard:executive-sales", "dashboard:fulfillment-operations", "dashboard:visual-showcase",
	}, objects)
	for _, binding := range compiled.Manifest().Access.RoleBindings {
		require.NotEqual(t, demoPrincipalID, binding.Subject.PrincipalID,
			"shared demo login must not inherit a role that enables chat or mutations")
	}
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
		"Publish the canonical Olist showcase",
	} {
		require.Contains(t, workflow, required)
	}

	script := read(t, filepath.Join(root, "scripts", "deploy_demo.sh"))
	for _, required := range []string{
		"dashboards/leapview.yaml",
		"bootstrapolist",
		"cd -P",
		"data sync",
		"plan",
		"build",
		"publish",
		"getDeliveryCandidateStatus",
		"requestDeliveryPublicationApproval",
		"approveDeliveryPublicationApproval",
		"getDeliveryPublicationApproval",
		"getDeliveryPublicationEvidence",
		"getDeliveryGenerationStatus",
		"getProject",
		"project:leapview-showcase",
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
		"--token dev",
		"demo_publisher_token",
		"demo_release_token",
		"quack",
		"getdeployment",
		"approvedeployment",
		"activatedeployment",
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
