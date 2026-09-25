package composectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// Preparation runs under the host install lock. Only resources absent at the
// ownership boundary may be discarded. Compose's network and application data
// volume are deliberately retained; both are reusable and may contain other
// application state. No cleanup is performed after preparation hands off to init.
type predecessorPreparation struct {
	controller        *Controller
	project           string
	deployment        []byte
	networkAttempted  bool
	providerAttempted bool
}

func (c *Controller) beginPredecessorPreparation(ctx context.Context) (*predecessorPreparation, error) {
	for _, name := range []string{".host-install.json", "predecessor-provider-tls", "predecessor-pool"} {
		if _, err := os.Lstat(c.path(name)); err == nil {
			return nil, fmt.Errorf("predecessor preparation refuses existing %s", name)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	deployment, err := os.ReadFile(c.path(deploymentEnvName))
	if err != nil {
		return nil, err
	}
	project, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_PROJECT_NAME")
	if err != nil {
		return nil, err
	}
	if validateQualificationNativePostgresIdentifier(project, "predecessor Compose project") != nil || normalizedQualificationName(project) != project {
		return nil, errors.New("predecessor Compose project is invalid")
	}
	preparation := &predecessorPreparation{controller: c, project: project, deployment: deployment}
	for _, resource := range []string{"container", "volume"} {
		exists, err := preparation.resourceExists(ctx, resource)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("predecessor preparation refuses existing provider %s", resource)
		}
	}
	return preparation, nil
}

func (p *predecessorPreparation) resourceName(resource string) string {
	if resource == "container" {
		return p.project + "-postgres"
	}
	return p.project + "_predecessor-postgres-data"
}

func (p *predecessorPreparation) resourceExists(ctx context.Context, resource string) (bool, error) {
	args := []string{resource, "ls", "--filter", "name=^" + p.resourceName(resource) + "$", "--format", "{{.Name}}"}
	if resource == "container" {
		args = []string{resource, "ls", "--all", "--filter", "name=^/" + p.resourceName(resource) + "$", "--format", "{{.Names}}"}
	}
	output, err := p.controller.qualificationDocker(ctx, nil, args...)
	return strings.TrimSpace(string(output)) != "", err
}

func (p *predecessorPreparation) cleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
	defer cancel()
	c := p.controller
	// A failed Docker response can still have created a container. Remove it
	// before its volume/TLS files, and retain leapview.env if cleanup is uncertain.
	if p.networkAttempted {
		if _, err := c.qualificationCompose(ctx, c.root, "rm", "--force", "--stop", "leapview"); err != nil {
			return err
		}
	}
	if p.providerAttempted {
		for _, resource := range []string{"container", "volume"} {
			exists, err := p.resourceExists(ctx, resource)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			args := []string{resource, "rm"}
			if resource == "container" {
				args = append(args, "--force")
			}
			if _, err := c.qualificationDocker(ctx, nil, append(args, p.resourceName(resource))...); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"predecessor-pool", "predecessor-provider-tls"} {
		if err := os.RemoveAll(c.path(name)); err != nil {
			return err
		}
	}
	if err := securefs.WritePrivateFileAtomic(c.path(deploymentEnvName), p.deployment); err != nil {
		return err
	}
	// Last: removing this guard makes a fresh attempt possible only after all
	// owned provider state has been removed successfully.
	return os.Remove(c.path(appEnvName))
}
