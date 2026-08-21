package http

import (
	"fmt"
	nethttp "net/http"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/go-chi/chi/v5"
)

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
