package http

import (
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListDashboards(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	catalog := metrics.Catalog()
	out := make([]api.DashboardSummary, 0, len(catalog.Dashboards))
	for _, row := range catalog.Dashboards {
		out = append(out, dashboardSummaryDTO(row))
	}
	principalID := ""
	if h.CurrentPrincipalID != nil {
		principalID = h.CurrentPrincipalID(r)
	}
	out, err := h.filterAuthorizedDashboards(r.Context(), principalID, out)
	if err != nil {
		if errors.Is(err, ErrDashboardAuthorizationUnavailable) {
			writeJSONError(w, err, nethttp.StatusServiceUnavailable)
			return
		}
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	page, nextCursor, ok := pageSliceForRequest(w, r, out)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.DashboardListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) GetDashboard(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	dashboardID := chi.URLParam(r, "dashboard")
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		writeJSONError(w, fmt.Errorf("dashboard %q not found", dashboardID), nethttp.StatusNotFound)
		return
	}
	report, model := resolved.Definition, resolved.Model
	writeJSON(w, nethttp.StatusOK, DashboardManifestProjection(report, model, metrics.Pages(dashboardID)))
}

func (h Handler) ListDashboardComponents(w nethttp.ResponseWriter, r *nethttp.Request) {
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	out := make([]api.DashboardComponentResponse, 0, len(page.Visuals))
	for _, component := range page.PlacedVisuals() {
		out = append(out, dashboardComponentDTO(component, report, page))
	}
	items, nextCursor, ok := pageSliceForRequest(w, r, out)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.DashboardComponentListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) GetDashboardPage(w nethttp.ResponseWriter, r *nethttp.Request) {
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	components := make([]api.DashboardComponentResponse, 0, len(page.Visuals))
	for _, component := range page.PlacedVisuals() {
		components = append(components, dashboardComponentDTO(component, report, page))
	}
	writeJSON(w, nethttp.StatusOK, api.DashboardPageResponse{
		ID: page.ID, Title: page.Title, Description: page.Description, Components: components,
	})
}

func (h Handler) GetDashboardFilter(w nethttp.ResponseWriter, r *nethttp.Request) {
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	filterID := chi.URLParam(r, "filter")
	binding, exists := filterBindingForPage(report, page, filterID)
	if !exists {
		writeJSONError(w, fmt.Errorf("filter %q not found", filterID), nethttp.StatusNotFound)
		return
	}
	filter, exists := report.FilterDefinitions[binding.Filter]
	if !exists {
		writeJSONError(w, fmt.Errorf("filter definition %q not found", binding.Filter), nethttp.StatusNotFound)
		return
	}
	component, _ := pageComponentForFilter(page, binding.Scope, binding.ID)
	bindingResponse := map[string]any{
		"key": binding.Key, "id": binding.ID, "filter": binding.Filter, "scope": binding.Scope,
		"default": binding.Default, "selectionMode": binding.Selection.Mode,
		"maxSelectedValues": binding.Selection.MaxSelectedValues, "readerEditable": binding.Editable(),
		"paneVisible": binding.Pane.IsVisible(), "paneOrder": binding.Pane.Order,
		"targets": binding.Targets, "optionDependencies": binding.OptionDependencies,
	}
	if binding.PageID != "" {
		bindingResponse["pageID"] = binding.PageID
	}
	if binding.URL.Param != "" {
		bindingResponse["urlParam"] = binding.URL.Param
	}
	if binding.Pane.Label != "" {
		bindingResponse["paneLabel"] = binding.Pane.Label
	}
	response := map[string]any{
		"definition": map[string]any{
			"id": binding.Filter, "label": filter.Label, "description": filter.Description,
			"field": filter.Field, "dataset": filter.Dataset, "valueKind": filter.ValueKind,
			"predicates": filter.Predicates, "options": filter.Options,
			"formatPattern": filter.Formatting.Pattern, "formatUnit": filter.Formatting.Unit,
			"timezone": filter.Time.Timezone, "calendar": filter.Time.Calendar, "weekStart": filter.Time.WeekStart,
		},
		"binding": bindingResponse,
	}
	if component.ID != "" {
		response["componentId"] = component.ID
		response["presentation"] = component.Presentation
		response["placement"] = componentPlacement(component)
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func (h Handler) GetDashboardVisual(w nethttp.ResponseWriter, r *nethttp.Request) {
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	visualID := chi.URLParam(r, "visual")
	component, onPage := pageComponentForVisual(page, visualID)
	definition, exists := report.Visualizations[visualID]
	if !exists {
		writeJSONError(w, fmt.Errorf("visual %q not found", visualID), nethttp.StatusNotFound)
		return
	}
	if !onPage {
		writeJSONError(w, fmt.Errorf("visual %q not found on page %q", visualID, page.ID), nethttp.StatusNotFound)
		return
	}
	writeJSON(w, nethttp.StatusOK, DashboardVisualProjection(definition, component))
}

func (h Handler) QueryDashboardPage(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.DashboardPageQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	dashboardID := chi.URLParam(r, "dashboard")
	filters, err := dashboardQueryFilters(report, page.ID, input.FilterState, input.InteractionSelections, input.SpatialSelections)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx := dataquery.WithMetadata(r.Context(), h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIQuery, "dashboard_page", dashboardID+":"+page.ID))
	patch, err := metrics.QueryDashboardPage(ctx, dashboardID, page.ID, filters)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	visuals := make(map[string]visualizationir.VisualizationEnvelope, len(patch.Visuals))
	for id, envelope := range patch.Visuals {
		visuals[id] = envelope
	}
	writeJSON(w, nethttp.StatusOK, map[string]any{"filterState": dashboardAPIFilterState(filters), "status": patch.Status, "visuals": visuals})
}

func (h Handler) QueryDashboardVisualData(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.DashboardVisualQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	visualID := chi.URLParam(r, "visual")
	definition, exists := report.Visualizations[visualID]
	if !exists {
		writeJSONError(w, fmt.Errorf("visual %q not found", visualID), nethttp.StatusNotFound)
		return
	}
	_, onPage := pageComponentForVisual(page, visualID)
	if !onPage {
		writeJSONError(w, fmt.Errorf("visual %q not found on page %q", visualID, page.ID), nethttp.StatusNotFound)
		return
	}
	if isGridQueryKind(definition.Query.Kind) {
		h.queryDashboardTabularVisual(w, r, metrics, page, visualID, input)
		return
	}
	compact := requestsCompactDashboardVisual(r)
	if !compact && (input.Limit != 0 || input.PageToken != "") {
		writeJSONError(w, fmt.Errorf("pagination is only supported for table, matrix, and pivot visuals"), nethttp.StatusBadRequest)
		return
	}
	if acceptsDashboardMediaType(r.Header.Get("Accept"), dashboardArrowMediaType) {
		writeJSONError(w, fmt.Errorf("Arrow output is only supported for table, matrix, and pivot visuals"), nethttp.StatusNotAcceptable)
		return
	}
	dashboardID := chi.URLParam(r, "dashboard")
	filters, err := dashboardQueryFilters(report, page.ID, input.FilterState, input.InteractionSelections, input.SpatialSelections)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	start, limit := 0, maxAgentDashboardVisualRows
	cursorInput := input
	cursorInput.PageToken = ""
	snapshot, snapshotErr := dashboardServingSnapshot(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	scope := dashboardRequestCursorScope(r, cursorInput)
	if compact {
		if input.Limit > 0 {
			limit = min(input.Limit, maxAgentDashboardVisualRows)
		}
		var err error
		start, err = decodeIndexCursor(input.PageToken, scope, snapshot)
		if err != nil {
			status := nethttp.StatusBadRequest
			if errors.Is(err, errDashboardCursorSnapshot) {
				status = nethttp.StatusConflict
			}
			writeJSONError(w, err, status)
			return
		}
	}
	ctx := dataquery.WithMetadata(r.Context(), h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIQuery, "dashboard_visual", dashboardID+":"+visualID))
	patch, err := metrics.QueryDashboardPage(ctx, dashboardID, page.ID, filters)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	visual, ok := patch.Visuals[visualID]
	if !ok {
		writeJSONError(w, fmt.Errorf("visual %q data not found", visualID), nethttp.StatusNotFound)
		return
	}
	if compact {
		result, err := h.dashboardVisualAgentProjection(r, metrics, visual, filters, start, limit, scope, snapshot)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusInternalServerError)
			return
		}
		writeJSON(w, nethttp.StatusOK, result)
		return
	}
	writeJSON(w, nethttp.StatusOK, visual)
}

