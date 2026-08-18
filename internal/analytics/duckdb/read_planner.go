package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/duckdb/queryjson"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

const rowPresenceColumn = "__leapview_row_present"

func PlanModelTable(ctx context.Context, runtimeDB queryContext, model *semanticmodel.Model, tableName string, table semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTable(ctx, runtimeDB, model, tableName, table, nil)
}

func planModelTable(ctx context.Context, runtimeDB queryContext, model *semanticmodel.Model, tableName string, table semanticmodel.Table, staged map[string]string) (analyticsmaterialize.ModelTablePlan, error) {
	if err := validateIdentifier(tableName); err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	sqlText := strings.TrimSpace(table.Transform.SQL)
	if table.Source != "" && sqlText == "" {
		return planDirectSourceTable(ctx, runtimeDB, model, tableName, table, staged)
	}
	if sqlText == "" {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("model table %q requires source or transform.sql", tableName)
	}
	if len(table.SourceDependencies) == 0 {
		return materializationPlan(analyticsmaterialize.PlanModeModelSQL, tableName, sqlText), nil
	}
	plannerDB, err := sql.Open("duckdb", "")
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	defer plannerDB.Close()
	plannerDB.SetMaxOpenConns(1)
	plannerDB.SetMaxIdleConns(1)
	if err := prepareSQLAnalysisDatabase(ctx, plannerDB); err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	sqlAnalysis, err := analyzeSQLWithDuckDB(ctx, plannerDB, sqlText)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("planning model table %q SQL AST: %w", tableName, err)
	}
	if err := validateSQLAnalysis(tableName, table, sqlAnalysis); err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	sourceSchemas, err := discoverPlanningSourceSchemas(ctx, runtimeDB, model, table.SourceDependencies)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	modelSchemas, err := discoverPlanningModelSchemas(ctx, runtimeDB, model, table.ModelDependencies)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	if err := preparePlanningDatabase(ctx, plannerDB, sourceSchemas, modelSchemas); err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	explainAnalysis, err := explainSQLWithDuckDB(ctx, plannerDB, sqlText)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("planning model table %q source reads: %w", tableName, err)
	}
	plans, err := sourceReadPlansFromExplain(tableName, table, sourceSchemas, explainAnalysis)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	replacements, err := inlineSourceReplacements(model, plans, staged)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	rewritten, err := queryjson.RewriteSourceRefsWithOptions(sqlText, sqlAnalysis.TableRefs, replacements, queryjson.RewriteOptions{AliasUnaliasedSourceRefs: true})
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("rewriting model table %q source refs: %w", tableName, err)
	}
	return materializationPlan(analyticsmaterialize.PlanModeProjectedSourceInline, tableName, rewritten), nil
}

func planDirectSourceTable(ctx context.Context, runtimeDB queryContext, model *semanticmodel.Model, tableName string, table semanticmodel.Table, staged map[string]string) (analyticsmaterialize.ModelTablePlan, error) {
	source, ok := model.Sources[table.Source]
	if !ok {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("unknown source %q", table.Source)
	}
	if len(source.Schema.Columns) == 0 {
		if columns, err := discoverSourceSchema(ctx, runtimeDB, model, source); err != nil {
			return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("discovering source %s schema: %w", table.Source, err)
		} else if len(columns) > 0 {
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			model.Sources[table.Source] = source
		}
	}
	relation, err := sourceReadRelation(model, table.Source, source, nil, modelTableReadColumns(table), false, staged)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	return materializationPlan(analyticsmaterialize.PlanModeDirectSourceRead, tableName, relation), nil
}

func materializationPlan(mode string, tableName string, query string) analyticsmaterialize.ModelTablePlan {
	return analyticsmaterialize.ModelTablePlan{
		Mode: mode,
		SQL:  fmt.Sprintf("CREATE OR REPLACE TABLE model.%s AS %s", tableName, query),
	}
}

