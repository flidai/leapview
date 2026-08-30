package ducklake

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// BaseTable identifies one visible, non-temporary DuckLake base table. It is
// intentionally value-only so callers cannot obtain a mutable SQL handle.
type BaseTable struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// VisibleBaseTables enumerates every current user table through DuckDB's
// catalog metadata. Global GC uses this before ducklake_list_files; it never
// trusts a cached or SQLite file manifest.
func (e *Environment) VisibleBaseTables(ctx context.Context) ([]BaseTable, error) {
	if e == nil || e.db == nil {
		return nil, fmt.Errorf("ducklake environment is not initialized")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.QueryContext(ctx, "SELECT schema_name, table_name FROM duckdb_tables() WHERE database_name = 'lake' AND NOT internal AND NOT temporary ORDER BY schema_name, table_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []BaseTable
	for rows.Next() {
		var table BaseTable
		if err := rows.Scan(&table.Schema, &table.Table); err != nil {
			return nil, err
		}
		if table.Schema == "" || table.Table == "" {
			return nil, fmt.Errorf("DuckDB table metadata has invalid schema/table")
		}
		result = append(result, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Schema == result[j].Schema {
			return result[i].Table < result[j].Table
		}
		return result[i].Schema < result[j].Schema
	})
	return result, nil
}

// CurrentFileClosure reads the current snapshot and every visible table's
// data/delete closure on one pinned DuckDB connection.  A GC mark must never
// combine file references from snapshots that changed between tables.
func (e *Environment) CurrentFileClosure(ctx context.Context, catalogID string) (int64, []BaseTable, CatalogFileSet, error) {
	if e == nil || e.db == nil {
		return 0, nil, CatalogFileSet{}, fmt.Errorf("ducklake environment is not initialized")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return 0, nil, CatalogFileSet{}, err
	}
	defer release()
	var snapshot int64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM ducklake_current_snapshot(?)", catalogAlias).Scan(&snapshot); err != nil {
		return 0, nil, CatalogFileSet{}, err
	}
	rows, err := conn.QueryContext(ctx, "SELECT schema_name, table_name FROM duckdb_tables() WHERE database_name = 'lake' AND NOT internal AND NOT temporary ORDER BY schema_name, table_name")
	if err != nil {
		return 0, nil, CatalogFileSet{}, err
	}
	var tables []BaseTable
	for rows.Next() {
		var table BaseTable
		if err := rows.Scan(&table.Schema, &table.Table); err != nil {
			rows.Close()
			return 0, nil, CatalogFileSet{}, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, CatalogFileSet{}, err
	}
	rows.Close()
	closure := CatalogFileSet{CatalogID: catalogID}
	for _, table := range tables {
		files, err := conn.QueryContext(ctx, "SELECT data_file, delete_file FROM ducklake_list_files(?, ?, schema => ?, snapshot_version => ?)", catalogAlias, table.Table, table.Schema, snapshot)
		if err != nil {
			return 0, nil, CatalogFileSet{}, fmt.Errorf("list DuckLake files for %s.%s: %w", table.Schema, table.Table, err)
		}
		for files.Next() {
			var dataFile, deleteFile sql.NullString
			if err := files.Scan(&dataFile, &deleteFile); err != nil {
				files.Close()
				return 0, nil, CatalogFileSet{}, err
			}
			if dataFile.Valid && dataFile.String != "" {
				closure.DataFiles = append(closure.DataFiles, dataFile.String)
			}
			if deleteFile.Valid && deleteFile.String != "" {
				closure.DeleteFiles = append(closure.DeleteFiles, deleteFile.String)
			}
		}
		if err := files.Err(); err != nil {
			files.Close()
			return 0, nil, CatalogFileSet{}, err
		}
		files.Close()
	}
	sort.Strings(closure.DataFiles)
	sort.Strings(closure.DeleteFiles)
	return snapshot, tables, closure, nil
}