func isGridQueryKind(kind visualizationdefinition.QueryKind) bool {
	return kind == visualizationdefinition.QueryDetail || kind == visualizationdefinition.QueryMatrix || kind == visualizationdefinition.QueryPivot
}

func (h Handler) queryDashboardTabularVisual(w nethttp.ResponseWriter, r *nethttp.Request, metrics Metrics, page dashboard.Page, visualID string, input api.DashboardVisualQueryRequest) {
	limit := input.Limit
	if limit <= 0 {
		limit = 100
	}
	if requestsCompactDashboardVisual(r) && limit > maxAgentDashboardVisualRows {
		limit = maxAgentDashboardVisualRows
	}
	if limit > dashboard.TableMaxRequestCount {
		limit = dashboard.TableMaxRequestCount
	}
	cursorInput := input
	cursorInput.PageToken = ""
	snapshot, snapshotErr := dashboardServingSnapshot(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	scope := dashboardRequestCursorScope(r, cursorInput)
	start, err := decodeIndexCursor(input.PageToken, scope, snapshot)
	if err != nil {
		status := nethttp.StatusBadRequest
		if errors.Is(err, errDashboardCursorSnapshot) {
			status = nethttp.StatusConflict
		}
		writeJSONError(w, err, status)
		return
	}
	dashboardID := chi.URLParam(r, "dashboard")
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		writeJSONError(w, fmt.Errorf("dashboard %q not found", dashboardID), nethttp.StatusNotFound)
		return
	}
	report := resolved.Definition
	filters, err := dashboardQueryFilters(report, page.ID, input.FilterState, input.InteractionSelections, input.SpatialSelections)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx := dataquery.WithMetadata(r.Context(), h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIQuery, "dashboard_visual", dashboardID+":"+visualID))
	definition, exists := resolved.Visualization(visualID)
	if !exists {
		writeJSONError(w, fmt.Errorf("compiled visualization %q not found", visualID), nethttp.StatusInternalServerError)
		return
	}
	request := visualizationir.VisualizationWindowRequest{VisualID: visualID, SpecRevision: definition.SpecRevision, DataRevision: 1, BlockID: "a", Start: int64(start), Limit: int64(limit)}
	envelope, err := metrics.QueryVisualizationWindow(ctx, dashboardID, page.ID, filters, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	if requestsCompactDashboardVisual(r) {
		result, err := h.dashboardVisualAgentProjection(r, metrics, envelope, filters, start, limit, scope, snapshot)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusInternalServerError)
			return
		}
		writeJSON(w, nethttp.StatusOK, result)
		return
	}
	if !acceptsDashboardMediaType(r.Header.Get("Accept"), dashboardArrowMediaType) {
		writeJSON(w, nethttp.StatusOK, envelope)
		return
	}
	rowset, err := dashboardVisualizationRowset(envelope, request.BlockID, start, limit, scope, snapshot)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	writeDashboardTableRowset(w, r, rowset, envelope)
}