func modelTableReadColumns(table semanticmodel.Table) []sourceReadColumn {
	columns := make([]sourceReadColumn, 0, len(table.Columns))
	for name, column := range table.Columns {
		output := column.Name
		if output == "" {
			output = name
		}
		source := column.SourceField
		if source == "" {
			source = output
		}
		columns = append(columns, sourceReadColumn{SourceField: source, OutputField: output})
	}
	sort.Slice(columns, func(i, j int) bool {
		if columns[i].OutputField == columns[j].OutputField {
			return columns[i].SourceField < columns[j].SourceField
		}
		return columns[i].OutputField < columns[j].OutputField
	})
	return columns
}

func discoverPlanningSourceSchemas(ctx context.Context, db queryContext, model *semanticmodel.Model, sources []string) (map[string][]semanticmodel.ColumnSchema, error) {
	result := map[string][]semanticmodel.ColumnSchema{}
	for _, sourceName := range sources {
		source, ok := model.Sources[sourceName]
		if !ok {
			return nil, fmt.Errorf("unknown source %q", sourceName)
		}
		columns := source.Schema.Columns
		if len(columns) == 0 {
			discovered, err := discoverSourceSchema(ctx, db, model, source)
			if err != nil {
				return nil, fmt.Errorf("discovering source %s schema: %w", sourceName, err)
			}
			columns = discovered
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			model.Sources[sourceName] = source
		}
		if len(columns) == 0 {
			return nil, fmt.Errorf("source %q has no discovered schema for SQL read planning", sourceName)
		}
		result[sourceName] = columns
	}
	return result, nil
}

func discoverPlanningModelSchemas(ctx context.Context, db queryContext, model *semanticmodel.Model, dependencies []string) (map[string][]semanticmodel.ColumnSchema, error) {
	result := map[string][]semanticmodel.ColumnSchema{}
	for _, tableName := range dependencies {
		columns, err := describeRelationSchema(ctx, db, "model."+tableName)
		if err == nil && len(columns) > 0 {
			result[tableName] = columns
			continue
		}
		table, ok := model.Tables[tableName]
		if !ok {
			return nil, fmt.Errorf("unknown model table dependency %q", tableName)
		}
		if len(table.Schema.Columns) > 0 {
			result[tableName] = table.Schema.Columns
			continue
		}
		columns = modelColumnsAsSchema(table)
		if len(columns) == 0 {
			return nil, fmt.Errorf("model table dependency %q has no schema for SQL read planning", tableName)
		}
		result[tableName] = columns
	}
	return result, nil
}

func modelColumnsAsSchema(table semanticmodel.Table) []semanticmodel.ColumnSchema {
	names := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]semanticmodel.ColumnSchema, 0, len(names))
	for index, name := range names {
		column := table.Columns[name]
		columnType := column.Type
		if columnType == "" {
			columnType = "VARCHAR"
		}
		result = append(result, semanticmodel.ColumnSchema{Name: name, Ordinal: index + 1, PhysicalType: columnType})
	}
	return result
}

func preparePlanningDatabase(ctx context.Context, db *sql.DB, sourceSchemas map[string][]semanticmodel.ColumnSchema, modelSchemas map[string][]semanticmodel.ColumnSchema) error {
	if err := prepareSQLAnalysisDatabase(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA source"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA model"); err != nil {
		return err
	}
	for _, name := range sortedKeys(sourceSchemas) {
		if err := createPlanningTable(ctx, db, "source", name, sourceSchemas[name]); err != nil {
			return err
		}
	}
	for _, name := range sortedKeys(modelSchemas) {
		if err := createPlanningTable(ctx, db, "model", name, modelSchemas[name]); err != nil {
			return err
		}
	}
	return nil
}

func prepareSQLAnalysisDatabase(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "LOAD json"); err != nil {
		return fmt.Errorf("loading DuckDB json extension: %w", err)
	}
	return nil
}

