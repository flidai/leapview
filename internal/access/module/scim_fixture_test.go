package module

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
)

type fakeMetrics struct{}

type assemblyConfig struct {
	store           *platform.Store
	AccessRepo      access.Repository
	SCIMBearerToken string
	RateLimits      apihttpmiddleware.RateLimitConfig
}

type scimTestHarness struct{ handler http.Handler }

func (a *scimTestHarness) Routes() http.Handler { return a.handler }

type RateLimitConfig = apihttpmiddleware.RateLimitConfig

func testStore(t *testing.T) *platform.Store {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testStoreOptions(store *platform.Store, config assemblyConfig) assemblyConfig {
	config.store = store
	return config
}

func testAccessRepository(store *platform.Store) access.Repository {
	return accesssqlite.NewRepository(store.SQLDB())
}

func assembleSCIMTestHarness(_ fakeMetrics, config assemblyConfig) *scimTestHarness {
	persistence, err := NewSQLitePersistence(context.Background(), SQLitePersistenceConfig{Database: config.store.SQLDB()})
	if err != nil {
		panic(err)
	}
	module, err := Build(context.Background(), Config{
		Persistence: &persistence,
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
