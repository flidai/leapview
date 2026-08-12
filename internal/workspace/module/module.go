package module

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/workspace"
	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	"github.com/flidai/leapview/internal/workspace/ui"
)

type Module struct {
	handler            workspacehttp.Handler
	search             searchService
	currentCredential  func(*http.Request) (access.APICredential, bool)
	readModel          workspace.ReadModel
	assetCatalog       workspace.AssetCatalogReader
	rootMetrics        queryruntime.Metrics
	runtimeEnvironment string
	defaultWorkspaceID string
	layout             func(*http.Request) webpage.Provider
	roleBindingUpsert  access.Privilege
	roleBindingDelete  access.Privilege
	grantUpsert        access.Privilege
	grantDelete        access.Privilege
}

type Principal struct {
	ID          string
	Email       string
	DisplayName string
	DevBypass   bool
}

type RefreshStateProvider interface {
	AssetRefreshState(context.Context, string, string, workspace.AssetView) (ui.AssetRefreshState, error)
}

type AssetRefreshInput struct {
	Request     *http.Request
	WorkspaceID string
	Asset       workspace.AssetView
	Assets      []workspace.AssetView
	Edges       []workspace.AssetEdgeView
}

type AssetRefreshRunner interface {
	RefreshAsset(context.Context, AssetRefreshInput) error
}

type AssetRefreshFunc func(context.Context, AssetRefreshInput) error

func (f AssetRefreshFunc) RefreshAsset(ctx context.Context, input AssetRefreshInput) error {
	return f(ctx, input)
}

type AccessCommandBindings = ui.AccessCommandBindings

type Config struct {
	Database            *sql.DB
	Directory           Directory
	ReadModel           ReadModel
	Securables          SecurableRegistrar
	WorkspaceID         func(string) string
	Environment         func(*http.Request) string
	AccessService       access.WorkspaceAccessService
	RoleBindingCommands access.RoleBindingOperations
	GrantCommands       access.GrantOperations
	AccessCommands      ui.AccessCommandBindings
	AssetCatalog        workspace.AssetCatalogReader
	MetricsForWorkspace func(string) (queryruntime.Metrics, bool)
	RootMetrics         queryruntime.Metrics
	CurrentPrincipal    func(*http.Request) (Principal, bool)
	AuthConfigured      bool
	RuntimeEnvironment  string
	DefaultWorkspaceID  string
	RefreshState        RefreshStateProvider
	RefreshRunner       AssetRefreshRunner
	Broker              *pagestream.Broker
	CSRFToken           func(*http.Request) string
	CurrentRoleLabel    func(*http.Request) string
	Layout              func(*http.Request) webpage.Provider
	CurrentCredential   func(*http.Request) (access.APICredential, bool)
	AuthorizeObject     func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error)
}

