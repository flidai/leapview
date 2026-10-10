package composectl

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBundledPostgresDockerStopsGracefully(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER") != "1" {
		t.Skip("set LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1 to exercise actual PostgreSQL shutdown")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../.."))
	root, _, controller := newBundledPostgresDockerProject(t, repository, fmt.Sprintf("lvpgstop-%x", time.Now().UnixNano()))
	require.NoError(t, os.WriteFile(filepath.Join(root, appEnvName), []byte("LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n"), 0o600))
	for attempt := 0; attempt < 2; attempt++ {
		_, err := controller.ensureBundledPostgres(ctx)
		require.NoError(t, err)
		var identity strings.Builder
		require.NoError(t, controller.compose(ctx, nil, &identity, io.Discard, "ps", "--quiet", "postgres"))
		id := strings.TrimSpace(identity.String())
		require.Regexp(t, `^[0-9a-f]{64}$`, id)
		require.NoError(t, controller.compose(ctx, nil, io.Discard, io.Discard, "stop", "postgres"))
		// Compose stop can succeed even when its init process cannot signal the
		// PostgreSQL child after it drops root. Require a clean server exit and
		// durable shutdown checkpoint, on both fresh and resumed containers.
		exit, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.ExitCode}}", id).Output()
		require.NoError(t, err)
		require.Equal(t, "0", strings.TrimSpace(string(exit)), "the init process must forward PostgreSQL's stop signal")
		var control strings.Builder
		require.NoError(t, controller.compose(ctx, nil, &control, io.Discard,
			"run", "--rm", "--no-deps", "--user", "postgres", "--entrypoint", "pg_controldata", "postgres", "/var/lib/postgresql/18/docker"))
		require.Regexp(t, `(?m)^Database cluster state:\s+shut down\s*$`, control.String(), "shutdown must complete before volume reuse")
	}
}
