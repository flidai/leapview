package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	httpmodel "github.com/flidai/leapview/internal/platform/http/model"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
	"github.com/go-chi/chi/v5"
)

type Metrics interface {
	Catalog() dashboard.Catalog
	ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error)
	Pages(dashboardID string) []dashboard.Page
	Resolver() dashboardresolver.Resolver
	SemanticModel(modelID string) (*semanticmodel.Model, bool)
}

type consumerPlannerProvider interface {
	Planner(modelID string) (consumer.Planner, bool)
}

type Handler struct {
	Metrics               Metrics
	ResolveProjectID      func(context.Context) (projectgraph.ResourceID, error)
	CurrentPrincipalID    func(r *nethttp.Request) string
	AuthorizeListResource func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error)
	QueryFreshness        func(ctx context.Context, projectID, modelID, servingSnapshot string) (api.QueryFreshness, bool)
}

var errSemanticAuthorizationUnavailable = errors.New("semantic model authorization is unavailable")
var errSemanticModelActivationUnavailable = errors.New("active semantic model planner is unavailable")

func (h Handler) authorizeSemanticModel(r *nethttp.Request, modelID string) (bool, error) {
	if h.AuthorizeListResource == nil {
		return false, errSemanticAuthorizationUnavailable
	}
	projectID, err := h.projectIDForRequest(r.Context())
	if err != nil {
		return false, err
	}
	resourceID, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return false, err
	}
	resource, err := access.NewResourceRef(resourceID, projectgraph.KindSemanticModel)
	if err != nil {
		return false, err
	}
	principalID := ""
	if h.CurrentPrincipalID != nil {
		principalID = h.CurrentPrincipalID(r)
	}
	return h.AuthorizeListResource(r.Context(), principalID, projectID, resource, access.CapabilityResourceRead)
}

func (h Handler) ListSemanticModels(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	catalog := metrics.Catalog()
	out := make([]api.SemanticModelSummary, 0, len(catalog.Models))
	for _, row := range catalog.Models {
		out = append(out, semanticModelSummaryDTO(row))
	}
	filtered := make([]api.SemanticModelSummary, 0, len(out))
	for _, row := range out {
		allowed, err := h.authorizeSemanticModel(r, row.ID)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusServiceUnavailable)
			return
		}
		if allowed {
			filtered = append(filtered, row)
		}
	}
	out = filtered
	page, nextCursor, ok := pageSliceForRequest(w, r, out)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticModelListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) GetSemanticModel(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	modelID := chi.URLParam(r, "model")
	model, ok := SemanticModelProjection(metrics, modelID)
	if !ok {
		if semanticModelActivationUnavailable(metrics, modelID) {
			writeJSONError(w, errSemanticModelActivationUnavailable, nethttp.StatusServiceUnavailable)
			return
		}
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return
	}
	writeJSON(w, nethttp.StatusOK, model)
}

func (h Handler) ListSemanticModelFields(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	fields := SemanticModelFieldsProjection(model)
	items, nextCursor, ok := pageSliceForRequest(w, r, fields)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticFieldListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) ListSemanticRelationships(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	items := make([]api.SemanticRelationshipResponse, 0, len(model.Relationships))
	for _, relationship := range model.Relationships {
		item, err := semanticRelationshipDTO(relationship)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticRelationshipListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) ListSemanticSources(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	names := make([]string, 0, len(model.Sources))
	for name := range model.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]api.SemanticSourceResponse, 0, len(names))
	for _, name := range names {
		source := model.Sources[name]
		items = append(items, api.SemanticSourceResponse{
			ID: name, Kind: source.Format, Connection: source.Connection,
			Table: source.Object, Description: source.Description,
		})
	}
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticSourceListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) QuerySemanticModel(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.SemanticQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	modelID := chi.URLParam(r, "model")
	if semanticModelForID(metrics, modelID) == nil {
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	scope := semanticAggregateCursorScope(r, input)
	queryID, queryIDErr := queryIDForRequest(r)
	if queryIDErr != nil {
		writeJSONError(w, queryIDErr, nethttp.StatusServiceUnavailable)
		return
	}
	request, limit, err := semanticAggregateRequest("", input, true, scope, snapshot)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return
	}
	plan, err := semanticExplainAggregate(metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx := dataquery.WithMetadata(r.Context(), h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIQuery, "semantic_model", modelID))
	if acceptsMediaType(r.Header.Get("Accept"), arrowStreamMediaType) {
		writeSemanticArrowResponse(w, r.WithContext(ctx), metrics, aggregateDataQuery(modelID, request), limit, request.Offset, queryID, snapshot, scope)
		return
	}
	rows, err := executeAggregateRows(ctx, metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, statusForDataExecutionError(err))
		return
	}
	response := semanticQueryResponse(plan.Columns, rows, limit, request.Offset, queryID, snapshot, scope)
	h.enrichSemanticQueryResponse(r, metrics, modelID, request.Dimensions, request.Metrics, &request.Time, &response)
	writeSemanticQueryResponse(w, r, response)
}

