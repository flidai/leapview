package app

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/app/config"
)

// Build is the sole process assembly entrypoint. Capability construction is
// exposed through module surfaces; Application retains only the final HTTP
// handler and lifecycle contracts.
func Build(ctx context.Context, cfg config.Config) (*Application, error) {
	if cfg.Production {
		return BuildProduction(ctx, cfg)
	}
	handler, lifecycle, cleanup, err := assemble(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newApplication(handler, []Lifecycle{lifecycle}, cleanup), nil
}

// BuildProduction is the production entrypoint used by the serve command.
// It applies and validates the PostgreSQL control-plane baseline before
// readiness, then refuses to construct the legacy SQLite-backed application
// graph until every capability authority has a PostgreSQL adapter.  Keeping
// this gate separate from Build preserves embedded SQLite fixtures used by
// development and unit tests without making them a production fallback.
func BuildProduction(ctx context.Context, cfg config.Config) (*Application, error) {
	cfg.Production = true
	bootstrap, err := openPostgresControlPlane(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := bootstrap.Start(ctx); err != nil {
		_ = bootstrap.Stop(context.Background())
		return nil, err
	}
	_ = bootstrap.Stop(context.Background())
	return nil, fmt.Errorf("%w: next wiring: inject PostgreSQL access, project, deployment, jobs, protocol, refresh, release, and managed-data authorities into buildApplicationSurfaces", errPostgresProductionCompositionIncomplete)
}