func Build(_ context.Context, config Config) (*Module, error) {
	roleBindingUpsert, roleBindingDelete, err := roleBindingRoutePrivileges(config.RoleBindingCommands)
	if err != nil {
		return nil, err
	}
	grantUpsert, grantDelete, err := grantRoutePrivileges(config.GrantCommands)
	if err != nil {
		return nil, err
	}
	directoryPort := config.Directory
	if directoryPort == nil && config.Database != nil {
		var err error
		directoryPort, err = BuildDirectory(config.Database, config.Securables)
		if err != nil {
			return nil, err
		}
	}
	var repository workspace.Repository
	if owned, ok := directoryPort.(*directory); ok {
		repository = owned.repository
	}
	readModel := config.ReadModel
	if readModel == nil {
		readModel = repository
	}
	m := &Module{
		readModel: readModel, currentCredential: config.CurrentCredential,
		rootMetrics: config.RootMetrics, runtimeEnvironment: config.RuntimeEnvironment,
		defaultWorkspaceID: config.DefaultWorkspaceID, layout: config.Layout,
		roleBindingUpsert: roleBindingUpsert, roleBindingDelete: roleBindingDelete,
		grantUpsert: grantUpsert, grantDelete: grantDelete,
	}
	m.assetCatalog = config.AssetCatalog
	if m.assetCatalog == nil && readModel != nil {
		m.assetCatalog = workspace.NewAssetCatalogService(readModel)
	}
	currentPrincipal := func(r *http.Request) (workspacehttp.Principal, bool) {
		if config.CurrentPrincipal == nil {
			return workspacehttp.Principal{}, false
		}
		principal, ok := config.CurrentPrincipal(r)
		return workspacehttp.Principal{
			ID: principal.ID, Email: principal.Email,
			DisplayName: principal.DisplayName, DevBypass: principal.DevBypass,
		}, ok
	}
	httpReadModel := workspacehttp.ReadModel{
		WorkspaceRepository: func() (workspace.ReadModel, error) { return readModel, nil },
		AccessService:       func() (access.WorkspaceAccessService, error) { return config.AccessService, nil },
		AssetCatalogReader:  m.AssetCatalogReader,
		MetricsForWorkspace: func(workspaceID string) (workspacehttp.Metrics, bool) {
			if config.MetricsForWorkspace == nil {
				return nil, false
			}
			metrics, ok := config.MetricsForWorkspace(workspaceID)
			if !ok || metrics == nil {
				return nil, ok
			}
			return MetricsAdapter{Metrics: metrics}, true
		},
		CatalogForWorkspace: func(workspaceID string) catalog.Catalog {
			if config.MetricsForWorkspace != nil {
				if metrics, ok := config.MetricsForWorkspace(workspaceID); ok && metrics != nil {
					return navigationCatalog(metrics.Catalog())
				}
			}
			if config.RootMetrics == nil {
				return catalog.Catalog{Workspace: catalog.Workspace{ID: workspaceID}}
			}
			return navigationCatalog(config.RootMetrics.Catalog())
		},
		RootCatalog: func() catalog.Catalog {
			if config.RootMetrics == nil {
				return catalog.Catalog{}
			}
			return navigationCatalog(config.RootMetrics.Catalog())
		},
		Environment:      config.Environment,
		CurrentPrincipal: currentPrincipal,
		AuthConfigured:   config.AuthConfigured,
	}
	var refreshRunner workspacehttp.AssetRefreshRunner
	if config.RefreshRunner != nil {
		refreshRunner = moduleRefreshRunner{upstream: config.RefreshRunner}
	}
	m.handler = workspacehttp.Handler{
		WorkspaceID: config.WorkspaceID, Environment: config.Environment, ReadModel: httpReadModel,
		RefreshState:  moduleRefreshState{module: m, upstream: config.RefreshState},
		RefreshRunner: refreshRunner, Broker: config.Broker,
		CSRFToken: config.CSRFToken, CurrentRoleLabel: config.CurrentRoleLabel, Layout: config.Layout,
		RoleBindingCommands: config.RoleBindingCommands,
		GrantCommands:       config.GrantCommands,
		AccessCommands:      config.AccessCommands,
	}
	m.search = buildSearch(config.Database, config.AuthorizeObject)
	return m, nil
}

func grantRoutePrivileges(operations access.GrantOperations) (access.Privilege, access.Privilege, error) {
	if operations == nil {
		return "", "", nil
	}
	descriptors := make(map[access.OperationID]access.OperationDescriptor, 2)
	for _, operationID := range []access.OperationID{access.OperationCreateGrant, access.OperationDeleteGrant} {
		descriptor, ok := operations.DescribeOperation(operationID)
		if !ok {
			return "", "", fmt.Errorf("workspace UI operation %q is not configured", operationID)
		}
		if !descriptor.Exposes(access.OperationSurfaceUI) || descriptor.Target.Type != access.SecurableWorkspace {
			return "", "", fmt.Errorf("workspace UI operation %q has an incompatible generated contract", operationID)
		}
		descriptors[operationID] = descriptor
	}
	return descriptors[access.OperationCreateGrant].Privilege, descriptors[access.OperationDeleteGrant].Privilege, nil
}