func (h Handler) ListDashboardFilterOptions(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.DashboardPageQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	report, page, ok := h.dashboardReportPage(w, r)
	if !ok {
		return
	}
	filterID := chi.URLParam(r, "filter")
	binding, exists := filterBindingForPage(report, page, filterID)
	if !exists {
		writeJSONError(w, fmt.Errorf("filter %q not found", filterID), nethttp.StatusNotFound)
		return
	}
	definition, exists := report.FilterDefinitions[binding.Filter]
	if !exists {
		writeJSONError(w, fmt.Errorf("filter definition %q not found", binding.Filter), nethttp.StatusNotFound)
		return
	}
	filters, err := dashboardQueryFilters(report, page.ID, input.FilterState, input.InteractionSelections, input.SpatialSelections)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	dashboardID := chi.URLParam(r, "dashboard")
	out := make([]api.DashboardFilterOptionResponse, 0, len(definition.Options.Values))
	for _, option := range definition.Options.Values {
		out = append(out, api.DashboardFilterOptionResponse{Value: fmt.Sprint(option.Value.Value), Label: option.Label})
	}
	if definition.Options.Kind == "distinct" {
		queryMetrics, supported := metrics.(compiledFilterOptionMetrics)
		if !supported {
			writeJSONError(w, fmt.Errorf("compiled filter options are not supported by this runtime"), nethttp.StatusNotImplemented)
			return
		}
		dependencies := map[string]dashboardfilter.Expression{}
		keysByReference := map[dashboardfilter.BindingRef]string{}
		for key, candidate := range report.CompiledFilterBindings() {
			if candidate.Scope == dashboardfilter.ScopeReport || candidate.PageID == page.ID {
				keysByReference[dashboardfilter.BindingRef{Scope: candidate.Scope, ID: candidate.ID}] = key
			}
		}
		if filters.CompiledState != nil {
			for _, reference := range binding.OptionDependencies {
				key := keysByReference[reference]
				if applied, ok := filters.CompiledState.AppliedControls[key]; ok && applied.Expression.Kind != dashboardfilter.ExpressionUnfiltered {
					dependencies[key] = applied.ResolvedExpression
				}
			}
		}
		result, err := queryMetrics.QueryCompiledFilterOptions(r.Context(), dashboardID, dashboardfilter.OptionQuery{
			Field: definition.Field, Dataset: definition.Dataset, ValueKind: definition.ValueKind,
			Dependencies: dependencies, Limit: 200,
		})
		if err != nil {
			writeJSONError(w, err, nethttp.StatusBadRequest)
			return
		}
		out = make([]api.DashboardFilterOptionResponse, 0, len(result.Items))
		for _, option := range result.Items {
			out = append(out, api.DashboardFilterOptionResponse{Value: fmt.Sprint(option.Value.Value), Label: option.Label})
		}
	}
	items, nextCursor, ok := pageSliceForRequest(w, r, out)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.DashboardFilterOptionListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) biMetrics(w nethttp.ResponseWriter, r *nethttp.Request) (Metrics, bool) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("project %q not found", chi.URLParam(r, "project")), nethttp.StatusNotFound)
		return nil, false
	}
	return metrics, true
}

