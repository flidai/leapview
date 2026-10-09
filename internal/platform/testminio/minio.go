// Package testminio supplies the pinned, source-built MinIO integration fixture.
// It is only for tests; production does not run or depend on this server.
package testminio

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

// Image identifies the locally built fixture, not an image to pull from a registry.
// Bump the suffix whenever Dockerfile build inputs change.
const Image = "leapview-test/minio:07c3a429bfed433e49018cb0f78a52145d4bedeb-v4"

//go:embed Dockerfile
var dockerfile []byte

//go:embed download-modules.sh
var downloadModules []byte

// Run builds the fixture using Docker's layer cache and retains the image across
// test containers. Each invocation still creates an isolated, disposable server.
// Callers should allow ten minutes for a cold build before container readiness.
func Run(ctx context.Context, opts ...testcontainers.ContainerCustomizer) (*tcminio.MinioContainer, error) {
	contextArchive, err := buildContextArchive()
	if err != nil {
		return nil, err
	}
	options := append([]testcontainers.ContainerCustomizer{}, opts...)
	repo, tag, _ := strings.Cut(Image, ":")
	options = append(options, testcontainers.WithDockerfile(testcontainers.FromDockerfile{
		ContextArchive: bytes.NewReader(contextArchive),
		Repo:           repo, Tag: tag, KeepImage: true, BuildLogWriter: os.Stderr,
	}))
	return tcminio.Run(ctx, "", options...)
}

func buildContextArchive() ([]byte, error) {
	var contextArchive bytes.Buffer
	archive := tar.NewWriter(&contextArchive)
	for _, input := range []struct {
		name string
		data []byte
	}{
		{name: "Dockerfile", data: dockerfile},
		{name: "download-modules.sh", data: downloadModules},
	} {
		if err := archive.WriteHeader(&tar.Header{Name: input.name, Mode: 0644, Size: int64(len(input.data))}); err != nil {
			return nil, fmt.Errorf("MinIO build context: %w", err)
		}
		if _, err := archive.Write(input.data); err != nil {
			return nil, fmt.Errorf("MinIO build context %s: %w", input.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("MinIO build context: %w", err)
	}
	return contextArchive.Bytes(), nil
}