func (h Handler) ExplainSemanticModelQuery(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.SemanticQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	modelID := chi.URLParam(r, "model")
	if semanticModelForID(metrics, modelID) == nil {
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	request, _, err := semanticAggregateRequest("", input, false, semanticAggregateCursorScope(r, input), snapshot)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	plan, err := semanticExplainAggregate(metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	writeJSON(w, nethttp.StatusOK, semanticExplainResponse("query", plan, semanticQueryWarnings(input.Sort)))
}

func (h Handler) ListSemanticDatasets(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	compiled := compiledSemanticModel(h.Metrics, chi.URLParam(r, "model"))
	if compiled == nil {
		writeJSONError(w, fmt.Errorf("model %q semantic dataset bindings are unavailable", chi.URLParam(r, "model")), nethttp.StatusServiceUnavailable)
		return
	}
	out := make([]api.SemanticDatasetSummary, 0, len(compiled.DatasetNames()))
	for _, datasetID := range compiled.DatasetNames() {
		dataset, _ := compiled.Dataset(datasetID)
		table := dataset.Table()
		out = append(out, api.SemanticDatasetSummary{
			ID:          datasetID,
			Model:       dataset.ModelName(),
			Description: firstSemanticNonEmpty(dataset.Description(), table.Description),
			FieldCount:  len(table.Dimensions),
			MetricCount: semanticDatasetMetricCount(model, datasetID),
		})
	}
	modelID := chi.URLParam(r, "model")
	allowed, err := h.authorizeSemanticModel(r, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	if !allowed {
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return
	}
	items, nextCursor, ok := pageSliceForRequest(w, r, out)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticDatasetListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) GetSemanticDataset(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, table, datasetID, ok := h.semanticDatasetForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, SemanticTableProjection(model, datasetID, table))
}

func (h Handler) ListSemanticFields(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, table, datasetID, ok := h.semanticDatasetForRequest(w, r)
	if !ok {
		return
	}
	fields := SemanticDatasetFieldsProjection(model, datasetID, table)
	items, nextCursor, ok := pageSliceForRequest(w, r, fields)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticFieldListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) QuerySemanticDataset(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.SemanticQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	modelID, datasetID := chi.URLParam(r, "model"), chi.URLParam(r, "dataset")
	if _, _, _, ok := h.semanticDatasetForRequest(w, r); !ok {
		return
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	scope := semanticAggregateCursorScope(r, input)
	queryID, queryIDErr := queryIDForRequest(r)
	if queryIDErr != nil {
		writeJSONError(w, queryIDErr, nethttp.StatusServiceUnavailable)
		return
	}
	request, limit, err := semanticAggregateRequest(datasetID, input, true, scope, snapshot)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return
	}
	plan, err := semanticExplainAggregate(metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx := dataquery.WithMetadata(r.Context(), h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIQuery, "semantic_dataset", modelID+":"+datasetID))
	if acceptsMediaType(r.Header.Get("Accept"), arrowStreamMediaType) {
		writeSemanticArrowResponse(w, r.WithContext(ctx), metrics, aggregateDataQuery(modelID, request), limit, request.Offset, queryID, snapshot, scope)
		return
	}
	rows, err := executeAggregateRows(ctx, metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, statusForDataExecutionError(err))
		return
	}
	response := semanticQueryResponse(plan.Columns, rows, limit, request.Offset, queryID, snapshot, scope)
	h.enrichSemanticQueryResponse(r, metrics, modelID, request.Dimensions, request.Metrics, &request.Time, &response)
	writeSemanticQueryResponse(w, r, response)
}

func (h Handler) PreviewSemanticDataset(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.SemanticPreviewRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	modelID, datasetID := chi.URLParam(r, "model"), chi.URLParam(r, "dataset")
	if _, _, _, ok := h.semanticDatasetForRequest(w, r); !ok {
		return
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	scope := semanticPreviewCursorScope(r, input)
	queryID, queryIDErr := queryIDForRequest(r)
	if queryIDErr != nil {
		writeJSONError(w, queryIDErr, nethttp.StatusServiceUnavailable)
		return
	}
	request, limit, err := semanticRowRequest(datasetID, input, true, scope, snapshot)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return
	}
	plan, err := semanticExplainRows(metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx := dataquery.WithMetadata(r.Context(), h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIPreview, "semantic_dataset", modelID+":"+datasetID))
	if acceptsMediaType(r.Header.Get("Accept"), arrowStreamMediaType) {
		writeSemanticArrowResponse(w, r.WithContext(ctx), metrics, previewDataQuery(modelID, request), limit, request.Offset, queryID, snapshot, scope)
		return
	}
	rows, err := executePreviewRows(ctx, metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, statusForDataExecutionError(err))
		return
	}
	response := semanticQueryResponse(plan.Columns, rows, limit, request.Offset, queryID, snapshot, scope)
	h.enrichSemanticQueryResponse(r, metrics, modelID, request.Dimensions, request.Metrics, nil, &response)
	writeSemanticQueryResponse(w, r, response)
}

func (h Handler) ExplainSemanticQuery(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.SemanticQueryRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	modelID, datasetID := chi.URLParam(r, "model"), chi.URLParam(r, "dataset")
	if _, _, _, ok := h.semanticDatasetForRequest(w, r); !ok {
		return
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	request, _, err := semanticAggregateRequest(datasetID, input, false, semanticAggregateCursorScope(r, input), snapshot)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	plan, err := semanticExplainAggregate(metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	writeJSON(w, nethttp.StatusOK, semanticExplainResponse("query", plan, semanticQueryWarnings(input.Sort)))
}

func (h Handler) ExplainSemanticPreview(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	var input api.SemanticPreviewRequest
	if err := decodeOptionalJSONBody(r, &input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	modelID, datasetID := chi.URLParam(r, "model"), chi.URLParam(r, "dataset")
	if _, _, _, ok := h.semanticDatasetForRequest(w, r); !ok {
		return
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return
	}
	request, _, err := semanticRowRequest(datasetID, input, false, semanticPreviewCursorScope(r, input), snapshot)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	plan, err := semanticExplainRows(metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	writeJSON(w, nethttp.StatusOK, semanticExplainResponse("preview", plan, semanticQueryWarnings(input.Sort)))
}

func (h Handler) biMetrics(w nethttp.ResponseWriter, r *nethttp.Request) (Metrics, bool) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("project metrics are unavailable"), nethttp.StatusServiceUnavailable)
		return nil, false
	}
	return metrics, true
}

func (h Handler) projectIDForRequest(ctx context.Context) (projectgraph.ResourceID, error) {
	if h.ResolveProjectID == nil {
		return "", errors.New("active project resolver is unavailable")
	}
	projectID, err := h.ResolveProjectID(ctx)
	if err != nil {
		return "", err
	}
	if err := projectID.Validate(); err != nil {
		return "", err
	}
	return projectID, nil
}

func (h Handler) metricsForRequest(r *nethttp.Request) (Metrics, bool) {
	if h.Metrics == nil {
		return nil, false
	}
	if _, err := h.projectIDForRequest(r.Context()); err != nil {
		return nil, false
	}
	return h.Metrics, true
}

func (h Handler) semanticModelForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (*semanticmodel.Model, bool) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return nil, false
	}
	modelID := chi.URLParam(r, "model")
	model := semanticModelForID(metrics, modelID)
	if model == nil {
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return nil, false
	}
	return model, true
}

