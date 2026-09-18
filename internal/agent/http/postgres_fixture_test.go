package http

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type agentHTTPPostgresFixture struct {
	Pool   *pgxpool.Pool
	Access *accesspostgres.Repository
	Agent  *agentpostgres.Repository
	Jobs   *jobspostgres.Repository
}

type agentHTTPAdmissionLease struct{ ctx context.Context }

func (l agentHTTPAdmissionLease) Context() context.Context { return l.ctx }
func (agentHTTPAdmissionLease) Release()                   {}

func openAgentHTTPPostgresFixture(t *testing.T, options agentpostgres.Options) *agentHTTPPostgresFixture {
	t.Helper()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema, jobspostgres.ApplySchema, agentpostgres.ApplySchema)
	if err := platformmigrations.ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	jobsRepository := jobspostgres.New(pool)
	persistence, err := jobsmodule.NewPostgresPersistence(jobsRepository)
	if err != nil {
		t.Fatal(err)
	}
	jobsModule, err := jobsmodule.Build(t.Context(), jobsmodule.Config{
		Persistence: &persistence, Production: true, OwnerID: "agent-http-test", LeaseTimeout: time.Minute,
		Admission: jobs.AdmitterFunc(func(ctx context.Context, _ jobs.AdmissionRequest) (jobs.AdmissionLease, error) {
			return agentHTTPAdmissionLease{ctx: ctx}, nil
		}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobsModule.Stop(context.Background()) })
	if options.Workflow == nil {
		options.Workflow = jobsRepository
	}
	if options.Jobs == nil {
		options.Jobs = jobsRepository
	}
	agentRepository, err := agentpostgres.NewWithOptions(pool, options)
	if err != nil {
		t.Fatal(err)
	}
	accessRepository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("agent-http-test-key", 2))})
	if err != nil {
		t.Fatal(err)
	}
	return &agentHTTPPostgresFixture{Pool: pool, Access: accessRepository, Agent: agentRepository, Jobs: jobsRepository}
}
