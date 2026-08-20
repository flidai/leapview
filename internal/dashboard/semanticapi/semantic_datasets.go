package http

import (
	"fmt"
	nethttp "net/http"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/go-chi/chi/v5"
)

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