func (h Handler) semanticDatasetForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (*semanticmodel.Model, semanticmodel.Table, string, bool) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return nil, semanticmodel.Table{}, "", false
	}
	datasetID := chi.URLParam(r, "dataset")
	metrics, metricsOK := h.biMetrics(w, r)
	if !metricsOK {
		return nil, semanticmodel.Table{}, "", false
	}
	compiled := compiledSemanticModel(metrics, chi.URLParam(r, "model"))
	if compiled == nil {
		writeJSONError(w, fmt.Errorf("model %q semantic dataset bindings are unavailable", chi.URLParam(r, "model")), nethttp.StatusServiceUnavailable)
		return nil, semanticmodel.Table{}, "", false
	}
	dataset, exists := compiled.Dataset(datasetID)
	if !exists {
		writeJSONError(w, fmt.Errorf("dataset %q not found", datasetID), nethttp.StatusNotFound)
		return nil, semanticmodel.Table{}, "", false
	}
	return model, dataset.Table(), datasetID, true
}

func semanticModelSummaryDTO(row dashboard.CatalogModel) api.SemanticModelSummary {
	return api.SemanticModelSummary{ID: row.ID.String(), Title: row.Title, Description: row.Description}
}

func SemanticTableProjection(model *semanticmodel.Model, datasetID string, table semanticmodel.Table) api.SemanticDatasetResponse {
	entities := make(map[string]api.SemanticEntitySummary, len(table.Entities))
	for name, entity := range table.Entities {
		entities[name] = api.SemanticEntitySummary{Type: entity.Type, Fields: append([]string(nil), entity.Fields...)}
	}
	return api.SemanticDatasetResponse{
		ID:          datasetID,
		Model:       table.ModelName,
		Description: table.Description,
		GrainEntity: table.GrainEntity,
		Entities:    entities,
		FieldCount:  len(table.Dimensions),
		MetricCount: semanticDatasetMetricCount(model, datasetID),
	}
}

func semanticDatasetMetricCount(model *semanticmodel.Model, datasetID string) int {
	if model == nil {
		return 0
	}
	count := 0
	for _, metric := range model.Metrics {
		if metric.Dataset == datasetID {
			count++
		}
	}
	return count
}

func SemanticDatasetFieldsProjection(model *semanticmodel.Model, datasetID string, table semanticmodel.Table) []api.SemanticFieldResponse {
	out := make([]api.SemanticFieldResponse, 0, len(table.Dimensions)+semanticDatasetMetricCount(model, datasetID))
	for _, fieldID := range sortedMapKeys(table.Dimensions) {
		dimension := table.Dimensions[fieldID]
		out = append(out, api.SemanticFieldResponse{
			ID:          datasetID + "." + fieldID,
			Kind:        "dimension",
			Dataset:     datasetID,
			Name:        fieldID,
			Label:       dimension.Label,
			Description: dimension.Description,
			Type:        dimension.Type,
			Datatype:    semanticDimensionDatatype(dimension),
		})
	}
	for _, metricID := range sortedMapKeys(model.Metrics) {
		metric := model.Metrics[metricID]
		if metric.Dataset != datasetID {
			continue
		}
		out = append(out, semanticMetricFieldDTO(model, metricID, datasetID, metricID, metric))
	}
	return out
}

func SemanticModelFieldsProjection(model *semanticmodel.Model) []api.SemanticFieldResponse {
	out := make([]api.SemanticFieldResponse, 0, len(model.Dimensions)+len(model.Metrics))
	for _, name := range sortedMapKeys(model.Dimensions) {
		dimension := model.Dimensions[name]
		datatype := string(dimension.Datatype)
		if datatype == "" {
			datatype = dimension.Type
		}
		out = append(out, api.SemanticFieldResponse{ID: name, Kind: "dimension", Name: name, Label: dimension.Label, Description: dimension.Description, Type: dimension.Type, Datatype: datatype, Grains: append([]string{}, dimension.Grains...)})
	}
	for _, name := range sortedMapKeys(model.Metrics) {
		metric := model.Metrics[name]
		out = append(out, api.SemanticFieldResponse{ID: name, Kind: "metric", Dataset: metric.Dataset, Name: name, Label: metric.Label, Description: metric.Description, Unit: metric.Unit, Format: metric.Format, Datatype: semanticMetricDatatype(model, metric)})
	}
	return out
}

func semanticMetricFieldDTO(model *semanticmodel.Model, id, datasetID, name string, metric semanticmodel.Metric) api.SemanticFieldResponse {
	return api.SemanticFieldResponse{
		ID:          id,
		Kind:        "metric",
		Dataset:     datasetID,
		Name:        name,
		Label:       metric.Label,
		Description: metric.Description,
		Unit:        metric.Unit,
		Format:      metric.Format,
		Datatype:    semanticMetricDatatype(model, metric),
	}
}

func semanticDimensionDatatype(dimension semanticmodel.MetricDimension) string {
	if dimension.Datatype != "" {
		return string(dimension.Datatype)
	}
	return semanticColumnType(dimension.Type)
}

func semanticMetricDatatype(model *semanticmodel.Model, metric semanticmodel.Metric) string {
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return "integer"
	}
	if metric.Aggregation == "avg" {
		return "decimal"
	}
	if model != nil && metric.Input != nil && metric.Input.Field != "" {
		if dimension, err := model.ResolveDimension(metric.Input.Field); err == nil {
			return semanticDimensionDatatype(dimension)
		}
	}
	return semanticColumnType(semanticMetricTypeFromModel(model, metric))
}

func semanticRelationshipDTO(relationship semanticmodel.Relationship) (api.SemanticRelationshipResponse, error) {
	fromDataset, fromFields, err := semanticmodel.RelationshipEndpoint(relationship, true)
	if err != nil {
		return api.SemanticRelationshipResponse{}, fmt.Errorf("relationship %q from endpoint: %w", relationship.ID, err)
	}
	toDataset, toFields, err := semanticmodel.RelationshipEndpoint(relationship, false)
	if err != nil {
		return api.SemanticRelationshipResponse{}, fmt.Errorf("relationship %q to endpoint: %w", relationship.ID, err)
	}
	return api.SemanticRelationshipResponse{
		ID: relationship.ID, FromDataset: fromDataset, FromFields: fromFields,
		ToDataset: toDataset, ToFields: toFields, Cardinality: relationship.Cardinality, Active: true,
	}, nil
}

