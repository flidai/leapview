package duckdb

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type catalogStatisticsDatabase struct {
	plan semanticquery.Plan
	rows semanticquery.Rows
}

func (*catalogStatisticsDatabase) Exec(context.Context, string) error { return nil }
func (*catalogStatisticsDatabase) Close() error                       { return nil }
func (*catalogStatisticsDatabase) Path() string                       { return "catalog.ducklake" }
func (d *catalogStatisticsDatabase) Query(_ context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	d.plan = plan
	return d.rows, nil
}

func TestProjectRuntimeCatalogTableStatisticsUsesServingSnapshot(t *testing.T) {
	snapshotAt := time.Date(2026, 8, 24, 14, 32, 0, 0, time.UTC)
	database := &catalogStatisticsDatabase{rows: semanticquery.Rows{{
		"schema_name": "model", "table_name": "orders", "row_count": big.NewInt(42),
		"column_count": int64(7), "file_count": int64(2), "byte_count": big.NewInt(4096), "snapshot_id": big.NewInt(17), "snapshot_time": snapshotAt,
		"column_name": "order_id", "column_type": "VARCHAR", "column_order": int64(0), "nulls_allowed": false,
	}}}
	runtime := &ProjectRuntime{db: database, lastSnapshotID: 17}

	statistics, err := runtime.CatalogTableStatistics(t.Context())
	if err != nil {
		t.Fatalf("CatalogTableStatistics() error = %v", err)
	}
	if len(statistics) != 1 {
		t.Fatalf("CatalogTableStatistics() = %#v, want one table", statistics)
	}
	got := statistics[0]
	if got.Schema != "model" || got.Name != "orders" || got.RowCount != 42 || got.ColumnCount != 7 || got.FileCount != 2 || got.SizeBytes != 4096 || got.SnapshotID != 17 || !got.SnapshotAt.Equal(snapshotAt) {
		t.Fatalf("CatalogTableStatistics()[0] = %#v", got)
	}
	if len(got.Columns) != 1 || got.Columns[0].Name != "order_id" || got.Columns[0].PhysicalType != "VARCHAR" || got.Columns[0].Nullable == nil || *got.Columns[0].Nullable {
		t.Fatalf("CatalogTableStatistics()[0].Columns = %#v", got.Columns)
	}
	if len(database.plan.Args) != 3 || database.plan.Args[0] != int64(17) || database.plan.Args[1] != int64(17) || database.plan.Args[2] != "lake" {
		t.Fatalf("query args = %#v, want serving snapshot 17 and lake alias", database.plan.Args)
	}
	if !strings.Contains(database.plan.SQL, "selected.id < f.end_snapshot") || !strings.Contains(database.plan.SQL, "selected.id < t.end_snapshot") {
		t.Fatalf("statistics query is not snapshot-aware:\n%s", database.plan.SQL)
	}
}
