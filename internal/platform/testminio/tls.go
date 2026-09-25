package testminio

import (
	"archive/tar"
	"bytes"
	"context"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// WithTLSCertificate installs ephemeral test certificates owned by the fixture's
// non-root user. The private key remains mode 0600 and never enters an image layer.
func WithTLSCertificate(certificate, privateKey []byte) testcontainers.ContainerCustomizer {
	return testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
		PostCreates: []testcontainers.ContainerHook{func(ctx context.Context, container testcontainers.Container) error {
			var content bytes.Buffer
			archive := tar.NewWriter(&content)
			for _, file := range []struct {
				name string
				data []byte
				mode int64
			}{
				{"root/.minio/certs/public.crt", certificate, 0644},
				{"root/.minio/certs/private.key", privateKey, 0600},
			} {
				if err := archive.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data)), Uid: 65532, Gid: 65532}); err != nil {
					return err
				}
				if _, err := archive.Write(file.data); err != nil {
					return err
				}
			}
			if err := archive.Close(); err != nil {
				return err
			}
			docker, err := testcontainers.NewDockerClientWithOpts(ctx)
			if err != nil {
				return err
			}
			defer docker.Close()
			_, err = docker.CopyToContainer(ctx, container.GetContainerID(), client.CopyToContainerOptions{DestinationPath: "/", Content: &content})
			return err
		}},
	})
}
