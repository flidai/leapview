package app

import (
	"context"

	"github.com/flidai/leapview/internal/app/config"
)

// Build is the sole process assembly entrypoint. Capability construction is
// exposed through module surfaces; Application retains only the final HTTP
// handler and lifecycle contracts.
func Build(ctx context.Context, cfg config.Config) (*Application, error) {
	if cfg.Production && !cfg.EvaluationMode {
		return BuildProduction(ctx, cfg)
	}
	handler, lifecycle, cleanup, err := assemble(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newApplication(handler, []Lifecycle{lifecycle}, cleanup), nil
}

// BuildProduction is the production entrypoint used by the serve command.
// It constructs only the native PostgreSQL application graph. Keeping this
// entrypoint separate from Build preserves embedded SQLite fixtures used by
// development and unit tests without making them a production fallback.
func BuildProduction(ctx context.Context, cfg config.Config) (*Application, error) {
	cfg.Production = true
	cfg.EvaluationMode = false
	// Preserve the PostgreSQL admission error as the first production failure,
	// then enforce the complete serving security contract before migrations or
	// any other database side effect can occur.
	if err := cfg.ValidatePostgresProduction(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(config.ProfileServe); err != nil {
		return nil, err
	}
	return buildPostgresProductionTarget(ctx, cfg)
}
