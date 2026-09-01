package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Persistence is the access module's complete authority bundle.  The module
// consumes this typed capability aggregate instead of discovering a database
// or selecting a dialect at runtime.  Production composition must inject the
// PostgreSQL repository; SQLite is available only through NewSQLitePersistence
// for local development and tests.
type Persistence struct {
	Repository       access.Repository
	OAuth            *mcpoauth.Service
	Avatar           avatar.Repository
	Authoring        access.AuthoringAuthRepository
	Desktop          access.DesktopSessionRepository
	Snapshot         SnapshotInstaller
	Publication      DashboardPublicationActivator
	backend          persistenceBackend
	nativeRepository *accesspostgres.Repository
	// legacyDatabase is retained only by the explicit SQLite adapter for
	// constructing the local MCP OAuth service. It is deliberately private so
	// callers cannot inject a database through the module configuration.
	legacyDatabase *sql.DB
}

type persistenceBackend uint8

const (
	backendUnknown persistenceBackend = iota
	backendPostgres
	backendSQLiteLegacy
)

// SnapshotInstaller and DashboardPublicationActivator are transaction-bound
// ports.  Their implementations own the SQL dialect and receive the caller's
// transaction; the access module never opens a hidden database connection.
type SnapshotInstaller interface {
	InstallSnapshot(context.Context, any, accesssnapshot.AuthorizationSnapshot) error
}

type DashboardPublicationActivator interface {
	ActivateDashboardPublicationPrincipal(context.Context, any, projectgraph.ResourceID, string) error
}

type sqliteActivationPorts struct{}

func (sqliteActivationPorts) InstallSnapshot(ctx context.Context, tx any, snapshot accesssnapshot.AuthorizationSnapshot) error {
	sqlTx, ok := tx.(transaction.Transaction)
	if !ok {
		return errors.New("SQLite snapshot activation requires database/sql transaction")
	}
	return accesssqlite.InstallAuthorizationSnapshotTx(ctx, sqlTx, snapshot)
}

func (sqliteActivationPorts) ActivateDashboardPublicationPrincipal(ctx context.Context, tx any, projectID projectgraph.ResourceID, name string) error {
	sqlTx, ok := tx.(transaction.Transaction)
	if !ok {
		return errors.New("SQLite publication activation requires database/sql transaction")
	}
	return accesssqlite.ActivateDashboardPublicationPrincipalTx(ctx, sqlTx, projectID, name)
}

type postgresActivationPorts struct{}

func (postgresActivationPorts) InstallSnapshot(ctx context.Context, tx any, snapshot accesssnapshot.AuthorizationSnapshot) error {
	pgTx, ok := tx.(accesspostgres.Tx)
	if !ok {
		return errors.New("PostgreSQL snapshot activation requires caller-owned pgx transaction")
	}
	return accesspostgres.InstallAuthorizationSnapshotTx(ctx, pgTx, snapshot)
}

func (postgresActivationPorts) ActivateDashboardPublicationPrincipal(ctx context.Context, tx any, projectID projectgraph.ResourceID, name string) error {
	pgTx, ok := tx.(accesspostgres.Tx)
	if !ok {
		return errors.New("PostgreSQL publication activation requires caller-owned pgx transaction")
	}
	return accesspostgres.ActivateDashboardPublicationPrincipalTx(ctx, pgTx, projectID, name)
}

