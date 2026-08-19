package ducklake

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// DuckLake hides its metadata database behind a generated database name when
// no explicit METADATA_CATALOG is supplied. Open always uses the stable alias
// "lake", so this is the corresponding generated metadata database name.
const metadataCatalogAlias = "__ducklake_metadata_lake"

// DataInliningPolicy reads the process/attach defaults recorded by this
// environment and all persisted DuckLake overrides. The latter are intentionally
// not inferred from a documented default: DuckLake's fallback has varied.
func (e *Environment) DataInliningPolicy(ctx context.Context) (DataInliningPolicy, error) {
	if e == nil || e.db == nil {
		return DataInliningPolicy{}, fmt.Errorf("ducklake environment is not initialized")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return DataInliningPolicy{}, err
	}
	defer release()

	policy := DataInliningPolicy{ProcessLimit: 0, ProcessSet: true, AttachLimit: 0, AttachSet: true}
	rows, err := conn.QueryContext(ctx, "SELECT option_name, value, scope, scope_entry FROM ducklake_options(?) WHERE lower(option_name) = 'data_inlining_row_limit'", catalogAlias)
	if err != nil {
		return DataInliningPolicy{}, fmt.Errorf("inspect DuckLake data-inlining options: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, value, scope string
		var entry sql.NullString
		if err := rows.Scan(&name, &value, &scope, &entry); err != nil {
			return DataInliningPolicy{}, err
		}
		limit, parseErr := parseInliningLimit(value)
		if parseErr != nil {
			return DataInliningPolicy{}, fmt.Errorf("invalid persisted data_inlining_row_limit: %w", parseErr)
		}
		var inliningScope InliningScope
		switch strings.ToUpper(strings.TrimSpace(scope)) {
		case string(InliningGlobal):
			inliningScope = InliningGlobal
		case string(InliningSchema):
			inliningScope = InliningSchema
		case string(InliningTable):
			inliningScope = InliningTable
		default:
			return DataInliningPolicy{}, fmt.Errorf("unknown DuckLake option scope %q", scope)
		}
		policy.Persisted = append(policy.Persisted, PersistedInliningOption{Scope: inliningScope, Entry: entryString(entry), Limit: limit})
	}
	if err := rows.Err(); err != nil {
		return DataInliningPolicy{}, err
	}
	sort.Slice(policy.Persisted, func(i, j int) bool {
		if policy.Persisted[i].Scope == policy.Persisted[j].Scope {
			return policy.Persisted[i].Entry < policy.Persisted[j].Entry
		}
		return policy.Persisted[i].Scope < policy.Persisted[j].Scope
	})
	return policy, nil
}

func parseInliningLimit(value string) (uint64, error) {
	var limit uint64
	if _, err := fmt.Sscan(strings.TrimSpace(value), &limit); err != nil {
		return 0, err
	}
	return limit, nil
}

func entryString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// ValidateNoLiveInlineData rejects catalogs that still contain current inlined
// rows or deletes. It intentionally returns no table state and never repairs
// the catalog; callers must reject the candidate and rebuild it through the
// normal materialization path.
func (e *Environment) ValidateNoLiveInlineData(ctx context.Context) error {
	if e == nil || e.db == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return err
	}
	defer release()
	var currentSnapshot int64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM ducklake_current_snapshot(?)", catalogAlias).Scan(&currentSnapshot); err != nil {
		return fmt.Errorf("read DuckLake current snapshot for inline validation: %w", err)
	}
	// Deletion inlining applies to rows in existing Parquet files, so a table
	// can have a live inlined-delete table without an entry in
	// ducklake_inlined_data_tables. Enumerate every current logical table first,
	// then associate its newest inlined-data table (if any) and probe the delete
	// table independently below.
	rows, err := conn.QueryContext(ctx, `
WITH current_snapshot AS (SELECT id FROM ducklake_current_snapshot(?)),
current_tables AS (
  SELECT t.table_id, t.table_name, s.schema_name
  FROM __ducklake_metadata_lake.ducklake_table t
  JOIN __ducklake_metadata_lake.ducklake_schema s ON s.schema_id = t.schema_id
  CROSS JOIN current_snapshot cs
  WHERE cs.id >= t.begin_snapshot
    AND (t.end_snapshot IS NULL OR cs.id < t.end_snapshot)
    AND cs.id >= s.begin_snapshot
    AND (s.end_snapshot IS NULL OR cs.id < s.end_snapshot)
), latest_inline AS (
  SELECT table_id, table_name,
         ROW_NUMBER() OVER (PARTITION BY table_id ORDER BY schema_version DESC) AS row_number
  FROM __ducklake_metadata_lake.ducklake_inlined_data_tables
)
SELECT ct.table_id, ct.table_name, ct.schema_name, li.table_name
FROM current_tables ct
LEFT JOIN latest_inline li ON li.table_id = ct.table_id AND li.row_number = 1`, catalogAlias)
	if err != nil {
		return fmt.Errorf("inspect DuckLake tables for inline data: %w", err)
	}
	type inlineEntry struct {
		tableID      int64
		inlinedTable sql.NullString
		schema       string
		table        string
	}
	var entries []inlineEntry
	for rows.Next() {
		var tableID int64
		var inlinedTable sql.NullString
		var schema, table string
		if err := rows.Scan(&tableID, &table, &schema, &inlinedTable); err != nil {
			_ = rows.Close()
			return err
		}
		entries = append(entries, inlineEntry{tableID: tableID, inlinedTable: inlinedTable, schema: schema, table: table})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.inlinedTable.Valid && strings.TrimSpace(entry.inlinedTable.String) != "" {
			query := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s WHERE begin_snapshot <= ? AND (end_snapshot IS NULL OR end_snapshot > ?)", metadataCatalogAlias, quoteIdentifier(entry.inlinedTable.String))
			var inlinedRows int64
			if err := conn.QueryRowContext(ctx, query, currentSnapshot, currentSnapshot).Scan(&inlinedRows); err != nil {
				return fmt.Errorf("count inlined rows for %s.%s: %w", entry.schema, entry.table, err)
			}
			if inlinedRows > 0 {
				return fmt.Errorf("%s.%s has %d live inlined rows: %w", entry.schema, entry.table, inlinedRows, ErrLiveInlineData)
			}
		}
		deleteTable := fmt.Sprintf("ducklake_inlined_delete_%d", entry.tableID)
		if exists, existsErr := duckLakeTableExists(ctx, conn, deleteTable); existsErr != nil {
			return existsErr
		} else if exists {
			deleteQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s WHERE begin_snapshot <= ?", metadataCatalogAlias, quoteIdentifier(deleteTable))
			var inlinedDeletes int64
			if err := conn.QueryRowContext(ctx, deleteQuery, currentSnapshot).Scan(&inlinedDeletes); err != nil {
				return fmt.Errorf("count inlined deletes for %s.%s: %w", entry.schema, entry.table, err)
			}
			if inlinedDeletes > 0 {
				return fmt.Errorf("%s.%s has %d live inlined deletes: %w", entry.schema, entry.table, inlinedDeletes, ErrLiveInlineData)
			}
		}
	}
	return nil
}

