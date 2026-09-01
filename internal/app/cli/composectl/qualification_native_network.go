package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const qualificationComposeNetworkInspectFormat = "{{json .NetworkSettings.Networks}}"

// prepareQualificationNativePostgresNetwork asks Compose to materialize the
// network that belongs to the qualification application service.  Compose is
// the owner of the network (and its labels), so this helper deliberately does
// not call docker network create or otherwise reproduce Compose metadata.
//
// A temporary application container is created only to cause Compose to
// create its project network.  The container is removed before this method
// returns, while the network is left in place for a native sidecar to join.
// The caller is responsible for supplying a pre-seeded leapview.env; this
// method must not synthesize application configuration before the final
// PostgreSQL URLs and credentials are known.
func (c *Controller) prepareQualificationNativePostgresNetwork(
	ctx context.Context,
) (network string, resultErr error) {
	if c == nil {
		return "", errors.New("controller is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.qualificationContainers == nil {
		return "", errors.New("qualification container runtime is required")
	}

	// Compose resolves env_file entries while creating the service.  Requiring
	// this file up front prevents a missing or empty seed from being silently
	// replaced by initialization defaults.
	if err := requireNonEmptyFile(c.path(appEnvName)); err != nil {
		return "", fmt.Errorf("qualification Compose application environment: %w", err)
	}
	if err := requireNonEmptyFile(c.path(deploymentEnvName)); err != nil {
		return "", fmt.Errorf("qualification Compose deployment environment: %w", err)
	}
	projectValue, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_PROJECT_NAME")
	if err != nil {
		return "", fmt.Errorf("qualification Compose project: %w", err)
	}
	project := strings.TrimSpace(projectValue)
	if project != projectValue {
		return "", fmt.Errorf("qualification Compose project must be a normalized identifier")
	}
	if err := validateQualificationNativePostgresIdentifier(project, "qualification Compose project"); err != nil {
		return "", err
	}
	if normalizedQualificationName(project) != project {
		return "", fmt.Errorf("qualification Compose project must be a normalized identifier")
	}
	expectedNetwork := project + "_default"

	// Once Compose has been asked to create the service, cleanup is attempted
	// on every subsequent path, including create/ps/inspect failures.  Joining
	// cleanup errors preserves the original failure while still surfacing a
	// stale-container cleanup problem to the caller.
	created := false
	defer func() {
		if !created {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		_, cleanupErr := c.qualificationCompose(
			cleanupCtx, c.root, "rm", "--force", "--stop", "leapview",
		)
		if cleanupErr == nil {
			return
		}
		cleanupErr = fmt.Errorf("remove pre-created qualification application container: %w", cleanupErr)
		if resultErr == nil {
			resultErr = cleanupErr
			return
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()

	if _, err := c.qualificationCompose(
		ctx, c.root, "create", "--no-build", "leapview",
	); err != nil {
		// Compose can leave a stopped service behind even when create reports an
		// error. Marking the cleanup boundary before returning keeps retries from
		// inheriting that stale app container.
		created = true
		return "", fmt.Errorf("create qualification Compose application: %w", err)
	}
	created = true

	container, err := c.qualificationApplicationContainer(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve pre-created qualification application container: %w", err)
	}
	inspection, err := container.Inspect(ctx, qualificationComposeNetworkInspectFormat)
	if err != nil {
		return "", fmt.Errorf("inspect pre-created qualification application networks: %w", err)
	}
	var networks map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(inspection))), &networks); err != nil {
		return "", fmt.Errorf("decode pre-created qualification application networks: %w", err)
	}
	if len(networks) != 1 {
		return "", fmt.Errorf(
			"pre-created qualification application must attach to exactly one Compose network; found %d",
			len(networks),
		)
	}
	for name := range networks {
		if name != expectedNetwork {
			return "", fmt.Errorf(
				"pre-created qualification application attached to network %q; want %q",
				name, expectedNetwork,
			)
		}
	}

	return expectedNetwork, nil
}
