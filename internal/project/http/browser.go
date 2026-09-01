// Package browser owns the authenticated, server-bound project browser
// surfaces. It deliberately accepts no project selector from the request:
// the active project and immutable serving generation come from composition.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	connectionadmin "github.com/flidai/leapview/internal/analytics/connectionadmin"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardauthoringcatalog "github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/publication"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
	g "maragu.dev/gomponents"
)

type GraphReader interface {
	ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
}

// AssetVersionsReader reads historical published configuration versions for
// one logical project asset. It is optional so deployments without serving
// history persistence can still render the current content hash.
type AssetVersionsReader interface {
	AssetVersions(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID) ([]servingstate.AssetVersion, error)
}

// AssetRefreshStateReader adapts the refresh capability's presentation state
// without coupling refresh persistence to project UI rendering.
type AssetRefreshStateReader interface {
	AssetRefreshState(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID, projectgraph.ResourceID) (refreshpresentation.AssetRefreshState, error)
	ModelRefreshState(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID) (refreshpresentation.AssetRefreshState, error)
	SemanticModelRefreshState(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID) (refreshpresentation.AssetRefreshState, error)
}

// ModelPhysicalMetadata is the credential-free DuckLake table rollup shown on
// a model's catalog detail page.
type ModelPhysicalMetadata struct {
	RowCount, ColumnCount, FileCount, SizeBytes, SnapshotID int64
	SnapshotAt                                              time.Time
	Schema                                                  semanticmodel.TableSchema
}

// PhysicalCatalogReader reads statistics from the exact active serving
// runtime, rather than from the node's mutable authoring catalog.
type PhysicalCatalogReader interface {
	ModelPhysicalMetadata(context.Context, projectgraph.ResourceID, string) (map[string]ModelPhysicalMetadata, error)
}

type SourceSchemaObservation = projectview.SourceSchemaObservationReadModel

// SourceSchemaReader reads the persisted, non-secret schema evidence captured
// for one source in the exact serving generation shown by the project graph.
// It must not introspect a live source on behalf of an HTTP request.
type SourceSchemaReader interface {
	SourceSchemaObservation(context.Context, projectgraph.ResourceID, string, string, projectgraph.ResourceID) (SourceSchemaObservation, bool, error)
}

type CatalogAuthorizer interface {
	List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error)
	Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error)
}

type ProductSearchCatalog interface {
	Search(context.Context, projectcatalog.SearchRequest) (projectcatalog.Page, error)
}

var productSearchKinds = []projectgraph.Kind{
	projectgraph.KindDashboard,
	projectgraph.KindModel,
	projectgraph.KindSource,
	projectgraph.KindConnection,
	projectgraph.KindSemanticModel,
	projectgraph.KindPipeline,
}

// ProjectDefinitionReader resolves one coherent complete definition snapshot
// from the exact active serving generation. The returned compiled semantic
// models are retained by that same generation; callers must not compile the
// manifest models or reacquire a separate serving lease for details.
type ProjectDefinitionReader interface {
	ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error)
}

type DashboardAppearanceStore interface {
	ListProject(context.Context, projectgraph.ResourceID) (map[projectgraph.ResourceID]dashboardappearance.Record, error)
	ApplyPatch(context.Context, dashboardappearance.Key, string, dashboardappearance.Patch) (dashboardappearance.Record, error)
}

type DashboardCatalogReader interface {
	List(context.Context, dashboardauthoringcatalog.ListRequest) (dashboardauthoringcatalog.ListResult, error)
}

// ErrSemanticModelUnavailable indicates that the active generation could not
// provide the compiled definition required to render semantic-model detail.
// A graph metadata payload is not a valid substitute because it would render
// misleading zero-valued tables, metrics, and relationships.
var ErrSemanticModelUnavailable = errors.New("active semantic model definition is unavailable")

// ErrProjectDefinitionUnavailable indicates that the selected graph resource
// cannot be paired with its typed definition from the active generation.
// Rendering graph metadata as a complete resource would produce misleading
// empty fields, sources, schedules, and configuration values.
var ErrProjectDefinitionUnavailable = errors.New("active project definition is unavailable")

type Principal struct {
	ID        string
	DevBypass bool
}

// ConnectionCommandBindings is the project HTTP composition contract for the
// connection administration actions rendered by the project UI.
type ConnectionCommandBindings = projectui.ConnectionCommandBindings

type CreatorCommandInvocation struct {
	Action         string
	Project        string
	Resource       string
	IdempotencyKey string
	RequestID      string
	CorrelationID  string
	Revision       int64
}

type BrowserHandler struct {
	Graph                    GraphReader
	AssetVersions            AssetVersionsReader
	RefreshState             AssetRefreshStateReader
	PhysicalCatalog          PhysicalCatalogReader
	SourceSchemas            SourceSchemaReader
	ProjectDefinitionReader  ProjectDefinitionReader
	DashboardAppearances     DashboardAppearanceStore
	DashboardCatalog         DashboardCatalogReader
	QueryExecutor            DataQueryExecutor
	Catalog                  CatalogAuthorizer
	SearchCatalog            ProductSearchCatalog
	ResolveProjectID         func(context.Context) (projectgraph.ResourceID, error)
	Environment              string
	TargetID                 string
	ConnectionAdministration connectionadmin.Administration
	ConnectionCommands       projectui.ConnectionCommandBindings
	PipelineRunCommand       uicommand.Binding
	PipelineCancelCommand    uicommand.Binding
	RunPipeline              func(context.Context, string, string, string) error
	// CancelPipeline receives both the pipeline and run identifiers from the
	// command. Implementations must verify that the run belongs to that
	// pipeline before mutating it; keeping the pipeline ID in this callback
	// prevents an opaque run ID from becoming a cross-pipeline capability.
	CancelPipeline    func(context.Context, string, string, string) error
	AuthorizePipeline func(*stdhttp.Request, string, access.Capability) (bool, error)
	// AuthorizeConnectionCreate checks the project-root capability required by
	// the generated createTargetConnectionBinding command. Updates remain
	// resource-scoped in the administration service.
	AuthorizeConnectionCreate func(*stdhttp.Request, projectgraph.ResourceID, access.Capability) (bool, error)
	// AuthorizeCreateDashboard evaluates the project-root edit capability used
	// to expose the browser's new-draft affordance. The catalog remains usable
	// for read-only principals when this decision is denied.
	AuthorizeCreateDashboard func(*stdhttp.Request, projectgraph.ResourceID, access.Capability) (bool, error)
	AuthorizeDashboard       func(*stdhttp.Request, string, access.Capability) (bool, error)
	AuthorizeConnection      func(*stdhttp.Request, string, access.Capability) (bool, error)
	BeginConnectionCommand   func(context.Context, CreatorCommandInvocation) (context.Context, error)
	BeginPipelineCommand     func(context.Context, CreatorCommandInvocation) (context.Context, error)
	MutationMiddleware       func(stdhttp.Handler) stdhttp.Handler
	Layout                   func(*stdhttp.Request) webpage.Provider
	CSRFToken                func(*stdhttp.Request) string
	CurrentUser              func(*stdhttp.Request) (Principal, bool)
	Authenticate             func(stdhttp.Handler) stdhttp.Handler
}

