package demo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoWorkflowGeneratesSourcesBeforePublication(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "demo-deploy.yml"))
	require.NoError(t, err)
	workflow := string(body)
	nodeSetup := strings.Index(workflow, "uses: actions/setup-node@")
	generation := strings.Index(workflow, "run: ./scripts/generate_build_sources.sh")
	credentials := strings.Index(workflow, "name: Fetch demo deployment credentials")
	publication := strings.Index(workflow, "run: ./scripts/deploy_demo.sh")
	require.NotEqual(t, -1, nodeSetup, "TypeSpec generation requires Node.js")
	require.Greater(t, generation, nodeSetup, "generate clean-checkout sources after installing Node.js")
	require.Greater(t, credentials, generation, "generate sources before fetching deployment credentials")
	require.Greater(t, publication, credentials, "publish only after generation and credential setup")
}