func (h Handler) dashboardReportPage(w nethttp.ResponseWriter, r *nethttp.Request) (dashboarddefinition.Definition, dashboard.Page, bool) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return dashboarddefinition.Definition{}, dashboard.Page{}, false
	}
	dashboardID := chi.URLParam(r, "dashboard")
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		writeJSONError(w, fmt.Errorf("dashboard %q not found", dashboardID), nethttp.StatusNotFound)
		return dashboarddefinition.Definition{}, dashboard.Page{}, false
	}
	report := resolved.Definition
	pageID := chi.URLParam(r, "page")
	pages := metrics.Pages(dashboardID)
	if pages == nil {
		pages = report.Pages
	}
	for _, page := range pages {
		if page.ID == pageID {
			return report, page.WithDefaults(), true
		}
	}
	writeJSONError(w, fmt.Errorf("page %q not found", pageID), nethttp.StatusNotFound)
	return dashboarddefinition.Definition{}, dashboard.Page{}, false
}

func (h Handler) requestQueryMetadata(r *nethttp.Request, surface, operation, objectType, objectID string) dataquery.Metadata {
	if surface == dataquery.SurfaceAPI && r.Header.Get("X-LeapView-Client") == dataquery.SurfaceCLI {
		surface = dataquery.SurfaceCLI
	}
	metadata := dataquery.Metadata{
		Surface:       surface,
		Operation:     requestQueryOperation(operation, objectType),
		ObjectType:    objectType,
		ObjectID:      objectID,
		RequestID:     r.Header.Get("X-Request-ID"),
		CorrelationID: r.Header.Get("X-Correlation-ID"),
	}
	if projectID, err := projectgraph.NewResourceID(strings.TrimSpace(chi.URLParam(r, "project"))); err == nil {
		metadata.ProjectID = projectID
	}
	if h.CurrentPrincipalID != nil {
		metadata.PrincipalID = h.CurrentPrincipalID(r)
	}
	existing := dataquery.MetadataFromContext(r.Context())
	if existing.ProjectID != "" {
		metadata.ProjectID = existing.ProjectID
	}
	if existing.Surface != "" {
		metadata.Surface = existing.Surface
	}
	if existing.Operation != "" {
		metadata.Operation = existing.Operation
	}
	if existing.PrincipalID != "" {
		metadata.PrincipalID = existing.PrincipalID
	}
	if existing.RequestID != "" {
		metadata.RequestID = existing.RequestID
	}
	if existing.ObjectType != "" {
		metadata.ObjectType = existing.ObjectType
	}
	if existing.ObjectID != "" {
		metadata.ObjectID = existing.ObjectID
	}
	if existing.CorrelationID != "" {
		metadata.CorrelationID = existing.CorrelationID
	}
	return metadata
}

func requestQueryOperation(operation, objectType string) string {
	if operation != dataquery.OperationAPIQuery {
		return operation
	}
	switch objectType {
	case "dashboard_page", "dashboard_visual", "dashboard_filter":
		return ""
	default:
		return operation
	}
}

