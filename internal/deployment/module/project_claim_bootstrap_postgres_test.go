package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestBootstrapProjectClaimPostgresAtomicReplayConflictAndEnvironment(t *testing.T) {
	db, repo := projectClaimBootstrapPostgresDB(t)
	m := productionProjectClaimBootstrapModule(repo, postgresProjectClaimAudit{audit: accesspostgres.New()})

	first := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")
	second := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")
	changed := callProjectClaimBootstrap(t, m, "project:two", "issuer:two", "prod", "key-1")
	conflict := callProjectClaimBootstrap(t, m, "project:two", "issuer:two", "prod", "key-2")

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, first.Body.String(), second.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, http.StatusConflict, changed.Code, changed.Body.String())
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	claim, err := repo.GetProjectClaim(t.Context())
	require.NoError(t, err)
	require.Equal(t, projectgraph.ResourceID("project:one"), claim.ProjectID)
	require.Equal(t, "instance-admin", claim.ClaimedBy)

	audits, err := accesspostgres.New().ListAuditEvents(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, audits, 2)
	require.Equal(t, "failure", audits[0].Outcome)
	require.Equal(t, "success", audits[1].Outcome)

	foreign := callProjectClaimBootstrap(t, m, "project:three", "issuer:three", "dev", "key-3")
	require.Equal(t, http.StatusConflict, foreign.Code, foreign.Body.String())
	audits, err = accesspostgres.New().ListAuditEvents(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, audits, 3)
	_, err = repo.GetProjectClaim(t.Context())
	require.NoError(t, err)
}

func TestBootstrapProjectClaimPostgresForeignEnvironmentFirstIsIdempotent(t *testing.T) {
	db, repo := projectClaimBootstrapPostgresDB(t)
	m := productionProjectClaimBootstrapModule(repo, postgresProjectClaimAudit{audit: accesspostgres.New()})

	first := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "dev", "foreign-key")
	replay := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "dev", "foreign-key")

	require.Equal(t, http.StatusConflict, first.Code, first.Body.String())
	require.Equal(t, http.StatusConflict, replay.Code, replay.Body.String())
	_, err := repo.GetProjectClaim(t.Context())
	require.ErrorIs(t, err, deployment.ErrProjectClaimNotFound)
	audits, err := accesspostgres.New().ListAuditEvents(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, audits, 1)
	require.Equal(t, "failure", audits[0].Outcome)
}

func TestBootstrapProjectClaimPostgresConcurrentSameTupleReplays(t *testing.T) {
	db, repo := projectClaimBootstrapPostgresDB(t)
	m := productionProjectClaimBootstrapModule(repo, postgresProjectClaimAudit{audit: accesspostgres.New()})

	responses := make([]*httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	wait.Add(len(responses))
	for index := range responses {
		go func(index int) {
			defer wait.Done()
			responses[index] = callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "concurrent-key")
		}(index)
	}
	wait.Wait()

	for _, response := range responses {
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	require.Equal(t, responses[0].Body.String(), responses[1].Body.String())
	claim, err := repo.GetProjectClaim(t.Context())
	require.NoError(t, err)
	require.Equal(t, projectgraph.ResourceID("project:one"), claim.ProjectID)
	audits, err := accesspostgres.New().ListAuditEvents(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, audits, 1)
	require.Equal(t, "success", audits[0].Outcome)
}

func TestBootstrapProjectClaimPostgresAuditFailureRollsBackClaim(t *testing.T) {
	_, repo := projectClaimBootstrapPostgresDB(t)
	m := productionProjectClaimBootstrapModule(repo, failingProjectClaimAudit{})

	response := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	_, err := repo.GetProjectClaim(t.Context())
	require.ErrorIs(t, err, deployment.ErrProjectClaimNotFound)
}

func TestBootstrapProjectClaimPostgresCompletesGeneratedCommandGuard(t *testing.T) {
	db, repo := projectClaimBootstrapPostgresDB(t)
	m := productionProjectClaimBootstrapModule(repo, postgresProjectClaimAudit{audit: accesspostgres.New()})
	ctx, guard, err := deploymentgen.BeginGenBootstrapProjectClaimCommand(t.Context(), deploymentgen.GenBootstrapProjectClaimCommandInvocation{
		Surface: apigencommand.SurfaceAPI, IdempotencyKey: "generated-key",
	})
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/instance/project-claim", strings.NewReader(`{"projectUid":"project:generated","issuerId":"issuer:generated","environment":"prod"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	m.BootstrapProjectClaim(response, request, "generated-key")

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.True(t, guard.Completed())
	claim, err := repo.GetProjectClaim(t.Context())
	require.NoError(t, err)
	require.Equal(t, projectgraph.ResourceID("project:generated"), claim.ProjectID)
	audits, err := accesspostgres.New().ListAuditEvents(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, audits, 1)
	require.Equal(t, "success", audits[0].Outcome)
}

func projectClaimBootstrapPostgresDB(t *testing.T) (*pgxpool.Pool, *deploymentnative.Repository) {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "project_claim_bootstrap")
	poolConfig, err := pgxpool.ParseConfig(database.AdminURL())
	require.NoError(t, err)
	poolConfig.MaxConns = 1
	db, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	require.NoError(t, err)
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	require.NoError(t, err)
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := platformbootstrappostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	require.NoError(t, tx.Commit(t.Context()))
	return db, deploymentnative.New(db)
}

