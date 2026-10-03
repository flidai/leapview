package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationImagePreservesImmutableReference(t *testing.T) {
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	stopAtCompose := errors.New("stop after checking qualification Compose input")
	var composeCreateReached bool
	var composeImage string
	var composeEnvErr error
	var forbiddenDockerCommand string

	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		arguments := request.Arguments
		if len(arguments) == 0 {
			return nil, errors.New("unexpected empty command")
		}
		joined := strings.Join(arguments, " ")
		switch arguments[0] {
		case "pull":
			if slices.Equal(arguments, []string{"pull", image}) {
				return nil, nil
			}
		case "run":
			if slices.Contains(arguments, qualificationRegistryImage) {
				forbiddenDockerCommand = joined
				return nil, errors.New("unexpected qualification registry startup")
			}
			if slices.Contains(arguments, "--entrypoint") && slices.Contains(arguments, image) {
				return []byte("1000\n"), nil
			}
			if slices.Contains(arguments, "--detach") && slices.Contains(arguments, image) {
				return nil, nil
			}
		case "inspect":
			switch {
			case slices.Contains(arguments, "{{.State.Status}}"):
				return []byte("exited\n"), nil
			case slices.Contains(arguments, "{{.State.ExitCode}}"):
				return []byte("1\n"), nil
			}
		case "logs":
			return []byte("production serve requires LEAPVIEW_POSTGRES_CONTROL_URL\n"), nil
		case "rm":
			if len(arguments) >= 2 && arguments[1] == "--force" {
				return nil, nil
			}
		case "tag", "push":
			forbiddenDockerCommand = joined
			return nil, errors.New("unexpected Docker tag or push")
		case "compose":
			switch {
			case strings.HasSuffix(joined, "create --no-build leapview"):
				composeCreateReached = true
				composeImage, composeEnvErr = envFileValue(
					filepath.Join(request.Directory, deploymentEnvName),
					"LEAPVIEW_IMAGE",
				)
				return nil, stopAtCompose
			case strings.HasSuffix(joined, "logs --no-color --tail 500"),
				strings.HasSuffix(joined, "rm --force --stop leapview"),
				strings.HasSuffix(joined, "rm --force --stop leapview caddy"),
				strings.HasSuffix(joined, "down --volumes --remove-orphans"):
				return nil, nil
			}
		}
		return nil, errors.New("unexpected qualification command: " + joined)
	})

	controller, err := New(Options{
		Root:      filepath.Join("..", "..", "..", "..", "deploy", "compose"),
		DockerBin: "docker-probe", Stderr: &strings.Builder{},
		qualificationExecutor: executor,
	})
	require.NoError(t, err)
	evidenceDir := filepath.Join(t.TempDir(), "evidence")

	err = controller.QualifyImage(t.Context(), QualificationImageOptions{
		Image: image, EvidenceDir: evidenceDir, RequireImmutable: true,
	})

	require.ErrorContains(t, err, stopAtCompose.Error())
	require.True(t, composeCreateReached, "qualification did not reach Compose create")
	require.NoError(t, composeEnvErr)
	require.Equal(t, image, composeImage, "generated deployment.env changed the immutable image reference")
	require.Empty(t, forbiddenDockerCommand, "immutable image qualification started a registry or tagged/pushed an image")

	reportContents, readErr := os.ReadFile(filepath.Join(evidenceDir, "image-qualification-report.json"))
	require.NoError(t, readErr)
	var report qualificationImageReport
	require.NoError(t, json.Unmarshal(reportContents, &report))
	require.Equal(t, image, report.Image)
}
