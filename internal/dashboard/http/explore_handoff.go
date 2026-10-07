package http

import (
	"context"
	stdhttp "net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/explorehandoff"
	"github.com/flidai/leapview/internal/dashboard/report"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// ExploreVisualization redirects an authorized app dashboard visual into
// Data Explorer when its compiled query and live session state are both
// representable there.
func (h Handler) ExploreVisualization(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if strings.TrimSpace(h.RouteScope.BasePath) != "" {
		stdhttp.NotFound(w, r)
		return
	}
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		stdhttp.NotFound(w, r)
		return
	}
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	pageID := strings.TrimSpace(chi.URLParam(r, "page"))
	visualID := strings.TrimSpace(chi.URLParam(r, "visual"))
	if dashboardID == "" || pageID == "" || visualID == "" {
		stdhttp.NotFound(w, r)
		return
	}
	if !dashboardHandoffQuery(r.URL.Query()) {
		stdhttp.Error(w, "unsupported dashboard handoff context", stdhttp.StatusBadRequest)
		return
	}

	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		stdhttp.NotFound(w, r)
		return
	}
	page, found := report.ActivePage(metrics.Pages(dashboardID), pageID)
	if !found || page.ID != pageID || !pageContainsVisual(page, visualID) {
		stdhttp.NotFound(w, r)
		return
	}
	visualDefinition, found := resolved.Visualization(visualID)
	if !found {
		stdhttp.NotFound(w, r)
		return
	}
	spec, eligible := explorehandoff.SpecForVisual(visualDefinition, resolved.Model)
	if !eligible {
		stdhttp.Error(w, "dashboard visual query cannot be represented in Data Explorer", stdhttp.StatusUnprocessableEntity)
		return
	}
	allowed, err := h.authorizeExplorerHandoff(r, resolved.Model, spec)
	if err != nil {
		stdhttp.Error(w, "Data Explorer authorization is unavailable", dashboardSemanticAuthorizationStatus(err))
		return
	}
	if !allowed {
		stdhttp.NotFound(w, r)
		return
	}

	clientID := strings.TrimSpace(r.URL.Query().Get("clientId"))
	streamInstanceID := strings.TrimSpace(r.URL.Query().Get("streamInstanceId"))
	if clientID == "" || streamInstanceID == "" || h.SessionStore == nil {
		stdhttp.Error(w, "dashboard session is unavailable", stdhttp.StatusConflict)
		return
	}
	key, err := h.dashboardSessionKey(r, resolved.Definition, clientID, streamInstanceID)
	if err != nil {
		stdhttp.NotFound(w, r)
		return
	}
	record, err := h.SessionStore.Load(r.Context(), key)
	if err != nil || record.Key.ID() != key.ID() || record.State.ActivePage != pageID {
		stdhttp.Error(w, "dashboard session changed; reopen the dashboard before exploring", stdhttp.StatusConflict)
		return
	}
	filters := dashboard.Filters{CompiledState: &record.State.Filters.State, ActivePageID: pageID}
	if decodeDashboardSelectionState(record.State.InteractionSelections, &filters.Selections) != nil || decodeDashboardSelectionState(record.State.SpatialSelections, &filters.SpatialSelections) != nil {
		stdhttp.Error(w, "dashboard selections are unavailable", stdhttp.StatusConflict)
		return
	}
	spec, eligible = explorehandoff.SpecForState(resolved.Definition, resolved.Model, visualID, pageID, filters)
	if !eligible {
		stdhttp.Error(w, "current dashboard filters or selections cannot be carried into Data Explorer", stdhttp.StatusConflict)
		return
	}

	allowed, err = h.authorizeExplorerHandoff(r, resolved.Model, spec)
	if err != nil || !allowed {
		stdhttp.NotFound(w, r)
		return
	}
	href, err := explorehandoff.TargetHref(h.RouteScope.BasePath, dashboardID, pageID, visualID, spec)
	if err != nil {
		stdhttp.Error(w, "could not create Data Explorer link", stdhttp.StatusInternalServerError)
		return
	}
	stdhttp.Redirect(w, r, href, stdhttp.StatusSeeOther)
}

