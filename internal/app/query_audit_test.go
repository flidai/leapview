package app

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/platform"
)

func TestAuditedQueryMetricsRecordsSuccessWithoutRows(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/leapview.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	request := dataquery.ModelTableRows("test", "orders", []string{"order_id", "status"}, nil, 0, 2, false)
	request.WorkspaceID = "test"
	request.Surface = dataquery.SurfaceDataExplorer
	request.Operation = dataquery.OperationPreviewWindow
	request.ObjectType = "model_table"
	request.ObjectID = "test:model_table:test.orders"
	ctx = withPrincipal(ctx, Principal{ID: "principal_admin@example.test"})

	if _, err := server.runtime.metrics.ExecuteDataQuery(ctx, request); err != nil {
		t.Fatal(err)
	}

	events := queryEventsForTest(t, server, queryaudit.Filter{WorkspaceID: "test"})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %#v", len(events), events)
	}
	event := events[0]
	if event.Status != dataquery.StatusSuccess {
		t.Fatalf("status = %q, want success", event.Status)
	}
	if event.Surface != dataquery.SurfaceDataExplorer || event.Operation != dataquery.OperationPreviewWindow {
		t.Fatalf("surface/operation = %q/%q", event.Surface, event.Operation)
	}
	if event.RowsReturned != 2 {
		t.Fatalf("rows returned = %d, want 2", event.RowsReturned)
	}
	if event.ExecutionState != dataquery.ExecutionSucceeded || event.ExecutionMS <= 0 {
		t.Fatalf("execution telemetry = state %q ms %d, want succeeded with duration", event.ExecutionState, event.ExecutionMS)
	}
	if event.SQL == "" {
		t.Fatal("expected generated SQL in query event")
	}
	if strings.Contains(event.QueryJSON, "delivered") || strings.Contains(event.QueryJSON, "shipped") {
		t.Fatalf("query JSON stored result row values: %s", event.QueryJSON)
	}
}

func TestAuditedQueryMetricsRecordsExecutionError(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/leapview.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	ctx = withPrincipal(ctx, Principal{ID: "principal_admin@example.test"})
	request := dataquery.Query{
		WorkspaceID: "test",
		Surface:     dataquery.SurfaceAPI,
		Operation:   dataquery.OperationAPIQuery,
		ModelID:     "test",
		Kind:        dataquery.Kind("unsupported"),
		Target:      "olist.orders",
		Limit:       1,
	}

	if _, err := server.runtime.metrics.ExecuteDataQuery(ctx, request); err == nil {
		t.Fatal("expected query execution error")
	}

	events := queryEventsForTest(t, server, queryaudit.Filter{WorkspaceID: "test"})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %#v", len(events), events)
	}
	event := events[0]
	if event.Status != dataquery.StatusError {
		t.Fatalf("status = %q, want error", event.Status)
	}
	if !strings.Contains(event.Error, "unsupported") {
		t.Fatalf("error = %q, want unsupported query message", event.Error)
	}
	if event.ExecutionState != dataquery.ExecutionFailed {
		t.Fatalf("execution state = %q, want failed", event.ExecutionState)
	}
}

func queryEventsForTest(t *testing.T, server *appTestHarness, filter queryaudit.Filter) []queryaudit.Event {
	t.Helper()
	repo := queryAuditRepositoryForTest(t, server)
	events, err := repo.ListQueryEvents(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return events
}
