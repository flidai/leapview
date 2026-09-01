package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
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
	if _, err := NewPostgresTerminalRecovery(otherRefresh, queue); err == nil || !strings.Contains(err.Error(), "does not match refresh repository") {
		t.Fatalf("mismatched queue recovery error = %v, want provenance rejection", err)
	}
	var nilQueue *PostgresJobsAdapter
	if _, err := NewPostgresTerminalRecovery(refresh, nilQueue); err == nil || !strings.Contains(err.Error(), "jobs recovery authority is required") {
		t.Fatalf("nil queue recovery error = %v, want nil-authority rejection", err)
	}
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
type fakeRefreshTx struct{}

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
