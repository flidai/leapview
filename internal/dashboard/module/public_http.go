package module

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	lddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/flidai/leapview/internal/dashboard/publication"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type ResolvedPublicDashboard struct {
	Publication publication.Publication
	Metrics     dashboardhttp.Metrics
	Report      dashboarddefinition.Definition
	ModelID     string
}

type PublicTelemetry struct {
	DocumentObserved func(presentation, outcome string)
	StreamStarted    func(presentation string) func()
	CommandObserved  func(command, outcome string)
}

func (m *Module) observePublicDocument(presentation, outcome string) {
	if m != nil && m.publicTelemetry.DocumentObserved != nil {
		m.publicTelemetry.DocumentObserved(presentation, outcome)
	}
}

func (m *Module) observePublicStream(presentation string) func() {
	if m != nil && m.publicTelemetry.StreamStarted != nil {
		return m.publicTelemetry.StreamStarted(presentation)
	}
	return func() {}
}

func (m *Module) observePublicCommand(command, outcome string) {
	if m != nil && m.publicTelemetry.CommandObserved != nil {
		m.publicTelemetry.CommandObserved(command, outcome)
	}
}

func (m *Module) ResolvePublicDashboard(ctx context.Context, publicID string) (ResolvedPublicDashboard, error) {
	if m == nil {
		return ResolvedPublicDashboard{}, publication.ErrNotFound
	}
	row, err := m.ResolvePublic(ctx, strings.TrimSpace(publicID))
	if err != nil {
		return ResolvedPublicDashboard{}, publication.ErrNotFound
	}
	if m.handler.Metrics == nil {
		return ResolvedPublicDashboard{}, publication.ErrNotFound
	}
	metrics := m.handler.Metrics
	resolvedDashboard, err := dashboardhttp.ResolveDashboard(metrics, row.Dashboard)
	if err != nil {
		return ResolvedPublicDashboard{}, publication.ErrNotFound
	}
	return ResolvedPublicDashboard{
		Publication: row, Metrics: metrics, Report: resolvedDashboard.Definition,
		ModelID: resolvedDashboard.Definition.SemanticModel,
	}, nil
}