func createPlanningTable(ctx context.Context, db *sql.DB, schema string, table string, columns []semanticmodel.ColumnSchema) error {
	if err := validateIdentifier(table); err != nil {
		return err
	}
	definitions := []string{}
	values := []string{}
	for _, column := range sortedColumns(columns) {
		if err := validateIdentifier(column.Name); err != nil {
			return fmt.Errorf("planning table %s.%s column %q is invalid: %w", schema, table, column.Name, err)
		}
		columnType := planningColumnType(column.PhysicalType)
		definitions = append(definitions, quoteIdentifier(column.Name)+" "+columnType)
		values = append(values, planningLiteral(columnType))
	}
	if len(definitions) == 0 {
		definitions = append(definitions, quoteIdentifier(rowPresenceColumn)+" BOOLEAN")
		values = append(values, "true")
	}
	tableName := quoteIdentifier(schema) + "." + quoteIdentifier(table)
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+tableName+" ("+strings.Join(definitions, ", ")+")"); err != nil {
		return fmt.Errorf("creating planning table %s.%s: %w", schema, table, err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+tableName+" VALUES ("+strings.Join(values, ", ")+")"); err != nil {
		return fmt.Errorf("seeding planning table %s.%s: %w", schema, table, err)
	}
	return nil
}

func planningColumnType(physicalType string) string {
	value := strings.TrimSpace(physicalType)
	if value == "" {
		return "VARCHAR"
	}
	return value
}

func planningLiteral(columnType string) string {
	upper := strings.ToUpper(columnType)
	switch {
	case strings.Contains(upper, "INT") || strings.Contains(upper, "DECIMAL") || strings.Contains(upper, "DOUBLE") || strings.Contains(upper, "FLOAT") || strings.Contains(upper, "REAL") || strings.Contains(upper, "NUMERIC"):
		return "0"
	case strings.Contains(upper, "BOOL"):
		return "false"
	case strings.Contains(upper, "DATE") && !strings.Contains(upper, "TIME"):
		return "DATE '1970-01-01'"
	case strings.Contains(upper, "TIME"):
		return "TIMESTAMP '1970-01-01 00:00:00'"
	default:
		return "'__leapview_stub__'"
	}
}

func analyzeSQLWithDuckDB(ctx context.Context, db *sql.DB, sqlText string) (queryjson.SQLAnalysis, error) {
	rows, err := db.QueryContext(ctx, "SELECT CAST(json_serialize_sql(CAST(? AS VARCHAR), skip_default := true, skip_empty := true, skip_null := true) AS VARCHAR)", sqlText)
	if err != nil {
		return queryjson.SQLAnalysis{}, err
	}
	defer rows.Close()
	payload, err := singleStringResult(rows, "json_serialize_sql")
	if err != nil {
		return queryjson.SQLAnalysis{}, err
	}
	return queryjson.AnalyzeSQL([]byte(payload))
}

func explainSQLWithDuckDB(ctx context.Context, db *sql.DB, sqlText string) (queryjson.ExplainAnalysis, error) {
	if _, err := db.ExecContext(ctx, "SET disabled_optimizers = 'filter_pushdown'"); err != nil {
		return queryjson.ExplainAnalysis{}, err
	}
	rows, err := db.QueryContext(ctx, "EXPLAIN (FORMAT json) "+sqlText)
	if err != nil {
		return queryjson.ExplainAnalysis{}, err
	}
	defer rows.Close()
	payload, err := singleStringResult(rows, "explain_value")
	if err != nil {
		return queryjson.ExplainAnalysis{}, err
	}
	return queryjson.AnalyzeExplain([]byte(payload))
}

func singleStringResult(rows *sql.Rows, preferredColumn string) (string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	values := make([]sql.NullString, len(columns))
	scan := make([]any, len(values))
	for index := range values {
		scan[index] = &values[index]
	}
	if err := rows.Scan(scan...); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	index := len(values) - 1
	for columnIndex, column := range columns {
		if strings.EqualFold(column, preferredColumn) {
			index = columnIndex
			break
		}
	}
	if index < 0 || index >= len(values) || !values[index].Valid {
		return "", fmt.Errorf("query returned no %s payload", preferredColumn)
	}
	return values[index].String, nil
}

