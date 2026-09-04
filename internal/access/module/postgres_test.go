package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type postgresBuildDBStub struct{}

func (*postgresBuildDBStub) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (*postgresBuildDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected query")
}

func (*postgresBuildDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (*postgresBuildDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func TestBuildPostgresOwnsConcreteAccessAdapters(t *testing.T) {
	database := &postgresBuildDBStub{}
	module, err := BuildPostgres(t.Context(), Config{
		Production: true,
		Auth:       AuthConfig{Disabled: true},
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project_internal", nil
		},
	}, PostgresBuildConfig{
		Database: database, FingerprintKey: []byte(strings.Repeat("f", 32)),
	})
	if err != nil {
		t.Fatalf("BuildPostgres() error = %v", err)
	}
	repository, ok := module.repositoryValue().(*accesspostgres.Repository)
	if !ok {
		t.Fatalf("repository = %T, want PostgreSQL repository", module.repositoryValue())
	}
	if repository.DB() != database {
		t.Fatal("PostgreSQL access repository did not retain the supplied shared handle")
	}
}

func TestBuildPostgresRejectsAmbiguousConstruction(t *testing.T) {
	if _, err := BuildPostgres(t.Context(), Config{}, PostgresBuildConfig{}); err == nil || !strings.Contains(err.Error(), "production mode") {
		t.Fatalf("non-production error = %v", err)
	}
	if _, err := BuildPostgres(t.Context(), Config{Production: true, ExistingAuth: &Auth{}}, PostgresBuildConfig{}); err == nil || !strings.Contains(err.Error(), "preconfigured persistence") {
		t.Fatalf("preconfigured authority error = %v", err)
	}
}
