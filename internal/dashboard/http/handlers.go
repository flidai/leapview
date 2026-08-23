package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	nethttp "net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/dashboard/usage"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type publicPresentationContextKey struct{}

// ErrDashboardAuthorizationUnavailable is returned when a dashboard list
// cannot evaluate its resource policy. Lists must fail closed rather than
// exposing the catalog when composition omitted the authorization callback.
var ErrDashboardAuthorizationUnavailable = errors.New("dashboard authorization is unavailable")

type PublicPresentation struct {
	PublicID     string
	Presentation string
}

func WithPublicPresentation(ctx context.Context, value PublicPresentation) context.Context {
	return context.WithValue(ctx, publicPresentationContextKey{}, value)
}

func publicPresentationFromContext(ctx context.Context) (PublicPresentation, bool) {
	value, ok := ctx.Value(publicPresentationContextKey{}).(PublicPresentation)
	return value, ok
}

type Metrics interface {
	consumer.Executor
	Catalog() catalog.Catalog
	DefaultDashboardID() string
	Resolver() dashboardresolver.Resolver
	DefaultFilters(dashboardID string) dashboard.Filters
	ModelIDForDashboard(dashboardID string) string
	NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest
	Pages(dashboardID string) []dashboard.Page
	QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error)
	QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error)
}

// resolveDashboard is the transport boundary for the capability-owned
// resolver. Project composition must select a concrete resolver before an
// HTTP dashboard lookup; no caller-controlled project argument is accepted.
func resolveDashboard(metrics Metrics, dashboardID string) (dashboardresolver.Resolved, error) {
	if metrics == nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	resolver := metrics.Resolver()
	if resolver == nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	identity, err := projectgraph.NewResourceID(strings.TrimSpace(dashboardID))
	if err != nil {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return resolver.Resolve(identity)
}

// ResolveDashboard exposes the shared transport adapter to dashboard module
// compositions that need the same compiled definition/model pair.
func ResolveDashboard(metrics Metrics, dashboardID string) (dashboardresolver.Resolved, error) {
	return resolveDashboard(metrics, dashboardID)
}

type SignalBroker interface {
	Subscribe(streamID string) (<-chan pagestream.SignalPatch, func())
	PublishEnvelope(streamID string, envelope pagestream.Envelope)
	TraceStore() *pagestream.TraceStore
}

type SharedCommandPrepare func(
	r *nethttp.Request,
	request command.Request,
	signals dashboard.Signals,
	prepare func(dashboard.Filters) (command.PreparedRefresh, error),
) (command.PreparedRefresh, uint64, error)

// AuthoringApplication is the narrow browser-facing authoring boundary. The
// implementation is the composed dashboard authoring application; keeping the
// interface here lets focused HTTP tests inject a fake without reaching into
// repository or runtime internals.
type AuthoringApplication interface {
	Builder(context.Context, builderview.Request) (uisignals.DashboardBuilderSignal, error)
	Execute(context.Context, projectgraph.ResourceID, authoring.Command) (authoringservice.Result, error)
	ExecuteIntent(context.Context, application.IntentRequest) (authoringservice.Result, error)
	Preview(context.Context, preview.PreviewRequest) (preview.Preview, error)
	ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error)
	ExportDraftYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error)
}

type browserDraftCreator interface {
	Create(context.Context, authoringservice.CreateRequest) (authoringservice.Result, error)
	Fork(context.Context, sourceadapter.ForkRequest) (authoringservice.Result, error)
}

// ProjectObjectRefs binds browser-level draft creation to the active project
// selected by composition. It is deliberately exported for the module router
// so a create action cannot smuggle a project selector through the URL.
func ProjectObjectRefs(_ *nethttp.Request, projectID projectgraph.ResourceID) []access.ResourceRef {
	ref, err := access.NewResourceRef(projectID, projectgraph.KindProject)
	if err != nil {
		return nil
	}
	return []access.ResourceRef{ref}
}

type SessionKeyFactory func(
	r *nethttp.Request,
	report dashboarddefinition.Definition,
	clientID string,
	streamInstanceID string,
) (dashboardsession.Key, error)