func SemanticModelProjection(metrics Metrics, id string) (api.SemanticModelDescriptionResponse, bool) {
	catalog := metrics.Catalog()
	var catalogModel dashboard.CatalogModel
	for _, model := range catalog.Models {
		if model.ID.String() == id {
			catalogModel = model
			break
		}
	}
	if catalogModel.ID == "" {
		return api.SemanticModelDescriptionResponse{}, false
	}
	out := api.SemanticModelDescriptionResponse{
		ID:          catalogModel.ID.String(),
		Title:       catalogModel.Title,
		Description: catalogModel.Description,
		Dashboards:  dashboardsForModel(metrics, id),
	}
	if model := semanticModelForID(metrics, id); model != nil {
		compiled := compiledSemanticModel(metrics, id)
		if compiled == nil {
			return api.SemanticModelDescriptionResponse{}, false
		}
		fieldCount := 0
		for _, datasetID := range compiled.DatasetNames() {
			dataset, _ := compiled.Dataset(datasetID)
			fieldCount += len(dataset.Table().Dimensions)
		}
		out.Counts = &api.SemanticModelCounts{
			Datasets:      len(compiled.DatasetNames()),
			Fields:        fieldCount,
			Dimensions:    len(model.Dimensions),
			Metrics:       len(model.Metrics),
			Filters:       len(model.Filters),
			Relationships: len(model.Relationships),
		}
		datasets := make([]api.SemanticDatasetSummary, 0, len(compiled.DatasetNames()))
		for _, datasetID := range compiled.DatasetNames() {
			dataset, _ := compiled.Dataset(datasetID)
			table := dataset.Table()
			description := firstSemanticNonEmpty(dataset.Description(), table.Description)
			datasets = append(datasets, api.SemanticDatasetSummary{
				ID:          datasetID,
				Model:       dataset.ModelName(),
				Description: description,
				FieldCount:  len(table.Dimensions),
				MetricCount: semanticDatasetMetricCount(model, datasetID),
			})
		}
		sort.SliceStable(datasets, func(i, j int) bool { return datasets[i].ID < datasets[j].ID })
		out.Datasets = datasets
	}
	return out, true
}

func dashboardsForModel(metrics Metrics, modelID string) []api.ModelDashboardUsage {
	out := make([]api.ModelDashboardUsage, 0)
	for _, dashboardSummary := range metrics.Catalog().Dashboards {
		if metrics.Resolver() == nil {
			continue
		}
		resolved, err := metrics.Resolver().Resolve(dashboardSummary.ID)
		if err != nil || (resolved.Definition.SemanticModel != modelID && (resolved.Model == nil || resolved.Model.Name != modelID)) {
			continue
		}
		out = append(out, api.ModelDashboardUsage{
			ID:            resolved.Definition.ID,
			Title:         resolved.Definition.Title,
			SemanticModel: resolved.Definition.SemanticModel,
			Pages:         len(metrics.Pages(resolved.Definition.ID)),
		})
	}
	return out
}

func semanticModelForID(metrics Metrics, modelID string) *semanticmodel.Model {
	if model, ok := metrics.SemanticModel(modelID); ok {
		return model
	}
	for _, dashboardSummary := range metrics.Catalog().Dashboards {
		if metrics.Resolver() == nil {
			continue
		}
		resolved, err := metrics.Resolver().Resolve(dashboardSummary.ID)
		if err == nil && resolved.Model != nil && resolved.Model.Name == modelID {
			return resolved.Model
		}
	}
	return nil
}

// compiledSemanticModel obtains the activation-owned dataset bindings. API
// request paths require an activation planner; authoring models are not
// compiled on demand here.
func compiledSemanticModel(metrics Metrics, modelID string) *semanticquery.CompiledModel {
	if planner, ok := semanticPlanner(metrics, modelID); ok {
		return planner.CompiledModel()
	}
	return nil
}

func semanticModelActivationUnavailable(metrics Metrics, modelID string) bool {
	if metrics == nil || semanticModelForID(metrics, modelID) == nil {
		return false
	}
	planner, available := semanticPlanner(metrics, modelID)
	return !available || planner == nil || planner.CompiledModel() == nil
}

func semanticPlanner(metrics any, modelID string) (*semanticquery.Planner, bool) {
	provider, ok := metrics.(consumerPlannerProvider)
	if !ok {
		return nil, false
	}
	value, available := provider.Planner(modelID)
	if !available {
		return nil, false
	}
	planner, ok := value.(*semanticquery.Planner)
	if !ok || planner == nil {
		return nil, false
	}
	return planner, true
}

func firstSemanticNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func semanticAggregateRequest(datasetID string, input api.SemanticQueryRequest, includeExtraRow bool, cursorScope ...string) (reportdef.AggregateQuery, int, error) {
	limit, offset, err := semanticLimitAndOffset(input.Limit, input.PageToken, cursorScope...)
	if err != nil {
		return reportdef.AggregateQuery{}, 0, err
	}
	requestLimit := limit
	if includeExtraRow {
		requestLimit++
	}
	request := reportdef.AggregateQuery{
		Dataset:    datasetID,
		Dimensions: semanticQueryFields(input.Dimensions),
		Metrics:    semanticQueryFields(input.Metrics),
		Filters:    semanticFilters(input.Filters),
		Sort:       semanticSorts(input.Sort),
		Limit:      requestLimit,
		Offset:     offset,
	}
	if input.Time != nil {
		request.Time = reportdef.QueryTime{Field: input.Time.Field, Grain: input.Time.Grain, Alias: input.Time.Alias}
	}
	return request, limit, nil
}