func (h Handler) authorizeExplorerHandoff(r *stdhttp.Request, model *semanticmodel.Model, spec exploration.ExplorationSpec) (bool, error) {
	return h.authorizeExplorerHandoffCached(r, model, spec, newExploreHandoffAuthorizationCache())
}

type exploreHandoffAuthorizationCache struct {
	projectChecked bool
	projectErr     error
	modelReads     map[string]exploreModelReadDecision
	contexts       map[string]exploreModelContext
	checks         map[string]error
}

type exploreModelReadDecision struct {
	allowed bool
	err     error
}

type exploreModelContext struct {
	ctx context.Context
	err error
}

func newExploreHandoffAuthorizationCache() *exploreHandoffAuthorizationCache {
	return &exploreHandoffAuthorizationCache{
		modelReads: map[string]exploreModelReadDecision{},
		contexts:   map[string]exploreModelContext{},
		checks:     map[string]error{},
	}
}

func (h Handler) authorizeExplorerHandoffCached(r *stdhttp.Request, model *semanticmodel.Model, spec exploration.ExplorationSpec, cache *exploreHandoffAuthorizationCache) (bool, error) {
	if h.CurrentPrincipalID == nil || h.AuthorizeListResource == nil {
		return false, errDashboardSemanticAuthorityUnavailable
	}
	principalID := strings.TrimSpace(h.CurrentPrincipalID(r))
	if principalID == "" {
		return false, errDashboardSemanticAuthorityUnavailable
	}
	if !cache.projectChecked {
		_, cache.projectErr = h.projectIDForRequest(r.Context())
		cache.projectChecked = true
	}
	if cache.projectErr != nil {
		return false, cache.projectErr
	}
	modelID := strings.TrimSpace(spec.ModelID)
	readDecision, checked := cache.modelReads[modelID]
	if !checked {
		modelResourceID, err := projectgraph.NewResourceID(modelID)
		if err == nil {
			var modelResource access.ResourceRef
			modelResource, err = access.NewResourceRef(modelResourceID, projectgraph.KindSemanticModel)
			if err == nil {
				readDecision.allowed, err = h.AuthorizeListResource(r.Context(), principalID, modelResource, access.CapabilityResourceRead)
			}
		}
		readDecision.err = err
		cache.modelReads[modelID] = readDecision
	}
	if readDecision.err != nil || !readDecision.allowed {
		return readDecision.allowed, readDecision.err
	}
	if model == nil || spec.DatasetID == nil {
		return false, errDashboardSemanticAuthorityUnavailable
	}
	modelContext, contextFound := cache.contexts[modelID]
	if !contextFound {
		modelContext.ctx, modelContext.err = dashboardSemanticConsumerForRequest(r.Context(), h.Metrics, modelID)
		cache.contexts[modelID] = modelContext
	}
	if modelContext.err != nil {
		return false, modelContext.err
	}
	dataset := strings.TrimSpace(*spec.DatasetID)
	if err := cache.authorize("dataset\x00"+modelID+"\x00"+dataset, func() error {
		return authorizeDashboardSemanticTarget(modelContext.ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: dataset})
	}); err != nil {
		return false, err
	}
	authorizeDimension := func(field, dataset string) error {
		if _, semanticErr := model.ResolveSemanticDimension(field); semanticErr == nil {
			return cache.authorize("dimension\x00"+modelID+"\x00"+dataset+"\x00"+field, func() error {
				return authorizeDashboardSemanticTarget(modelContext.ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: dataset, Dimension: field})
			})
		}
		return cache.authorize("field\x00"+modelID+"\x00"+dataset+"\x00"+field, func() error {
			return authorizeDashboardSemanticField(modelContext.ctx, h.Metrics, modelID, dataset, field)
		})
	}
	for _, dimension := range spec.Dimensions {
		if err := authorizeDimension(dimension.Field, dataset); err != nil {
			return false, err
		}
	}
	if spec.Time != nil {
		if err := authorizeDimension(spec.Time.Field, dataset); err != nil {
			return false, err
		}
	}
	for _, filter := range spec.Filters {
		filterDataset := dataset
		if filter.DatasetID != nil {
			filterDataset = *filter.DatasetID
		}
		if err := authorizeDimension(filter.Field, filterDataset); err != nil {
			return false, err
		}
	}
	for _, metric := range spec.Metrics {
		if err := cache.authorize("metric\x00"+modelID+"\x00"+metric.Field, func() error {
			return authorizeDashboardSemanticTarget(modelContext.ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Metric: metric.Field})
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (c *exploreHandoffAuthorizationCache) authorize(key string, check func() error) error {
	if err, exists := c.checks[key]; exists {
		return err
	}
	err := check()
	c.checks[key] = err
	return err
}

func dashboardHandoffQuery(values url.Values) bool {
	if len(values) != 2 {
		return false
	}
	for _, key := range []string{"clientId", "streamInstanceId"} {
		if len(values[key]) != 1 || strings.TrimSpace(values.Get(key)) == "" {
			return false
		}
	}
	return true
}

func pageContainsVisual(page dashboard.Page, visualID string) bool {
	for _, component := range page.PlacedVisuals() {
		if component.Visual == visualID {
			return true
		}
	}
	return false
}

func (h Handler) decorateExploreHrefs(envelope dashboardstream.Envelope, event dashboardstream.RefreshEvent, definition dashboarddefinition.Definition, model *semanticmodel.Model, page dashboard.Page, clientID, streamInstanceID string) dashboardstream.Envelope {
	return h.decorateExploreHrefsForAuthorizedVisuals(envelope, event, definition, model, page, clientID, streamInstanceID, nil)
}

func (h Handler) decorateExploreHrefsForAuthorizedVisuals(envelope dashboardstream.Envelope, event dashboardstream.RefreshEvent, definition dashboarddefinition.Definition, model *semanticmodel.Model, page dashboard.Page, clientID, streamInstanceID string, authorized map[string]bool) dashboardstream.Envelope {
	visuals, ok := envelope.Signals["visuals"]
	if !ok {
		return envelope
	}
	attach := func(visualID string, value any) any {
		href := ""
		_, found := definition.Visualizations[visualID]
		if found && authorized[visualID] && pageContainsVisual(page, visualID) {
			if _, eligible := explorehandoff.SpecForState(definition, model, visualID, page.ID, event.Filters); eligible {
				href, _ = explorehandoff.RouteHref(h.RouteScope.BasePath, definition.ID, page.ID, visualID, clientID, streamInstanceID)
			}
		}
		switch signal := value.(type) {
		case map[string]any:
			signal["exploreHref"] = href
			return signal
		case dashboarddatastar.VisualizationSignal:
			signal.ExploreHref = href
			return signal
		default:
			return value
		}
	}
	switch values := visuals.(type) {
	case map[string]any:
		for visualID, value := range values {
			values[visualID] = attach(visualID, value)
		}
		if event.Type == dashboardstream.RefreshEventStart && event.Command != "visual_window" {
			for _, component := range page.PlacedVisuals() {
				if component.Visual == "" {
					continue
				}
				if _, exists := values[component.Visual]; !exists {
					values[component.Visual] = attach(component.Visual, map[string]any{})
				}
			}
		}
	case map[string]dashboarddatastar.VisualizationSignal:
		for visualID, value := range values {
			values[visualID] = attach(visualID, value).(dashboarddatastar.VisualizationSignal)
		}
	}
	envelope.Signals["visuals"] = visuals
	return envelope
}

func (h Handler) authorizedExploreVisuals(r *stdhttp.Request, definition dashboarddefinition.Definition, model *semanticmodel.Model, page dashboard.Page) map[string]bool {
	if strings.TrimSpace(h.RouteScope.BasePath) != "" {
		return map[string]bool{}
	}
	authorized := make(map[string]bool)
	cache := newExploreHandoffAuthorizationCache()
	for _, component := range page.PlacedVisuals() {
		visualID := strings.TrimSpace(component.Visual)
		if visualID == "" {
			continue
		}
		visualDefinition, found := definition.Visualizations[visualID]
		if !found {
			continue
		}
		spec, eligible := explorehandoff.SpecForVisual(visualDefinition, model)
		if !eligible {
			continue
		}
		allowed, err := h.authorizeExplorerHandoffCached(r, model, spec, cache)
		if err == nil && allowed {
			authorized[visualID] = true
		}
	}
	return authorized
}
