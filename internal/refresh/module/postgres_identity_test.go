package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrecovery "github.com/flidai/leapview/internal/refresh/recovery"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewPostgresPersistenceRequiresPublicationIdentityResolver(t *testing.T) {
	_, err := NewPostgresPersistence(refreshpostgres.New(fakeRefreshTx{}), PostgresPersistenceConfig{SchedulerOwner: "scheduler"})
	if !errors.Is(err, ErrPublicationIdentityUnavailable) {
		t.Fatalf("NewPostgresPersistence error=%v, want ErrPublicationIdentityUnavailable", err)
	}
}

func TestNewPostgresPersistenceRejectsUnconfiguredRepository(t *testing.T) {
	_, err := NewPostgresPersistence(refreshpostgres.New(nil), PostgresPersistenceConfig{})
	if err == nil || !strings.Contains(err.Error(), "configured refresh PostgreSQL repository") {
		t.Fatalf("NewPostgresPersistence error=%v, want configured repository admission", err)
	}
}

func TestPostgresQueueAuthoritiesRejectNilAndMismatchedRefresh(t *testing.T) {
	db := &fakeRefreshTx{}
	refresh := refreshpostgres.New(db)
	queue := NewPostgresJobsAdapter(jobspostgres.New(db), refresh)
	if !queue.Configured() || !queue.MatchesRefreshRepository(refresh) {
		t.Fatal("configured queue adapter did not retain canonical refresh authority")
	}
	otherRefresh := refreshpostgres.New(&fakeRefreshTx{})
	if queue.MatchesRefreshRepository(otherRefresh) {
		t.Fatal("queue adapter accepted a mismatched refresh authority")
	}
	var nilQueue *PostgresJobsAdapter
	if _, err := NewPostgresPersistence(refresh, PostgresPersistenceConfig{
		SchedulerOwner: "scheduler", PublicationIdentityResolver: staticPublicationIdentityResolver("pool", "catalog"),
		Jobs: nilQueue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool", catalogID: "catalog"}, CancelAuditWriter: integrationAuditWriter{},
	}); err == nil || !strings.Contains(err.Error(), "canonical jobs authority is required") {
		t.Fatalf("nil queue persistence error = %v, want nil-authority rejection", err)
	}
}

func TestBuildProductionRequiresNativeFinalizer(t *testing.T) {
	db := &fakeRefreshTx{}
	refresh := refreshpostgres.New(db)
	queue := NewPostgresJobsAdapter(jobspostgres.New(db), refresh)
	persistence, err := NewPostgresPersistence(refresh, PostgresPersistenceConfig{
		SchedulerOwner: "scheduler", PublicationIdentityResolver: staticPublicationIdentityResolver("pool", "catalog"),
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool", catalogID: "catalog"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(t.Context(), Config{Persistence: &persistence, Production: true, Authorization: testAuthorization()})
	if err == nil || !strings.Contains(err.Error(), "native finalizer") {
		t.Fatalf("production build error = %v, want native-finalizer admission", err)
	}
}

func TestNewPostgresPersistenceComposesNativeRecoveryLedger(t *testing.T) {
	db := &fakeRefreshTx{}
	refresh := refreshpostgres.New(db)
	queue := NewPostgresJobsAdapter(jobspostgres.New(db), refresh)
	persistence, err := NewPostgresPersistence(refresh, PostgresPersistenceConfig{
		SchedulerOwner: "scheduler", PublicationIdentityResolver: staticPublicationIdentityResolver("pool", "catalog"),
		Jobs: queue, CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool", catalogID: "catalog"}, CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, ok := persistence.Recovery.(*refreshpostgres.RecoveryLedger)
	if !ok || ledger == nil || ledger.DB() != refresh.DB() {
		t.Fatalf("recovery persistence = %T, want native PostgreSQL ledger on configured authority", persistence.Recovery)
	}
	if err := persistence.Validate(); err != nil {
		t.Fatalf("native persistence validation: %v", err)
	}
}

func TestBuildProductionInjectsNativeRecoveryLedger(t *testing.T) {
	db := &fakeRefreshTx{}
	refresh := refreshpostgres.New(db)
	queue := NewPostgresJobsAdapter(jobspostgres.New(db), refresh)
	persistence, err := NewPostgresPersistence(refresh, PostgresPersistenceConfig{
		SchedulerOwner: "scheduler", PublicationIdentityResolver: staticPublicationIdentityResolver("pool", "catalog"), Jobs: queue,
		CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool", catalogID: "catalog"}, CancelAuditWriter: integrationAuditWriter{},
		NativeFinalizer: PostgresNativeRefreshFinalizerFunc(func(context.Context, refreshpostgres.Tx, refreshrun.JobRecord, refreshrun.CanonicalRefreshResult, refreshpostgres.PublicationInput) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &RecoveryLifecycle{
		Definitions: func(context.Context) ([]refreshrecovery.Definition, error) { return nil, nil }, WorkerID: "worker", Actor: "operator",
		Lease: time.Minute, BatchSize: 1, ComplianceWindow: time.Hour, EvidenceRoot: "/var/lib/leapview/evidence",
	}
	if _, err := Build(t.Context(), Config{Persistence: &persistence, Production: true, Authorization: testAuthorization(), RecoveryLifecycle: lifecycle}); err != nil {
		t.Fatalf("build production recovery lifecycle: %v", err)
	}
	if lifecycle.Repository != persistence.Recovery {
		t.Fatal("production build did not inject the native recovery ledger")
	}
}

func TestResolvePublicationIdentityRejectsUnadmittedIdentity(t *testing.T) {
	resolver := PostgresPublicationIdentityResolverFunc(func(context.Context, refreshpostgres.Tx, PostgresPublicationIdentityRequest) (PostgresPublicationIdentity, error) {
		return PostgresPublicationIdentity{PhysicalPoolID: " ", CatalogID: "catalog"}, nil
	})
	identity, err := resolvePublicationIdentityTx(context.Background(), fakeRefreshTx{}, resolver, PostgresPublicationIdentityRequest{ProjectID: "project", Environment: "prod", GenerationID: "generation", Source: "refresh"})
	if !errors.Is(err, ErrPublicationIdentityUnavailable) {
		t.Fatalf("resolve identity error=%v, want ErrPublicationIdentityUnavailable", err)
	}
	if identity != (PostgresPublicationIdentity{}) {
		t.Fatalf("identity=%#v after rejected resolution, want zero", identity)
	}
}

// fakeRefreshTx is only used to prove resolver validation is fail-closed; no
// database method is reached because the resolver returns an invalid tuple.
type fakeRefreshTx struct {
	pgx.Tx
}

func (fakeRefreshTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected fake transaction exec")
}

func (fakeRefreshTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected fake transaction query")
}

func (fakeRefreshTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (fakeRefreshTx) Commit(context.Context) error {
	return errors.New("unexpected fake transaction commit")
}
func (fakeRefreshTx) Rollback(context.Context) error {
	return errors.New("unexpected fake transaction rollback")
}
