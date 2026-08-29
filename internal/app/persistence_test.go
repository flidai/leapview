package app

import (
	"context"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	queryauditsqlite "github.com/flidai/leapview/internal/analytics/queryaudit/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func testStoreOptions(store *platform.Store, options assemblyConfig) assemblyConfig {
	options.Database = store.SQLDB()
	options.PlatformHealth = store
	options.AgentSettings = store
	options.AdminDatabase = store.SQLDB()
	if options.AccessRepo == nil {
		options.AccessRepo = accesssqlite.NewRepository(store.SQLDB())
	}
	if options.AccessModule == nil && options.Auth != nil {
		publicURL := options.PublicURL
		if publicURL == "" {
			publicURL = options.MCPOAuth.PublicURL
		}
		module, err := accessmodule.Build(context.Background(), accessmodule.Config{
			Database:     store.SQLDB(),
			ExistingAuth: options.Auth, PublicURL: publicURL,
			MCPIssuerURL: options.MCPOAuth.IssuerURL,
		})
		if err != nil {
			panic(err)
		}
		options.AccessModule = module
	}
	if options.ServingStateRepo == nil {
		states, err := servingstatemodule.Build(context.Background(), servingstatemodule.Config{Database: store.SQLDB()})
		if err != nil {
			panic(err)
		}
		options.ServingStateRepo = states
	}
	if options.RuntimeHost == nil {
		environment := servingstate.NormalizeEnvironment(servingstate.Environment(options.DefaultEnvironment))
		host, err := ensureTestRuntimeHost(context.Background(), store, options.ServingStateRepo.(*servingstatemodule.Module), testProjectID, environment)
		if err != nil {
			panic(err)
		}
		options.RuntimeHost = host
		options.ProjectID = testProjectID
		options.DefaultEnvironment = string(environment)
	}
	if options.RuntimeHost != nil {
		host := options.RuntimeHost
		activeIdentity := func(ctx context.Context) (projectgraph.ServingIdentity, error) {
			lease, err := host.Acquire(ctx)
			if err != nil {
				return projectgraph.ServingIdentity{}, err
			}
			defer lease.Release()
			identity := lease.Identity()
			if err := identity.Validate(); err != nil {
				return projectgraph.ServingIdentity{}, err
			}
			return identity, nil
		}
		if options.ProjectIDResolver == nil {
			options.ProjectIDResolver = func(ctx context.Context) (projectgraph.ResourceID, error) {
				identity, err := activeIdentity(ctx)
				if err != nil {
					return "", err
				}
				return identity.ProjectID, nil
			}
		}
		if options.ServingSnapshotResolver == nil {
			options.ServingSnapshotResolver = func(ctx context.Context) (string, error) {
				identity, err := activeIdentity(ctx)
				if err != nil {
					return "", err
				}
				return identity.GenerationID, nil
			}
		}
	}
	if options.ProjectGraph == nil {
		if graph, ok := options.ServingStateRepo.(interface {
			ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
		}); ok {
			options.ProjectGraph = graph
		}
	}
	if options.ProjectCatalog == nil && options.AccessModule != nil && options.RuntimeHost != nil {
		catalog, err := projectcatalog.NewService(
			projectCatalogLeaseProvider{provider: options.RuntimeHost.Provider()},
			projectCatalogSubjectResolver{resolve: options.AccessModule.AuthorizationSubjects},
		)
		if err != nil {
			panic(err)
		}
		options.ProjectCatalog = catalog
	}
	if options.QueryAudit == nil && (options.AnalyticsModule == nil || options.AnalyticsModule.QueryAuditReader() == nil) {
		options.QueryAudit = analyticsmodule.BuildQueryAuditSurface(queryauditsqlite.NewRepository(store.SQLDB()))
	}
	return options
}

func queryAuditRepositoryForTest(t *testing.T, server *appTestHarness) queryaudit.Repository {
	t.Helper()
	if server.runtime.queryAuditProvider == nil {
		t.Fatal("query audit provider is not configured")
	}
	reader, err := server.runtime.queryAuditProvider()
	if err != nil {
		t.Fatal(err)
	}
	repository, ok := reader.(queryaudit.Repository)
	if !ok || repository == nil {
		t.Fatal("query audit repository is not configured")
	}
	return repository
}
