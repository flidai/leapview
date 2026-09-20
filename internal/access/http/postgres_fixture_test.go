package http

import (
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type accessHTTPTestStore struct {
	pool       *pgxpool.Pool
	repository *accesspostgres.Repository
}

func openAccessHTTPTestStore(t *testing.T) *accessHTTPTestStore {
	t.Helper()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	repository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("access-http-test-key", 2))})
	if err != nil {
		t.Fatal(err)
	}
	return &accessHTTPTestStore{pool: pool, repository: repository}
}
