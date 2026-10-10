package composectl

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Pin the linux/amd64 manifest, matching the historical candidate platform.
// The official CLI image supplies a static client; a host-side Nix executable
// may require its wrapper, loader and libraries from /nix/store.
const qualificationHistoricalDockerClientImage = "public.ecr.aws/docker/library/docker:29.8.1-cli@sha256:6602978e2be3c20e530e33773b8cadcef5fe998a71a534ee24516f1176973cdf"

func qualificationHistoricalDockerClient(t *testing.T, ctx context.Context) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, "docker", "create", "--platform", "linux/amd64",
		"--entrypoint", "/bin/true", qualificationHistoricalDockerClientImage).Output()
	if err != nil {
		var diagnostic []byte
		if exit, ok := err.(*exec.ExitError); ok {
			diagnostic = exit.Stderr
		}
		t.Fatalf("create pinned portable Docker client fixture: %v (%s)", err, diagnostic)
	}
	id := strings.TrimSpace(string(output))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		output, err := exec.CommandContext(cleanupCtx, "docker", "rm", "--force", id).CombinedOutput()
		require.NoError(t, err, "remove portable Docker client fixture: %s", output)
	})
	destination := filepath.Join(t.TempDir(), "docker")
	output, err = exec.CommandContext(ctx, "docker", "cp", id+":/usr/local/bin/docker", destination).CombinedOutput()
	require.NoError(t, err, "extract the pinned portable Docker client: %s", output)
	require.NoError(t, os.Chmod(destination, 0o755))
	client, err := elf.Open(destination)
	require.NoError(t, err, "the portable Docker client must be a Linux ELF executable")
	defer client.Close()
	for _, program := range client.Progs {
		require.NotEqual(t, elf.PT_INTERP, program.Type, "the portable Docker client must not require a host ELF loader")
	}
	return destination
}

func TestQualificationHistoricalDockerClientPortable(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_CONTAINERS") != "1" {
		t.Skip("set LEAPVIEW_TEST_CONTAINERS=1 to verify the historical fixture's portable Docker client")
	}
	// Use the actual utility setup in a conventional image without /nix/store.
	// This catches both a missing ELF loader and a wrapper that refers back to
	// the host's toolchain, even when host-side Docker commands work normally.
	utility := startQualificationHistoricalTransitionUtility(t, t.Context(), newTestcontainersQualificationRuntime(),
		"", t.TempDir(), qualificationPostgreSQL18Image, "", true, nil,
		"leapview-historical-docker-"+uuid.NewString())
	output, err := utility.Exec(t.Context(), nil, "docker", "context", "show")
	require.NoError(t, err, "the mounted Docker client must run without the host toolchain's loader or libraries: %s", output)
	require.Equal(t, "default", strings.TrimSpace(string(output)))
	output, err = utility.Exec(t.Context(), nil, "docker", "version", "--format", "{{.Server.Version}}")
	require.NoError(t, err, "the portable client must reach the local Docker daemon: %s", output)
	require.NotEmpty(t, strings.TrimSpace(string(output)))
}