func semanticRowRequest(datasetID string, input api.SemanticPreviewRequest, includeExtraRow bool, cursorScope ...string) (reportdef.RowQuery, int, error) {
	limit, offset, err := semanticLimitAndOffset(input.Limit, input.PageToken, cursorScope...)
	if err != nil {
		return reportdef.RowQuery{}, 0, err
	}
	requestLimit := limit
	if includeExtraRow {
		requestLimit++
	}
	return reportdef.RowQuery{
		Dataset:    datasetID,
		Dimensions: semanticQueryFields(input.Dimensions),
		Metrics:    semanticQueryFields(input.Metrics),
		Filters:    semanticFilters(input.Filters),
		Sort:       semanticSorts(input.Sort),
		Limit:      requestLimit,
		Offset:     offset,
	}, limit, nil
}

func semanticLimitAndOffset(limitValue int, pageToken string, cursorScope ...string) (int, int, error) {
	limit := limitValue
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	if limit > maxQueryLimit {
		limit = maxQueryLimit
	}
	offset, err := decodeIndexCursor(pageToken, cursorScope...)
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

func semanticQueryFields(fields []api.SemanticFieldRef) []reportdef.QueryField {
	out := make([]reportdef.QueryField, 0, len(fields))
	for _, field := range fields {
		out = append(out, reportdef.QueryField{Field: field.Field, Alias: field.Alias})
	}
	return out
}

func semanticExplainAggregate(metrics Metrics, modelID string, request reportdef.AggregateQuery) (semanticquery.Plan, error) {
	compiled, ok := semanticPlanner(metrics, modelID)
	if !ok {
		return semanticquery.Plan{}, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return compiled.Plan(reportdef.SemanticAggregateRequest(request))
}

func semanticExplainRows(metrics Metrics, modelID string, request reportdef.RowQuery) (semanticquery.Plan, error) {
	compiled, ok := semanticPlanner(metrics, modelID)
	if !ok {
		return semanticquery.Plan{}, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return compiled.PlanRows(reportdef.SemanticRowRequest(request))
}

func semanticFilters(filters []api.SemanticFilter) []reportdef.QueryFilter {
	out := make([]reportdef.QueryFilter, 0, len(filters))
	for _, filter := range filters {
		out = append(out, reportdef.QueryFilter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   semanticFilterGroups(filter.Groups),
		})
	}
	return out
}

func semanticFilterGroups(groups []api.SemanticFilterGroup) []reportdef.QueryFilterGroup {
	out := make([]reportdef.QueryFilterGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, reportdef.QueryFilterGroup{Filters: semanticFilters(group.Filters)})
	}
	return out
}

func semanticSorts(sorts []api.SemanticSort) []reportdef.QuerySort {
	out := make([]reportdef.QuerySort, 0, len(sorts))
	for _, sortSpec := range sorts {
		out = append(out, reportdef.QuerySort{Field: sortSpec.Field, Direction: sortSpec.Direction})
	}
	return out
}

func semanticQueryResponse(columns []string, rows reportdef.QueryRows, limit, offset int, queryID, snapshot string, cursorScope ...string) api.SemanticQueryResponse {
	encodedRows := make([][]any, 0, min(len(rows), limit))
	descriptors := make([]api.QueryColumn, len(columns))
	for index, name := range columns {
		descriptors[index] = api.QueryColumn{Name: name, Type: queryColumnType(rows, name), Nullable: queryColumnNullable(rows, name)}
	}
	for i, row := range rows {
		if i >= limit {
			break
		}
		values := make([]any, len(columns))
		for index, column := range columns {
			values[index] = queryCellValue(row[column])
		}
		encodedRows = append(encodedRows, values)
	}
	nextCursor := ""
	if len(rows) > limit {
		scopes := append(append([]string{}, cursorScope...), snapshot)
		nextCursor = encodeIndexCursor(offset+limit, scopes...)
	}
	return api.SemanticQueryResponse{
		QueryID: queryID, ServingSnapshot: snapshot, Columns: descriptors, Rows: encodedRows,
		Completeness: api.QueryCompleteness{ReturnedRows: len(encodedRows), HasMore: nextCursor != ""},
		Page:         api.PageInfo{NextCursor: nextCursor},
	}
}

func queryColumnType(rows reportdef.QueryRows, column string) string {
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}
		switch value.(type) {
		case bool:
			return "boolean"
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return "int64"
		case float32, float64:
			return "float64"
		case time.Time:
			return "timestamp"
		case json.RawMessage, map[string]any, []any:
			return "json"
		default:
			return "string"
		}
	}
	return "string"
}

func queryColumnNullable(rows reportdef.QueryRows, column string) bool {
	for _, row := range rows {
		if value, ok := row[column]; !ok || value == nil {
			return true
		}
	}
	return len(rows) == 0
}

func queryCellValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return typed
	case []byte:
		return string(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		encoded, err := json.Marshal(value)
		if err == nil && (strings.HasPrefix(string(encoded), "{") || strings.HasPrefix(string(encoded), "[")) {
			return string(encoded)
		}
		return fmt.Sprint(value)
	}
}

func (h Handler) enrichSemanticQueryResponse(
	r *nethttp.Request,
	metrics Metrics,
	modelID string,
	dimensions, metricFields []reportdef.QueryField,
	timeRef *reportdef.QueryTime,
	response *api.SemanticQueryResponse,
) {
	if response == nil {
		return
	}
	projectID, err := h.projectIDForRequest(r.Context())
	if err != nil {
		return
	}
	if model := semanticModelForID(metrics, modelID); model != nil {
		if compiled := compiledSemanticModel(metrics, modelID); compiled != nil {
			response.Columns = semanticQueryColumnsCompiled(modelID, model, compiled, response.Columns, dimensions, metricFields, timeRef)
		}
	}
	if h.QueryFreshness != nil {
		if freshness, ok := h.QueryFreshness(r.Context(), projectID.String(), modelID, response.ServingSnapshot); ok {
			response.Freshness = &freshness
		}
	}
}

