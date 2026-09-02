// Package adminpostgres adapts production Admin bootstrap commands to the
// native PostgreSQL authorities while retaining the explicit offline adapter
// for development and evaluation.
package adminpostgres

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	appadminoffline "github.com/flidai/leapview/internal/app/adminoffline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

type AccessPool interface {
	accesspostgres.DBTX
	postgresbaseline.RevisionReader
	Begin(context.Context) (pgx.Tx, error)
	Close()
}

type AccessInitializer interface {
	Initialized(context.Context) (bool, error)
	InitializeInstance(context.Context, access.InstanceInitializationInput, func(access.InitialInstanceCredentials) error) (access.InitialInstanceCredentials, error)
}

type Bootstrap interface {
	InstanceEnvironment(context.Context) (string, error)
	BindInstanceEnvironment(context.Context, string) error
}

type Dependencies struct {
	LoadConfig      func() (config.Config, error)
	OpenAccess      func(context.Context, platformpostgres.Config) (AccessPool, error)
	PrepareBaseline func(context.Context, config.Config) error
	VerifyBaseline  func(context.Context, postgresbaseline.RevisionReader) error
	NewAccess       func(AccessPool, []byte) (AccessInitializer, error)
	NewBootstrap    func(AccessPool) Bootstrap
	AcquireLock     func(string) (adminoffline.Lock, error)
	Now             func() time.Time
}

// Operations overrides only the reconciled PostgreSQL bootstrap operations.
// All other commands remain on the explicit offline adapter until their
// capability authority is independently reconciled.
type Operations struct {
	appadminoffline.Operations
	Dependencies Dependencies
}

func New(dependencies Dependencies) Operations {
	return Operations{Dependencies: dependencies.withDefaults()}
}

func (d Dependencies) withDefaults() Dependencies {
	if d.LoadConfig == nil {
		d.LoadConfig = config.Load
	}
	if d.OpenAccess == nil {
		d.OpenAccess = func(ctx context.Context, cfg platformpostgres.Config) (AccessPool, error) {
			return platformpostgres.OpenControl(ctx, cfg)
		}
	}
	if d.PrepareBaseline == nil {
		d.PrepareBaseline = prepareProductionBaseline
	}
	if d.VerifyBaseline == nil {
		d.VerifyBaseline = postgresbaseline.Verify
	}
	if d.NewAccess == nil {
		d.NewAccess = func(pool AccessPool, key []byte) (AccessInitializer, error) {
			return accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: key})
		}
	}
	if d.NewBootstrap == nil {
		d.NewBootstrap = func(pool AccessPool) Bootstrap { return platformbootstrap.New(pool) }
	}
	if d.AcquireLock == nil {
		d.AcquireLock = func(home string) (adminoffline.Lock, error) { return instancelock.Acquire(home) }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return d
}
