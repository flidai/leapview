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
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
)

type fakeMetrics struct{}

type assemblyConfig struct {
	store           *platform.Store
	AccessRepo      access.Repository
	WorkspaceID     string
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
	if config.WorkspaceID != "" {
		if err := projectsqlite.NewRepository(config.store.SQLDB()).Ensure(context.Background(), workspace.EnsureInput{
			ID: workspace.WorkspaceID(config.WorkspaceID), Title: config.WorkspaceID,
		}); err != nil {
			panic(err)
		}
	}
	module, err := Build(context.Background(), Config{
		Database: config.store.SQLDB(),
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