func semanticQueryColumnsCompiled(
	modelID string,
	model *semanticmodel.Model,
	compiled *semanticquery.CompiledModel,
	columns []api.QueryColumn,
	dimensions, metrics []reportdef.QueryField,
	timeRef *reportdef.QueryTime,
) []api.QueryColumn {
	if compiled == nil {
		return append([]api.QueryColumn(nil), columns...)
	}
	semantic := make(map[string]api.QueryColumn, len(dimensions)+len(metrics)+1)
	for _, field := range dimensions {
		semantic[semanticOutputName(field.Field, field.Alias)] = semanticDimensionColumn(modelID, model, compiled, field)
	}
	if timeRef != nil && timeRef.Field != "" {
		field := reportdef.QueryField{Field: timeRef.Field, Alias: timeRef.Alias}
		semantic[semanticOutputName(field.Field, field.Alias)] = semanticDimensionColumn(modelID, model, compiled, field)
	}
	for _, field := range metrics {
		semantic[semanticOutputName(field.Field, field.Alias)] = semanticMetricColumn(modelID, model, compiled, field)
	}
	out := make([]api.QueryColumn, len(columns))
	for index, column := range columns {
		if descriptor, ok := semantic[column.Name]; ok {
			descriptor.Name = column.Name
			out[index] = descriptor
			continue
		}
		out[index] = column
	}
	return out
}

func semanticDimensionColumn(modelID string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, field reportdef.QueryField) api.QueryColumn {
	if compiled == nil {
		return api.QueryColumn{Name: semanticOutputName(field.Field, field.Alias), Type: "string", Nullable: true}
	}
	if model != nil {
		if dimension, ok := model.Dimensions[field.Field]; ok {
			dataType := semanticColumnType(dimension.Type)
			return api.QueryColumn{
				Name: semanticOutputName(field.Field, field.Alias), Type: dataType,
				Nullable: semanticDimensionNullable(compiled, dimension),
				FieldRef: &api.QueryFieldRef{Type: "field", ID: modelID + "." + field.Field},
				Label:    semanticLabel(dimension.Label, field.Field), Kind: "dimension",
			}
		}
	}
	if compiled != nil {
		if dimension, err := compiled.ResolveDimension(field.Field); err == nil {
			dataType := semanticColumnType(dimension.Type)
			return api.QueryColumn{
				Name: semanticOutputName(field.Field, field.Alias), Type: dataType,
				Nullable: physicalDimensionNullable(compiledSemanticTable(compiled, dimension.Table), dimension.Name),
				FieldRef: &api.QueryFieldRef{Type: "field", ID: modelID + "." + field.Field},
				Label:    semanticLabel(dimension.Label, dimension.Name), Kind: "dimension",
			}
		}
	}
	return api.QueryColumn{Name: semanticOutputName(field.Field, field.Alias), Type: "string", Nullable: true}
}

func semanticMetricColumn(modelID string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, field reportdef.QueryField) api.QueryColumn {
	if model != nil {
		if metric, ok := model.Metrics[field.Field]; ok {
			return api.QueryColumn{
				Name: semanticOutputName(field.Field, field.Alias), Type: semanticMetricTypeCompiled(compiled, metric), Nullable: metric.Empty != "zero",
				FieldRef: &api.QueryFieldRef{Type: "metric", ID: modelID + "." + field.Field},
				Label:    semanticLabel(metric.Label, field.Field), Kind: "metric", Unit: metric.Unit, Format: metric.Format,
			}
		}
	}
	return api.QueryColumn{Name: semanticOutputName(field.Field, field.Alias), Type: "string", Nullable: true}
}

func semanticMetricTypeCompiled(compiled *semanticquery.CompiledModel, metric semanticmodel.Metric) string {
	if compiled == nil {
		return "decimal"
	}
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return "int64"
	}
	if metric.Aggregation == "avg" {
		return "decimal"
	}
	if metric.Input != nil && metric.Input.Field != "" {
		if dimension, err := compiled.ResolveDimension(metric.Input.Field); err == nil {
			return semanticColumnType(dimension.Type)
		}
	}
	return "decimal"
}

func semanticMetricTypeFromModel(model *semanticmodel.Model, metric semanticmodel.Metric) string {
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return "int64"
	}
	if metric.Input != nil && metric.Input.Field != "" && model != nil {
		if dimension, err := model.ResolveDimension(metric.Input.Field); err == nil {
			return dimension.Type
		}
	}
	return "decimal"
}

func semanticColumnType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == "boolean" || strings.Contains(value, "bool"):
		return "boolean"
	case value == "date":
		return "date"
	case strings.Contains(value, "timestamp") || strings.Contains(value, "datetime"):
		return "timestamp"
	case strings.Contains(value, "int"):
		return "int64"
	case value == "number" || strings.Contains(value, "decimal") || strings.Contains(value, "numeric") ||
		strings.Contains(value, "double") || strings.Contains(value, "float") || strings.Contains(value, "real"):
		return "decimal"
	case value == "json":
		return "json"
	default:
		return "string"
	}
}

func semanticDimensionNullable(compiled *semanticquery.CompiledModel, dimension semanticmodel.SemanticDimension) bool {
	if compiled == nil {
		return true
	}
	for _, binding := range dimension.Bindings {
		physical, err := compiled.ResolveDimension(binding.Field)
		if err != nil || physicalDimensionNullable(compiledSemanticTable(compiled, physical.Table), physical.Name) {
			return true
		}
	}
	return len(dimension.Bindings) == 0
}

func compiledSemanticTable(compiled *semanticquery.CompiledModel, alias string) semanticmodel.Table {
	if compiled != nil {
		if dataset, ok := compiled.Dataset(alias); ok {
			return dataset.Table()
		}
	}
	return semanticmodel.Table{}
}

func physicalDimensionNullable(table semanticmodel.Table, field string) bool {
	for _, column := range table.Schema.Columns {
		if column.Name == field && column.Nullable != nil {
			return *column.Nullable
		}
	}
	return true
}

func semanticOutputName(field, alias string) string {
	if alias != "" {
		return alias
	}
	if index := strings.LastIndex(field, "."); index >= 0 {
		return field[index+1:]
	}
	return field
}

func semanticLabel(label, field string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	name := semanticOutputName(field, "")
	words := strings.Fields(strings.ReplaceAll(name, "_", " "))
	for index := range words {
		if words[index] != "" {
			words[index] = strings.ToUpper(words[index][:1]) + words[index][1:]
		}
	}
	return strings.Join(words, " ")
}

func queryIDForRequest(r *nethttp.Request) (string, error) {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value, nil
	}
	return "", errors.New("request ID is unavailable")
}

