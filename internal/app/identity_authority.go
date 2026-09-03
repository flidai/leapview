package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/app/config"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
)

// identityControlPool is the small control-pool surface needed by the
// identity ledger and composition cleanup. Keeping this seam narrower than
// platformpostgres.Pool makes composition tests independent of a live server.
type identityControlPool = identitymodule.ControlPool

type identityControlOpener = identitymodule.ControlOpener

type identityAuthorityBundle struct {
	Repository identitymodule.Repository
	Cleanup    cleanupFunc
}

// buildIdentityAuthority composes the production identity ledger from the
// dedicated PostgreSQL control pool. Local and development processes do not
// get an identity fallback: they receive an empty authority bundle.
func buildIdentityAuthority(ctx context.Context, cfg config.Config) (identityAuthorityBundle, error) {
	return buildIdentityAuthorityWithOpener(ctx, cfg, openIdentityControl)
}

func openIdentityControl(ctx context.Context, cfg platformpostgres.Config) (identityControlPool, error) {
	return identitymodule.OpenControl(ctx, cfg)
}

func buildIdentityAuthorityWithOpener(ctx context.Context, cfg config.Config, open identityControlOpener) (identityAuthorityBundle, error) {
	if !cfg.Production {
		return identityAuthorityBundle{}, nil
	}
	if open == nil {
		return identityAuthorityBundle{}, errors.New("identity authority PostgreSQL opener is required")
	}
	if strings.TrimSpace(cfg.PostgresControlURL) == "" {
		return identityAuthorityBundle{}, fmt.Errorf("%w: PostgreSQL control URL is required", ErrIdentityLifecycleUnavailable)
	}
	if cfg.PostgresControlIntent != string(platformpostgres.IntentReadWrite) {
		return identityAuthorityBundle{}, fmt.Errorf("%w: identity authority requires read-write PostgreSQL intent", ErrIdentityLifecycleUnavailable)
	}
	if !cfg.PostgresRequireTLS {
		return identityAuthorityBundle{}, fmt.Errorf("%w: identity authority requires PostgreSQL TLS in production", ErrIdentityLifecycleUnavailable)
	}
	poolConfig := platformpostgres.Config{
		URL:                    cfg.PostgresControlURL,
		ExpectedMajor:          cfg.PostgresExpectedMajor,
		RuntimeRole:            cfg.PostgresControlRuntimeRole,
		Intent:                 platformpostgres.Intent(cfg.PostgresControlIntent),
		RequireTLS:             cfg.PostgresRequireTLS,
		MinConns:               int32(cfg.PostgresControlPoolMinConns),
		MaxConns:               int32(cfg.PostgresControlPoolMaxConns),
		AcquireTimeout:         cfg.PostgresControlAcquireTimeout,
		StatementTimeout:       cfg.PostgresControlStatementTimeout,
		LockTimeout:            cfg.PostgresControlLockTimeout,
		IdleTransactionTimeout: cfg.PostgresControlIdleTransactionTimeout,
	}
	if err := poolConfig.Validate(); err != nil {
		return identityAuthorityBundle{}, fmt.Errorf("%w: invalid PostgreSQL control authority configuration: %w", ErrIdentityLifecycleUnavailable, err)
	}
	pool, err := open(ctx, poolConfig)
	if err != nil {
		return identityAuthorityBundle{}, fmt.Errorf("%w: open PostgreSQL identity authority: %w", ErrIdentityLifecycleUnavailable, err)
	}
	if pool == nil {
		return identityAuthorityBundle{}, fmt.Errorf("%w: PostgreSQL identity authority opener returned nil pool", ErrIdentityLifecycleUnavailable)
	}
	repository, err := identitymodule.NewPostgresRepository(pool)
	if err != nil {
		pool.Close()
		return identityAuthorityBundle{}, fmt.Errorf("%w: build PostgreSQL identity ledger: %w", ErrIdentityLifecycleUnavailable, err)
	}
	return identityAuthorityBundle{
		Repository: repository,
		Cleanup: func(context.Context) error {
			pool.Close()
			return nil
		},
	}, nil
}
