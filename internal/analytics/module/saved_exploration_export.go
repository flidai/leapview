package module

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	explorationexport "github.com/flidai/leapview/internal/analytics/arrowquery/export"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ExportSavedExploration executes exactly the authorized current revision and
// encodes it only after the governed executor has returned a complete result.
// If-Match is required even though the application can execute "current" for
// interactive callers: an export is a durable snapshot claim, so a changed
// saved exploration must never silently produce a different file.
func (h savedExplorationAPIHandler) ExportSavedExploration(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenExportSavedExplorationHeaders) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	executor, ok := h.config.Service.(savedExplorationQueryExecutor)
	if !ok {
		h.unavailable(w, r)
		return
	}
	if h.config.ExportAuditRecorder == nil {
		h.unavailable(w, r)
		return
	}
	expected, err := parseRevisionToken(headers.IfMatch)
	if err != nil {
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", err)
		return
	}
	var body analyticsgen.GenExportSavedExplorationBody
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Request body is invalid.", nil)
		return
	}
	format, limits, err := exportRequest(body.Format, body.MaxRows, body.MaxBytes)
	if err != nil {
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", err)
		return
	}
	requestID, correlationID := exportRequestIDs(r)
	ctx := dataquery.WithIndependentResultBudget(r.Context(), limits)
	auditQuery := exportAuditQuery(project, actor, requestID, correlationID, "saved_exploration", exploration)
	result, err := executor.Execute(ctx, saved.ExecuteRequest{
		ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(exploration), ActorID: actor,
		RequestID: requestID, CorrelationID: correlationID, ExpectedRevision: expected,
		Operation: "saved_exploration_export",
	})
	if err != nil {
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, auditQuery, result.Result, format, exportOutcome(err), result.Result.BytesEstimate, err)
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", err)
		return
	}
	if err := rejectTruncatedExplorationResult(result); err != nil {
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, exportOutcome(err), result.Result.BytesEstimate, err)
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", err)
		return
	}
	bodyBytes, err := explorationexport.Encode(ctx, result.Result, format, limits)
	if err != nil {
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, exportOutcome(err), result.Result.BytesEstimate, err)
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", err)
		return
	}
	if err := recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, "success", int64(len(bodyBytes)), nil); err != nil {
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", err)
		return
	}
	if err := r.Context().Err(); err != nil {
		canceled := fmt.Errorf("%w: %v", explorationexport.ErrCanceled, err)
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, "canceled", int64(len(bodyBytes)), canceled)
		writeSavedExplorationExportFailure(w, r, "exportSavedExploration", canceled)
		return
	}
	writeExplorationExport(w, bodyBytes, format)
}

// ExportSavedExplorationURL accepts only canonical authored state. It never
// dereferences a saved ID (or the browser's `saved=` selection), so possessing
// a live URL cannot read another principal's unpublished working copy.
func (h savedExplorationAPIHandler) ExportSavedExplorationURL(w http.ResponseWriter, r *http.Request, project string) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	executor, ok := h.config.Service.(savedExplorationQueryExecutor)
	if !ok {
		h.unavailable(w, r)
		return
	}
	if h.config.ExportAuditRecorder == nil {
		h.unavailable(w, r)
		return
	}
	var body analyticsgen.GenExportSavedExplorationURLBody
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Request body is invalid.", nil)
		return
	}
	format, limits, err := exportRequest(body.Format, body.MaxRows, body.MaxBytes)
	if err != nil {
		writeSavedExplorationExportFailure(w, r, "exportSavedExplorationURL", err)
		return
	}
	requestID, correlationID := exportRequestIDs(r)
	ctx := dataquery.WithIndependentResultBudget(r.Context(), limits)
	auditQuery := exportAuditQuery(project, actor, requestID, correlationID, "exploration_url", "url-export")
	result, err := executor.ExecuteSpec(ctx, saved.ExecuteSpecRequest{
		ProjectID: projectgraph.ResourceID(project), ActorID: actor, Spec: body.Spec,
		RequestID: requestID, CorrelationID: correlationID, Operation: "saved_exploration_url_export",
	})
	if err != nil {
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, auditQuery, result.Result, format, exportOutcome(err), result.Result.BytesEstimate, err)
		writeSavedExplorationExportFailure(w, r, "exportSavedExplorationURL", err)
		return
	}
	if err := rejectTruncatedExplorationResult(result); err != nil {
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, exportOutcome(err), result.Result.BytesEstimate, err)
		writeSavedExplorationExportFailure(w, r, "exportSavedExplorationURL", err)
		return
	}
	bodyBytes, err := explorationexport.Encode(ctx, result.Result, format, limits)
	if err != nil {
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, exportOutcome(err), result.Result.BytesEstimate, err)
		writeSavedExplorationExportFailure(w, r, "exportSavedExplorationURL", err)
		return
	}
	if err := recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, "success", int64(len(bodyBytes)), nil); err != nil {
		writeSavedExplorationExportFailure(w, r, "exportSavedExplorationURL", err)
		return
	}
	if err := r.Context().Err(); err != nil {
		canceled := fmt.Errorf("%w: %v", explorationexport.ErrCanceled, err)
		_ = recordSavedExplorationExportOutcome(ctx, h.config.ExportAuditRecorder, result.Query, result.Result, format, "canceled", int64(len(bodyBytes)), canceled)
		writeSavedExplorationExportFailure(w, r, "exportSavedExplorationURL", canceled)
		return
	}
	writeExplorationExport(w, bodyBytes, format)
}