func servingSnapshotForRequest(r *nethttp.Request) (string, error) {
	if value := strings.TrimSpace(r.Header.Get("X-Serving-Snapshot")); value != "" {
		return value, nil
	}
	return "", errors.New("serving snapshot is unavailable")
}

func semanticExplainResponse(mode string, plan semanticquery.Plan, warnings []string) api.SemanticExplainResponse {
	if plan.Mode != "" {
		mode = plan.Mode
	}
	return api.SemanticExplainResponse{
		Mode:                 mode,
		Datasets:             append([]string{}, plan.Datasets...),
		StitchDimensions:     append([]string{}, plan.StitchDimensions...),
		PhysicalDependencies: append([]string{}, plan.PhysicalDependencies...),
		RelationshipPaths:    append([]string{}, plan.RelationshipPaths...),
		SQL:                  plan.SQL,
		Args:                 semanticExplainArgs(plan.Args),
		Columns:              append([]string{}, plan.Columns...),
		Warnings:             warnings,
		EffectiveOrdering:    semanticSortResponse(plan.EffectiveOrdering),
	}
}

func semanticSortResponse(sorts []semanticquery.Sort) []api.SemanticSort {
	out := make([]api.SemanticSort, 0, len(sorts))
	for _, item := range sorts {
		out = append(out, api.SemanticSort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func semanticExplainArgs(args []any) []map[string]any {
	out := make([]map[string]any, 0, len(args))
	for i, value := range args {
		out = append(out, map[string]any{"index": i + 1, "value": value})
	}
	return out
}

func semanticQueryWarnings(sorts []api.SemanticSort) []string {
	// The planner now supplies deterministic tie-breakers even when callers do
	// not provide an explicit sort, so an omitted sort is no longer a warning.
	return nil
}

func executeAggregateRows(ctx context.Context, metrics Metrics, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	query := aggregateDataQuery(modelID, request)
	query.ProjectID = metrics.Catalog().Project.ID
	result, err := metrics.ExecuteDataQuery(ctx, query)
	return queryRowsFromDataResult(result.Rows), err
}

func aggregateDataQuery(modelID string, request reportdef.AggregateQuery) dataquery.Query {
	return dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticAggregate,
		Target:  request.Dataset,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Time:    dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	}
}

func executePreviewRows(ctx context.Context, metrics Metrics, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	query := previewDataQuery(modelID, request)
	query.ProjectID = metrics.Catalog().Project.ID
	result, err := metrics.ExecuteDataQuery(ctx, query)
	return queryRowsFromDataResult(result.Rows), err
}

func previewDataQuery(modelID string, request reportdef.RowQuery) dataquery.Query {
	return dataquery.Query{
		ModelID: modelID,
		Kind:    dataquery.KindSemanticRows,
		Target:  request.Dataset,
		Fields:  queryFieldsToDataFields(request.Dimensions),
		Metrics: queryFieldsToDataFields(request.Metrics),
		Filters: queryFiltersToDataFilters(request.Filters),
		Sort:    querySortToDataSort(request.Sort),
		Limit:   request.Limit,
		Offset:  request.Offset,
	}
}

func statusForDataExecutionError(err error) int {
	if err == nil {
		return nethttp.StatusOK
	}
	if queryauthz.IsDenied(err) {
		// Query routes identify a concrete semantic model or dataset. Conceal
		// inaccessible IDs consistently with metadata handlers.
		return nethttp.StatusNotFound
	}
	if reason, ok := workload.ReasonOf(err); ok {
		if reason == workload.QueueTimeout {
			return nethttp.StatusGatewayTimeout
		}
		return nethttp.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nethttp.StatusGatewayTimeout
	}
	return nethttp.StatusBadRequest
}

func queryFieldsToDataFields(fields []reportdef.QueryField) []dataquery.Field {
	out := make([]dataquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, dataquery.Field{
			Field: field.Field,
			Alias: field.Alias,
		})
	}
	return out
}

func queryFiltersToDataFilters(filters []reportdef.QueryFilter) []dataquery.Filter {
	out := make([]dataquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]dataquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, dataquery.FilterGroup{Filters: queryFiltersToDataFilters(group.Filters)})
		}
		out = append(out, dataquery.Filter{
			Field:    filter.Field,
			Dataset:  filter.Dataset,
			Operator: filter.Operator,
			Values:   append([]any{}, filter.Values...),
			Groups:   groups,
		})
	}
	return out
}

