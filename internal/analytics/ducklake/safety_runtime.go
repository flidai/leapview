package ducklake

import (
	"context"
	"database/sql"
	"errors"
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

// TableAutoCompact reports the persisted table-scoped auto_compact override.
// The second return value distinguishes an explicit false override from an
// absent row (which would otherwise inherit a DuckLake default).
func (e *Environment) TableAutoCompact(ctx context.Context, schema, table string) (enabled, present bool, err error) {
	if e == nil || e.db == nil {
		return false, false, fmt.Errorf("ducklake environment is not initialized")
	}
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(table) == "" {
		return false, false, fmt.Errorf("table auto_compact scope requires schema and table")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	var value string
	err = conn.QueryRowContext(ctx, "SELECT value FROM ducklake_options(?) WHERE lower(option_name) = 'auto_compact' AND lower(scope) = 'table' AND lower(scope_entry) = lower(?)", catalogAlias, schema+"."+table).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("inspect table auto_compact for %s.%s: %w", schema, table, err)
	}
	return strings.EqualFold(strings.TrimSpace(value), "true"), true, nil
}

// DisableDataInlining explicitly clears every persisted data-inlining option
// in the private catalog. DuckLake's process/attach defaults are set to zero
// when the environment is opened, but inherited global/schema/table options
// can still override those defaults. Normalization must clear those scoped
// options before it inspects the effective policy; merely rejecting a
// non-zero option would make older catalogs impossible to migrate safely.
//
// This operation is metadata-only. It never invokes compaction, checkpoint,
// or any physical maintenance function. The caller is expected to flush live
// inlined rows/deletes separately and re-read both policy and inline state.
func (e *Environment) DisableDataInlining(ctx context.Context) (DataInliningPolicy, error) {
	if e == nil || e.db == nil {
		return DataInliningPolicy{}, fmt.Errorf("ducklake environment is not initialized")
	}
	policy, err := e.DataInliningPolicy(ctx)
	if err != nil {
		return DataInliningPolicy{}, err
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return DataInliningPolicy{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	for _, option := range policy.Persisted {
		if option.Limit == 0 {
			continue
		}
		var statement string
		var args []any
		switch option.Scope {
		case InliningGlobal:
			statement = "CALL ducklake_set_option(?, 'data_inlining_row_limit', 0)"
			args = []any{catalogAlias}
		case InliningSchema:
			if strings.TrimSpace(option.Entry) == "" {
				return DataInliningPolicy{}, fmt.Errorf("persisted schema inlining option has no schema entry")
			}
			statement = "CALL ducklake_set_option(?, 'data_inlining_row_limit', 0, schema => ?)"
			args = []any{catalogAlias, option.Entry}
		case InliningTable:
			schema, table := splitTableOptionEntry(option.Entry)
			if table == "" {
				return DataInliningPolicy{}, fmt.Errorf("persisted table inlining option has no table entry")
			}
			if schema == "" {
				statement = "CALL ducklake_set_option(?, 'data_inlining_row_limit', 0, table_name => ?)"
				args = []any{catalogAlias, table}
			} else {
				statement = "CALL ducklake_set_option(?, 'data_inlining_row_limit', 0, schema => ?, table_name => ?)"
				args = []any{catalogAlias, schema, table}
			}
		default:
			return DataInliningPolicy{}, fmt.Errorf("unsupported persisted data-inlining scope %q", option.Scope)
		}
		if _, err := conn.ExecContext(ctx, statement, args...); err != nil {
			return DataInliningPolicy{}, fmt.Errorf("disable %s data inlining for %q: %w", option.Scope, option.Entry, err)
		}
	}
	release()
	released = true
	updated, err := e.DataInliningPolicy(ctx)
	if err != nil {
		return DataInliningPolicy{}, fmt.Errorf("re-read data-inlining policy after disabling options: %w", err)
	}
	if err := updated.ValidateZero(); err != nil {
		return DataInliningPolicy{}, err
	}
	// Every persisted scope must still be represented after the update. A
	// missing row would indicate that DuckLake interpreted the statement as a
	// delete rather than a zero-valued override, so fail closed instead of
	// silently changing precedence.
	for _, before := range policy.Persisted {
		found := false
		for _, after := range updated.Persisted {
			if before.Scope == after.Scope && sameIdentifier(before.Entry, after.Entry) {
				if after.Limit != 0 {
					return DataInliningPolicy{}, fmt.Errorf("persisted %s data-inlining option for %q remained non-zero", before.Scope, before.Entry)
				}
				found = true
				break
			}
		}
		if !found {
			return DataInliningPolicy{}, fmt.Errorf("persisted %s data-inlining option for %q disappeared while disabling", before.Scope, before.Entry)
		}
	}
	return updated, nil
}

func splitTableOptionEntry(entry string) (schema, table string) {
	entry = strings.TrimSpace(entry)
	if idx := strings.LastIndex(entry, "."); idx > 0 && idx < len(entry)-1 {
		return strings.TrimSpace(entry[:idx]), strings.TrimSpace(entry[idx+1:])
	}
	return "", entry
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

// LegacyInlineTables enumerates currently registered inlined data tables and
// their live row counts. Inlined delete tables are deterministic by table id;
// probing ducklake_tables() keeps this compatible with catalogs which never
// created one.
func (e *Environment) LegacyInlineTables(ctx context.Context) ([]InlineTableState, error) {
	if e == nil || e.db == nil {
		return nil, fmt.Errorf("ducklake environment is not initialized")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var currentSnapshot int64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM ducklake_current_snapshot(?)", catalogAlias).Scan(&currentSnapshot); err != nil {
		return nil, fmt.Errorf("read DuckLake current snapshot for inline validation: %w", err)
	}
	rows, err := conn.QueryContext(ctx, `
WITH current_snapshot AS (SELECT id FROM ducklake_current_snapshot(?))
SELECT i.table_id, t.table_name, i.table_name, t.schema_id, s.schema_name, i.schema_version
FROM __ducklake_metadata_lake.ducklake_inlined_data_tables i
CROSS JOIN current_snapshot cs
JOIN __ducklake_metadata_lake.ducklake_table t ON t.table_id = i.table_id
  AND cs.id >= t.begin_snapshot
  AND (t.end_snapshot IS NULL OR cs.id < t.end_snapshot)
JOIN __ducklake_metadata_lake.ducklake_schema s ON s.schema_id = t.schema_id
  AND cs.id >= s.begin_snapshot
  AND (s.end_snapshot IS NULL OR cs.id < s.end_snapshot)
WHERE i.schema_version = (SELECT MAX(i2.schema_version) FROM __ducklake_metadata_lake.ducklake_inlined_data_tables i2 WHERE i2.table_id = i.table_id)`, catalogAlias)
	if err != nil {
		return nil, fmt.Errorf("inspect DuckLake inlined data tables: %w", err)
	}
	defer rows.Close()
	type inlineEntry struct {
		tableID, schemaID, schemaVersion int64
		inlinedTable, schema, table      string
	}
	var entries []inlineEntry
	for rows.Next() {
		var tableID, schemaID, schemaVersion int64
		var inlinedTable, schema, table string
		if err := rows.Scan(&tableID, &table, &inlinedTable, &schemaID, &schema, &schemaVersion); err != nil {
			return nil, err
		}
		entries = append(entries, inlineEntry{tableID: tableID, schemaID: schemaID, schemaVersion: schemaVersion, inlinedTable: inlinedTable, schema: schema, table: table})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	var result []InlineTableState
	for _, entry := range entries {
		state := InlineTableState{Schema: entry.schema, Table: entry.table}
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s WHERE begin_snapshot <= ? AND (end_snapshot IS NULL OR end_snapshot > ?)", metadataCatalogAlias, quoteIdentifier(entry.inlinedTable))
		if err := conn.QueryRowContext(ctx, query, currentSnapshot, currentSnapshot).Scan(&state.InlinedRows); err != nil {
			return nil, fmt.Errorf("count inlined rows for %s.%s: %w", entry.schema, entry.table, err)
		}
		deleteTable := fmt.Sprintf("ducklake_inlined_delete_%d", entry.tableID)
		if exists, existsErr := duckLakeTableExists(ctx, conn, deleteTable); existsErr != nil {
			return nil, existsErr
		} else if exists {
			deleteQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.%s WHERE begin_snapshot <= ?", metadataCatalogAlias, quoteIdentifier(deleteTable))
			if err := conn.QueryRowContext(ctx, deleteQuery, currentSnapshot).Scan(&state.InlinedDeletes); err != nil {
				return nil, fmt.Errorf("count inlined deletes for %s.%s: %w", entry.schema, entry.table, err)
			}
		}
		result = append(result, state)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Schema == result[j].Schema {
			return result[i].Table < result[j].Table
		}
		return result[i].Schema < result[j].Schema
	})
	return result, nil
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

// FlushLegacyInlineData executes only the table-scoped operation generated by
// PlanLegacyInlineFlush. DuckLake currently skips that operation when the
// target's auto_compact option is false, so the exact table is enabled for
// the call and explicitly disabled again before returning. Native flush is
// not physical cleanup and is therefore allowed for shared pools.
func (e *Environment) FlushLegacyInlineData(ctx context.Context, plan InlineFlushPlan) error {
	if e == nil || e.db == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return err
	}
	defer release()
	for _, target := range plan.Targets {
		if strings.TrimSpace(target.Schema) == "" || strings.TrimSpace(target.Table) == "" {
			return fmt.Errorf("legacy inline flush target requires schema and table")
		}
		// DuckLake's table-scoped flush still consults the persisted
		// auto_compact option and silently returns an empty result when that
		// option is false.  Make the exact target eligible for this one flush,
		// then force the persisted option back to false before returning.  The
		// latter is deliberately fail-closed: a failed restore must never leave
		// a sealed catalog with an implicit maintenance default enabled.
		setAutoCompact := func(enabled bool) error {
			statement := "CALL ducklake_set_option(?, 'auto_compact', ?, schema => ?, table_name => ?)"
			if _, err := conn.ExecContext(ctx, statement, catalogAlias, enabled, target.Schema, target.Table); err != nil {
				return fmt.Errorf("set auto_compact=%t for %s.%s: %w", enabled, target.Schema, target.Table, err)
			}
			return nil
		}
		if err := setAutoCompact(true); err != nil {
			return err
		}
		statement := fmt.Sprintf("CALL ducklake_flush_inlined_data(?, schema_name => ?, table_name => ?)")
		flushErr := error(nil)
		if _, err := conn.ExecContext(ctx, statement, catalogAlias, target.Schema, target.Table); err != nil {
			flushErr = fmt.Errorf("flush legacy inlined data for %s.%s: %w", target.Schema, target.Table, err)
		}
		if err := setAutoCompact(false); err != nil {
			if flushErr != nil {
				return errors.Join(flushErr, fmt.Errorf("restore table maintenance option: %w", err))
			}
			return err
		}
		if flushErr != nil {
			return flushErr
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