func productionProjectClaimBootstrapModule(repo *deploymentnative.Repository, audit ProjectClaimAuditAppender) *Module {
	return &Module{
		instanceID: "lvinst_test", instanceEnvironment: servingstate.Environment("prod"),
		persistence: &Persistence{Repository: repo}, projectClaimAudit: audit,
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
			return deploymenthttp.Principal{ID: "instance-admin"}, true
		}}),
	}
}

// postgresProjectClaimAudit keeps this package-level conformance test free of
// an app-composition import cycle while exercising the same Access-owned audit
// repository and caller-owned transaction as deploymentaudit.Adapter.
type postgresProjectClaimAudit struct {
	audit *accesspostgres.AuditRepository
}

func (a postgresProjectClaimAudit) AppendProjectClaimAudit(ctx context.Context, tx deploymentnative.Tx, input ProjectClaimAuditInput) (deploymentnative.AuditEvent, error) {
	stored, err := a.audit.RecordAuditEvent(ctx, tx, projectClaimAuditIntent(input))
	if err != nil {
		return deploymentnative.AuditEvent{}, err
	}
	return projectClaimAuditEvent(stored), nil
}

func (a postgresProjectClaimAudit) GetProjectClaimAudit(ctx context.Context, tx deploymentnative.Tx, input ProjectClaimAuditInput) (deploymentnative.AuditEvent, error) {
	stored, err := a.audit.GetAuditEvent(ctx, tx, input.AuditID)
	if errors.Is(err, pgx.ErrNoRows) {
		return deploymentnative.AuditEvent{}, ErrProjectClaimAuditNotFound
	}
	if err != nil {
		return deploymentnative.AuditEvent{}, err
	}
	expected := projectClaimAuditIntent(input)
	digest, err := expected.PayloadDigest()
	if err != nil || stored.IntentDigest != digest || stored.RequestDigest != input.RequestDigest || stored.ActorID != input.ActorID || stored.ResourceID != input.ProjectUID {
		return deploymentnative.AuditEvent{}, deploymentnative.ErrConflict
	}
	return projectClaimAuditEvent(stored), nil
}

func projectClaimAuditIntent(input ProjectClaimAuditInput) access.AuditIntent {
	return access.AuditIntent{EventID: input.AuditID, ScopeID: input.ScopeID, ActorID: input.ActorID, RequestDigest: input.RequestDigest, Source: "deployment", Operation: "bootstrap_project_claim", Action: "project.claim.bootstrapped", ResourceKind: "project", ResourceID: input.ProjectUID, Outcome: input.Outcome, AggregateKey: input.AggregateKey, MetadataJSON: input.MetadataJSON}
}

func projectClaimAuditEvent(stored accesspostgres.Event) deploymentnative.AuditEvent {
	outcome := stored.Outcome
	if outcome == "success" {
		outcome = "accepted"
	}
	return deploymentnative.AuditEvent{AuditID: stored.AuditID, ScopeID: stored.ScopeID, ActorID: stored.ActorID, ResourceID: stored.ResourceID, Outcome: outcome, RequestDigest: stored.RequestDigest, Metadata: []byte(stored.MetadataJSON), OccurredAt: stored.OccurredAt}
}

type failingProjectClaimAudit struct{}

func (failingProjectClaimAudit) AppendProjectClaimAudit(context.Context, deploymentnative.Tx, ProjectClaimAuditInput) (deploymentnative.AuditEvent, error) {
	return deploymentnative.AuditEvent{}, errors.New("audit append failed")
}

func (failingProjectClaimAudit) GetProjectClaimAudit(context.Context, deploymentnative.Tx, ProjectClaimAuditInput) (deploymentnative.AuditEvent, error) {
	return deploymentnative.AuditEvent{}, ErrProjectClaimAuditNotFound
}