type Handler struct {
	Metrics Metrics
	// ProjectID is the stable graph project resource selected by app
	// composition. It is deliberately not taken from a route segment. When
	// ResolveProjectID is configured, the lease-bound resolver is authoritative.
	ProjectID               projectgraph.ResourceID
	ResolveProjectID        func(context.Context) (projectgraph.ResourceID, error)
	AnalyticalContext       func(context.Context) context.Context
	Broker                  SignalBroker
	Coordinators            *dashboardstream.Registry
	Logger                  *slog.Logger
	RefreshStarted          dashboardstream.StartObserver
	RefreshFinished         dashboardstream.SummaryObserver
	RefreshEventObserved    dashboardstream.EventPublisher
	CacheObserved           dataquery.CacheOutcomeObserver
	CurrentPrincipalID      func(r *nethttp.Request) string
	CurrentUsagePrincipal   func(r *nethttp.Request) (string, bool)
	RecordDashboardView     func(context.Context, usage.View) error
	AuthorizeListResource   func(ctx context.Context, principalID string, resource access.ResourceRef, capability access.Capability) (bool, error)
	CSRFToken               func(r *nethttp.Request) string
	Layout                  func(r *nethttp.Request) webpage.Provider
	Presentation            reportui.Presentation
	Assets                  staticasset.Resolver
	Environment             func(*nethttp.Request) string
	DataRefreshedAt         func(context.Context, string, string, string) string
	QueryFreshness          func(context.Context, string, string, string) (api.QueryFreshness, bool)
	CommandGuard            func(*nethttp.Request, Metrics, command.Request, dashboard.Signals) error
	SharedCommandPrepare    SharedCommandPrepare
	SessionStore            dashboardsession.Store
	SessionKey              SessionKeyFactory
	OptionCursorSecret      []byte
	OptionCache             *dashboardfilter.OptionCache
	AgentBootstrap          func(*nethttp.Request, string) reportui.AgentBootstrap
	AgentCommands           reportui.AgentCommandBindings
	RouteScope              reportui.RouteScope
	StreamNamespace         string
	SpatialTileStreamClosed func(Metrics, string)
	Authoring               AuthoringApplication
}

func (h Handler) projectIDForRequest(ctx context.Context) (projectgraph.ResourceID, error) {
	if h.ResolveProjectID != nil {
		projectID, err := h.ResolveProjectID(ctx)
		if err != nil {
			return "", err
		}
		if err := projectID.Validate(); err != nil {
			return "", err
		}
		return projectID, nil
	}
	if h.ProjectID == "" {
		if routeProject := strings.TrimSpace(chi.URLParamFromCtx(ctx, "project")); routeProject != "" {
			projectID, err := projectgraph.NewResourceID(routeProject)
			if err != nil {
				return "", err
			}
			return projectID, nil
		}
	}
	if err := h.ProjectID.Validate(); err != nil {
		return "", err
	}
	return h.ProjectID, nil
}

func commandDashboardID(r *nethttp.Request, signals dashboard.Signals) (string, bool) {
	routeID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	if routeID == "" {
		return "", false
	}
	if queryID := strings.TrimSpace(r.URL.Query().Get("dashboard")); queryID != "" && queryID != routeID {
		return "", false
	}
	if signalID := strings.TrimSpace(signals.Runtime.DashboardID); signalID != "" && signalID != routeID {
		return "", false
	}
	return routeID, true
}

func commandModelMatches(r *nethttp.Request, signals dashboard.Signals, modelID string) bool {
	if requested := strings.TrimSpace(r.URL.Query().Get("model")); requested != "" && requested != modelID {
		return false
	}
	if requested := strings.TrimSpace(signals.Runtime.ModelID); requested != "" && requested != modelID {
		return false
	}
	return true
}

func (h Handler) scopedStreamID(streamID string) string {
	namespace := strings.TrimSpace(h.StreamNamespace)
	if namespace == "" {
		return streamID
	}
	return namespace + ":" + streamID
}

func (h Handler) dashboardSessionKey(r *nethttp.Request, definition dashboarddefinition.Definition, clientID, streamInstanceID string) (dashboardsession.Key, error) {
	if h.SessionKey != nil {
		return h.SessionKey(r, definition, clientID, streamInstanceID)
	}
	principalOrClient := clientID
	if h.CurrentPrincipalID != nil {
		if principalID := h.CurrentPrincipalID(r); principalID != "" {
			principalOrClient = principalID + ":" + clientID
		}
	}
	projectID, err := h.projectIDForRequest(r.Context())
	if err != nil {
		return dashboardsession.Key{}, err
	}
	dashboardID, err := projectgraph.NewResourceID(definition.ID)
	if err != nil {
		return dashboardsession.Key{}, err
	}
	return dashboardsession.Key{
		ProjectID:         projectID,
		PrincipalOrClient: principalOrClient,
		DashboardID:       dashboardID,
		ServingStateID:    definition.DefaultFilterState().DefaultsRevision,
		StreamInstanceID:  streamInstanceID,
	}, nil
}

