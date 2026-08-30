package postgresauthority

import (
	"context"
	"fmt"
	"strings"

	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// ResolveInstanceIdentity loads or creates the durable process identity and
// binds its environment before target-scoped authorities are composed. The
// bootstrap repository and the eventual authority graph share the same
// retained runtime pool.
func ResolveInstanceIdentity(ctx context.Context, runtime *platformpostgres.Pool, environment string) (string, error) {
	if runtime == nil {
		return "", fmt.Errorf("PostgreSQL instance identity requires the runtime control pool")
	}
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return "", fmt.Errorf("PostgreSQL instance environment is required")
	}
	repository := platformbootstrappostgres.New(runtime)
	instanceID, err := repository.InstanceID(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve PostgreSQL instance identity: %w", err)
	}
	if err := repository.BindInstanceEnvironment(ctx, environment); err != nil {
		return "", fmt.Errorf("bind PostgreSQL instance environment: %w", err)
	}
	return instanceID, nil
}