// Query lowering asks for one row beyond the authored limit so interactive
// callers can display a truncation indicator. Export must never turn that
// sentinel row into a silently incomplete file.
func rejectTruncatedExplorationResult(result saved.ExecuteResult) error {
	if result.Query.Limit > 0 && len(result.Result.Rows) >= result.Query.Limit {
		return explorationexport.ErrPartial
	}
	return nil
}

func exportRequest(format analyticsgen.SavedExplorationExportFormat, maxRows *int32, maxBytes *int64) (explorationexport.Format, explorationexport.Limits, error) {
	var outputFormat explorationexport.Format
	switch format {
	case analyticsgen.SavedExplorationExportFormatCsv:
		outputFormat = explorationexport.CSV
	case analyticsgen.SavedExplorationExportFormatParquet:
		outputFormat = explorationexport.Parquet
	case "":
		return "", explorationexport.Limits{}, fmt.Errorf("%w: format is required", explorationexport.ErrInvalidRequest)
	default:
		return "", explorationexport.Limits{}, fmt.Errorf("%w: %q", explorationexport.ErrInvalidFormat, format)
	}
	limits := explorationexport.Limits{MaxRows: explorationexport.DefaultMaxRows, MaxBytes: explorationexport.DefaultMaxBytes}
	if maxRows != nil {
		limits.MaxRows = int(*maxRows)
	}
	if maxBytes != nil {
		limits.MaxBytes = *maxBytes
	}
	if err := limits.Validate(); err != nil {
		return "", explorationexport.Limits{}, fmt.Errorf("%w: %v", explorationexport.ErrInvalidRequest, err)
	}
	if limits.MaxRows > explorationexport.MaximumMaxRows {
		return "", explorationexport.Limits{}, fmt.Errorf("%w: export row limit %d exceeds maximum %d", explorationexport.ErrInvalidRequest, limits.MaxRows, explorationexport.MaximumMaxRows)
	}
	if limits.MaxBytes > explorationexport.MaximumMaxBytes {
		return "", explorationexport.Limits{}, fmt.Errorf("%w: export byte limit %d exceeds maximum %d", explorationexport.ErrInvalidRequest, limits.MaxBytes, explorationexport.MaximumMaxBytes)
	}
	return outputFormat, limits, nil
}

func exportRequestIDs(r *http.Request) (string, string) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = apitransport.NewRequestID()
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return requestID, correlationID
}

// writeExplorationExport is called only after authentication, policy, query
// admission, complete-result validation, and bounded encoding all succeed.
// Thus response metadata cannot disclose an unauthorized source and no
// partial CSV/Parquet stream can be mistaken for a successful export.
func writeExplorationExport(w http.ResponseWriter, body []byte, format explorationexport.Format) {
	contentType, extension := "", ""
	switch format {
	case explorationexport.CSV:
		contentType, extension = "text/csv; charset=utf-8", "csv"
	case explorationexport.Parquet:
		contentType, extension = "application/vnd.apache.parquet", "parquet"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="exploration.`+extension+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeSavedExplorationExportFailure(w http.ResponseWriter, r *http.Request, operationID string, err error) {
	classified := classifySavedExplorationFailure(err)
	kind, _ := apigenfailure.KindOf(classified)
	failure := apitransport.APIGenFailure{OperationID: operationID, Kind: kind, StatusCode: http.StatusInternalServerError, Code: "EXPORT_FAILED", PublicDetail: "The exploration export could not be completed.", Cause: err}
	var limitErr *dataquery.ResultLimitError
	switch {
	case errors.As(err, &limitErr):
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusRequestEntityTooLarge, "EXPORT_LIMIT_EXCEEDED", "The exploration export exceeds its result bounds."
	case errors.Is(err, explorationexport.ErrInvalidFormat):
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusUnsupportedMediaType, "UNSUPPORTED_EXPORT_FORMAT", "The requested export format is unsupported."
	case errors.Is(err, explorationexport.ErrInvalidRequest):
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusBadRequest, "INVALID_EXPORT_REQUEST", "The export bounds are invalid."
	case errors.Is(err, explorationexport.ErrPartial):
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusUnprocessableEntity, "INCOMPLETE_EXPORT_RESULT", "The exploration result is incomplete and cannot be exported."
	case errors.Is(err, explorationexport.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusConflict, "EXPORT_CANCELED", "The exploration export was canceled before it was sent."
	case errors.Is(err, errSavedExplorationExportAuditUnavailable):
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusServiceUnavailable, "EXPORT_AUDIT_UNAVAILABLE", "The exploration export audit is temporarily unavailable."
	case kind == "not_found":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusNotFound, "SAVED_EXPLORATION_NOT_FOUND", "Saved exploration not found."
	case kind == "precondition":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusPreconditionFailed, "STALE_SAVED_EXPLORATION", "The saved exploration revision is stale."
	case kind == "conflict":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusConflict, "SAVED_EXPLORATION_CONFLICT", "The saved exploration cannot be exported in its current state."
	case kind == "invalid":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusUnprocessableEntity, "INVALID_SAVED_EXPLORATION", "The saved exploration request is invalid."
	case kind == "unavailable":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusServiceUnavailable, "SAVED_EXPLORATION_UNAVAILABLE", "Saved explorations are temporarily unavailable."
	}
	apitransport.WriteAPIGenFailure(r.Context(), w, r, nil, failure)
}