// MountAuthenticated mounts only canonical browser paths. Legacy tenant
// paths are intentionally absent; requests to them remain ordinary 404s.
func (h *BrowserHandler) MountAuthenticated(r chi.Router) {
	if h == nil || r == nil {
		return
	}
	wrap := func(next stdhttp.HandlerFunc) stdhttp.HandlerFunc {
		if h.Authenticate == nil {
			return next
		}
		return h.Authenticate(stdhttp.HandlerFunc(next)).ServeHTTP
	}
	wrapMutation := func(next stdhttp.HandlerFunc) stdhttp.HandlerFunc {
		var handler stdhttp.Handler = next
		if h.MutationMiddleware == nil {
			handler = stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				stdhttp.Error(w, "browser mutation idempotency is unavailable", stdhttp.StatusServiceUnavailable)
			})
		} else {
			handler = h.MutationMiddleware(handler)
		}
		if h.Authenticate != nil {
			handler = h.Authenticate(handler)
		}
		return handler.ServeHTTP
	}
	r.Get("/", wrap(h.Insights))
	r.Get("/search", wrap(h.ProductSearch))
	r.Get("/explore", wrap(h.Explore))
	r.Post("/explore/command", wrap(h.DataExplorerCommand))
	r.Get("/sources", wrap(h.Sources))
	r.Get("/sources/{asset}/{section}", wrap(h.SourceAsset))
	r.Get("/models", wrap(h.Models))
	r.Get("/models/{asset}/{section}", wrap(h.ModelAsset))
	r.Post("/models/{asset}/data/command", wrap(h.ModelDataExplorerCommand))
	r.Get("/semantic-models", wrap(h.SemanticModels))
	r.Get("/semantic-models/{asset}/{section}", wrap(h.SemanticModelAsset))
	r.Post("/semantic-models/{asset}/data/command", wrap(h.SemanticModelDataExplorerCommand))
	r.Get("/dashboards", wrap(h.Dashboards))
	// Keep dashboard builder/runtime routes such as /edit and /preview owned by
	// the dashboard module; Develop owns only its catalog detail sections.
	r.Get("/dashboards/{asset}/details", wrap(h.DashboardAsset))
	r.Get("/dashboards/{asset}/definition", wrap(h.DashboardAsset))
	r.Get("/dashboards/{asset}/versions", wrap(h.DashboardAsset))
	r.Get("/dashboards/{asset}/lineage", wrap(h.DashboardAsset))
	r.Post("/dashboards/{asset}/appearance", wrapMutation(h.DashboardAppearanceCommand))
	r.Get("/pipelines", wrap(h.Pipelines))
	r.Get("/pipelines/{asset}/{section}", wrap(h.PipelineAsset))
	r.Post("/pipelines/command", wrapMutation(h.PipelineCommand))
	r.Get("/connections", wrap(h.Connections))
	r.Get("/connections/{asset}/{section}", wrap(h.ConnectionAsset))
	r.Post("/connections/administration/configuration", wrapMutation(h.ConnectionAdministrationConfigurationCommand))
	r.Post("/connections/administration/lifecycle", wrapMutation(h.ConnectionAdministrationLifecycleCommand))
	r.Get("/catalog/search", wrap(h.CatalogSearch))
	r.Get("/sources/search", wrap(h.SourcesSearch))
	r.Get("/connections/search", wrap(h.ConnectionsSearch))
	r.Get("/models/search", wrap(h.ModelsSearch))
	r.Get("/semantic-models/search", wrap(h.SemanticModelsSearch))
	r.Get("/dashboards/search", wrap(h.DashboardsSearch))
}

func (h *BrowserHandler) ProductSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, "authentication is required", stdhttp.StatusUnauthorized)
		return
	}
	if h.SearchCatalog == nil {
		stdhttp.Error(w, "search is temporarily unavailable", stdhttp.StatusServiceUnavailable)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 24
	page, err := h.SearchCatalog.Search(r.Context(), projectcatalog.SearchRequest{
		PrincipalID: principal.ID, DevAuthBypass: principal.DevBypass, Query: query,
		Kinds: append([]projectgraph.Kind(nil), productSearchKinds...), Limit: limit,
	})
	if err != nil {
		status := stdhttp.StatusServiceUnavailable
		if errors.Is(err, projectcatalog.ErrInvalidRequest) || errors.Is(err, projectcatalog.ErrInvalidCursor) {
			status = stdhttp.StatusBadRequest
		}
		stdhttp.Error(w, stdhttp.StatusText(status), status)
		return
	}
	items := make([]productSearchResult, 0, len(page.Items))
	for _, item := range page.Items {
		if result, ok := productSearchResultFor(item); ok {
			items = append(items, result)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Items []productSearchResult `json:"items"`
	}{Items: items})
}

type productSearchResult struct {
	Reference   projectcatalog.Ref `json:"reference"`
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName,omitempty"`
	Description string             `json:"description,omitempty"`
	Href        string             `json:"href"`
}

func productSearchResultFor(item projectcatalog.Result) (productSearchResult, bool) {
	href := productSearchHref(item)
	if href == "" {
		return productSearchResult{}, false
	}
	return productSearchResult{
		Reference: item.Ref, Name: item.Name, DisplayName: item.DisplayName, Description: item.Description,
		Href: href,
	}, true
}

func productSearchHref(item projectcatalog.Result) string {
	id := item.Ref.ID.String()
	switch item.Ref.Kind {
	case projectgraph.KindConnection:
		return assetnav.ConnectionAssetSectionHref(id, "details")
	case projectgraph.KindSource:
		return assetnav.ProjectAssetSectionHref(id, "details")
	case projectgraph.KindModel:
		return "/models/" + url.PathEscape(id) + "/details"
	case projectgraph.KindSemanticModel:
		return "/semantic-models/" + url.PathEscape(id) + "/details"
	case projectgraph.KindPipeline:
		return "/pipelines/" + url.PathEscape(id) + "/details"
	case projectgraph.KindDashboard:
		return "/dashboards/" + url.PathEscape(id)
	default:
		return ""
	}
}

func (h *BrowserHandler) Insights(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindDashboard, projectgraph.KindModel, projectgraph.KindSemanticModel}) {
		return
	}
	catalog, options, err := h.dashboardCatalogPage(r, r.URL.Query().Get("q"))
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	canCreateDraft := h.dashboardCreationAllowed(r)
	options.CanCreateDraft = canCreateDraft
	writeDocument(w, projectui.CatalogPageForCatalogsWithOptions([]projectnavigation.Catalog{catalog}, options, h.csrf(r), h.layout(r)))
}

func (h *BrowserHandler) dashboardCreationAllowed(r *stdhttp.Request) bool {
	if h == nil || r == nil {
		return false
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false
	}
	if principal.DevBypass {
		return true
	}
	if h.AuthorizeCreateDashboard == nil {
		return false
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return false
	}
	allowed, err := h.AuthorizeCreateDashboard(r, projectID, access.CapabilityResourceEdit)
	return err == nil && allowed
}

func (h *BrowserHandler) CatalogSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindDashboard, projectgraph.KindModel, projectgraph.KindSemanticModel}) {
		return
	}
	var signals struct {
		Query string `json:"entityListQuery"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "catalog search signals are required", stdhttp.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(signals.Query)
	catalog, options, err := h.dashboardCatalogPage(r, query)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	patch := projectui.CatalogListPatchForCatalogs([]projectnavigation.Catalog{catalog}, options)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) Explore(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	catalog := h.navigationCatalog(r)
	page, explorer, ok := h.dataExplorerSignals(w, r)
	if !ok {
		return
	}
	writeDocument(w, projectui.DataExplorerPage(catalog, page, explorer, h.csrf(r), h.layout(r)))
}

func (h *BrowserHandler) DataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	var signals struct {
		Command projectsignals.DataExplorerCommand `json:"dataExplorerCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "data explorer command payload is required", stdhttp.StatusBadRequest)
		return
	}
	page, explorer, ok := h.dataExplorerSignalsForCommand(w, r, signals.Command)
	if !ok {
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
		"page": page, "dataExplorer": explorer, "dataExplorerCommand": explorer.Command,
	})
}

func (h *BrowserHandler) ModelDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDataExplorerCommand(w, r, string(projectview.AssetTypeModelTable))
}

