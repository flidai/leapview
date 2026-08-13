package module

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
)

type Module struct {
	handler              workspacehttp.Handler
	search               searchService
	currentCredential    func(*http.Request) (access.APICredential, bool)
	readModel            workspace.ReadModel
	assetCatalog         workspace.AssetCatalogReader
	rootMetrics          queryruntime.Metrics
	runtimeEnvironment   string
	defaultWorkspaceID   string
	layout               func(*http.Request) webpage.Provider
	roleBindingUpsert    access.Privilege
	roleBindingDelete    access.Privilege
	grantUpsert          access.Privilege
	grantDelete          access.Privilege
	dashboardPopularity  DashboardPopularityProvider
	dashboardRefreshedAt DashboardLastRefreshedProvider
	environment          func(*http.Request) string
	logger               *slog.Logger
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
type PopularityLevel = uisignals.PopularityLevel
type DashboardPopularityProvider func(context.Context, int) (map[string]PopularityLevel, error)
type DashboardLastRefreshedProvider func(context.Context, string, string, string) (string, bool, error)

type Config struct {
	Database             *sql.DB
	Directory            Directory
	ReadModel            ReadModel
	Securables           SecurableRegistrar
	WorkspaceID          func(string) string
	Environment          func(*http.Request) string
	AccessService        access.WorkspaceAccessService
	RoleBindingCommands  access.RoleBindingOperations
	GrantCommands        access.GrantOperations
	CommandPrivileges    access.WorkspaceCommandPrivileges
	AccessCommands       ui.AccessCommandBindings
	AssetCatalog         workspace.AssetCatalogReader
	MetricsForWorkspace  func(string) (queryruntime.Metrics, bool)
	RootMetrics          queryruntime.Metrics
	CurrentPrincipal     func(*http.Request) (Principal, bool)
	AuthConfigured       bool
	RuntimeEnvironment   string
	DefaultWorkspaceID   string
	RefreshState         RefreshStateProvider
	RefreshRunner        AssetRefreshRunner
	Broker               *pagestream.Broker
	CSRFToken            func(*http.Request) string
	CurrentRoleLabel     func(*http.Request) string
	Layout               func(*http.Request) webpage.Provider
	CurrentCredential    func(*http.Request) (access.APICredential, bool)
	AuthorizeObject      func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error)
	DashboardPopularity  DashboardPopularityProvider
	DashboardRefreshedAt DashboardLastRefreshedProvider
	Logger               *slog.Logger
}

