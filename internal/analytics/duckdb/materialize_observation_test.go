package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform/transaction"
)

// recordingCommitter wraps the real DuckLake committer so this test observes
// the production CommitTransaction callback rather than a test-only
// materialization implementation.
type recordingCommitter struct {
	inner  duckLakeCommitter
	events *[]string
}

func (c recordingCommitter) CommitTransaction(ctx context.Context, servingStateID string, extra map[string]string, fn func(transaction.Transaction) error) (int64, error) {
	snapshotID, err := c.inner.CommitTransaction(ctx, servingStateID, extra, func(tx transaction.Transaction) error {
		return fn(recordingTransaction{Transaction: tx, events: c.events})
	})
	if err == nil {
		*c.events = append(*c.events, "commit")
	}
	return snapshotID, err
}

type recordingTransaction struct {
	transaction.Transaction
	events *[]string
}

func (tx recordingTransaction) ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	upper := strings.ToUpper(strings.TrimSpace(statement))
	if len(*tx.events) == 0 && (strings.HasPrefix(upper, "CREATE SCHEMA") || strings.HasPrefix(upper, "CREATE OR REPLACE TABLE")) {
		*tx.events = append(*tx.events, "materialize")
	}
	return tx.Transaction.ExecContext(ctx, statement, args...)
}

func TestProjectRuntimeRefreshWithObservationWriterUsesCommitCallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		writerErr  error
		wantEvents []string
	}{
		{name: "success", wantEvents: []string{"materialize", "writer", "commit"}},
		{name: "writer failure aborts commit", writerErr: errors.New("capture failed"), wantEvents: []string{"materialize", "writer"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, environment, runtime := openObservationWriterRuntime(t)
			events := []string{}
			runtime.committer = recordingCommitter{inner: environment, events: &events}
			beforeSnapshot := runtime.DuckLakeSnapshotID()
			beforeSnapshots, err := environment.SnapshotIDs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			writer := func(_ context.Context, observations []analyticsmaterialize.SourceObservation) error {
				events = append(events, "writer")
				if len(observations) != 1 || observations[0].ID != "orders" {
					t.Fatalf("observations = %#v, want orders evidence", observations)
				}
				return test.writerErr
			}
			err = runtime.RefreshProjectTablesWithObservationWriter(ctx, []string{"sales_orders"}, writer)
			if test.writerErr != nil {
				if !errors.Is(err, test.writerErr) {
					t.Fatalf("refresh error = %v, want writer error", err)
				}
				if got := runtime.DuckLakeSnapshotID(); got != beforeSnapshot {
					t.Fatalf("runtime snapshot = %d, want unchanged %d", got, beforeSnapshot)
				}
				afterSnapshots, snapshotErr := environment.SnapshotIDs(ctx)
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				if len(afterSnapshots) != len(beforeSnapshots) {
					t.Fatalf("snapshot count = %d, want unchanged %d", len(afterSnapshots), len(beforeSnapshots))
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(events) != len(test.wantEvents) {
				t.Fatalf("events = %v, want %v", events, test.wantEvents)
			}
			for index, want := range test.wantEvents {
				if events[index] != want {
					t.Fatalf("events = %v, want %v", events, test.wantEvents)
				}
			}
		})
	}
}

func TestProjectRuntimeSourceObservationsDeepCopyNullable(t *testing.T) {
	nullable := true
	runtime := &ProjectRuntime{sourceObservations: []analyticsmaterialize.SourceObservation{{
		ID: "orders", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Nullable: &nullable}},
	}}}

	first := runtime.SourceObservations()
	*first[0].Schema[0].Nullable = false
	second := runtime.SourceObservations()
	if second[0].Schema[0].Nullable == nil || !*second[0].Schema[0].Nullable {
		t.Fatalf("source observation nullable identity was aliased: %#v", second)
	}
}

func openObservationWriterRuntime(t *testing.T) (context.Context, *analyticsducklake.Environment, *ProjectRuntime) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("order_id,revenue\n1,10.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name: "olist", DefaultConnection: "local",
		Connections: map[string]semanticmodel.Connection{"local": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{"orders": {
			Connection: "local", Path: "orders.csv", Format: "csv",
			EffectivePathLocation: testCSVPathLocationWithHeader("orders.csv", true), SchemaMode: "compatible",
			Fields: map[string]semanticmodel.SourceField{"order_id": {}, "revenue": {}},
		}},
		Tables: map[string]semanticmodel.Table{"orders": {
			Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, ModelName: "sales_orders",
			Entities: map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Datatype: semanticmodel.DataTypeInteger}, "revenue": {Datatype: semanticmodel.DataTypeFloat},
			},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "sales_orders"}},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindTestManagedRoot(model, "local", dir)
	ctx, environment, runtime := openSchemaTestRuntime(t, context.Background(), dir, model)
	return ctx, environment, runtime
}
