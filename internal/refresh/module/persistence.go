package module

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/access"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
)

var _ RunPersistence = (*refreshsqlite.SQLRunRepository)(nil)

// TerminalRunRecovery is the module-facing recovery capability.  It carries
// no database or engine-specific types.
type TerminalRunRecovery interface {
	FailRunsForTerminalServingStates(context.Context, string, string) error
}

func RecoverWithPersistence(ctx context.Context, recovery TerminalRunRecovery, environment string) error {
	if recovery == nil {
		return errors.New("refresh terminal recovery persistence is required")
	}
	if strings.TrimSpace(environment) == "" {
		return errors.New("refresh terminal recovery environment is required")
	}
	return recovery.FailRunsForTerminalServingStates(ctx, environment, "refresh did not complete")
}

// Persistence is the refresh capability's storage bundle.  Domain services
// consume the narrow repository contracts they own; no handler or service
// needs to know which database adapter supplies them. The private backend and
// authority identity are set only by the explicit constructors below, so a
// repository-shaped struct literal cannot opt into production PostgreSQL.
//
// Recovery is the qualification-ledger repository and is intentionally
// optional because scheduled qualification is independently configured.
// TerminalRecovery is a separate startup authority for failing runs/jobs left
// live by an interrupted process. It is required whenever persistence is
// enabled so serving cannot start with stale live work; PostgreSQL composition
// injects an explicit implementation. Runs, schedules and publication are
// required whenever persistence is enabled.
type Persistence struct {
	Runs             RunPersistence
	Schedules        refreshschedule.Repository
	Publication      refreshrun.PublicationUnitOfWork
	Recovery         RecoveryRepository
	TerminalRecovery TerminalRunRecovery

	backend          persistenceBackend
	legacyDatabase   *sql.DB
	nativeRepository *refreshpostgres.Repository
}

type persistenceBackend uint8

const (
	backendUnknown persistenceBackend = iota
	backendPostgres
	backendSQLiteLegacy
)

// RunPersistence is the complete module-facing run capability.  It embeds
// queue/workflow, read projection, lease-fenced completion, admission and
// cancellation contracts so a configured module never discovers a missing
// operation through a runtime type assertion.
type RunPersistence interface {
	refreshrun.QueueRepository
	refreshrun.RunRepository
	refreshrun.RunTreeRepository
	refreshrun.LeaseFencedRunRepository
	refreshrun.LeaseFencedSupersedeRepository
	refreshrun.InvocationAdmissionChecker
	refreshrun.ScheduledInvocationAdmissionChecker
	ListTargetRuns(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error)
	LatestSuccessfulTargetRun(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error)
	ListSemanticModelRuns(context.Context, refreshrun.ReadScope, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error)
	LatestSuccessfulSemanticModelRun(context.Context, refreshrun.ReadScope, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error)
	CancelRun(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error)
	CancelRunWithAudit(context.Context, projectgraph.ServingIdentity, string, *access.AuditIntent) (refreshrun.RunRecord, error)
}

