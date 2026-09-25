package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This exercises the real revision-019 image, TLS PostgreSQL/DuckLake and the
// canonical pool bootstrap. It is opt-in because it pulls immutable images and
// starts disposable Docker resources, which the ordinary PR unit shard cannot
// assume are available.
func TestRevision019PredecessorBootstrapWithRealProviders(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_REV019_PROVIDERS") != "1" {
		t.Skip("set LEAPVIEW_TEST_REV019_PROVIDERS=1 to exercise the real predecessor and providers")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	paths := testPaths(t)
	image := revision019PredecessorImage
	container := strings.ReplaceAll(t.Name(), "/", "-") + fmt.Sprintf("-%d", os.Getpid())
	project := fmt.Sprintf("fai518predecessor%d", time.Now().UnixNano()%1_000_000_000)
	runDocker := func(args ...string) error {
		command := exec.CommandContext(ctx, "docker", args...)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker %s: %w: %s", args[0], err, output)
		}
		return nil
	}
	require.NoError(t, runDocker("create", "--name", container, image))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", container).Run()
		_ = exec.Command("docker", "rm", "--force", project+"-postgres").Run()
		_ = exec.Command("docker", "compose", "--project-name", project,
			"--project-directory", paths.Root, "--env-file", filepath.Join(paths.Root, "deployment.env"),
			"--file", filepath.Join(paths.Root, "compose.yaml"), "down", "--volumes", "--remove-orphans").Run()
		_ = exec.Command("docker", "volume", "rm", project+"_predecessor-postgres-data").Run()
	})
	require.NoError(t, os.MkdirAll(paths.Payload, 0o700))
	require.NoError(t, runDocker("cp", container+":/usr/local/share/leapview/deployment/.", paths.Payload))
	require.NoError(t, runDocker("rm", "--force", container))
	config := Config{SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: image, HTTPS: boolPointer(false)}
	writeConfig(t, paths.Config, config)
	require.NoError(t, os.MkdirAll(paths.Root, 0o700))
	binding, err := json.Marshal(revision019Binding{SchemaVersion: 1, Image: image,
		TargetID: "fai518-disposable-target", LegacyConfig: config})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, revision019BindingName), binding, 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	deploymentExample, err := os.ReadFile(filepath.Join(paths.Payload, "deployment.env.example"))
	require.NoError(t, err)
	deployment := strings.Replace(string(deploymentExample), "COMPOSE_PROJECT_NAME=leapview", "COMPOSE_PROJECT_NAME="+project, 1)
	deployment = strings.Replace(deployment, "COMPOSE_APP_BIND=127.0.0.1:8080", fmt.Sprintf("COMPOSE_APP_BIND=127.0.0.1:%d", port), 1)
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, "deployment.env"), []byte(deployment), 0o600))
	initScript, err := filepath.Abs("../../../../deploy/postgres/init.sh")
	require.NoError(t, err)
	installer, err := New(Options{Paths: paths, ExpectedImage: image, Revision019InitScript: initScript})
	require.NoError(t, err)
	require.NoError(t, installer.Install(ctx))
	installed, _, err := readUpgradeInstallation(paths.Root)
	require.NoError(t, err)
	require.Equal(t, "fai518-disposable-target", installed.TargetID)
	query := exec.CommandContext(ctx, "docker", "exec", project+"-postgres", "sh", "-ec",
		`export PGPASSWORD="$POSTGRES_PASSWORD"; psql --username "$POSTGRES_USER" --dbname leapview_control --tuples-only --no-align --command "SELECT version_id FROM public.goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1"`)
	output, err := query.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "19", strings.TrimSpace(string(output)))
	admissionQuery := exec.CommandContext(ctx, "docker", "exec", project+"-postgres", "sh", "-ec",
		`export PGPASSWORD="$POSTGRES_PASSWORD"; psql --username "$POSTGRES_USER" --dbname leapview_control --tuples-only --no-align --command "SELECT count(*) FROM physical_pool.physical_pool_admissions"`)
	output, err = admissionQuery.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "1", strings.TrimSpace(string(output)))
	duckLakeQuery := exec.CommandContext(ctx, "docker", "exec", project+"-postgres", "sh", "-ec",
		`export PGPASSWORD="$POSTGRES_PASSWORD"; psql --username "$POSTGRES_USER" --dbname leapview_ducklake --tuples-only --no-align --command "SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'ducklake'"`)
	output, err = duckLakeQuery.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, "1", strings.TrimSpace(string(output)))
	imageInspect := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Config.Image}}", project+"-leapview-1")
	output, err = imageInspect.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, image, strings.TrimSpace(string(output)))
}
