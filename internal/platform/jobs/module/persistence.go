package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/flidai/leapview/pkg/jobs"
)

// Persistence is the complete authority bundle consumed by the jobs module.
// The backend marker is private and can only be set by one of the constructors
// below; a struct literal with a jobs.Repository therefore cannot accidentally
// opt into the production PostgreSQL path.
type Persistence struct {
	Repository jobs.Repository

	// SQLWorkflow is populated only by the explicit SQLite legacy adapter. It
	// accepts database/sql transactions and is intentionally unavailable to the
	// production PostgreSQL bundle.
	SQLWorkflow SQLWorkflowPort

	// NativeWorkflow and NativeCommitter are populated by the PostgreSQL
	// adapter. NativeWorkflow receives the caller-owned pgx transaction directly;
	// no database/sql compatibility assertion is made on this path.
	NativeWorkflow  NativeWorkflowPort
	NativeCommitter jobs.WorkflowCommitter

	backend        persistenceBackend
	legacyDatabase *sql.DB
}

type persistenceBackend uint8

const (
	backendUnknown persistenceBackend = iota
	backendPostgres
	backendSQLiteLegacy
)

// SQLWorkflowPort is the legacy transaction-bound workflow surface. It is
// retained solely for SQLite development and tests.
type SQLWorkflowPort interface {
	RecordWorkflow(context.Context, transaction.Transaction, jobs.WorkflowIntent) error
	CancelWorkflowJob(context.Context, transaction.Transaction, string) error
}

// NativeWorkflowPort is the production transaction-bound workflow surface.
// Callers own begin/commit/rollback of the pgx transaction.
type NativeWorkflowPort interface {
	RecordWorkflow(context.Context, jobpostgres.Tx, jobs.WorkflowIntent) error
}

// NewPostgresPersistence adapts the canonical PostgreSQL jobs repository.
// The concrete repository requirement prevents a SQLite or arbitrary
// jobs.Repository from being mislabeled as production PostgreSQL authority.
func NewPostgresPersistence(repository *jobpostgres.Repository) (Persistence, error) {
	if repository == nil {
		return Persistence{}, errors.New("PostgreSQL jobs repository is required")
	}
	return Persistence{
		Repository: repository, NativeWorkflow: repository,
		NativeCommitter: repository, backend: backendPostgres,
	}, nil
}

// SQLitePersistenceConfig contains the complete explicit development/test
// adapter construction input. Production composition must inject
// NewPostgresPersistence instead.
type SQLitePersistenceConfig struct {
	Database *sql.DB
}

// NewSQLitePersistence constructs the legacy SQLite adapter. It is never
// selected implicitly by Build.
func NewSQLitePersistence(config SQLitePersistenceConfig) (Persistence, error) {
	if config.Database == nil {
		return Persistence{}, errors.New("SQLite jobs database is required")
	}
	repository := jobsqlite.NewRepository(config.Database)
	return Persistence{
		Repository: repository, SQLWorkflow: repository,
		backend: backendSQLiteLegacy, legacyDatabase: config.Database,
	}, nil
}

func (p Persistence) validate() error {
	if p.Repository == nil {
		return errors.New("jobs repository is required")
	}
	switch p.backend {
	case backendPostgres:
		if p.NativeWorkflow == nil || p.NativeCommitter == nil {
			return errors.New("PostgreSQL jobs workflow and committer are required")
		}
		if p.SQLWorkflow != nil {
			return errors.New("PostgreSQL jobs persistence cannot expose database/sql workflow")
		}
	case backendSQLiteLegacy:
		if p.SQLWorkflow == nil {
			return errors.New("SQLite jobs workflow is required")
		}
		if p.NativeWorkflow != nil || p.NativeCommitter != nil {
			return errors.New("SQLite jobs persistence cannot expose native PostgreSQL workflow")
		}
	default:
		return fmt.Errorf("jobs persistence backend is not configured")
	}
	return nil
}

func (p Persistence) isPostgres() bool { return p.backend == backendPostgres }
