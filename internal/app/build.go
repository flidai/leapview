package app

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/app/config"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

// Build is the sole process assembly entrypoint. Capability construction is
// exposed through module surfaces; Application retains only the final HTTP
// handler and lifecycle contracts. Every process profile enters the native
// PostgreSQL authority graph; local SQLite composition is no longer an app
// entrypoint.
func Build(ctx context.Context, cfg config.Config) (*Application, error) {
	if cfg.Production {
		return BuildProduction(ctx, cfg)
	}
	return BuildDevelopment(ctx, cfg)
}

// BuildDevelopment assembles the native PostgreSQL graph with local,
// non-production policies. It intentionally does not weaken the authority
// graph: the same PostgreSQL repositories and lifecycle are used, while TLS,
// authentication, and delivery-admission requirements remain development
// appropriate.
func BuildDevelopment(ctx context.Context, cfg config.Config) (*Application, error) {
	cfg.Production = false
	// Native delivery coordinators retain the exact pool identity in their
	// request contract even on a fresh, unclaimed development target. Until a
	// developer explicitly bootstraps a physical pool, carry a stable local
	// sentinel that can never collide with a qualified production admission.
	if cfg.DeliveryPhysicalPoolID == "" && cfg.DeliveryPhysicalPoolCompatibilityDigest == "" {
		cfg.DeliveryPhysicalPoolID = developmentPoolSentinelID
		cfg.DeliveryPhysicalPoolCompatibilityDigest = developmentPoolSentinelDigest
	} else {
		// A real development process must receive the immutable identity pair
		// together. Reject partial or non-canonical values before composing any
		// PostgreSQL pools so a malformed environment cannot become a mixed
		// sentinel/real configuration that fails later during delivery.
		if cfg.DeliveryPhysicalPoolID == "" || cfg.DeliveryPhysicalPoolCompatibilityDigest == "" {
			return nil, errors.New("development serve requires LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID and LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST together")
		}
		if err := platformdigest.ValidateSHA256Identity(cfg.DeliveryPhysicalPoolID); err != nil {
			return nil, errors.New("development serve requires a canonical LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID")
		}
		if err := platformdigest.ValidateSHA256Identity(cfg.DeliveryPhysicalPoolCompatibilityDigest); err != nil {
			return nil, errors.New("development serve requires a canonical LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST")
		}
	}
	if err := cfg.ValidatePostgresDevelopment(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(config.ProfileServe); err != nil {
		return nil, err
	}
	return buildPostgresTarget(ctx, cfg, false)
}

// BuildProduction is the production entrypoint used by the serve command.
// It constructs only the native PostgreSQL application graph. Production
// profiles delegate here, while development uses the same native builder, so
// no process profile can select a local SQLite authority graph.
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
	return buildPostgresTarget(ctx, cfg, true)
}