func (h *BrowserHandler) SemanticModelDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDataExplorerCommand(w, r, string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) assetDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, expectedType string) {
	kind, ok := catalogKindForAssetType(expectedType)
	if !ok || !h.authorizeAny(w, r, []projectgraph.Kind{kind}) {
		return
	}
	var signals struct {
		Command projectsignals.DataExplorerCommand `json:"dataExplorerCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "data explorer command payload is required", stdhttp.StatusBadRequest)
		return
	}
	_, explorer, asset, ok := h.dataExplorerSignalsForAssetCommand(w, r, chi.URLParam(r, "asset"), signals.Command)
	if !ok {
		return
	}
	if asset.Type != expectedType {
		stdhttp.NotFound(w, r)
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
		"dataExplorer": explorer, "dataExplorerCommand": explorer.Command,
	})
}

func (h *BrowserHandler) Sources(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "sources", string(projectview.AssetTypeSource))
}

func (h *BrowserHandler) Models(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "models", string(projectview.AssetTypeModelTable))
}

func (h *BrowserHandler) SemanticModels(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "semantic-models", string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) Dashboards(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "dashboards", string(projectview.AssetTypeDashboard))
}

func (h *BrowserHandler) SourceAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindSource)
}

func (h *BrowserHandler) ModelAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindModel)
}

func (h *BrowserHandler) SemanticModelAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindSemanticModel)
}

func (h *BrowserHandler) DashboardAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindDashboard)
}

func (h *BrowserHandler) PipelineAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindPipeline)
}

func (h *BrowserHandler) ConnectionAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindConnection)
}

func (h *BrowserHandler) assetDocument(w stdhttp.ResponseWriter, r *stdhttp.Request, kinds ...projectgraph.Kind) {
	if !h.authorizeAny(w, r, kinds) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	assetID := chi.URLParam(r, "asset")
	asset, found := projectview.AssetByID(assets, assetID)
	if !found || !assetMatchesCatalogKinds(asset, kinds) {
		stdhttp.NotFound(w, r)
		return
	}
	section := requestedAssetSection(r)
	if asset.Type == string(projectview.AssetTypeModelTable) && section == "refresh" {
		target := assetnav.CanonicalAssetSectionHref(asset, "refreshes")
		if query := r.URL.Query().Encode(); query != "" {
			target += "?" + query
		}
		stdhttp.Redirect(w, r, target, stdhttp.StatusPermanentRedirect)
		return
	}
	if !projectui.ValidProjectAssetSection(asset.Type, section) {
		stdhttp.NotFound(w, r)
		return
	}
	projection, err := h.assetPageState(r, projectID, assets, edges, assetID, section)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			stdhttp.NotFound(w, r)
			return
		}
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	if projection.Asset.Type == string(projectview.AssetTypeConnection) {
		administration, _ := h.connectionAdministrationView(r.Context(), projectID, projection.Assets, projection.Edges, r)
		writeDocument(w, projectui.ConnectionAssetPageWithAdministrationForEnvironment(projection.Catalog, projection.Project, projection.Asset, projection.Assets, projection.Edges, projection.Section, h.Environment, "", projection.Versions, administration, h.ConnectionCommands, h.csrf(r), []webpage.Provider{h.layout(r)}))
		return
	}
	createDashboardHref := ""
	if projection.Asset.Type == string(projectview.AssetTypeSemanticModel) && h.dashboardCreationAllowed(r) {
		createDashboardHref = "/dashboards/new?semanticModel=" + url.QueryEscape(projection.Asset.ID)
	}
	writeDocument(w, projectui.ProjectAssetPageWithRefreshAndVersionsForEnvironmentAndDashboardCreation(projection.Catalog, projection.Project, projection.Asset, projection.Assets, projection.Edges, projection.Section, h.Environment, "", projection.Refresh, projection.Versions, h.csrf(r), createDashboardHref, h.layout(r)))
}

func (h *BrowserHandler) projectAssets(w stdhttp.ResponseWriter, r *stdhttp.Request, area, activeType string) {
	kinds := []projectgraph.Kind{projectgraph.KindSource}
	if area == "models" {
		kinds = []projectgraph.Kind{projectgraph.KindModel}
	}
	if area == "semantic-models" {
		kinds = []projectgraph.Kind{projectgraph.KindSemanticModel}
	}
	if area == "dashboards" {
		kinds = []projectgraph.Kind{projectgraph.KindDashboard}
	}
	if !h.authorizeAny(w, r, kinds) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	catalog := h.navigationCatalog(r)
	project := projectview.DevelopView{ID: projectID.String(), Title: catalog.Project.Title, Description: catalog.Project.Description}
	contextAssets := assets
	assets = projectview.FilterProjectLandingAssets(assets, activeType, strings.TrimSpace(r.URL.Query().Get("q")))
	writeDocument(w, projectui.ProjectAreaPageWithContext(catalog, project, assets, contextAssets, edges, area, activeType, r.URL.Query().Get("q"), h.Environment, "", h.csrf(r), h.layout(r)))
}

func (h *BrowserHandler) Pipelines(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindPipeline}) {
		return
	}
	projectID, assets, _, ok := h.assets(w, r)
	if !ok {
		return
	}
	state, err := h.pipelineMonitorState(r, projectID, assets)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	writeDocument(w, projectui.PipelinesPage(h.navigationCatalog(r), state, r.URL.Query().Get("view"), "", h.layout(r)))
}

// pipelineMutationAllowed is used while building every pipeline projection,
// including SSE refreshes after a command. Read access to the pipeline is not
// sufficient to expose run, retry, or cancel controls; all of those actions
// require the resource's canonical ID and RESOURCE_USE capability.
func (h *BrowserHandler) pipelineMutationAllowed(r *stdhttp.Request, pipelineID string) bool {
	if h == nil || h.AuthorizePipeline == nil || r == nil {
		return false
	}
	if principal, ok := h.currentPrincipal(r); ok && principal.DevBypass {
		return true
	}
	allowed, err := h.AuthorizePipeline(r, strings.TrimSpace(pipelineID), access.CapabilityResourceUse)
	return err == nil && allowed
}

func (h *BrowserHandler) Connections(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindConnection}) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	assets = projectview.FilterConnections(assets, strings.TrimSpace(r.URL.Query().Get("q")))
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	administration, _ := h.connectionAdministrationView(r.Context(), projectID, assets, edges, r)
	writeDocument(w, projectui.ConnectionsPageWithAdministrationForEnvironment(h.navigationCatalog(r), projectID.String(), assets, edges, r.URL.Query().Get("q"), h.Environment, "", h.csrf(r), administration, h.ConnectionCommands, h.layout(r)))
}

func (h *BrowserHandler) SourcesSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeSource))
}

func (h *BrowserHandler) ModelsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeModelTable))
}

func (h *BrowserHandler) SemanticModelsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) DashboardsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeDashboard))
}

func (h *BrowserHandler) projectAreaSearch(w stdhttp.ResponseWriter, r *stdhttp.Request, typ string) {
	kind, ok := catalogKindForAssetType(typ)
	if !ok {
		uitransport.WriteBrowserAuthorizationError(w, r, stdhttp.StatusForbidden)
		return
	}
	if !h.authorizeAny(w, r, []projectgraph.Kind{kind}) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	var signals struct {
		Query string `json:"projectAssetQuery"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "project asset search signals are required", stdhttp.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(signals.Query)
	patch := projectui.ProjectAssetListResultsPatchWithContext(projectID.String(), projectview.FilterProjectLandingAssets(assets, typ, query), assets, edges)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) ConnectionsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindConnection}) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	var signals struct {
		Query string `json:"entityListQuery"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "connection search signals are required", stdhttp.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(signals.Query)
	assets = projectview.FilterConnections(assets, query)
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	administration, _ := h.connectionAdministrationView(r.Context(), projectID, assets, edges, r)
	patch := projectui.ConnectionsListResultsPatchWithAdministration(assets, edges, administration)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) Updates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindConnection, projectgraph.KindDashboard}) {
		return
	}
	patch := map[string]any{"status": projectsignals.DashboardStatus{}, "runtime": projectsignals.RouteRuntimeSignal{Kind: projectsignals.RouteKindData}}
	switch uitransport.Route(r) {
	case "catalog":
		catalog, options, err := h.dashboardCatalogPage(r, r.URL.Query().Get("q"))
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return
		}
		patch = projectui.CatalogBootstrapSignalsForCatalogsWithOptions([]projectnavigation.Catalog{catalog}, options, h.layout(r))
	case "data":
		surface := r.URL.Query().Get("surface")
		if surface == "explore" {
			page, explorer, ok := h.dataExplorerSignals(w, r)
			if !ok {
				return
			}
			patch = projectui.DataExplorerBootstrapSignals(h.navigationCatalog(r), page, explorer, h.layout(r))
		} else if surface == "asset" {
			if assetPatch, ok := h.assetBootstrap(w, r); ok {
				patch = assetPatch
			} else {
				return
			}
		} else if projectPatch, ok := h.projectBootstrap(w, r); ok {
			patch = projectPatch
		} else {
			return
		}
	case "connections":
		if connectionPatch, ok := h.connectionsBootstrap(w, r); ok {
			patch = connectionPatch
		} else {
			return
		}
	case "pipelines":
		if pipelinePatch, ok := h.pipelinesBootstrap(w, r); ok {
			patch = pipelinePatch
		} else {
			return
		}
	case "asset", "connection_asset":
		if assetPatch, ok := h.assetBootstrap(w, r); ok {
			patch = assetPatch
		} else {
			return
		}
	}
	uitransport.PatchAndWait(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) projectBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	area := strings.TrimSpace(r.URL.Query().Get("area"))
	if area == "" {
		area = "sources"
	}
	activeType := projectAreaType(area)
	contextAssets := assets
	assets = projectview.FilterProjectLandingAssets(assets, activeType, r.URL.Query().Get("q"))
	catalog := h.navigationCatalog(r)
	project := projectview.DevelopView{ID: projectID.String(), Title: catalog.Project.Title, Description: catalog.Project.Description}
	return projectui.ProjectBootstrapSignalsForAreaWithContext(catalog, project, assets, contextAssets, edges, area, activeType, r.URL.Query().Get("q"), h.Environment, "", h.layout(r)), true
}

func (h *BrowserHandler) connectionsBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	assets = projectview.FilterConnections(assets, r.URL.Query().Get("q"))
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return nil, false
	}
	administration, _ := h.connectionAdministrationView(r.Context(), projectID, assets, edges, r)
	return projectui.ConnectionsBootstrapSignalsWithAdministrationForEnvironment(h.navigationCatalog(r), projectID.String(), assets, edges, r.URL.Query().Get("q"), h.Environment, "", administration, h.layout(r)), true
}

func (h *BrowserHandler) pipelinesBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, _, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	state, err := h.pipelineMonitorState(r, projectID, assets)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return nil, false
	}
	return projectui.PipelinesBootstrapSignals(h.navigationCatalog(r), state, r.URL.Query().Get("view"), "", h.layout(r)), true
}

func (h *BrowserHandler) assetBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	assetID := strings.TrimSpace(r.URL.Query().Get("asset"))
	asset, found := projectview.AssetByID(assets, assetID)
	if !found {
		stdhttp.NotFound(w, r)
		return nil, false
	}
	section := strings.TrimSpace(r.URL.Query().Get("section"))
	if !projectui.ValidProjectAssetSection(asset.Type, section) {
		stdhttp.NotFound(w, r)
		return nil, false
	}
	route := uitransport.Route(r)
	isConnection := asset.Type == string(projectview.AssetTypeConnection)
	if (route == "connection_asset" && !isConnection) || ((route == "asset" || route == "data") && isConnection) {
		stdhttp.NotFound(w, r)
		return nil, false
	}
	projection, err := h.assetPageState(r, projectID, assets, edges, assetID, section)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			stdhttp.NotFound(w, r)
			return nil, false
		}
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return nil, false
	}
	if projection.Asset.Type == string(projectview.AssetTypeConnection) {
		administration, _ := h.connectionAdministrationView(r.Context(), projectID, projection.Assets, projection.Edges, r)
		return projectui.ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(projection.Catalog, projection.Project, projection.Asset, projection.Assets, projection.Edges, projection.Section, h.Environment, "", projection.Versions, administration, h.layout(r)), true
	}
	if r.URL.Query().Get("surface") == "asset" {
		patch := projectui.ProjectAssetBootstrapSignalsForEnvironment(projection.Catalog, projection.Project, projection.Asset, projection.Assets, projection.Edges, projection.Section, h.Environment, "", projection.Refresh, projection.Versions, h.layout(r))
		if r.URL.Query().Get("section") == "data" && (projection.Asset.Type == string(projectview.AssetTypeModelTable) || projection.Asset.Type == string(projectview.AssetTypeSemanticModel)) {
			_, explorer, _, explorerOK := h.dataExplorerSignalsForAssetCommand(w, r, projection.Asset.ID, projectsignals.DataExplorerCommand{})
			if !explorerOK {
				return nil, false
			}
			patch["dataExplorer"] = explorer
			patch["dataExplorerCommand"] = explorer.Command
		}
		return patch, true
	}
	stdhttp.NotFound(w, r)
	return nil, false
}

func requestedAssetSection(r *stdhttp.Request) string {
	if section := strings.TrimSpace(chi.URLParam(r, "section")); section != "" {
		return section
	}
	if r == nil || r.URL == nil {
		return ""
	}
	return strings.TrimSpace(path.Base(r.URL.Path))
}

func (h *BrowserHandler) assetVersionsState(ctx context.Context, projectID projectgraph.ResourceID, asset projectview.DevelopAssetView, section string) (projectui.AssetVersionsState, error) {
	state := projectui.AssetVersionsState{CurrentContentHash: asset.ContentHash}
	if h == nil || h.AssetVersions == nil || (section != "versions" && strings.TrimSpace(asset.ContentHash) != "") {
		return state, nil
	}
	versions, err := h.AssetVersions.AssetVersions(ctx, projectID, h.Environment, projectgraph.ResourceID(asset.ID))
	if err != nil {
		if section != "versions" {
			return state, nil
		}
		return state, err
	}
	state.Versions = make([]projectui.AssetVersionState, 0, len(versions))
	for _, version := range versions {
		state.Versions = append(state.Versions, projectui.AssetVersionState{
			ServingStateID: string(version.ServingStateID), Environment: string(version.Environment), Status: version.Status, Digest: version.Digest,
			CreatedBy: version.CreatedBy, CreatedAt: version.CreatedAt, ActivatedAt: version.ActivatedAt,
			SnapshotID: version.SnapshotID, SourceFile: version.SourceFile, PayloadJSON: version.PayloadJSON, ContentHash: version.ContentHash,
		})
	}
	return state, nil
}

func (h *BrowserHandler) assetRefreshState(ctx context.Context, projectID projectgraph.ResourceID, asset projectview.DevelopAssetView) (projectui.AssetRefreshState, error) {
	state := projectui.AssetRefreshState{}
	if asset.Type != string(projectview.AssetTypeRefreshPipeline) && asset.Type != string(projectview.AssetTypeModelTable) && asset.Type != string(projectview.AssetTypeSemanticModel) {
		return state, nil
	}
	if h == nil || h.RefreshState == nil {
		return projectui.AssetRefreshState{Unavailable: true}, nil
	}
	if asset.Type == string(projectview.AssetTypeModelTable) {
		modelKey := strings.TrimSpace(asset.Key)
		if modelKey == "" {
			modelKey = strings.TrimPrefix(asset.ID, "model:")
		}
		modelID, err := projectgraph.NewResourceID(modelKey)
		if err != nil {
			return state, err
		}
		return refreshStateToProjectUI(h.RefreshState.ModelRefreshState(ctx, projectID, h.Environment, modelID))
	}
	if asset.Type == string(projectview.AssetTypeSemanticModel) {
		semanticModelRef := strings.TrimSpace(asset.ID)
		if semanticModelRef == "" {
			semanticModelRef = strings.TrimSpace(asset.Key)
		}
		semanticModelID, err := projectgraph.NewResourceID(semanticModelRef)
		if err != nil {
			return state, err
		}
		return refreshStateToProjectUI(h.RefreshState.SemanticModelRefreshState(ctx, projectID, h.Environment, semanticModelID))
	}
	pipelineID, err := projectgraph.NewResourceID(asset.ID)
	if err != nil {
		return state, err
	}
	modelID := projectAssetPayloadResourceID(asset.Payload, "SemanticModel", "semanticModel", "SemanticModelID", "semanticModelId")
	return refreshStateToProjectUI(h.RefreshState.AssetRefreshState(ctx, projectID, h.Environment, pipelineID, modelID))
}

func refreshStateToProjectUI(state refreshpresentation.AssetRefreshState, err error) (projectui.AssetRefreshState, error) {
	return projectui.AssetRefreshState{
		Unavailable: state.Unavailable,
		RunCommand:  state.RunCommand, CancelCommand: state.CancelCommand,
		Runs: projectRefreshRuns(state.Runs), Latest: projectRefreshRun(state.Latest),
		LatestSuccessful: projectRefreshRun(state.LatestSuccessful),
		DataVersion: projectui.AssetDataVersion{
			SnapshotID: state.DataVersion.SnapshotID, ServingStateID: state.DataVersion.ServingStateID,
			RefreshedAt: state.DataVersion.RefreshedAt, Source: state.DataVersion.Source,
		},
		NextRun: state.NextRun,
	}, err
}

func assetMatchesCatalogKinds(asset projectview.DevelopAssetView, kinds []projectgraph.Kind) bool {
	kind, ok := catalogKindForAssetType(asset.Type)
	if !ok {
		return false
	}
	for _, expected := range kinds {
		if kind == expected {
			return true
		}
	}
	return false
}

func projectRefreshRuns(runs []refreshpresentation.AssetRefreshRun) []projectui.AssetRefreshRun {
	out := make([]projectui.AssetRefreshRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, projectRefreshRun(run))
	}
	return out
}

func projectRefreshRun(run refreshpresentation.AssetRefreshRun) projectui.AssetRefreshRun {
	return projectui.AssetRefreshRun{
		ID: run.ID, Environment: run.Environment, ModelID: run.ModelID, ServingStateID: run.ServingStateID,
		PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName,
		TriggerType: run.TriggerType, ParentRunID: run.ParentRunID,
		TargetGeneration: run.TargetGeneration, Status: run.Status, CreatedAt: run.CreatedAt,
		UpdatedAt: run.UpdatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
	}
}

func projectAssetPayloadResourceID(payload map[string]any, keys ...string) projectgraph.ResourceID {
	for _, key := range keys {
		value, ok := payload[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		id, err := projectgraph.NewResourceID(value)
		if err == nil {
			return id
		}
	}
	return ""
}

// projectAssetReadModel enriches only the selected asset. Lists and lineage
// continue to use the immutable graph projection, while details and their SSE
// bootstrap read the complete typed definition from the active generation.
func (h *BrowserHandler) projectAssetReadModel(ctx context.Context, asset projectview.DevelopAssetView) (projectview.DevelopAssetView, error) {
	if h == nil {
		return projectview.DevelopAssetView{}, ErrProjectDefinitionUnavailable
	}
	if h.ProjectDefinitionReader == nil {
		return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
	}
	definition, compiled, err := h.ProjectDefinitionReader.ProjectDefinitionSnapshot(ctx)
	if err != nil {
		return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s: %v", ErrProjectDefinitionUnavailable, asset.ID, err)
	}
	enriched, err := projectAssetReadModelFromDefinition(asset, definition, compiled[asset.ID])
	if err != nil {
		return projectview.DevelopAssetView{}, err
	}
	return h.enrichAssetRuntimeMetadata(ctx, enriched)
}

func (h *BrowserHandler) enrichAssetRuntimeMetadata(ctx context.Context, asset projectview.DevelopAssetView) (projectview.DevelopAssetView, error) {
	enriched, err := h.enrichModelPhysicalMetadata(ctx, asset)
	if err != nil {
		return projectview.DevelopAssetView{}, err
	}
	return h.enrichSourceSchemaObservation(ctx, enriched)
}

func (h *BrowserHandler) enrichModelPhysicalMetadata(ctx context.Context, asset projectview.DevelopAssetView) (projectview.DevelopAssetView, error) {
	if asset.Type != string(projectview.AssetTypeModelTable) {
		return asset, nil
	}
	if h.PhysicalCatalog == nil {
		asset.Payload["PhysicalStatus"] = "unavailable"
		return asset, nil
	}
	projectID, err := projectgraph.NewResourceID(asset.ProjectID)
	if err != nil {
		return asset, nil
	}
	statistics, err := h.PhysicalCatalog.ModelPhysicalMetadata(ctx, projectID, h.Environment)
	if err != nil {
		// Physical catalog statistics enrich the authored definition but are not
		// required to browse it. A temporarily unavailable DuckLake snapshot must
		// not regress the entire model detail route.
		asset.Payload["PhysicalStatus"] = "unavailable"
		return asset, nil
	}
	_, tableName := projectModelTableKeyParts(asset.Key)
	physical, ok := statistics[tableName]
	if !ok {
		asset.Payload["PhysicalStatus"] = "not refreshed"
		return asset, nil
	}
	physicalPayload := map[string]any{
		"RowCount": physical.RowCount, "ColumnCount": physical.ColumnCount,
		"FileCount": physical.FileCount, "SizeBytes": physical.SizeBytes, "SnapshotID": physical.SnapshotID,
	}
	if !physical.SnapshotAt.IsZero() {
		physicalPayload["SnapshotAt"] = physical.SnapshotAt.UTC().Format(time.RFC3339)
	}
	asset.Payload["Physical"] = physicalPayload
	asset.Payload["PhysicalStatus"] = "available"
	for key, value := range projectview.ModelSchemaPayload(physical.Schema) {
		asset.Payload[key] = value
	}
	return asset, nil
}

func (h *BrowserHandler) enrichSourceSchemaObservation(ctx context.Context, asset projectview.DevelopAssetView) (projectview.DevelopAssetView, error) {
	if asset.Type != string(projectview.AssetTypeSource) || h.SourceSchemas == nil {
		return asset, nil
	}
	projectID, projectErr := projectgraph.NewResourceID(asset.ProjectID)
	sourceID, sourceErr := projectgraph.NewResourceID(asset.ID)
	servingStateID := strings.TrimSpace(asset.ServingStateID)
	if projectErr != nil || sourceErr != nil || servingStateID == "" {
		return asset, nil
	}
	observation, found, err := h.SourceSchemas.SourceSchemaObservation(ctx, projectID, h.Environment, servingStateID, sourceID)
	if err != nil || !found {
		// Observed schema enriches the authored definition. Historical generations
		// without gate provenance remain browsable with their contract fields.
		return asset, nil
	}
	for key, value := range projectview.SourceSchemaObservationPayload(observation) {
		asset.Payload[key] = value
	}
	return asset, nil
}

func projectModelTableKeyParts(key string) (string, string) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
}

func (h *BrowserHandler) projectAssetReadModels(ctx context.Context, assets []projectview.DevelopAssetView) ([]projectview.DevelopAssetView, error) {
	if len(assets) == 0 {
		return assets, nil
	}
	if h == nil || h.ProjectDefinitionReader == nil {
		return nil, ErrProjectDefinitionUnavailable
	}
	definition, compiled, err := h.ProjectDefinitionReader.ProjectDefinitionSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectDefinitionUnavailable, err)
	}
	out := make([]projectview.DevelopAssetView, 0, len(assets))
	for _, asset := range assets {
		enriched, err := projectAssetReadModelFromDefinition(asset, definition, compiled[asset.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, enriched)
	}
	return out, nil
}

func projectAssetReadModelFromDefinition(asset projectview.DevelopAssetView, definition projectmanifest.Project, compiled *semanticquery.CompiledModel) (projectview.DevelopAssetView, error) {
	var payload map[string]any
	switch asset.Type {
	case string(projectview.AssetTypeConnection):
		resource, ok := definition.Connections[asset.ID]
		if !ok {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
		}
		payload = projectview.ConnectionAssetPayload(resource)
		payload["Configuration"] = projectview.ConnectionAssetConfiguration(asset.ID, asset.Key, resource)
	case string(projectview.AssetTypeSource):
		resource, ok := definition.Sources[asset.ID]
		if !ok {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
		}
		payload = projectview.SourceAssetPayload(resource)
	case string(projectview.AssetTypeModelTable):
		resource, ok := definition.Models[asset.ID]
		if !ok {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
		}
		configuration := definition.AuthoredResourceSources[asset.ID]
		if configuration == "" {
			configuration = definition.AuthoredModelSources[asset.ID]
		}
		if authored, authoredOK := definition.AuthoredModelDefinitions[asset.ID]; authoredOK {
			payload = projectview.ModelTableAssetPayloadWithAuthoredSource(resource, &authored, configuration)
		} else {
			payload = projectview.ModelTableAssetPayloadWithAuthoredSource(resource, nil, configuration)
		}
	case string(projectview.AssetTypeSemanticModel):
		resource, ok := definition.SemanticModels[asset.ID]
		if !ok || resource == nil {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrSemanticModelUnavailable, asset.ID)
		}
		payload = projectview.SemanticModelAssetPayload(resource, compiled)
		if len(payload) == 0 {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrSemanticModelUnavailable, asset.ID)
		}
	case string(projectview.AssetTypeDashboard):
		resource, ok := definition.DashboardDefinitions[asset.ID]
		if !ok {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
		}
		payload = projectview.DashboardAssetPayload(resource)
		payload["Publications"] = dashboardPublicationPayloads(asset.ID, definition.Publications)
	case string(projectview.AssetTypeRefreshPipeline):
		resource, ok := definition.RefreshPipelines[asset.ID]
		if !ok {
			return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
		}
		payload = projectview.RefreshPipelineAssetPayload(resource)
	default:
		return asset, nil
	}
	if configuration := definition.AuthoredResourceSources[asset.ID]; configuration != "" {
		payload["Configuration"] = configuration
	}
	return mergeProjectAssetPayload(asset, payload)
}

func dashboardPublicationPayloads(dashboardID string, definitions map[string]publication.Definition) []map[string]any {
	ids := make([]string, 0, len(definitions))
	for id, value := range definitions {
		if value.Dashboard == dashboardID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		value := definitions[id]
		name := value.Name
		if strings.TrimSpace(name) == "" {
			name = id
		}
		out = append(out, map[string]any{
			"Name":                name,
			"Dashboard":           value.Dashboard,
			"DefaultPage":         value.DefaultPage,
			"AllowedOrigins":      append([]string(nil), value.AllowedOrigins...),
			"ConfigurationDigest": value.ConfigurationDigest,
		})
	}
	return out
}

func mergeProjectAssetPayload(asset projectview.DevelopAssetView, payload map[string]any) (projectview.DevelopAssetView, error) {
	if len(payload) == 0 {
		return projectview.DevelopAssetView{}, fmt.Errorf("%w: %s", ErrProjectDefinitionUnavailable, asset.ID)
	}
	// Preserve graph identity/metadata keys while replacing the resource's
	// generic payload fields with the typed detail projection.
	merged := make(map[string]any, len(asset.Payload)+len(payload))
	for key, value := range asset.Payload {
		merged[key] = value
	}
	for key, value := range payload {
		merged[key] = value
	}
	asset.Payload = merged
	return asset, nil
}

// ProtectStream applies the same authenticated subject resolution used by the
// document surface to the canonical project SSE stream.
func (h *BrowserHandler) ProtectStream(next stdhttp.Handler) stdhttp.Handler {
	if h == nil || next == nil {
		return stdhttp.NotFoundHandler()
	}
	protected := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindConnection, projectgraph.KindDashboard}) {
			return
		}
		next.ServeHTTP(w, r)
	})
	if h.Authenticate != nil {
		return h.Authenticate(protected)
	}
	return protected
}

func (h *BrowserHandler) assets(w stdhttp.ResponseWriter, r *stdhttp.Request) (projectgraph.ResourceID, []projectview.DevelopAssetView, []projectview.DevelopEdgeView, bool) {
	projectID, assets, edges, err := h.loadAssets(r)
	if err == nil {
		return projectID, assets, edges, true
	}
	status := stdhttp.StatusServiceUnavailable
	var loadErr assetLoadError
	if errors.As(err, &loadErr) {
		status = loadErr.status
	}
	stdhttp.Error(w, stdhttp.StatusText(status), status)
	return "", nil, nil, false
}

type assetLoadError struct {
	status int
	err    error
}

func (e assetLoadError) Error() string { return e.err.Error() }
func (e assetLoadError) Unwrap() error { return e.err }

// loadAssets is the transport-free read path used after successful mutations.
// Its callers can preserve a committed command response when reprojection is
// temporarily unavailable, without fabricating an HTTP response writer.
func (h *BrowserHandler) loadAssets(r *stdhttp.Request) (projectgraph.ResourceID, []projectview.DevelopAssetView, []projectview.DevelopEdgeView, error) {
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: err}
	}
	if h.Graph == nil {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: errors.New("project graph is unavailable")}
	}
	graph, ok, err := h.Graph.ActiveServingStateGraph(r.Context(), projectID, h.Environment)
	if err != nil {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: err}
	}
	if !ok {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: errors.New("active project graph is unavailable")}
	}
	if h.CurrentUser == nil {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: errors.New("current principal resolver is unavailable")}
	}
	principal, authenticated := h.CurrentUser(r)
	if !authenticated {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusUnauthorized, err: errors.New("current principal is unauthenticated")}
	}
	if !principal.DevBypass {
		if h.Catalog == nil {
			return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: errors.New("project catalog is unavailable")}
		}
		allowedPage, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindDashboard, projectgraph.KindPipeline})
		if err != nil {
			return "", nil, nil, assetLoadError{status: stdhttp.StatusServiceUnavailable, err: err}
		}
		allowed := make(map[projectgraph.ResourceID]struct{}, len(allowedPage.Items))
		for _, item := range allowedPage.Items {
			allowed[item.Ref.ID] = struct{}{}
		}
		filteredAssets := make([]servingstate.Asset, 0, len(graph.Assets))
		for _, asset := range graph.Assets {
			_, mapped := catalogKindForAssetType(asset.Type)
			if !mapped {
				continue
			}
			if _, visible := allowed[asset.ID]; visible {
				filteredAssets = append(filteredAssets, asset)
				continue
			}
		}
		graph.Assets = filteredAssets
		visibleIDs := make(map[projectgraph.ResourceID]struct{}, len(graph.Assets))
		for _, asset := range graph.Assets {
			visibleIDs[asset.ID] = struct{}{}
		}
		filteredEdges := make([]servingstate.AssetEdge, 0, len(graph.Edges))
		for _, edge := range graph.Edges {
			if _, from := visibleIDs[edge.FromAssetID]; !from {
				continue
			}
			if _, to := visibleIDs[edge.ToAssetID]; !to {
				continue
			}
			filteredEdges = append(filteredEdges, edge)
		}
		graph.Edges = filteredEdges
	}
	projectGraph := projectview.DevelopAssetGraph{Assets: make([]projectview.Asset, 0, len(graph.Assets)), Edges: make([]projectview.AssetEdge, 0, len(graph.Edges))}
	for _, asset := range graph.Assets {
		projectGraph.Assets = append(projectGraph.Assets, projectview.Asset{ID: projectview.AssetID(asset.ID), SnapshotID: projectview.AssetSnapshotID(asset.SnapshotID), ProjectID: asset.ProjectID, ServingStateID: projectview.ServingStateID(asset.ServingStateID), Type: projectview.AssetType(asset.Type), Key: asset.Key, ParentID: projectview.AssetID(asset.ParentID), Title: asset.Title, Description: asset.Description, SourceFile: asset.SourceFile, PayloadSchema: asset.PayloadSchema, PayloadJSON: asset.PayloadJSON, ContentHash: asset.ContentHash})
	}
	for _, edge := range graph.Edges {
		projectGraph.Edges = append(projectGraph.Edges, projectview.AssetEdge{ID: projectview.AssetEdgeID(edge.ID), ProjectID: edge.ProjectID, ServingStateID: projectview.ServingStateID(edge.ServingStateID), FromAssetID: projectview.AssetID(edge.FromAssetID), ToAssetID: projectview.AssetID(edge.ToAssetID), Type: projectview.AssetEdgeType(edge.Type)})
	}
	decoded, err := projectview.DecodeDevelopCatalog(projectGraph)
	if err != nil {
		return "", nil, nil, assetLoadError{status: stdhttp.StatusInternalServerError, err: err}
	}
	assets := make([]projectview.DevelopAssetView, 0, len(decoded.Assets))
	for _, asset := range decoded.Assets {
		assets = append(assets, projectview.DevelopAssetViewFromCatalogRecord(asset))
	}
	edges := make([]projectview.DevelopEdgeView, 0, len(decoded.Edges))
	for _, edge := range decoded.Edges {
		edges = append(edges, projectview.DevelopEdgeViewFromCatalogRecord(edge))
	}
	return projectID, assets, edges, nil
}

func (h *BrowserHandler) authorizeAny(w stdhttp.ResponseWriter, r *stdhttp.Request, kinds []projectgraph.Kind) bool {
	if h.CurrentUser == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return false
	}
	if principal.DevBypass {
		return true
	}
	if _, err := h.boundProject(r.Context()); err != nil || h.Catalog == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	selector := strings.TrimSpace(r.URL.Query().Get("asset"))
	if selector == "" {
		selector = strings.TrimSpace(r.URL.Query().Get("model"))
	}
	if selector == "" {
		selector = strings.TrimSpace(chi.URLParam(r, "asset"))
	}
	if selector != "" {
		for _, kind := range kinds {
			if _, err := h.Catalog.Resolve(r.Context(), principal.ID, projectcatalog.Ref{ID: projectgraph.ResourceID(selector), Kind: kind}, access.CapabilityResourceRead, principal.DevBypass); err == nil {
				return true
			}
		}
		uitransport.WriteBrowserAuthorizationError(w, r, stdhttp.StatusForbidden)
		return false
	}
	page, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, kinds)
	if err != nil {
		if errors.Is(err, projectcatalog.ErrNotFound) {
			uitransport.WriteBrowserAuthorizationError(w, r, stdhttp.StatusForbidden)
			return false
		}
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	if len(page.Items) == 0 {
		uitransport.WriteBrowserAuthorizationError(w, r, stdhttp.StatusForbidden)
		return false
	}
	return true
}

func (h *BrowserHandler) boundProject(ctx context.Context) (projectgraph.ResourceID, error) {
	if h.ResolveProjectID == nil {
		return "", errors.New("active project resolver is unavailable")
	}
	projectID, err := h.ResolveProjectID(ctx)
	if err != nil {
		return "", err
	}
	if err := projectID.Validate(); err != nil {
		return "", fmt.Errorf("active project: %w", err)
	}
	return projectID, nil
}

func (h *BrowserHandler) navigationCatalog(r *stdhttp.Request) projectnavigation.Catalog {
	if h.Catalog == nil || h.CurrentUser == nil {
		return projectnavigation.Catalog{}
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		return projectnavigation.Catalog{}
	}
	page, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindDashboard})
	if err != nil {
		return projectnavigation.Catalog{}
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return projectnavigation.Catalog{}
	}
	out := projectnavigation.Catalog{Project: projectnavigation.Project{ID: projectID.String(), Title: projectID.String()}}
	for _, item := range page.Items {
		switch item.Ref.Kind {
		case projectgraph.KindProject:
			out.Project = projectnavigation.Project{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description}
		case projectgraph.KindModel:
			out.Models = append(out.Models, projectnavigation.Model{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description})
		case projectgraph.KindSemanticModel:
			out.SemanticModels = append(out.SemanticModels, projectnavigation.Model{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description})
		case projectgraph.KindDashboard:
			out.Dashboards = append(out.Dashboards, projectnavigation.Dashboard{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description})
		}
	}
	h.enrichDashboardAppearances(r.Context(), projectID, &out)
	return out
}

func (h *BrowserHandler) dashboardCatalogPage(r *stdhttp.Request, query string) (projectnavigation.Catalog, projectui.CatalogListOptions, error) {
	catalog := h.navigationCatalog(r)
	options := projectui.CatalogListOptions{Query: strings.TrimSpace(query)}
	if h.DashboardCatalog == nil {
		return catalog, options, nil
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return projectnavigation.Catalog{}, projectui.CatalogListOptions{}, errors.New("current principal is unavailable")
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return projectnavigation.Catalog{}, projectui.CatalogListOptions{}, err
	}
	result, err := h.DashboardCatalog.List(r.Context(), dashboardauthoringcatalog.ListRequest{ProjectID: projectID, ActorID: principal.ID})
	if err != nil {
		return projectnavigation.Catalog{}, projectui.CatalogListOptions{}, err
	}
	appearanceByID := make(map[string]dashboardappearance.Value, len(catalog.Dashboards))
	for _, item := range catalog.Dashboards {
		appearanceByID[item.ID] = item.Appearance
	}
	options.Dashboards = make([]projectui.CatalogDashboardItem, 0, len(result.Items))
	for _, item := range result.Items {
		status := dashboardCatalogStatus(item)
		href := "/dashboards/" + url.PathEscape(item.ID.String())
		if item.Source == dashboardauthoringcatalog.SourceInstance && item.DraftID != "" && status != "published" {
			values := url.Values{}
			values.Set("draft", item.DraftID.String())
			href += "/edit?" + values.Encode()
		}
		scope := "managed"
		owner := strings.TrimSpace(item.Owner)
		if item.Source == dashboardauthoringcatalog.SourceInstance {
			scope = "shared"
			if owner == principal.ID {
				scope, owner = "mine", "You"
			}
		}
		updatedAt := ""
		if item.Revision != nil && !item.Revision.CreatedAt.IsZero() {
			updatedAt = item.Revision.CreatedAt.UTC().Format(time.RFC3339)
		}
		options.Dashboards = append(options.Dashboards, projectui.CatalogDashboardItem{
			ID: item.StableID, DashboardID: item.ID.String(), Title: item.Title, Description: item.Description,
			SemanticModel: item.SemanticModel.String(), Href: href, Owner: owner, Status: status,
			CatalogScope: scope, UpdatedAt: updatedAt, PageCount: item.PageCount, Tags: append([]string(nil), item.Tags...),
			Appearance: appearanceByID[item.ID.String()], RepositoryManaged: item.Source == dashboardauthoringcatalog.SourceProject,
		})
	}
	return catalog, options, nil
}

func dashboardCatalogStatus(item dashboardauthoringcatalog.Dashboard) string {
	if item.Source == dashboardauthoringcatalog.SourceProject {
		return "published"
	}
	if item.Publication == nil {
		return "private_draft"
	}
	if item.Revision != nil && item.Revision.ID == item.Publication.Revision.ID && item.Revision.Number == item.Publication.Revision.Number && item.Revision.ContentHash == item.Publication.Revision.ContentHash {
		return "published"
	}
	return "unpublished_changes"
}

func (h *BrowserHandler) enrichDashboardAppearances(ctx context.Context, projectID projectgraph.ResourceID, catalog *projectnavigation.Catalog) {
	if catalog == nil {
		return
	}
	byID := make(map[string]*projectnavigation.Dashboard, len(catalog.Dashboards))
	for index := range catalog.Dashboards {
		byID[catalog.Dashboards[index].ID] = &catalog.Dashboards[index]
	}
	if h.ProjectDefinitionReader != nil {
		if project, _, err := h.ProjectDefinitionReader.ProjectDefinitionSnapshot(ctx); err == nil {
			for _, source := range project.DashboardSources {
				dashboard := byID[source.Document.Metadata.ID]
				if dashboard == nil || source.Document.Spec.Appearance == nil {
					continue
				}
				if source.Document.Spec.Appearance.Icon != nil {
					dashboard.Appearance.Icon = strings.TrimSpace(*source.Document.Spec.Appearance.Icon)
				}
				if source.Document.Spec.Appearance.Color != nil {
					dashboard.Appearance.Color = strings.TrimSpace(string(*source.Document.Spec.Appearance.Color))
				}
			}
		}
	}
	if h.DashboardAppearances == nil {
		return
	}
	overrides, err := h.DashboardAppearances.ListProject(ctx, projectID)
	if err != nil {
		return
	}
	for dashboardID, record := range overrides {
		dashboard := byID[dashboardID.String()]
		if dashboard == nil {
			continue
		}
		dashboard.Appearance = dashboardappearance.Resolve(record.Value)
		dashboard.AppearanceRevision = record.Revision
	}
}

func listCatalogAll(ctx context.Context, catalog CatalogAuthorizer, principalID string, devAuthBypass bool, kinds []projectgraph.Kind) (projectcatalog.Page, error) {
	if catalog == nil {
		return projectcatalog.Page{}, projectcatalog.ErrUnavailable
	}
	items := make([]projectcatalog.Result, 0)
	cursor := ""
	seenCursors := map[string]struct{}{}
	for pages := 0; ; pages++ {
		if pages >= 10000 {
			return projectcatalog.Page{}, fmt.Errorf("catalog pagination exceeded safety bound")
		}
		page, err := catalog.List(ctx, projectcatalog.ListRequest{PrincipalID: principalID, DevAuthBypass: devAuthBypass, Kinds: kinds, Limit: projectcatalog.MaxLimit, Cursor: cursor})
		if err != nil {
			return projectcatalog.Page{}, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			return projectcatalog.Page{Items: items}, nil
		}
		if _, seen := seenCursors[page.NextCursor]; seen {
			return projectcatalog.Page{}, fmt.Errorf("catalog pagination cursor repeated")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func browserFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func catalogKindForAssetType(typ string) (projectgraph.Kind, bool) {
	switch typ {
	case string(projectview.AssetTypeConnection):
		return projectgraph.KindConnection, true
	case string(projectview.AssetTypeSource):
		return projectgraph.KindSource, true
	case string(projectview.AssetTypeModelTable):
		return projectgraph.KindModel, true
	case string(projectview.AssetTypeSemanticModel):
		return projectgraph.KindSemanticModel, true
	case string(projectview.AssetTypeDashboard):
		return projectgraph.KindDashboard, true
	case string(projectview.AssetTypeRefreshPipeline):
		return projectgraph.KindPipeline, true
	default:
		return "", false
	}
}

func projectAreaType(area string) string {
	switch strings.TrimSpace(area) {
	case "dashboards":
		return string(projectview.AssetTypeDashboard)
	case "models":
		return string(projectview.AssetTypeModelTable)
	case "semantic-models":
		return string(projectview.AssetTypeSemanticModel)
	default:
		return string(projectview.AssetTypeSource)
	}
}

func (h *BrowserHandler) dataExplorerSignals(w stdhttp.ResponseWriter, r *stdhttp.Request) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	command := projectsignals.DataExplorerCommand{
		ObjectKey: projectsignals.Optional(strings.TrimSpace(r.URL.Query().Get("object"))),
		Mode:      projectsignals.Optional(strings.TrimSpace(r.URL.Query().Get("mode"))),
		Limit:     dataExplorerDefaultLimit, Count: dataExplorerDefaultLimit, Block: projectsignals.Pointer("all"),
	}
	return h.dataExplorerSignalsForCommand(w, r, command)
}

func (h *BrowserHandler) dataExplorerSignalsForCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.DataExplorerCommand) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	command = normalizeDataExplorerCommand(command)
	project := h.navigationCatalog(r).Project
	page := projectsignals.DataExplorerPageSignal{Kind: projectsignals.RouteKindData, Title: "Data Explorer", Description: projectsignals.Optional("Explore governed semantic data."), Tabs: []projectsignals.ResourceTabSignal{}, Context: projectsignals.DataExplorerContextSignal{Active: true, Environment: h.Environment, ProjectID: project.ID, ProjectTitle: projectsignals.Optional(project.Title)}}
	exploreCommand := projectsignals.DataExploreCommand{Dimensions: []string{}, Metrics: []string{}, Filters: []projectsignals.DataExploreFilterSignal{}, Sort: []projectsignals.DataExploreSortSignal{}, Limit: dataExplorerDefaultLimit}
	if command.Explore != nil {
		exploreCommand = *command.Explore
	}
	if value := strings.TrimSpace(r.URL.Query().Get("model")); value != "" && exploreCommand.ModelID == nil {
		exploreCommand.ModelID = projectsignals.Optional(value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("dataset")); value != "" && exploreCommand.DatasetID == nil {
		exploreCommand.DatasetID = projectsignals.Optional(value)
	}
	command.Explore = &exploreCommand
	explorer := projectsignals.DataExplorerSignal{Command: command, Explore: projectsignals.DataExploreSignal{Command: exploreCommand, Models: []projectsignals.DataExploreModelSignal{}, Datasets: []projectsignals.DataExploreDatasetSignal{}, Fields: []projectsignals.DataExploreFieldSignal{}, Result: projectsignals.DataExploreResultSignal{Columns: []projectsignals.DataPreviewColumnSignal{}, Rows: []map[string]any{}, Warnings: []string{}}}, Objects: []projectsignals.DataExplorerObjectSignal{}, Preview: projectsignals.DataPreviewSignal{Blocks: emptyDataExplorerBlocks(command), Columns: []projectsignals.DataPreviewColumnSignal{}, ChunkSize: command.Count, RowHeight: dataExplorerRowHeight}}
	_, assets, _, ok := h.assets(w, r)
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	if h == nil || h.ProjectDefinitionReader == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	definition, compiledModels, err := h.ProjectDefinitionReader.ProjectDefinitionSnapshot(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	projection := BuildDataExplorerProjection(assets, definition, exploreCommand, compiledModels)
	explorer.Objects = projection.Objects
	explorer.Explore.Models = projection.Models
	explorer.Explore.SelectedModel = projection.SelectedModel
	explorer.Explore.Datasets = projection.Datasets
	explorer.Explore.SelectedDataset = projection.SelectedDataset
	explorer.Explore.Fields = projection.Fields
	exploreCommand = projection.Command
	explorer.Explore.Command = exploreCommand
	explorer.Command.Explore = &exploreCommand
	if projectsignals.ValueOrZero(explorer.Command.Mode) == "explore" {
		projectID, err := h.boundProject(r.Context())
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
		}
		exploreCommand, explorer.Explore.Result = dataExplorerSemanticResult(r.Context(), h.QueryExecutor, projectID, exploreCommand, explorer.Explore.Fields)
		explorer.Explore.Result.Warnings = append(explorer.Explore.Result.Warnings, projection.Warnings...)
		explorer.Explore.Command = exploreCommand
		explorer.Command.Explore = &exploreCommand
	}
	page.Context.ObjectCount = int64(len(explorer.Objects))

	requestedObject := strings.TrimSpace(projectsignals.ValueOrZero(command.ObjectKey))
	if projectsignals.ValueOrZero(explorer.Command.Mode) == "explore" {
		requestedObject = ""
		modelID := strings.TrimSpace(projectsignals.ValueOrZero(exploreCommand.ModelID))
		datasetID := strings.TrimSpace(projectsignals.ValueOrZero(exploreCommand.DatasetID))
		for _, object := range explorer.Objects {
			if object.Layer == "model_table" && projectsignals.ValueOrZero(object.ModelID) == modelID && projectsignals.ValueOrZero(object.Table) == datasetID {
				requestedObject = object.Key
				break
			}
		}
	}
	if requestedObject != "" {
		for index := range explorer.Objects {
			object := explorer.Objects[index]
			if object.Key != requestedObject && object.ResourceID != requestedObject && projectsignals.ValueOrZero(object.AssetID) != requestedObject {
				continue
			}
			explorer.Command.ObjectKey = projectsignals.Optional(object.Key)
			explorer.SelectedKey = projectsignals.Optional(object.Key)
			explorer.SelectedObject = &object
			page.SelectedObject = projectsignals.Optional(object.Key)
			projectID, err := h.boundProject(r.Context())
			if err != nil {
				stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
				return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
			}
			if projectsignals.ValueOrZero(explorer.Command.Mode) != "explore" {
				explorer.Preview = dataExplorerPreview(r.Context(), h.QueryExecutor, projectID, object, explorer.Command)
			}
			break
		}
	}
	return page, explorer, true
}

func (h *BrowserHandler) dataExplorerSignalsForAssetCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, assetID string, command projectsignals.DataExplorerCommand) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, projectview.DevelopAssetView, bool) {
	_, assets, _, ok := h.assets(w, r)
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, projectview.DevelopAssetView{}, false
	}
	asset, found := projectview.AssetByID(assets, assetID)
	if !found || (asset.Type != string(projectview.AssetTypeModelTable) && asset.Type != string(projectview.AssetTypeSemanticModel)) {
		stdhttp.NotFound(w, r)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, projectview.DevelopAssetView{}, false
	}

	if asset.Type == string(projectview.AssetTypeModelTable) {
		command.Mode = projectsignals.Pointer("browse")
		command.ObjectKey = projectsignals.Pointer(asset.ID)
	} else {
		command.Mode = projectsignals.Pointer("explore")
		explore := projectsignals.DataExploreCommand{}
		if command.Explore != nil {
			explore = *command.Explore
		}
		explore.ModelID = projectsignals.Pointer(asset.ID)
		command.Explore = &explore
	}

	page, explorer, ok := h.dataExplorerSignalsForCommand(w, r, command)
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, projectview.DevelopAssetView{}, false
	}
	objects := make([]projectsignals.DataExplorerObjectSignal, 0, len(explorer.Objects))
	for _, object := range explorer.Objects {
		include := asset.Type == string(projectview.AssetTypeModelTable) && explorer.SelectedObject != nil && object.Key == explorer.SelectedObject.Key
		include = include || asset.Type == string(projectview.AssetTypeSemanticModel) && projectsignals.ValueOrZero(object.ModelID) == asset.ID
		if include {
			objects = append(objects, object)
		}
	}
	explorer.Objects = objects
	page.Context.ObjectCount = int64(len(objects))
	if explorer.SelectedObject != nil {
		selected := false
		for _, object := range objects {
			if object.Key == explorer.SelectedObject.Key {
				selected = true
				break
			}
		}
		if !selected {
			explorer.SelectedKey = nil
			explorer.SelectedObject = nil
			page.SelectedObject = nil
		}
	}
	if asset.Type == string(projectview.AssetTypeSemanticModel) {
		models := make([]projectsignals.DataExploreModelSignal, 0, 1)
		for _, model := range explorer.Explore.Models {
			if model.ID == asset.ID {
				models = append(models, model)
			}
		}
		explorer.Explore.Models = models
	}
	return page, explorer, asset, true
}

func (h *BrowserHandler) layout(r *stdhttp.Request) webpage.Provider {
	if h.Layout == nil {
		return nil
	}
	return h.Layout(r)
}

func (h *BrowserHandler) csrf(r *stdhttp.Request) string {
	if h.CSRFToken == nil {
		return ""
	}
	return h.CSRFToken(r)
}

func writeDocument(w stdhttp.ResponseWriter, node g.Node) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := node.Render(w); err != nil {
		return
	}
}