func querySortToDataSort(sort []reportdef.QuerySort) []dataquery.Sort {
	out := make([]dataquery.Sort, 0, len(sort))
	for _, item := range sort {
		out = append(out, dataquery.Sort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func queryRowsFromDataResult(rows []dataquery.Row) reportdef.QueryRows {
	out := make(reportdef.QueryRows, 0, len(rows))
	for _, row := range rows {
		converted := reportdef.QueryRow{}
		for key, value := range row {
			converted[key] = value
		}
		out = append(out, converted)
	}
	return out
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
	if h.CurrentPrincipalID != nil {
		metadata.PrincipalID = h.CurrentPrincipalID(r)
	}
	existing := dataquery.MetadataFromContext(r.Context())
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

func sortedMapKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func pageSliceForRequest[T any](w nethttp.ResponseWriter, r *nethttp.Request, items []T) ([]T, string, bool) {
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return nil, "", false
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	scope := requestCursorScope(r, nil)
	lastKey, err := decodeListKeysetCursor(r.URL.Query().Get("pageToken"), scope, snapshot)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return nil, "", false
	}
	start := 0
	if lastKey != "" {
		start = -1
		for index, item := range items {
			if listPageItemKey(item) == lastKey {
				start = index + 1
				break
			}
		}
		if start < 0 {
			writeJSONError(w, errCursorSnapshotUnavailable, nethttp.StatusConflict)
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	nextCursor := ""
	if end < len(items) {
		nextCursor = encodeListKeysetCursor(listPageItemKey(items[end-1]), scope, snapshot)
	}
	return append(make([]T, 0, end-start), items[start:end]...), nextCursor, true
}

type listKeysetCursor struct {
	Key      string `json:"key"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot,omitempty"`
	Expires  int64  `json:"expires"`
}

func listPageItemKey(value any) string {
	payload, _ := json.Marshal(value)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func encodeListKeysetCursor(key, scope, snapshot string) string {
	payload, _ := json.Marshal(listKeysetCursor{Key: key, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(indexCursorLifetime).Unix()})
	return cursorsigning.Sign("q2", payload)
}

func decodeListKeysetCursor(token, scope, snapshot string) (string, error) {
	if token == "" {
		return "", nil
	}
	if !strings.HasPrefix(token, "q2.") {
		return "", fmt.Errorf("invalid page token")
	}
	payload, err := cursorsigning.Verify("q2", token)
	if err != nil {
		return "", fmt.Errorf("invalid page token")
	}
	var cursor listKeysetCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Key == "" || cursor.Expires < time.Now().Unix() || cursor.Scope != scope {
		return "", fmt.Errorf("invalid page token")
	}
	if cursor.Snapshot != snapshot {
		return "", errCursorSnapshotUnavailable
	}
	return cursor.Key, nil
}

const (
	defaultAPILimit   = 50
	maxAPILimit       = 200
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
)

func apiLimitForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (int, bool) {
	limit, err := parseAPILimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func parseAPILimit(value string) (int, error) {
	if value == "" {
		return defaultAPILimit, nil
	}
	var limit int
	if _, err := fmt.Sscanf(value, "%d", &limit); err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if limit < 1 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if limit > maxAPILimit {
		return 0, fmt.Errorf("limit must not exceed %d", maxAPILimit)
	}
	return limit, nil
}

func apiCursorOffsetForRequest(w nethttp.ResponseWriter, r *nethttp.Request, scopes ...string) (int, bool) {
	offset, err := decodeIndexCursor(r.URL.Query().Get("pageToken"), scopes...)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return 0, false
	}
	return offset, true
}

const indexCursorLifetime = 15 * time.Minute

type indexCursor struct {
	Offset   int    `json:"offset"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot,omitempty"`
	Expires  int64  `json:"expires"`
}

var errCursorSnapshotUnavailable = errors.New("cursor serving snapshot is unavailable")

func decodeIndexCursor(token string, scopes ...string) (int, error) {
	if token == "" {
		return 0, nil
	}
	if !strings.HasPrefix(token, "q1.") {
		return 0, fmt.Errorf("invalid page token")
	}
	payload, err := cursorsigning.Verify("q1", token)
	if err != nil {
		return 0, fmt.Errorf("invalid page token")
	}
	var cursor indexCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Offset < 0 || cursor.Expires < time.Now().Unix() {
		return 0, fmt.Errorf("invalid page token")
	}
	expectedScope, expectedSnapshot := cursorScopeParts(scopes...)
	if cursor.Snapshot != expectedSnapshot {
		return 0, errCursorSnapshotUnavailable
	}
	if cursor.Scope != expectedScope {
		return 0, fmt.Errorf("invalid page token")
	}
	return cursor.Offset, nil
}

func encodeIndexCursor(offset int, scopes ...string) string {
	scope, snapshot := cursorScopeParts(scopes...)
	return encodeIndexCursorValue(indexCursor{Offset: offset, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(indexCursorLifetime).Unix()})
}

func encodeIndexCursorValue(cursor indexCursor) string {
	payload, _ := json.Marshal(cursor)
	return cursorsigning.Sign("q1", payload)
}

func cursorScopeParts(scopes ...string) (string, string) {
	if len(scopes) == 0 || strings.TrimSpace(scopes[0]) == "" {
		return "list", ""
	}
	snapshot := ""
	if len(scopes) > 1 {
		snapshot = scopes[1]
	}
	return scopes[0], snapshot
}

func statusForCursorError(err error) int {
	if errors.Is(err, errCursorSnapshotUnavailable) {
		return nethttp.StatusConflict
	}
	return nethttp.StatusBadRequest
}

func semanticAggregateCursorScope(r *nethttp.Request, input api.SemanticQueryRequest) string {
	input.PageToken = ""
	return requestCursorScope(r, input)
}

func semanticPreviewCursorScope(r *nethttp.Request, input api.SemanticPreviewRequest) string {
	input.PageToken = ""
	return requestCursorScope(r, input)
}

func requestCursorScope(r *nethttp.Request, payload any) string {
	query := r.URL.Query()
	query.Del("pageToken")
	body, _ := json.Marshal(payload)
	digest := sha256.Sum256([]byte(r.Method + "\n" + r.URL.Path + "\n" + query.Encode() + "\n" + string(body)))
	return hex.EncodeToString(digest[:])
}

func writeJSON(w nethttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}

func writeJSONError(w nethttp.ResponseWriter, err error, status int) {
	details := map[string]any{}
	if reason, ok := workload.ReasonOf(err); ok {
		if reason == workload.QueueTimeout {
			status = nethttp.StatusGatewayTimeout
			details["problemCode"] = "WORKLOAD_QUEUE_TIMEOUT"
		} else {
			status = nethttp.StatusServiceUnavailable
			w.Header().Set("Retry-After", "1")
			details["problemCode"] = "WORKLOAD_OVERLOADED"
		}
	} else if reason, ok := dataquery.ResultLimitReasonOf(err); ok {
		status = nethttp.StatusUnprocessableEntity
		if reason == dataquery.ResultRows {
			details["problemCode"] = "QUERY_RESULT_ROW_LIMIT"
		} else {
			details["problemCode"] = "QUERY_RESULT_BYTE_LIMIT"
		}
	} else if _, ok := analyticsresource.ResourceExhaustedReasonOf(err); ok {
		status = nethttp.StatusServiceUnavailable
		w.Header().Set("Retry-After", "1")
		details["problemCode"] = "ANALYTICS_RESOURCE_EXHAUSTED"
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = nethttp.StatusGatewayTimeout
		details["problemCode"] = "WORKLOAD_EXECUTION_TIMEOUT"
	}
	writeJSON(w, status, httpmodel.ErrorResponse{
		Code:      status,
		Message:   err.Error(),
		Details:   details,
		RequestID: "",
	})
}

func decodeOptionalJSONBody(r *nethttp.Request, dst any) error {
	if r.Body == nil || r.Body == nethttp.NoBody {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("malformed JSON: %w", err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("malformed JSON: %w", err)
	}
	return fmt.Errorf("malformed JSON: multiple JSON values")
}
