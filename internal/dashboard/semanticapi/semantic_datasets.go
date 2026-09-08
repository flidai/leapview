package http

import (
	"context"
	"fmt"
	nethttp "net/http"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/api"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListSemanticDatasets(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	modelID := chi.URLParam(r, "model")
	ctx, err := semanticConsumerForRequest(r.Context(), h.Metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	allowed, err := h.authorizeSemanticModelResource(r, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	if !allowed {
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
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
	filtered := out[:0]
	for _, item := range out {
		if err := authorizeSemanticTarget(ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: item.ID}); err != nil {
			if protectedSemanticModel(h.Metrics, modelID) && semanticMemberDenied(err) {
				continue
			}
			status := nethttp.StatusServiceUnavailable
			if !semanticAuthorizationUnavailable(err) {
				status = nethttp.StatusForbidden
			}
			writeJSONError(w, err, status)
			return
		}
		if protectedSemanticModel(h.Metrics, modelID) {
			dataset, _ := compiled.Dataset(item.ID)
			if err := authorizeSemanticDatasetMembers(ctx, h.Metrics, modelID, item.ID, model, dataset.Table()); err != nil {
				if semanticMemberDenied(err) {
					continue
				}
				writeJSONError(w, err, semanticAuthorizationStatus(err))
				return
			}
		}
		filtered = append(filtered, item)
	}
	out = filtered
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
	modelID := chi.URLParam(r, "model")
	ctx, err := semanticConsumerForRequest(r.Context(), h.Metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	if err := authorizeSemanticTarget(ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: datasetID}); err != nil {
		writeJSONError(w, err, semanticAuthorizationStatus(err))
		return
	}
	if protectedSemanticModel(h.Metrics, modelID) {
		if err := authorizeSemanticDatasetMembers(ctx, h.Metrics, modelID, datasetID, model, table); err != nil {
			writeJSONError(w, err, semanticAuthorizationStatus(err))
			return
		}
	}
	writeJSON(w, nethttp.StatusOK, SemanticTableProjection(model, datasetID, table))
}

// authorizeSemanticDatasetMembers protects whole-dataset responses whose
// counts, entities, or descriptions cannot safely be filtered per member.
// A denied member therefore denies the complete protected projection.
func authorizeSemanticDatasetMembers(ctx context.Context, metrics Metrics, modelID, datasetID string, model *semanticmodel.Model, table semanticmodel.Table) error {
	for _, field := range sortedMapKeys(table.Dimensions) {
		// Table dimensions are physical fields even when a same-named metric
		// exists in the semantic namespace; never infer kind from the name.
		if err := authorizeSemanticField(ctx, metrics, modelID, datasetID, datasetID+"."+field); err != nil {
			return err
		}
	}
	for _, metric := range sortedMapKeys(model.Metrics) {
		if model.Metrics[metric].Dataset != datasetID {
			continue
		}
		if err := authorizeSemanticTarget(ctx, metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: datasetID, Metric: metric}); err != nil {
			return err
		}
	}
	return nil
}

func (h Handler) ListSemanticFields(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, table, datasetID, ok := h.semanticDatasetForRequest(w, r)
	if !ok {
		return
	}
	fields := SemanticDatasetFieldsProjection(model, datasetID, table)
	authorized := fields[:0]
	modelID := chi.URLParam(r, "model")
	ctx, err := semanticConsumerForRequest(r.Context(), h.Metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	for _, field := range fields {
		var fieldErr error
		if field.Kind == "metric" {
			fieldErr = authorizeSemanticTarget(ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: datasetID, Metric: field.Name})
		} else {
			fieldErr = authorizeSemanticField(ctx, h.Metrics, modelID, datasetID, field.ID)
		}
		if fieldErr != nil {
			if protectedSemanticModel(h.Metrics, modelID) && semanticMemberDenied(fieldErr) {
				continue
			}
			status := nethttp.StatusServiceUnavailable
			if !semanticAuthorizationUnavailable(fieldErr) {
				status = nethttp.StatusForbidden
			}
			writeJSONError(w, fieldErr, status)
			return
		}
		authorized = append(authorized, field)
	}
	fields = authorized
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
	ctx, err := semanticConsumerForRequest(r.Context(), metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
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
	if err := authorizeSemanticRequest(ctx, metrics, modelID, reportdef.SemanticAggregateRequest(request)); err != nil {
		writeJSONError(w, err, semanticRequestAuthorizationStatus(metrics, modelID, err))
		return
	}
	plan, err := semanticExplainAggregate(ctx, metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx = dataquery.WithMetadata(ctx, h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIQuery, "semantic_dataset", modelID+":"+datasetID))
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
	ctx, err := semanticConsumerForRequest(r.Context(), metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
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
	if err := authorizeSemanticRequest(ctx, metrics, modelID, reportdef.SemanticRowRequest(request)); err != nil {
		writeJSONError(w, err, semanticRequestAuthorizationStatus(metrics, modelID, err))
		return
	}
	plan, err := semanticExplainRows(ctx, metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	ctx = dataquery.WithMetadata(ctx, h.requestQueryMetadata(r, dataquery.SurfaceAPI, dataquery.OperationAPIPreview, "semantic_dataset", modelID+":"+datasetID))
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
	ctx, err := semanticConsumerForRequest(r.Context(), metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
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
	if err := authorizeSemanticRequest(ctx, metrics, modelID, reportdef.SemanticAggregateRequest(request)); err != nil {
		writeJSONError(w, err, semanticRequestAuthorizationStatus(metrics, modelID, err))
		return
	}
	plan, err := semanticExplainAggregate(ctx, metrics, modelID, request)
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
	ctx, err := semanticConsumerForRequest(r.Context(), metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
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
	if err := authorizeSemanticRequest(ctx, metrics, modelID, reportdef.SemanticRowRequest(request)); err != nil {
		writeJSONError(w, err, semanticRequestAuthorizationStatus(metrics, modelID, err))
		return
	}
	plan, err := semanticExplainRows(ctx, metrics, modelID, request)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	writeJSON(w, nethttp.StatusOK, semanticExplainResponse("preview", plan, semanticQueryWarnings(input.Sort)))
}