func Build(_ context.Context, config Config) (*Module, error) {
	roleBindingUpsert, roleBindingDelete, err := roleBindingRoutePrivileges(config.RoleBindingCommands, config.CommandPrivileges)
	if err != nil {
		return nil, err
	}
	grantUpsert, grantDelete, err := grantRoutePrivileges(config.GrantCommands, config.CommandPrivileges)
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
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &Module{
		readModel: readModel, currentCredential: config.CurrentCredential,
		rootMetrics: config.RootMetrics, runtimeEnvironment: config.RuntimeEnvironment,
		defaultWorkspaceID: config.DefaultWorkspaceID, layout: config.Layout,
		roleBindingUpsert: roleBindingUpsert, roleBindingDelete: roleBindingDelete,
		grantUpsert: grantUpsert, grantDelete: grantDelete,
		dashboardPopularity: config.DashboardPopularity, dashboardRefreshedAt: config.DashboardRefreshedAt,
		environment: config.Environment, logger: logger,
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

func grantRoutePrivileges(operations access.GrantOperations, privileges access.WorkspaceCommandPrivileges) (access.Privilege, access.Privilege, error) {
	if operations == nil {
		return "", "", nil
	}
	if privileges.GrantUpsert == "" || privileges.GrantDelete == "" {
		return "", "", fmt.Errorf("workspace grant command privileges are required")
	}
	return privileges.GrantUpsert, privileges.GrantDelete, nil
}

func roleBindingRoutePrivileges(operations access.RoleBindingOperations, privileges access.WorkspaceCommandPrivileges) (access.Privilege, access.Privilege, error) {
	if operations == nil {
		return "", "", nil
	}
	if privileges.RoleBindingUpsert == "" || privileges.RoleBindingDelete == "" {
		return "", "", fmt.Errorf("workspace role binding command privileges are required")
	}
	return privileges.RoleBindingUpsert, privileges.RoleBindingDelete, nil
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

func (m *Module) catalogPopularity(ctx context.Context, visible []catalog.Catalog) map[string]uisignals.PopularityLevel {
	if m == nil || m.dashboardPopularity == nil {
		return nil
	}
	popularity, err := m.dashboardPopularity(ctx, m.dashboardCount(ctx, visible))
	if err != nil {
		m.logger.WarnContext(ctx, "dashboard popularity unavailable", "error", err)
		return nil
	}
	return popularity
}

func (m *Module) catalogDashboardMetadata(r *http.Request, visible []catalog.Catalog) map[string]ui.CatalogDashboardMetadata {
	metadata := make(map[string]ui.CatalogDashboardMetadata)
	for dashboardID, level := range m.catalogPopularity(r.Context(), visible) {
		metadata[dashboardID] = ui.CatalogDashboardMetadata{Popularity: level}
	}
	if m == nil || m.dashboardRefreshedAt == nil {
		return metadata
	}
	environment := m.runtimeEnvironment
	if m.environment != nil {
		environment = m.environment(r)
	}
	type refreshResult struct {
		refreshedAt string
		ok          bool
		err         error
	}
	refreshes := make(map[string]refreshResult)
	for _, workspaceCatalog := range visible {
		for _, dashboard := range workspaceCatalog.Dashboards {
			if strings.TrimSpace(dashboard.SemanticModel) == "" {
				continue
			}
			modelKey := workspaceCatalog.Workspace.ID + "\x00" + dashboard.SemanticModel
			result, found := refreshes[modelKey]
			if !found {
				result.refreshedAt, result.ok, result.err = m.dashboardRefreshedAt(r.Context(), workspaceCatalog.Workspace.ID, environment, dashboard.SemanticModel)
				refreshes[modelKey] = result
			}
			if result.err != nil {
				if !found {
					m.logger.WarnContext(r.Context(), "dashboard refresh timestamp unavailable", "workspace", workspaceCatalog.Workspace.ID, "semantic_model", dashboard.SemanticModel, "error", result.err)
				}
				continue
			}
			if !result.ok || strings.TrimSpace(result.refreshedAt) == "" {
				continue
			}
			dashboardID := workspaceCatalog.Workspace.ID + "." + dashboard.ID
			value := metadata[dashboardID]
			value.LastRefreshedAt = result.refreshedAt
			metadata[dashboardID] = value
		}
	}
	return metadata
}

func (m *Module) dashboardCount(ctx context.Context, visible []catalog.Catalog) int {
	ids := map[string]bool{}
	add := func(catalogs []catalog.Catalog) {
		for _, workspaceCatalog := range catalogs {
			for _, dashboard := range workspaceCatalog.Dashboards {
				ids[workspaceCatalog.Workspace.ID+"."+dashboard.ID] = true
			}
		}
	}
	if m.readModel != nil && m.handler.ReadModel.MetricsForWorkspace != nil {
		if workspaces, err := m.readModel.List(ctx); err == nil {
			for _, workspace := range workspaces {
				if metrics, ok := m.handler.ReadModel.MetricsForWorkspace(string(workspace.ID)); ok && metrics != nil {
					add([]catalog.Catalog{metrics.Catalog()})
				}
			}
		}
	}
	if len(ids) == 0 {
		add(visible)
	}
	return len(ids)
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
	catalogs := m.CatalogsForVisibleWorkspaces(r)
	if err := ui.CatalogPageForCatalogsWithOptions(catalogs, ui.CatalogListOptions{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Metadata: m.catalogDashboardMetadata(r, catalogs),
	}, csrfToken, providers...).Render(w); err != nil {
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
	filter := "all"
	if signals.Filter != nil {
		filter = strings.TrimSpace(*signals.Filter)
	}
	catalogs := m.CatalogsForVisibleWorkspaces(r)
	return ui.CatalogBootstrapSignalsForCatalogsWithOptions(catalogs, ui.CatalogListOptions{
		Query: query, WorkspaceFilter: filter, Metadata: m.catalogDashboardMetadata(r, catalogs),
	}, provider), nil
}

func (m *Module) CatalogSearch(w http.ResponseWriter, r *http.Request) {
	var signals struct {
		Query  *string `json:"entityListQuery"`
		Filter *string `json:"entityListFilter"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := ""
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	filter := "all"
	if signals.Filter != nil {
		filter = strings.TrimSpace(*signals.Filter)
	}
	catalogs := m.CatalogsForVisibleWorkspaces(r)
	_ = pagestream.PatchResponse(w, r, ui.CatalogListPatchForCatalogs(catalogs, ui.CatalogListOptions{
		Query: query, WorkspaceFilter: filter, Metadata: m.catalogDashboardMetadata(r, catalogs),
	}))
}
