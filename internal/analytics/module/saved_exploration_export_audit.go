package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	explorationexport "github.com/flidai/leapview/internal/analytics/arrowquery/export"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// QueryAuditRecorder exposes query/export audit recording through the analytics
// capability surface, keeping composition independent of its implementation.
type QueryAuditRecorder = queryaudit.Recorder

var errSavedExplorationExportAuditUnavailable = errors.New("saved exploration export audit is unavailable")

// recordSavedExplorationExportOutcome records export preparation, not network delivery.
// It intentionally carries no SQL, plan, filters, or result values: those are
// query concerns, while this event records only export format, outcome, and
// bounded row/byte counts. Errors are reduced to stable classes to avoid
// copying connector or policy details into audit storage.
func recordSavedExplorationExportOutcome(ctx context.Context, recorder queryaudit.Recorder, query dataquery.Query, result dataquery.Result, format explorationexport.Format, outcome string, bytes int64, err error) error {
	if recorder == nil {
		return errSavedExplorationExportAuditUnavailable
	}
	if bytes < 0 {
		bytes = 0
	}
	metadata, _ := json.Marshal(map[string]string{"exportFormat": string(format), "exportOutcome": outcome})
	status := "error"
	if outcome == "success" {
		status = "success"
	}
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
	auditContext := context.WithoutCancel(ctx)
	auditContext, cancel := context.WithTimeout(auditContext, 5*time.Second)
	defer cancel()
	if err := recorder.RecordQueryEvent(auditContext, queryaudit.EventInput{
		ProjectID: query.ProjectID, PrincipalID: query.PrincipalID, Surface: "saved_exploration",
		Operation: "saved_exploration_export_preparation", QueryKind: string(query.Kind), ModelID: query.ModelID,
		ObjectType: query.ObjectType, ObjectID: query.ObjectID, RequestID: query.RequestID, CorrelationID: query.CorrelationID,
		Status: status, ExecutionState: "export_" + outcome, RowsReturned: len(result.Rows), BytesEstimate: bytes,
		Error: errorClass, QueryJSON: string(metadata),
	}); err != nil {
		return fmt.Errorf("%w: %v", errSavedExplorationExportAuditUnavailable, err)
	}
	return nil
}

func exportAuditQuery(project, actor, requestID, correlationID, objectType, objectID string) dataquery.Query {
	return dataquery.Query{ProjectID: projectgraph.ResourceID(project), Surface: "saved_exploration", Operation: "saved_exploration_export", PrincipalID: actor, RequestID: requestID, CorrelationID: correlationID, ObjectType: objectType, ObjectID: objectID}
}

func exportOutcome(err error) string {
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
