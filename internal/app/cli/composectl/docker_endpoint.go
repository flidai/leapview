package composectl

import (
	"context"
	"fmt"
)

// PinnedDockerEndpoint is supplied by the local-development endpoint verifier.
// It keeps every Docker and Compose subprocess on the qualified daemon.
type PinnedDockerEndpoint interface {
	Host() string
	Environment([]string) []string
	DockerArguments(...string) []string
	Verify(context.Context) error
}

func (c *Controller) verifyDockerEndpoint(ctx context.Context) error {
	if c.dockerEndpoint == nil {
		return nil
	}
	if err := c.dockerEndpoint.Verify(ctx); err != nil {
		return fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	return nil
}

func (c *Controller) dockerArguments(arguments ...string) []string {
	if c.dockerEndpoint == nil {
		return arguments
	}
	return c.dockerEndpoint.DockerArguments(arguments...)
}

func (c *Controller) dockerEnvironment(environment []string) []string {
	if c.dockerEndpoint == nil {
		return environment
	}
	return c.dockerEndpoint.Environment(environment)
}
