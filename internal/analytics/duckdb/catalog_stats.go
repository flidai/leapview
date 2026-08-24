package duckdb

import (
	"context"
	"fmt"
	"math/big"

	"github.com/flidai/leapview/internal/analytics/catalogstats"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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
), active_columns AS (
	SELECT c.table_id, c.column_name, c.column_type, c.column_order,
	       c.nulls_allowed, c.default_value
	FROM __ducklake_metadata_lake.ducklake_column c
	CROSS JOIN selected_snapshot selected
	WHERE c.parent_column IS NULL
	  AND c.begin_snapshot <= selected.id
	  AND (c.end_snapshot IS NULL OR selected.id < c.end_snapshot)
)
SELECT a.schema_name, a.table_name,
       coalesce(f.row_count, 0), coalesce(c.column_count, 0),
       coalesce(f.file_count, 0), coalesce(f.byte_count, 0),
	   selected.id AS snapshot_id,
	   columns.column_name, columns.column_type, columns.column_order,
	   columns.nulls_allowed, columns.default_value
FROM active_tables a
CROSS JOIN selected_snapshot selected
LEFT JOIN file_rollup f ON f.table_id = a.table_id
LEFT JOIN column_rollup c ON c.table_id = a.table_id
LEFT JOIN active_columns columns ON columns.table_id = a.table_id
ORDER BY a.schema_name, a.table_name, columns.column_order`,
		Args:    []any{snapshotID, snapshotID, "lake"},
		Columns: []string{"schema_name", "table_name", "row_count", "column_count", "file_count", "byte_count", "snapshot_id", "column_name", "column_type", "column_order", "nulls_allowed", "default_value"},
	})
	if err != nil {
		return nil, fmt.Errorf("inspect serving DuckLake catalog: %w", err)
	}
	statistics := make([]catalogstats.Table, 0, len(rows))
	indexes := make(map[string]int, len(rows))
	for _, row := range rows {
		schemaName := catalogStatisticString(row["schema_name"])
		tableName := catalogStatisticString(row["table_name"])
		key := schemaName + "\x00" + tableName
		index, ok := indexes[key]
		if !ok {
			index = len(statistics)
			indexes[key] = index
			statistics = append(statistics, catalogstats.Table{
				Schema:      schemaName,
				Name:        tableName,
				RowCount:    catalogStatisticInt64(row["row_count"]),
				ColumnCount: catalogStatisticInt64(row["column_count"]),
				FileCount:   catalogStatisticInt64(row["file_count"]),
				SizeBytes:   catalogStatisticInt64(row["byte_count"]),
				SnapshotID:  catalogStatisticInt64(row["snapshot_id"]),
			})
		}
		if columnName := catalogStatisticString(row["column_name"]); columnName != "" {
			statistics[index].Columns = append(statistics[index].Columns, semanticmodel.ColumnSchema{
				Name: columnName, Ordinal: int(catalogStatisticInt64(row["column_order"])),
				PhysicalType: catalogStatisticString(row["column_type"]),
				Nullable:     catalogStatisticNullable(row["nulls_allowed"]),
				Default:      catalogStatisticString(row["default_value"]),
			})
		}
	}
	return statistics, nil
}

func catalogStatisticNullable(value any) *bool {
	valueBool, ok := value.(bool)
	if !ok {
		return nil
	}
	return &valueBool
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