func duckLakeTableExists(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	// DuckLake's metadata catalog is a hidden DuckDB database, so the generic
	// duckdb_tables() table function does not enumerate its internal tables.
	// Probe the exact generated table instead; a missing table is the normal
	// case for catalogs which have never recorded an inlined file deletion.
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s", metadataCatalogAlias, quoteIdentifier(name))
	err := conn.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("inspect DuckLake inlined delete table %q: %w", name, err)
	}
	return true, nil
}

// CurrentFileSet enumerates the complete current data/delete-file closure for
// one logical table through DuckLake's metadata function. It is used by the
// compatibility fixture and by global mark-and-sweep qualification; it never
// treats SQLite or a directory walk as a physical manifest.
func (e *Environment) CurrentFileSet(ctx context.Context, catalogID, schema, table string) (CatalogFileSet, error) {
	if e == nil || e.db == nil {
		return CatalogFileSet{}, fmt.Errorf("ducklake environment is not initialized")
	}
	if strings.TrimSpace(catalogID) == "" {
		catalogID = catalogAlias
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return CatalogFileSet{}, err
	}
	defer release()
	var snapshot int64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM ducklake_current_snapshot(?)", catalogAlias).Scan(&snapshot); err != nil {
		return CatalogFileSet{}, err
	}
	rows, err := conn.QueryContext(ctx, "SELECT data_file, delete_file FROM ducklake_list_files(?, ?, schema => ?, snapshot_version => ?)", catalogAlias, table, schema, snapshot)
	if err != nil {
		return CatalogFileSet{}, fmt.Errorf("list DuckLake files for %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	result := CatalogFileSet{CatalogID: catalogID}
	for rows.Next() {
		var dataFile, deleteFile sql.NullString
		if err := rows.Scan(&dataFile, &deleteFile); err != nil {
			return CatalogFileSet{}, err
		}
		if dataFile.Valid && dataFile.String != "" {
			result.DataFiles = append(result.DataFiles, dataFile.String)
		}
		if deleteFile.Valid && deleteFile.String != "" {
			result.DeleteFiles = append(result.DeleteFiles, deleteFile.String)
		}
	}
	if err := rows.Err(); err != nil {
		return CatalogFileSet{}, err
	}
	sort.Strings(result.DataFiles)
	sort.Strings(result.DeleteFiles)
	return result, nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