func (m *Module) PublicDashboardDocument(presentation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resolved, err := m.ResolvePublicDashboard(r.Context(), chi.URLParam(r, "publicId"))
		if err != nil {
			m.observePublicDocument(presentation, "not_found")
			http.NotFound(w, r)
			return
		}
		pageID := strings.TrimSpace(chi.URLParam(r, "page"))
		if pageID == "" {
			pageID = resolved.Publication.DefaultPage
		}
		pages := resolved.Metrics.Pages(resolved.Publication.Dashboard)
		activePage, ok := reportdef.ActivePage(pages, pageID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		resolvedDashboard, err := dashboardhttp.ResolveDashboard(resolved.Metrics, resolved.Publication.Dashboard)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		model := resolvedDashboard.Model
		SetPublicDashboardSecurityHeaders(w.Header(), presentation, resolved.Publication.AllowedOrigins)
		m.observePublicDocument(presentation, "success")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		initialFilters := resolved.Report.FiltersFromURLForPage(activePage.ID, r.URL.Query())
		filterState, err := resolved.Report.FilterStateFromURL(activePage.ID, r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		initialFilters.CompiledState = &filterState
		if err := reportui.PublicPage(reportui.PublicPageOptions{
			PublicID: resolved.Publication.PublicID, Presentation: presentation, Assets: m.handler.Assets,
		}, resolved.Metrics.Catalog(), resolved.Report, model, pages, activePage, initialFilters).Render(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (m *Module) PublicDashboardUpdates(w http.ResponseWriter, r *http.Request) {
	resolved, err := m.ResolvePublicDashboard(r.Context(), chi.URLParam(r, "publicId"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clientID := strings.TrimSpace(r.URL.Query().Get("clientId"))
	streamInstanceID := strings.TrimSpace(r.URL.Query().Get("streamInstance"))
	pageID := strings.TrimSpace(r.URL.Query().Get("page"))
	if pageID == "" {
		pageID = resolved.Publication.DefaultPage
	}
	if clientID == "" || streamInstanceID == "" || !publicationPageExists(resolved.Metrics.Pages(resolved.Publication.Dashboard), pageID) {
		http.NotFound(w, r)
		return
	}
	presentation := strings.TrimSpace(r.URL.Query().Get("presentation"))
	if presentation != reportui.PresentationEmbed {
		presentation = reportui.PresentationPublic
	}
	streamID := lddatastar.StreamID(clientID, resolved.Publication.Dashboard, pageID, streamInstanceID)
	version := publication.StreamVersion{PublicID: resolved.Publication.PublicID, ServingStateID: resolved.Publication.ServingStateID}
	initialFilters := resolved.Report.NormalizeFiltersForPage(pageID, resolved.Report.FiltersFromURLForPage(pageID, r.URL.Query()))
	filterState, err := resolved.Report.FilterStateFromURL(pageID, r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	initialFilters.CompiledState = &filterState
	ctx, unregister, err := m.streams.Register(r.Context(), resolved.Publication.ID, streamID, version, initialFilters)
	if err != nil {
		http.Error(w, "public dashboard stream is unavailable", http.StatusServiceUnavailable)
		return
	}
	defer unregister()
	streamFinished := m.observePublicStream(presentation)
	defer streamFinished()
	query := r.URL.Query()
	query.Set("project", resolved.Publication.ProjectID.String())
	query.Set("dashboard", resolved.Publication.Dashboard)
	query.Set("model", resolved.ModelID)
	query.Set("page", pageID)
	r.URL.RawQuery = query.Encode()
	ctx = PublicationExecutionContext(ctx, resolved.Publication, resolved.ModelID)
	ctx = dashboardruntime.WithPublicSpatialTiles(ctx, resolved.Publication.PublicID)
	ctx = dashboardhttp.WithPublicPresentation(ctx, dashboardhttp.PublicPresentation{PublicID: resolved.Publication.PublicID, Presentation: presentation})
	SetPublicDashboardSecurityHeaders(w.Header(), presentation, resolved.Publication.AllowedOrigins)
	m.PublicDashboardHTTP(resolved).Updates(w, r.WithContext(ctx))
}

func (m *Module) PublicDashboardCommand(commandName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resolved, err := m.ResolvePublicDashboard(r.Context(), chi.URLParam(r, "publicId"))
		if err != nil {
			m.observePublicCommand(commandName, "not_found")
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		query.Set("project", resolved.Publication.ProjectID.String())
		query.Set("dashboard", resolved.Publication.Dashboard)
		query.Set("model", resolved.ModelID)
		r.URL.RawQuery = query.Encode()
		ctx := PublicationExecutionContext(r.Context(), resolved.Publication, resolved.ModelID)
		ctx = dashboardruntime.WithPublicSpatialTiles(ctx, resolved.Publication.PublicID)
		SetPublicDashboardSecurityHeaders(w.Header(), reportui.PresentationPublic, resolved.Publication.AllowedOrigins)
		recorder := &apihttpmiddleware.Recorder{ResponseWriter: w, StatusCode: http.StatusOK}
		handler := m.PublicDashboardHTTP(resolved)
		switch commandName {
		case "filter":
			handler.FilterCommand(recorder, r.WithContext(ctx))
		case "filter_options":
			handler.FilterOptions(recorder, r.WithContext(ctx))
		case "navigate":
			handler.Navigate(recorder, r.WithContext(ctx))
		case "select":
			handler.Select(recorder, r.WithContext(ctx))
		case "spatial_select":
			handler.SpatialSelect(recorder, r.WithContext(ctx))
		case "clear_selection":
			handler.ClearSelection(recorder, r.WithContext(ctx))
		case "visual_window":
			handler.VisualWindow(recorder, r.WithContext(ctx))
		default:
			http.NotFound(recorder, r)
		}
		outcome := "accepted"
		if recorder.StatusCode >= http.StatusBadRequest {
			outcome = "rejected"
		}
		m.observePublicCommand(commandName, outcome)
	}
}

func (m *Module) PublicDashboardHTTP(resolved ResolvedPublicDashboard) dashboardhttp.Handler {
	handler := m.HTTP()
	handler.Metrics = resolved.Metrics
	handler.Broker = m.publicBroker
	handler.CSRFToken = nil
	handler.Layout = nil
	handler.SessionKey = func(_ *http.Request, definition dashboarddefinition.Definition, clientID, streamInstanceID string) (dashboardsession.Key, error) {
		dashboardID, err := projectgraph.NewResourceID(definition.ID)
		if err != nil {
			return dashboardsession.Key{}, err
		}
		return dashboardsession.Key{
			PublicationID:     resolved.Publication.ID,
			PrincipalOrClient: clientID,
			DashboardID:       dashboardID,
			ServingStateID:    resolved.Publication.ServingStateID,
			StreamInstanceID:  streamInstanceID,
		}, nil
	}
	handler.CommandGuard = func(r *http.Request, _ dashboardhttp.Metrics, request command.Request, signals dashboard.Signals) error {
		current, err := m.PublicationByPublicID(r.Context(), resolved.Publication.PublicID)
		if err != nil || current.Status() != publication.StatusActive || current.ID != resolved.Publication.ID || current.ServingStateID != resolved.Publication.ServingStateID {
			return publication.ErrNotFound
		}
		if request.DashboardID != resolved.Publication.Dashboard || request.ModelID != resolved.ModelID || !publicationPageExists(resolved.Metrics.Pages(resolved.Publication.Dashboard), request.PageID) {
			return fmt.Errorf("command target is outside publication")
		}
		if signals.Runtime.ClientID == "" || signals.Runtime.StreamInstanceID == "" {
			return fmt.Errorf("public command requires stream identity")
		}
		streamID := lddatastar.StreamID(signals.Runtime.ClientID, request.DashboardID, request.PageID, signals.Runtime.StreamInstanceID)
		version := publication.StreamVersion{PublicID: resolved.Publication.PublicID, ServingStateID: resolved.Publication.ServingStateID}
		if !m.streams.Active(resolved.Publication.ID, streamID, version) {
			return fmt.Errorf("public command stream is not active")
		}
		return nil
	}
	handler.SharedCommandPrepare = func(r *http.Request, request command.Request, signals dashboard.Signals, prepare func(dashboard.Filters) (command.PreparedRefresh, error)) (command.PreparedRefresh, uint64, error) {
		streamID := lddatastar.StreamID(signals.Runtime.ClientID, request.DashboardID, request.PageID, signals.Runtime.StreamInstanceID)
		version := publication.StreamVersion{PublicID: resolved.Publication.PublicID, ServingStateID: resolved.Publication.ServingStateID}
		return m.streams.PrepareCommand(r.Context(), resolved.Publication.ID, streamID, version, prepare)
	}
	return handler
}

func PublicationExecutionContext(ctx context.Context, row publication.Publication, modelID string) context.Context {
	principalID := "dashboard_publication:" + row.ProjectID.String() + "." + strings.TrimSpace(row.Name)
	dashboard := canonicalPublicationResource(row.Dashboard, projectgraph.KindDashboard)
	model := canonicalPublicationResource(modelID, projectgraph.KindSemanticModel)
	dependencies := make([]access.ResourceRef, 0, len(row.DependencyAssetIDs))
	for _, id := range row.DependencyAssetIDs {
		if resource := canonicalPublicationResourceID(id); resource.Validate() == nil {
			dependencies = append(dependencies, resource)
		}
	}
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{
		ProjectID: row.ProjectID, Surface: dataquery.SurfacePublicDashboard,
		PrincipalID: principalID, ObjectType: "dashboard_publication", ObjectID: row.Name,
	})
	return queryauthz.WithDashboardPublicationCapability(ctx, queryauthz.DashboardPublicationCapability{
		ProjectID: row.ProjectID, Publication: row.Name,
		Dashboard: dashboard, ModelID: model, DependencyAssetIDs: dependencies,
	})
}

func canonicalPublicationResource(id string, kind projectgraph.Kind) access.ResourceRef {
	resourceID, err := projectgraph.NewResourceID(strings.TrimSpace(id))
	if err != nil {
		return access.ResourceRef{}
	}
	resource, err := access.NewResourceRef(resourceID, kind)
	if err != nil {
		return access.ResourceRef{}
	}
	return resource
}

func canonicalPublicationResourceID(id string) access.ResourceRef {
	parts := strings.SplitN(strings.TrimSpace(id), ":", 2)
	if len(parts) != 2 {
		return access.ResourceRef{}
	}
	kind := projectgraph.Kind(parts[0])
	// Stable resource IDs use the authored `semantic-model:` prefix, while the
	// graph's descriptive kind is `semantic_model`. Keep identity and kind
	// separate when reconstructing the publication capability so the semantic
	// model is not silently dropped from its dependency closure.
	if parts[0] == "semantic-model" {
		kind = projectgraph.KindSemanticModel
	}
	return canonicalPublicationResource(id, kind)
}

func publicationPageExists(pages []dashboard.Page, pageID string) bool {
	for _, page := range pages {
		if page.ID == pageID {
			return true
		}
	}
	return false
}

func SetPublicDashboardSecurityHeaders(header http.Header, presentation string, origins []string) {
	frameAncestors := "'none'"
	if presentation == reportui.PresentationEmbed {
		header.Del("X-Frame-Options")
		if len(origins) > 0 {
			allowed := append([]string(nil), origins...)
			sort.Strings(allowed)
			frameAncestors = strings.Join(allowed, " ")
		}
	} else {
		header.Set("X-Frame-Options", "DENY")
	}
	header.Set("Content-Security-Policy", apihttpmiddleware.ContentSecurityPolicy(apihttpmiddleware.ContentSecurityPolicyConfig{
		BaseURI:             "'none'",
		FrameAncestors:      frameAncestors,
		FormAction:          "'none'",
		DatastarExpressions: true,
		DynamicStyles:       true,
	}))
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Robots-Tag", "noindex")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
}