func validateSQLAnalysis(tableName string, table semanticmodel.Table, analysis queryjson.SQLAnalysis) error {
	if len(analysis.RawRefs) > 0 {
		return fmt.Errorf("model table %q model SQL must reference sources through source.<name>; raw.<name> is internal", tableName)
	}
	if len(analysis.QualifiedSourceColumnRefs) > 0 {
		return fmt.Errorf("model table %q column reference %q must use a table alias; source.<name> is only valid in FROM/JOIN relations", tableName, analysis.QualifiedSourceColumnRefs[0])
	}
	if !sameStringSet(sortedStrings(table.SourceDependencies), analysis.SourceRefs) {
		return fmt.Errorf("model table %q SQL source references %v do not match declared sources %v", tableName, analysis.SourceRefs, sortedStrings(table.SourceDependencies))
	}
	return nil
}

func sourceReadPlansFromExplain(tableName string, table semanticmodel.Table, sourceSchemas map[string][]semanticmodel.ColumnSchema, analysis queryjson.ExplainAnalysis) ([]sourceReadPlan, error) {
	type accumulator struct {
		fields          map[string]struct{}
		rowPresenceOnly bool
	}
	accumulators := map[string]*accumulator{}
	declared := map[string]struct{}{}
	for _, source := range table.SourceDependencies {
		declared[source] = struct{}{}
		accumulators[source] = &accumulator{fields: map[string]struct{}{}}
	}
	for _, scan := range analysis.Scans {
		if scan.Schema != "source" {
			continue
		}
		if _, ok := declared[scan.Table]; !ok {
			return nil, fmt.Errorf("model table %q SQL plan scanned undeclared source %q", tableName, scan.Table)
		}
		current := accumulators[scan.Table]
		if len(scan.Projections) == 0 {
			current.rowPresenceOnly = true
			continue
		}
		for _, projection := range scan.Projections {
			current.fields[projection] = struct{}{}
		}
	}
	plans := []sourceReadPlan{}
	for _, source := range sortedStrings(table.SourceDependencies) {
		current := accumulators[source]
		if current == nil {
			return nil, fmt.Errorf("model table %q SQL plan did not scan declared source %q", tableName, source)
		}
		fields := sortedSet(current.fields)
		if len(fields) == 0 && !current.rowPresenceOnly {
			return nil, fmt.Errorf("model table %q SQL plan did not expose projections for source %q", tableName, source)
		}
		if err := validatePlannedFields(source, sourceSchemas[source], fields); err != nil {
			return nil, fmt.Errorf("model table %q: %w", tableName, err)
		}
		plans = append(plans, sourceReadPlan{
			Source:          source,
			Fields:          fields,
			RowPresenceOnly: len(fields) == 0 && current.rowPresenceOnly,
		})
	}
	return plans, nil
}

func inlineSourceReplacements(model *semanticmodel.Model, plans []sourceReadPlan, staged map[string]string) (map[string]string, error) {
	replacements := map[string]string{}
	for _, plan := range plans {
		source, ok := model.Sources[plan.Source]
		if !ok {
			return nil, fmt.Errorf("unknown source %q", plan.Source)
		}
		relation, err := sourceReadRelation(model, plan.Source, source, plan.Fields, plan.Columns, plan.RowPresenceOnly, staged)
		if err != nil {
			return nil, fmt.Errorf("compiling source %s relation: %w", plan.Source, err)
		}
		replacements[plan.Source] = "(" + relation + ")"
	}
	return replacements, nil
}

func sourceReadRelation(model *semanticmodel.Model, sourceName string, source semanticmodel.Source, fields []string, columns []sourceReadColumn, rowPresenceOnly bool, staged map[string]string) (string, error) {
	if relation := staged[sourceName]; relation != "" {
		return projectedRelation(relation, fields, columns, rowPresenceOnly)
	}
	return SourceReadRelation(model, source, fields, columns, rowPresenceOnly)
}

func validatePlannedFields(source string, columns []semanticmodel.ColumnSchema, fields []string) error {
	available := map[string]struct{}{}
	for _, column := range columns {
		available[column.Name] = struct{}{}
	}
	for _, field := range fields {
		if err := validateIdentifier(field); err != nil {
			return fmt.Errorf("source %q planned field %q is invalid: %w", source, field, err)
		}
		if _, ok := available[field]; !ok {
			return fmt.Errorf("source %q planned field %q is not in discovered schema", source, field)
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sortedStrings(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	return result
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