func roleBindingRoutePrivileges(operations access.RoleBindingOperations) (access.Privilege, access.Privilege, error) {
	if operations == nil {
		return "", "", nil
	}
	descriptors := make(map[access.OperationID]access.OperationDescriptor, 3)
	for _, operationID := range []access.OperationID{
		access.OperationCreateRoleBinding,
		access.OperationUpdateRoleBinding,
		access.OperationDeleteRoleBinding,
	} {
		descriptor, ok := operations.DescribeOperation(operationID)
		if !ok {
			return "", "", fmt.Errorf("workspace UI operation %q is not configured", operationID)
		}
		if !descriptor.Exposes(access.OperationSurfaceUI) || descriptor.Target.Type != access.SecurableWorkspace {
			return "", "", fmt.Errorf("workspace UI operation %q has an incompatible generated contract", operationID)
		}
		descriptors[operationID] = descriptor
	}
	create := descriptors[access.OperationCreateRoleBinding]
	update := descriptors[access.OperationUpdateRoleBinding]
	if create.Privilege != update.Privilege {
		return "", "", fmt.Errorf("workspace role binding upsert operations require different privileges")
	}
	return create.Privilege, descriptors[access.OperationDeleteRoleBinding].Privilege, nil
}

func navigationCatalog(source dashboardcatalog.Catalog) catalog.Catalog {
	result := catalog.Catalog{
		Workspace: catalog.Workspace{
			ID: source.Workspace.ID, Title: source.Workspace.Title, Description: source.Workspace.Description,
		},
		Models:     make([]catalog.Model, 0, len(source.Models)),
		Dashboards: make([]catalog.Dashboard, 0, len(source.Dashboards)),
	}
	for _, model := range source.Models {
		result.Models = append(result.Models, catalog.Model{ID: model.ID, Title: model.Title, Description: model.Description})
	}
	for _, dashboard := range source.Dashboards {
		result.Dashboards = append(result.Dashboards, catalog.Dashboard{
			ID: dashboard.ID, Title: dashboard.Title, Description: dashboard.Description,
			SemanticModel: dashboard.SemanticModel, Tags: append([]string(nil), dashboard.Tags...), PageCount: dashboard.PageCount,
		})
	}
	return result
}

func (m *Module) HTTP() workspacehttp.Handler { return m.handler }

func (m *Module) CatalogsForVisibleWorkspaces(r *http.Request) []catalog.Catalog {
	return m.handler.ReadModel.CatalogsForVisibleWorkspaces(r)
}

func (m *Module) NavigationCatalog() catalog.Catalog {
	if m == nil || m.rootMetrics == nil {
		return catalog.Catalog{}
	}
	return navigationCatalog(m.rootMetrics.Catalog())
}

func (m *Module) WorkspaceAssetsAndEdgesForData(ctx context.Context, workspaceID, environment string) ([]workspace.AssetView, []workspace.AssetEdgeView, error) {
	return m.handler.ReadModel.WorkspaceAssetsAndEdgesForData(ctx, workspaceID, environment)
}

func (m *Module) WorkspaceResponse(r *http.Request, workspaceID string) workspace.WorkspaceView {
	return m.handler.ReadModel.WorkspaceResponse(r, workspaceID)
}

func (m *Module) WorkspaceViewContext(ctx context.Context, workspaceID string) workspace.WorkspaceView {
	return m.handler.ReadModel.WorkspaceViewContext(ctx, workspaceID)
}

func (m *Module) Home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	var providers []webpage.Provider
	if m.layout != nil {
		providers = []webpage.Provider{m.layout(r)}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	csrfToken := ""
	if m.handler.CSRFToken != nil {
		csrfToken = m.handler.CSRFToken(r)
	}
	if err := ui.CatalogPageForCatalogsQueryWithCSRF(m.CatalogsForVisibleWorkspaces(r), strings.TrimSpace(r.URL.Query().Get("q")), csrfToken, providers...).Render(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (m *Module) CatalogBootstrapSignals(r *http.Request, provider webpage.Provider) (map[string]any, error) {
	var signals struct {
		Query  *string `json:"entityListQuery"`
		Filter *string `json:"entityListFilter"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	return ui.CatalogBootstrapSignalsForCatalogsQuery(m.CatalogsForVisibleWorkspaces(r), query, provider), nil
}

func (m *Module) CatalogSearch(w http.ResponseWriter, r *http.Request) {
	var signals struct {
		Query *string `json:"entityListQuery"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := ""
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	_ = pagestream.PatchResponse(w, r, ui.CatalogListPatchForCatalogsQuery(m.CatalogsForVisibleWorkspaces(r), query))
}
