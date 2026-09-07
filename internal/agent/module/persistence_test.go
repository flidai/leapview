package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type moduleWorkflowStub struct{}

func (moduleWorkflowStub) RecordWorkflow(context.Context, jobspostgres.Tx, jobs.WorkflowIntent) error {
	return nil
}

type moduleJobsStub struct{}

func (moduleJobsStub) Get(context.Context, string) (jobs.Job, error) { return jobs.Job{}, nil }
func (moduleJobsStub) GetTx(context.Context, jobspostgres.Tx, string) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (moduleJobsStub) CancelTx(context.Context, jobspostgres.Tx, string) error { return nil }

type moduleAuditStub struct{}

func (moduleAuditStub) RecordAuditIntent(context.Context, agentpostgres.Tx, access.AuditIntent) error {
	return nil
}

type moduleDomainStub struct{}

func (moduleDomainStub) AppendDomainEvent(context.Context, agentpostgres.Tx, agentpostgres.DomainEventInput) (agentpostgres.DomainEvent, error) {
	return agentpostgres.DomainEvent{EventID: "event", AggregateVersion: 1}, nil
}

func TestNewPostgresPersistenceRequiresNativeCapabilities(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://invalid.invalid/agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workflow, authority, audit, domain := moduleWorkflowStub{}, moduleJobsStub{}, moduleAuditStub{}, moduleDomainStub{}
	all := agentpostgres.Options{Workflow: workflow, Jobs: authority, Audit: audit, Domain: domain}
	for name, options := range map[string]agentpostgres.Options{
		"workflow": {Jobs: authority, Audit: audit, Domain: domain},
		"jobs":     {Workflow: workflow, Audit: audit, Domain: domain},
		"audit":    {Workflow: workflow, Jobs: authority, Domain: domain},
		"domain":   {Workflow: workflow, Jobs: authority, Audit: audit},
	} {
		t.Run(name, func(t *testing.T) {
			repository, err := agentpostgres.NewWithOptions(pool, options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewPostgresPersistence(repository); err == nil {
				t.Fatal("missing native capability unexpectedly accepted")
			}
		})
	}
	repository, err := agentpostgres.NewProduction(pool, all)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPostgresPersistence(repository); err != nil {
		t.Fatalf("fully configured native persistence rejected: %v", err)
	}
}

func TestNewPostgresPersistenceRejectsNilAndNonTransactional(t *testing.T) {
	if _, err := NewPostgresPersistence(nil); err == nil {
		t.Fatal("nil PostgreSQL repository unexpectedly accepted")
	}
	repository, err := agentpostgres.NewWithOptions(moduleDBStub{}, agentpostgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPostgresPersistence(repository); err == nil {
		t.Fatal("non-transactional repository unexpectedly accepted")
	}
}

type moduleDBStub struct{}

func (moduleDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (moduleDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (moduleDBStub) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