// Validate rejects partial authority bundles before handlers are mounted.
// Repository is deliberately checked for every module-facing capability so a
// PostgreSQL path cannot fail later through an optional type assertion.
func (p Persistence) Validate() error {
	if p.Repository == nil {
		return errors.New("access repository is required")
	}
	if p.backend == backendPostgres && (p.nativeRepository == nil || p.Repository != p.nativeRepository || !p.nativeRepository.Configured()) {
		return errors.New("PostgreSQL access repository does not match the configured native authority")
	}
	if p.backend == backendPostgres {
		if _, ok := p.Snapshot.(postgresActivationPorts); !ok {
			return errors.New("PostgreSQL access snapshot authority does not match the configured backend")
		}
		if _, ok := p.Publication.(postgresActivationPorts); !ok {
			return errors.New("PostgreSQL access publication authority does not match the configured backend")
		}
	}
	if p.backend == backendSQLiteLegacy {
		if _, ok := p.Snapshot.(sqliteActivationPorts); !ok {
			return errors.New("SQLite access snapshot authority does not match the configured backend")
		}
		if _, ok := p.Publication.(sqliteActivationPorts); !ok {
			return errors.New("SQLite access publication authority does not match the configured backend")
		}
	}
	if _, ok := p.Repository.(access.AuthoringAuthRepository); !ok {
		return errors.New("access repository does not implement authoring authentication")
	}
	if _, ok := p.Repository.(access.DesktopSessionRepository); !ok {
		return errors.New("access repository does not implement desktop sessions")
	}
	if _, ok := p.Repository.(access.PlatformAdminReader); !ok {
		return errors.New("access repository does not implement platform administration")
	}
	if _, ok := p.Repository.(principalGroupResolver); !ok {
		return errors.New("access repository does not implement group resolution")
	}
	if p.Snapshot == nil {
		return errors.New("access authorization snapshot installer is required")
	}
	if p.Publication == nil {
		return errors.New("access dashboard publication activator is required")
	}
	return nil
}

func (p Persistence) isPostgres() bool { return p.backend == backendPostgres }

// NewPostgresPersistence adapts the clean-slate PostgreSQL access authority to
// the module boundary.  It does not initialize or migrate the schema; schema
// ownership belongs to the migration runner and callers must apply the
// already-authored baseline before constructing this bundle.
func NewPostgresPersistence(repository *accesspostgres.Repository, oauth *mcpoauth.Service) (Persistence, error) {
	if repository == nil {
		return Persistence{}, errors.New("PostgreSQL access repository is required")
	}
	if !repository.Configured() {
		return Persistence{}, errors.New("PostgreSQL access repository is not configured")
	}
	if oauth != nil && !oauth.IsPostgresBacked() {
		return Persistence{}, errors.New("PostgreSQL access persistence requires PostgreSQL-backed MCP OAuth state")
	}
	p := Persistence{Repository: repository, OAuth: oauth, backend: backendPostgres, nativeRepository: repository}
	p.Snapshot, p.Publication = postgresActivationPorts{}, postgresActivationPorts{}
	p.Authoring, _ = any(repository).(access.AuthoringAuthRepository)
	p.Desktop, _ = any(repository).(access.DesktopSessionRepository)
	p.Avatar, _ = any(repository).(avatar.Repository)
	if err := p.Validate(); err != nil {
		return Persistence{}, fmt.Errorf("validate PostgreSQL access persistence: %w", err)
	}
	return p, nil
}

// SQLitePersistenceConfig is intentionally named and constructor-scoped so
// selecting SQLite remains an explicit legacy/dev/test decision.
type SQLitePersistenceConfig struct {
	Database *sql.DB
}

// NewSQLitePersistence is the legacy adapter for existing local tests.  It is
// not used by the PostgreSQL constructor and must not be used for production
// composition.
func NewSQLitePersistence(ctx context.Context, config SQLitePersistenceConfig) (Persistence, error) {
	if config.Database == nil {
		return Persistence{}, errors.New("SQLite access database is required")
	}
	if err := accesssqlite.Initialize(ctx, config.Database); err != nil {
		return Persistence{}, err
	}
	repository := accesssqlite.NewRepository(config.Database)
	p := Persistence{Repository: repository, backend: backendSQLiteLegacy, legacyDatabase: config.Database}
	p.Snapshot, p.Publication = sqliteActivationPorts{}, sqliteActivationPorts{}
	p.Authoring, _ = any(repository).(access.AuthoringAuthRepository)
	p.Desktop, _ = any(repository).(access.DesktopSessionRepository)
	p.Avatar, _ = any(repository).(avatar.Repository)
	if err := p.Validate(); err != nil {
		return Persistence{}, fmt.Errorf("validate SQLite access persistence: %w", err)
	}
	return p, nil
}