func dashboardQueryFilters(
	definition dashboarddefinition.Definition,
	pageID string,
	rawState map[string]any,
	rawSelections []map[string]any,
	rawSpatialSelections []map[string]any,
) (dashboard.Filters, error) {
	filters := definition.DefaultFiltersForPage(pageID)
	if len(rawState) > 0 {
		var input struct {
			Version  string                     `json:"version"`
			Controls map[string]json.RawMessage `json:"controls"`
		}
		encoded, err := json.Marshal(rawState)
		if err != nil {
			return dashboard.Filters{}, fmt.Errorf("encode filterState: %w", err)
		}
		if err := json.Unmarshal(encoded, &input); err != nil {
			return dashboard.Filters{}, fmt.Errorf("decode filterState: %w", err)
		}
		if input.Version != "typed_v1" {
			return dashboard.Filters{}, fmt.Errorf("filterState version must be typed_v1")
		}
		machine := dashboardfilter.NewMachine(dashboardfilter.ApplicationImmediate, definition.FilterBindingSpecs())
		bindings := definition.CompiledFilterBindings()
		keys := make([]string, 0, len(input.Controls))
		for key := range input.Controls {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			binding, ok := bindings[key]
			if !ok || binding.Scope == dashboardfilter.ScopePage && binding.PageID != pageID {
				return dashboard.Filters{}, fmt.Errorf("unknown filter binding %q for page %q", key, pageID)
			}
			var expression dashboardfilter.Expression
			if err := json.Unmarshal(input.Controls[key], &expression); err != nil {
				return dashboard.Filters{}, fmt.Errorf("filter binding %q: %w", key, err)
			}
			state := machine.State()
			if _, err := machine.Execute(dashboardfilter.Command{
				Kind: dashboardfilter.CommandMutate, BaseRevision: state.Revision,
				ClientMutationID: "api:" + key, BindingKey: key,
				Operation: dashboardfilter.MutationSet, Expression: &expression,
			}); err != nil {
				return dashboard.Filters{}, fmt.Errorf("filter binding %q: %w", key, err)
			}
		}
		state := machine.State()
		filters.CompiledState = &state
	}
	if err := decodeDashboardSelectionState(rawSelections, &filters.Selections); err != nil {
		return dashboard.Filters{}, fmt.Errorf("interactionSelections: %w", err)
	}
	if err := decodeDashboardSelectionState(rawSpatialSelections, &filters.SpatialSelections); err != nil {
		return dashboard.Filters{}, fmt.Errorf("spatialSelections: %w", err)
	}
	return filters, nil
}

