package module

import (
	"context"
	"errors"
	"strings"
	"testing"

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
