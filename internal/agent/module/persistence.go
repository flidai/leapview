package module

import (
	"database/sql"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
)

type persistenceBackend uint8

const (
	backendSQLite persistenceBackend = iota + 1
	backendPostgres
)

// Persistence is the typed agent storage selection passed into module
// composition. backend is intentionally private so callers cannot forge a
// native marker or accidentally route production through SQLite.
type Persistence struct {
	Repository     agent.Repository
	backend        persistenceBackend
	legacyDatabase *sql.DB
}

func NewPostgresPersistence(repository *agentpostgres.Repository) (Persistence, error) {
	if repository == nil {
		return Persistence{}, fmt.Errorf("agent PostgreSQL repository is required")
	}
	if !repository.Configured() || !repository.TransactionCapable() {
		return Persistence{}, fmt.Errorf("agent PostgreSQL repository must be configured with a transactional database")
	}
	if !repository.WorkflowCapable() || !repository.JobsCapable() || !repository.AuditCapable() || !repository.DomainEventCapable() {
		return Persistence{}, fmt.Errorf("agent PostgreSQL workflow, jobs, audit, and domain-event authorities are required")
	}
	return Persistence{Repository: repository, backend: backendPostgres}, nil
}

// SQLitePersistenceConfig is explicit by design: SQLite is a development and
// test backend and must never be inferred from a production configuration.
type SQLitePersistenceConfig struct {
	Database            *sql.DB
	Workflow            jobplatform.WorkflowRecorder
	AuditIntentRecorder access.AuditIntentRecorder
}

func NewSQLitePersistence(config SQLitePersistenceConfig) (Persistence, error) {
	if config.Database == nil {
		return Persistence{}, fmt.Errorf("agent SQLite database is required")
	}
	return Persistence{Repository: newRepository(config.Database, config.Workflow, config.AuditIntentRecorder), backend: backendSQLite, legacyDatabase: config.Database}, nil
}

func (p *Persistence) isPostgres() bool { return p != nil && p.backend == backendPostgres }
func (p *Persistence) isSQLite() bool {
	return p != nil && p.backend == backendSQLite && p.legacyDatabase != nil
}

func (p Persistence) validate() error {
	if p.Repository == nil {
		return fmt.Errorf("agent persistence is required")
	}
	if !p.isPostgres() && !p.isSQLite() {
		return fmt.Errorf("agent persistence backend is invalid")
	}
	if p.isPostgres() {
		native, ok := p.Repository.(*agentpostgres.Repository)
		if !ok || !native.Configured() || !native.TransactionCapable() || !native.WorkflowCapable() || !native.JobsCapable() || !native.AuditCapable() || !native.DomainEventCapable() {
			return fmt.Errorf("agent PostgreSQL persistence is not fully configured")
		}
	}
	return nil
}

func newRepository(database *sql.DB, workflow jobplatform.WorkflowRecorder, audits ...access.AuditIntentRecorder) agent.Repository {
	var audit access.AuditIntentRecorder
	if len(audits) > 0 {
		audit = audits[0]
	}
	return agentsqlite.NewRepositoryWithWorkflowAndAudit(database, jobsqlite.NewRepository(database), workflow, audit)
}
