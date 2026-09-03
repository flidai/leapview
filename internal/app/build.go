package app

import (
	"context"

	"github.com/flidai/leapview/internal/app/config"
)

// Build is the sole process assembly entrypoint. Capability construction is
// exposed through module surfaces; Application retains only the final HTTP
// handler and lifecycle contracts. Every process profile enters the native
// PostgreSQL authority graph; local SQLite composition is no longer an app
// entrypoint.
func Build(ctx context.Context, cfg config.Config) (*Application, error) {
	return BuildProduction(ctx, cfg)
}

// BuildProduction is the production entrypoint used by the serve command.
// It constructs only the native PostgreSQL application graph. Build delegates
// here unconditionally, so no process profile can select a local SQLite
// authority graph.
func BuildProduction(ctx context.Context, cfg config.Config) (*Application, error) {
	cfg.Production = true
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