func (p Persistence) Validate() error {
	if p.Runs == nil {
		return errors.New("refresh run persistence is required")
	}
	if p.Schedules == nil {
		return errors.New("refresh schedule persistence is required")
	}
	if p.Publication == nil {
		return errors.New("refresh publication persistence is required")
	}
	if p.TerminalRecovery == nil {
		return errors.New("refresh terminal recovery persistence is required")
	}
	switch p.backend {
	case backendPostgres:
		if p.nativeRepository == nil || !p.nativeRepository.Configured() {
			return errors.New("PostgreSQL refresh persistence is not configured")
		}
		runs, runsOK := p.Runs.(*postgresRunPersistence)
		schedules, schedulesOK := p.Schedules.(*postgresSchedulePersistence)
		publication, publicationOK := p.Publication.(*postgresPublicationPersistence)
		recovery, recoveryOK := p.TerminalRecovery.(*PostgresTerminalRecovery)
		if !runsOK || runs == nil || runs.repository != p.nativeRepository ||
			!schedulesOK || schedules == nil || schedules.repository != p.nativeRepository ||
			!publicationOK || publication == nil || publication.repository != p.nativeRepository ||
			!recoveryOK || recovery == nil || recovery.Refresh != p.nativeRepository ||
			validatePostgresQueueAuthority(p.nativeRepository, recovery.Jobs) != nil {
			return errors.New("PostgreSQL refresh persistence surfaces do not match the configured native authority")
		}
		return nil
	case backendSQLiteLegacy:
		if p.legacyDatabase == nil {
			return errors.New("SQLite refresh persistence is not configured")
		}
		if p.nativeRepository != nil {
			return errors.New("SQLite refresh persistence cannot expose native PostgreSQL authority")
		}
		return nil
	default:
		return errors.New("refresh persistence backend is not configured")
	}
}

func (p Persistence) isPostgres() bool {
	return p.backend == backendPostgres && p.nativeRepository != nil && p.nativeRepository.Configured()
}

func (m *Module) readRuns() (RunPersistence, error) {
	if m == nil || m.runs == nil {
		return nil, errors.New("refresh run persistence is not configured")
	}
	return m.runs, nil
}

func (m *Module) cancelRuns() (RunPersistence, error) {
	if m == nil || m.runs == nil {
		return nil, errors.New("refresh run persistence is not configured")
	}
	return m.runs, nil
}

// SQLitePersistenceConfig contains the complete explicit development/test
// adapter construction inputs.  Callers selecting SQLite must do so here;
// Build never silently chooses SQLite when an injected bundle is absent.
type SQLitePersistenceConfig struct {
	Database            *sql.DB
	Workflow            jobplatform.WorkflowRecorder
	Execution           RunWorkflowConfig
	Audit               access.AuditIntentRecorder
	ApplyAccessSnapshot func(context.Context, transaction.Transaction, string) error
}

// RunWorkflowConfig mirrors the SQLite adapter's workflow contract without
// leaking its concrete package type through the module boundary.
type RunWorkflowConfig struct {
	ResourceKind string
	InitialEvent string
	InitialState string
}

// NewSQLitePersistence is the explicit development/test adapter.  Production
// composition should inject Persistence built from a non-SQLite capability.
func NewSQLitePersistence(config SQLitePersistenceConfig) (Persistence, error) {
	if config.Database == nil {
		return Persistence{}, errors.New("SQLite refresh database is required")
	}
	if config.Workflow == nil {
		return Persistence{}, errors.New("refresh workflow recorder is required")
	}
	if config.Execution.ResourceKind == "" {
		config.Execution.ResourceKind = "refresh"
	}
	if config.Execution.InitialEvent == "" {
		config.Execution.InitialEvent = "refresh.queued"
	}
	if config.Execution.InitialState == "" {
		config.Execution.InitialState = refreshrun.RunStatusQueued
	}
	// Audit and access-snapshot ports are optional in the development adapter;
	// the concrete constructors receive nil when the caller does not configure
	// those optional capabilities.
	runs := refreshsqlite.NewSQLRunRepositoryWithWorkflowAndAudit(config.Database, config.Workflow, refreshsqlite.RunWorkflowConfig{
		ResourceKind: config.Execution.ResourceKind,
		InitialEvent: config.Execution.InitialEvent,
		InitialState: config.Execution.InitialState,
	}, config.Audit)
	schedules := refreshsqlite.NewRepository(config.Database)
	return Persistence{Runs: runs, Schedules: schedules, Publication: refreshsqlite.NewPublicationUnitOfWork(config.Database, config.ApplyAccessSnapshot), Recovery: NewSQLiteRecoveryRepository(config.Database), TerminalRecovery: runs, backend: backendSQLiteLegacy, legacyDatabase: config.Database}, nil
}
