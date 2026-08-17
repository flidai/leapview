package dataquery

import (
	"context"
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type recordingAuditRecorder struct {
	queries []Query
	results []Result
}

func (r *recordingAuditRecorder) RecordDataQuery(_ context.Context, query Query, result Result) error {
	r.queries = append(r.queries, query)
	r.results = append(r.results, result)
	return nil
}

func TestExecuteAuditedNormalizesMetadataAndRecordsOnce(t *testing.T) {
	recorder := &recordingAuditRecorder{}
	ctx := WithAuditRecorder(context.Background(), recorder)
	ctx = WithMetadata(ctx, Metadata{
		ProjectID:     projectgraph.ResourceID("sales"),
		Surface:       SurfaceAPI,
		Operation:     OperationAPIQuery,
		PrincipalID:   "principal_1",
		RequestID:     "req_1",
		ObjectType:    "semantic_dataset",
		ObjectID:      "sales:orders",
		CorrelationID: "corr_1",
	})

	result, err := ExecuteAudited(ctx, SemanticAggregate("sales", "orders", []Field{{Field: "orders.status"}}, nil, nil, nil, 0, 5), func(ctx context.Context, query Query) (Result, error) {
		if query.ProjectID != projectgraph.ResourceID("sales") || query.Surface != SurfaceAPI || query.RequestID != "req_1" {
			t.Fatalf("query was not normalized before execute: %#v", query)
		}
		return ExecuteAudited(ctx, query, func(context.Context, Query) (Result, error) {
			return Result{Rows: []Row{{"status": "delivered"}}}, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSuccess || result.RowsReturned != 1 {
		t.Fatalf("result metadata = %#v", result)
	}
	if len(recorder.queries) != 1 {
		t.Fatalf("recorded queries = %d, want 1", len(recorder.queries))
	}
	recorded := recorder.queries[0]
	if recorded.ProjectID != projectgraph.ResourceID("sales") || recorded.Surface != SurfaceAPI || recorded.Operation != OperationAPIQuery || recorded.PrincipalID != "principal_1" || recorded.CorrelationID != "corr_1" {
		t.Fatalf("recorded query metadata = %#v", recorded)
	}
}

func TestExecuteAuditedRecordsValidationFailure(t *testing.T) {
	recorder := &recordingAuditRecorder{}
	ctx := WithAuditRecorder(context.Background(), recorder)
	ctx = WithMetadata(ctx, Metadata{PrincipalID: "principal_1"})
	_, err := ExecuteAudited(ctx, Query{ProjectID: projectgraph.ResourceID("sales"), Surface: SurfaceAPI, Operation: OperationAPIQuery}, func(context.Context, Query) (Result, error) {
		return Result{}, errors.New("should not execute invalid query")
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if len(recorder.results) != 1 {
		t.Fatalf("recorded results = %d, want 1", len(recorder.results))
	}
	if recorder.results[0].Status != StatusError || recorder.results[0].Error == "" {
		t.Fatalf("recorded validation result = %#v", recorder.results[0])
	}
}

func TestExecuteAuditedRequiresPrincipalBeforeExecution(t *testing.T) {
	recorder := &recordingAuditRecorder{}
	ctx := WithAuditRecorder(context.Background(), recorder)
	executed := false

	_, err := ExecuteAudited(ctx, SemanticRows("sales", "orders", []Field{{Field: "orders.status"}}, nil, nil, nil, 0, 10, false), func(context.Context, Query) (Result, error) {
		executed = true
		return Result{}, nil
	})
	if err == nil {
		t.Fatal("expected missing principal error")
	}
	if !errors.Is(err, ErrMissingPrincipal) {
		t.Fatalf("error = %v, want ErrMissingPrincipal", err)
	}
	if executed {
		t.Fatal("query executed without a principal")
	}
	if len(recorder.queries) != 0 {
		t.Fatalf("recorded anonymous queries = %#v, want none", recorder.queries)
	}
}