func (h Handler) analyticalContext(ctx context.Context) context.Context {
	if h.AnalyticalContext == nil {
		return ctx
	}
	return h.AnalyticalContext(ctx)
}

func (h Handler) analyticalStreamContext(ctx context.Context, streamID string) context.Context {
	ctx = h.analyticalContext(ctx)
	metadata := dataquery.MetadataFromContext(ctx)
	metadata.StreamID = streamID
	return dataquery.WithMetadata(ctx, metadata)
}

func (h Handler) filterAuthorizedDashboards(ctx context.Context, principalID string, rows []api.DashboardSummary) ([]api.DashboardSummary, error) {
	if h.AuthorizeListResource == nil {
		return nil, ErrDashboardAuthorizationUnavailable
	}
	out := make([]api.DashboardSummary, 0, len(rows))
	for _, row := range rows {
		resourceID, err := projectgraph.NewResourceID(strings.TrimSpace(row.ID))
		if err != nil {
			return nil, fmt.Errorf("invalid dashboard resource ID %q: %w", row.ID, err)
		}
		resource, err := access.NewResourceRef(resourceID, projectgraph.KindDashboard)
		if err != nil {
			return nil, err
		}
		allowed, err := h.AuthorizeListResource(ctx, principalID, resource, access.CapabilityResourceRead)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, row)
		}
	}
	return out, nil
}

func DashboardObjectRefs(r *nethttp.Request, _ projectgraph.ResourceID) []access.ResourceRef {
	objects := []access.ResourceRef{}
	if dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard")); dashboardID != "" {
		resourceID, err := projectgraph.NewResourceID(dashboardID)
		if err != nil {
			return nil
		}
		if resource, err := access.NewResourceRef(resourceID, projectgraph.KindDashboard); err == nil {
			objects = append(objects, resource)
		}
	}
	return objects
}

func (h Handler) Dashboard(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	dashboardID := chi.URLParam(r, "dashboard")
	pages := metrics.Pages(dashboardID)
	if len(pages) == 0 {
		nethttp.NotFound(w, r)
		return
	}
	base := ""
	if h.RouteScope.BasePath != "" {
		base = strings.TrimSuffix(h.RouteScope.BasePath, "/")
	}
	nethttp.Redirect(w, r, base+"/dashboards/"+dashboardID+"/pages/"+pages[0].ID, nethttp.StatusFound)
}

func (h Handler) Page(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.RenderPage(w, r, chi.URLParam(r, "dashboard"), chi.URLParam(r, "page"))
}

func (h Handler) RenderPage(w nethttp.ResponseWriter, r *nethttp.Request, dashboardID, pageID string) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	clientID := pagestream.EnsureClientID(w, r)
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	reportDefinition, model := resolved.Definition, resolved.Model
	pages := metrics.Pages(dashboardID)
	activePage, ok := report.ActivePage(pages, pageID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	initialFilters := reportDefinition.FiltersFromURLForPage(activePage.ID, r.URL.Query())
	filterState, err := reportDefinition.FilterStateFromURL(activePage.ID, r.URL.Query())
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	initialFilters.CompiledState = &filterState
	w.WriteHeader(nethttp.StatusOK)
	csrfToken := ""
	if h.CSRFToken != nil {
		csrfToken = h.CSRFToken(r)
	}
	var providers []webpage.Provider
	if h.Layout != nil {
		providers = []webpage.Provider{h.Layout(r)}
	}
	if err := reportui.PageWithRouteScopeAndAgentCommands(h.Presentation, h.RouteScope, clientID, csrfToken, metrics.Catalog(), reportDefinition, model, pages, activePage, initialFilters, h.AgentCommands, providers...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) metricsForRequest(_ *nethttp.Request) (Metrics, bool) {
	return h.Metrics, h.Metrics != nil
}
