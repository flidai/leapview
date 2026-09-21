package hostinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/ociref"
)

// extractCandidatePayload uses the same digest-pinned Docker extraction path
// as initial host bootstrap. No payload bytes come from the checkout or the
// existing host generation.
func extractCandidatePayload(ctx context.Context, dockerBin, image string, stderr io.Writer) (map[string][]byte, error) {
	if _, err := ociref.ParseImmutable(image); err != nil {
		return nil, err
	}
	if dockerBin == "" {
		dockerBin = "docker"
	}
	run := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, dockerBin, args...)
		command.Stderr = stderr
		var output bytes.Buffer
		command.Stdout = &output
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("docker %s: %w", args[0], err)
		}
		return strings.TrimSpace(output.String()), nil
	}
	if _, err := run("pull", "--quiet", image); err != nil {
		return nil, err
	}
	container, err := run("create", image)
	if err != nil {
		return nil, err
	}
	if container == "" || strings.ContainsAny(container, " \t\r\n/") {
		return nil, fmt.Errorf("Docker returned an invalid candidate container ID")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(cleanup, dockerBin, "rm", "--force", container)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		_ = command.Run()
	}()
	directory, err := os.MkdirTemp("", "leapview-upgrade-payload-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	if _, err := run("cp", container+":/usr/local/share/leapview/deployment/.", directory); err != nil {
		return nil, err
	}
	return readPayload(directory)
}
