package duckdb

import (
	"context"
	"fmt"
	"math/big"

	"github.com/flidai/leapview/internal/analytics/catalogstats"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type catalogStatisticsQueryer interface {
	Query(context.Context, semanticquery.Plan) (semanticquery.Rows, error)
}

// CatalogTableStatistics reads table statistics from the same DuckLake
// environment and serving snapshot as this runtime. This is intentionally a
// runtime port: node-level admin storage may point at a different mutable
// catalog after sealed-catalog activation.
func (r *ProjectRuntime) CatalogTableStatistics(ctx context.Context) ([]catalogstats.Table, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("project runtime database is unavailable")
	}
	queryer, ok := r.db.(catalogStatisticsQueryer)
	if !ok {
		return nil, fmt.Errorf("project runtime database does not expose catalog metadata queries")
	}
	snapshotID := r.DuckLakeSnapshotID()
	rows, err := queryer.Query(ctx, semanticquery.Plan{
		SQL: `
WITH selected_snapshot AS (
	SELECT CASE WHEN ? > 0 THEN ? ELSE id END AS id
	FROM ducklake_current_snapshot(?)
), active_tables AS (
	SELECT s.schema_name, t.table_name, t.table_id
	FROM __ducklake_metadata_lake.ducklake_table t
	JOIN __ducklake_metadata_lake.ducklake_schema s ON s.schema_id = t.schema_id
	CROSS JOIN selected_snapshot selected
	WHERE t.begin_snapshot <= selected.id
	  AND (t.end_snapshot IS NULL OR selected.id < t.end_snapshot)
	  AND s.begin_snapshot <= selected.id
	  AND (s.end_snapshot IS NULL OR selected.id < s.end_snapshot)
), file_rollup AS (
	SELECT f.table_id,
	       count(*) AS file_count,
	       coalesce(sum(f.record_count), 0) AS row_count,
	       coalesce(sum(f.file_size_bytes), 0) AS byte_count
	FROM __ducklake_metadata_lake.ducklake_data_file f
	CROSS JOIN selected_snapshot selected
	WHERE f.begin_snapshot <= selected.id
	  AND (f.end_snapshot IS NULL OR selected.id < f.end_snapshot)
	GROUP BY f.table_id
), column_rollup AS (
	SELECT c.table_id, count(*) AS column_count
	FROM __ducklake_metadata_lake.ducklake_column c
	CROSS JOIN selected_snapshot selected
	WHERE c.parent_column IS NULL
	  AND c.begin_snapshot <= selected.id
	  AND (c.end_snapshot IS NULL OR selected.id < c.end_snapshot)
	GROUP BY c.table_id
)
SELECT a.schema_name, a.table_name,
       coalesce(f.row_count, 0), coalesce(c.column_count, 0),
       coalesce(f.file_count, 0), coalesce(f.byte_count, 0),
       selected.id AS snapshot_id
FROM active_tables a
CROSS JOIN selected_snapshot selected
LEFT JOIN file_rollup f ON f.table_id = a.table_id
LEFT JOIN column_rollup c ON c.table_id = a.table_id
ORDER BY a.schema_name, a.table_name`,
		Args:    []any{snapshotID, snapshotID, "lake"},
		Columns: []string{"schema_name", "table_name", "row_count", "column_count", "file_count", "byte_count", "snapshot_id"},
	})
	if err != nil {
		return nil, fmt.Errorf("inspect serving DuckLake catalog: %w", err)
	}
	statistics := make([]catalogstats.Table, 0, len(rows))
	for _, row := range rows {
		statistics = append(statistics, catalogstats.Table{
			Schema:      catalogStatisticString(row["schema_name"]),
			Name:        catalogStatisticString(row["table_name"]),
			RowCount:    catalogStatisticInt64(row["row_count"]),
			ColumnCount: catalogStatisticInt64(row["column_count"]),
			FileCount:   catalogStatisticInt64(row["file_count"]),
			SizeBytes:   catalogStatisticInt64(row["byte_count"]),
			SnapshotID:  catalogStatisticInt64(row["snapshot_id"]),
		})
	}
	return statistics, nil
}

func catalogStatisticString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

func catalogStatisticInt64(value any) int64 {
	switch value := value.(type) {
	case *big.Int:
		if value != nil && value.IsInt64() {
			return value.Int64()
		}
		return 0
	case big.Int:
		if value.IsInt64() {
			return value.Int64()
		}
		return 0
	case int64:
		return value
	case int32:
		return int64(value)
	case int:
		return int64(value)
	case uint64:
		return int64(value)
	case uint32:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}
