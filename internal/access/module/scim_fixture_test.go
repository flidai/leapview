package module

import (
	"net/http"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeMetrics struct{}

type assemblyConfig struct {
	store           *accessTestStore
	AccessRepo      access.Repository
	SCIMBearerToken string
	RateLimits      apihttpmiddleware.RateLimitConfig
}

type scimTestHarness struct{ handler http.Handler }

func (a *scimTestHarness) Routes() http.Handler { return a.handler }

type RateLimitConfig = apihttpmiddleware.RateLimitConfig

type accessTestStore struct {
	pool       *pgxpool.Pool
	repository *accesspostgres.Repository
}

func testStore(t *testing.T) *accessTestStore {
	t.Helper()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	repository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("access-test-key", 3))})
	if err != nil {
		t.Fatal(err)
	}
	return &accessTestStore{pool: pool, repository: repository}
}

func testStoreOptions(store *accessTestStore, config assemblyConfig) assemblyConfig {
	config.store = store
	return config
}

func testAccessRepository(store *accessTestStore) access.Repository {
	return store.repository
}

func assembleSCIMTestHarness(_ fakeMetrics, config assemblyConfig) *scimTestHarness {
	repository := testAccessRepository(config.store)
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
	})
	if err != nil {
		panic(err)
	}
	handler, err := module.SCIMHandler(config.SCIMBearerToken)
	if err != nil {
		panic(err)
	}
	handler = http.StripPrefix("/scim", handler)
	if config.RateLimits.Enabled {
		handler = config.RateLimits.API()(handler)
	}
	return &scimTestHarness{handler: handler}
}