func decodeDashboardSelectionState[T any](raw []map[string]any, target *[]T) error {
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func dashboardAPIFilterState(filters dashboard.Filters) map[string]any {
	controls := map[string]dashboardfilter.Expression{}
	if filters.CompiledState != nil {
		for key, applied := range filters.CompiledState.AppliedControls {
			controls[key] = applied.Expression
		}
	}
	return map[string]any{"version": "typed_v1", "controls": controls}
}

func dashboardSummaryDTO(row catalog.Dashboard) api.DashboardSummary {
	return api.DashboardSummary{
		ID:            row.ID.String(),
		Title:         row.Title,
		Description:   row.Description,
		SemanticModel: row.SemanticModel.String(),
		Tags:          row.Tags,
		PageCount:     row.PageCount,
	}
}

func dashboardComponentDTO(component dashboard.PageVisual, report dashboarddefinition.Definition, page dashboard.Page) api.DashboardComponentResponse {
	summary := dashboardComponentSummary(component, report, page)
	out := api.DashboardComponentResponse{
		ID:          component.ID,
		Kind:        summary.Kind,
		Ref:         summary.Ref,
		Title:       summary.Title,
		Description: component.Description,
		X:           component.X,
		Y:           component.Y,
		Width:       component.Width,
		Height:      component.Height,
	}
	if !component.Placement.IsZero() {
		out.Placement = &api.DashboardComponentPlacement{
			Col:     component.Placement.Col,
			Row:     component.Placement.Row,
			ColSpan: component.Placement.ColSpan,
			RowSpan: component.Placement.RowSpan,
		}
	}
	switch out.Kind {
	case "visual":
		out.VisualID = out.Ref
	case "slicer":
		out.FilterID = out.Ref
	}
	return out
}

func componentPlacement(component dashboard.PageVisual) *api.DashboardComponentPlacement {
	if component.Placement.IsZero() {
		return nil
	}
	return &api.DashboardComponentPlacement{
		Col: component.Placement.Col, Row: component.Placement.Row,
		ColSpan: component.Placement.ColSpan, RowSpan: component.Placement.RowSpan,
	}
}

func DashboardVisualProjection(definition visualizationdefinition.Definition, component dashboard.PageVisual) api.DashboardVisualDescribeResponse {
	return api.DashboardVisualDescribeResponse{
		ID: definition.ID, ComponentID: component.ID, RendererID: definition.RendererID,
		SpecRevision: definition.SpecRevision, Spec: definition.Spec, Placement: componentPlacement(component),
		X: component.X, Y: component.Y, Width: component.Width, Height: component.Height,
	}
}

func dashboardVisualizationDefinitionDTO(definition visualizationdefinition.Definition, component dashboard.PageVisual) api.DashboardVisualDescribeResponse {
	return DashboardVisualProjection(definition, component)
}

func modelSummary(model *semanticmodel.Model) *api.ModelRef {
	if model == nil {
		return nil
	}
	return &api.ModelRef{ID: model.Name, Title: model.Title}
}

func DashboardManifestProjection(report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page) api.DashboardManifestResponse {
	if pages == nil {
		pages = report.Pages
	}
	out := api.DashboardManifestResponse{
		ID:            report.ID,
		Title:         report.Title,
		Description:   report.Description,
		SemanticModel: report.SemanticModel,
		Model:         modelSummary(model),
		Counts: api.DashboardManifestCounts{
			Pages:   len(pages),
			Visuals: len(report.Visualizations),
			Filters: len(report.FilterDefinitions),
		},
		Pages: make([]api.DashboardManifestPage, 0, len(pages)),
		DetailTools: map[string]string{
			"model":       "catalog_get",
			"page_data":   "catalog_get",
			"visual_data": "query_dashboard_visual",
		},
	}
	for _, page := range pages {
		pageSummary := api.DashboardManifestPage{
			ID:          page.ID,
			Title:       page.Title,
			Description: page.Description,
			Components:  make([]api.DashboardManifestComponent, 0, len(page.Visuals)),
		}
		for _, component := range page.Visuals {
			pageSummary.Components = append(pageSummary.Components, dashboardComponentSummary(component, report, page))
		}
		out.Pages = append(out.Pages, pageSummary)
	}
	return out
}

func dashboardComponentSummary(component dashboard.PageVisual, report dashboarddefinition.Definition, page dashboard.Page) api.DashboardManifestComponent {
	switch {
	case component.Visual != "":
		title := component.Title
		if title == "" {
			title = dashboarddefinition.SpecTitle(report.Visualizations[component.Visual].Spec)
		}
		return api.DashboardManifestComponent{ID: component.ID, Kind: "visual", Ref: component.Visual, Title: title}
	case component.Binding.ID != "":
		title := component.Title
		if title == "" {
			if binding, ok := filterBindingForPage(report, page, component.Binding.ID); ok {
				title = report.FilterDefinitions[binding.Filter].Label
			}
		}
		return api.DashboardManifestComponent{ID: component.ID, Kind: "slicer", Ref: component.Binding.ID, Title: title}
	default:
		kind := component.Kind
		if kind == "" {
			kind = "component"
		}
		return api.DashboardManifestComponent{ID: component.ID, Kind: kind, Title: component.Title}
	}
}

func pageComponentForVisual(page dashboard.Page, visualID string) (dashboard.PageVisual, bool) {
	for _, component := range page.PlacedVisuals() {
		if component.Visual == visualID {
			return component, true
		}
	}
	return dashboard.PageVisual{}, false
}

func pageComponentForFilter(page dashboard.Page, scope dashboardfilter.Scope, bindingID string) (dashboard.PageVisual, bool) {
	for _, component := range page.PlacedVisuals() {
		if component.Binding.Scope == scope && component.Binding.ID == bindingID {
			return component, true
		}
	}
	return dashboard.PageVisual{}, false
}

func filterBindingForPage(report dashboarddefinition.Definition, page dashboard.Page, bindingID string) (dashboardfilter.Binding, bool) {
	if binding, ok := page.FilterBindings[bindingID]; ok {
		return binding, true
	}
	if binding, ok := report.FilterBindings[bindingID]; ok {
		return binding, true
	}
	return dashboardfilter.Binding{}, false
}
