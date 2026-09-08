package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	explorationexport "github.com/flidai/leapview/internal/analytics/exploration/export"
	savedexploration "github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

// ExplorationExport serves canonical v2 explorer URL state through the same
// governed ExecuteSpec capability used by the API. The optional `saved`
// selection is navigation metadata only and is never dereferenced here.
func (h *BrowserHandler) ExplorationExport(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.NotFound(w, r)
		return
	}
	executor, ok := h.SavedExplorations.(savedExplorationQueryExecutor)
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	if h.ExplorationExportAuditRecorder == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		stdhttp.Error(w, "invalid exploration URL", stdhttp.StatusBadRequest)
		return
	}
	if version, present := dataExplorerURLValue(values, "v"); !present || version != dataExploreCanonicalURLVersion {
		stdhttp.Error(w, "canonical exploration state is required", stdhttp.StatusBadRequest)
		return
	}
	if err := validateBrowserExportURLOptions(values); err != nil {
		writeBrowserExportError(w, r, err)
		return
	}
	format, limits, err := browserExportRequest(values.Get("format"), values.Get("maxRows"), values.Get("maxBytes"))
	if err != nil {
		writeBrowserExportError(w, r, err)
		return
	}
	command, err := dataExploreCommandFromQuery(values)
	if err != nil {
		stdhttp.Error(w, "invalid exploration URL", stdhttp.StatusBadRequest)
		return
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = apitransport.NewRequestID()
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	ctx := dataquery.WithIndependentResultBudget(r.Context(), limits)
	auditQuery := dataquery.Query{ProjectID: projectID, Surface: "saved_exploration", Operation: "saved_exploration_url_export", PrincipalID: principal.ID, RequestID: requestID, CorrelationID: correlationID, ObjectType: "exploration_url", ObjectID: "url-export"}
	result, err := executor.ExecuteSpec(ctx, savedexploration.ExecuteSpecRequest{
		ProjectID: projectID, ActorID: principal.ID, Spec: command.Spec,
		RequestID: requestID, CorrelationID: correlationID, Operation: "saved_exploration_url_export",
	})
	if err != nil {
		_ = recordBrowserExportOutcome(ctx, h.ExplorationExportAuditRecorder, auditQuery, result.Result, format, browserExportOutcome(err), result.Result.BytesEstimate, err)
		writeBrowserExportError(w, r, err)
		return
	}
	if err := rejectTruncatedBrowserExportResult(result); err != nil {
		_ = recordBrowserExportOutcome(ctx, h.ExplorationExportAuditRecorder, result.Query, result.Result, format, browserExportOutcome(err), result.Result.BytesEstimate, err)
		writeBrowserExportError(w, r, err)
		return
	}
	body, err := explorationexport.Encode(ctx, result.Result, format, limits)
	if err != nil {
		_ = recordBrowserExportOutcome(ctx, h.ExplorationExportAuditRecorder, result.Query, result.Result, format, browserExportOutcome(err), result.Result.BytesEstimate, err)
		writeBrowserExportError(w, r, err)
		return
	}
	if err := recordBrowserExportOutcome(ctx, h.ExplorationExportAuditRecorder, result.Query, result.Result, format, "success", int64(len(body)), nil); err != nil {
		stdhttp.Error(w, "exploration export audit unavailable", stdhttp.StatusServiceUnavailable)
		return
	}
	if err := r.Context().Err(); err != nil {
		canceled := fmt.Errorf("%w: %v", explorationexport.ErrCanceled, err)
		_ = recordBrowserExportOutcome(ctx, h.ExplorationExportAuditRecorder, result.Query, result.Result, format, "canceled", int64(len(body)), canceled)
		writeBrowserExportError(w, r, canceled)
		return
	}
	contentType, extension := "text/csv; charset=utf-8", "csv"
	if format == explorationexport.Parquet {
		contentType, extension = "application/vnd.apache.parquet", "parquet"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="exploration.`+extension+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(stdhttp.StatusOK)
	_, _ = w.Write(body)
}

func validateBrowserExportURLOptions(values url.Values) error {
	for _, key := range []string{"format", "maxRows", "maxBytes"} {
		if len(values[key]) > 1 {
			return fmt.Errorf("%w: %s may only be specified once", explorationexport.ErrInvalidRequest, key)
		}
	}
	return nil
}

// URL exploration lowering requests one sentinel row beyond the authored
// limit. Reject it here so a live download cannot masquerade as complete.
func rejectTruncatedBrowserExportResult(result savedexploration.ExecuteResult) error {
	if result.Query.Limit > 0 && len(result.Result.Rows) >= result.Query.Limit {
		return explorationexport.ErrPartial
	}
	return nil
}

func browserExportRequest(rawFormat, rawRows, rawBytes string) (explorationexport.Format, explorationexport.Limits, error) {
	var format explorationexport.Format
	switch strings.ToLower(strings.TrimSpace(rawFormat)) {
	case "csv":
		format = explorationexport.CSV
	case "parquet":
		format = explorationexport.Parquet
	case "":
		return "", explorationexport.Limits{}, fmt.Errorf("%w: format is required", explorationexport.ErrInvalidRequest)
	default:
		return "", explorationexport.Limits{}, explorationexport.ErrInvalidFormat
	}
	limits := explorationexport.Limits{MaxRows: explorationexport.DefaultMaxRows, MaxBytes: explorationexport.DefaultMaxBytes}
	if rawRows != "" {
		value, err := strconv.Atoi(rawRows)
		if err != nil {
			return "", explorationexport.Limits{}, errors.Join(explorationexport.ErrInvalidRequest, err)
		}
		limits.MaxRows = value
	}
	if rawBytes != "" {
		value, err := strconv.ParseInt(rawBytes, 10, 64)
		if err != nil {
			return "", explorationexport.Limits{}, errors.Join(explorationexport.ErrInvalidRequest, err)
		}
		limits.MaxBytes = value
	}
	if err := limits.Validate(); err != nil {
		return "", explorationexport.Limits{}, errors.Join(explorationexport.ErrInvalidRequest, err)
	}
	if limits.MaxRows > explorationexport.MaximumMaxRows || limits.MaxBytes > explorationexport.MaximumMaxBytes {
		return "", explorationexport.Limits{}, fmt.Errorf("%w: export bounds exceed maximum", explorationexport.ErrInvalidRequest)
	}
	return format, limits, nil
}

func writeBrowserExportError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	var limitErr *dataquery.ResultLimitError
	switch {
	case errors.As(err, &limitErr):
		stdhttp.Error(w, "export exceeds its bounds", stdhttp.StatusRequestEntityTooLarge)
	case errors.Is(err, explorationexport.ErrInvalidFormat):
		stdhttp.Error(w, "unsupported export format", stdhttp.StatusUnsupportedMediaType)
	case errors.Is(err, explorationexport.ErrInvalidRequest):
		stdhttp.Error(w, "invalid export request", stdhttp.StatusBadRequest)
	case errors.Is(err, explorationexport.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		stdhttp.Error(w, "export canceled", stdhttp.StatusConflict)
	case errors.Is(err, savedexploration.ErrNotFound), errors.Is(err, savedexploration.ErrUnauthorized), errors.Is(err, access.ErrForbidden):
		// Do not turn a revoked/private saved-model capability into a retryable
		// outage, and do not disclose whether the URL's target exists.
		stdhttp.NotFound(w, r)
	case errors.Is(err, explorationexport.ErrPartial):
		stdhttp.Error(w, "incomplete exploration result", stdhttp.StatusUnprocessableEntity)
	case errors.Is(err, savedexploration.ErrInvalid), errors.Is(err, savedexploration.ErrInvalidPayload):
		stdhttp.Error(w, "invalid exploration", stdhttp.StatusUnprocessableEntity)
	default:
		stdhttp.Error(w, "exploration export unavailable", stdhttp.StatusServiceUnavailable)
	}
}

func recordBrowserExportOutcome(ctx context.Context, recorder queryaudit.Recorder, query dataquery.Query, result dataquery.Result, format explorationexport.Format, outcome string, bytes int64, err error) error {
	if recorder == nil {
		return errors.New("export audit recorder unavailable")
	}
	metadata, _ := json.Marshal(map[string]string{"exportFormat": string(format), "exportOutcome": outcome})
	errorClass := ""
	if err != nil {
		var limit *dataquery.ResultLimitError
		switch {
		case errors.Is(err, explorationexport.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			errorClass = "canceled"
		case errors.As(err, &limit):
			errorClass = "bounds"
		case errors.Is(err, explorationexport.ErrPartial):
			errorClass = "partial"
		default:
			errorClass = "failed"
		}
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return recorder.RecordQueryEvent(auditContext, queryaudit.EventInput{
		ProjectID: query.ProjectID, PrincipalID: query.PrincipalID, Surface: "saved_exploration", Operation: "saved_exploration_export_preparation",
		QueryKind: string(query.Kind), ModelID: query.ModelID, ObjectType: query.ObjectType, ObjectID: query.ObjectID,
		RequestID: query.RequestID, CorrelationID: query.CorrelationID, Status: map[bool]string{true: "success", false: "error"}[outcome == "success"],
		ExecutionState: "export_" + outcome, RowsReturned: len(result.Rows), BytesEstimate: bytes, Error: errorClass, QueryJSON: string(metadata),
	})
}

func browserExportOutcome(err error) string {
	var limit *dataquery.ResultLimitError
	switch {
	case errors.Is(err, explorationexport.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.As(err, &limit):
		return "bounds"
	case errors.Is(err, explorationexport.ErrPartial):
		return "partial"
	default:
		return "failure"
	}
}
