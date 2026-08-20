package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/modelsql"
	"github.com/flidai/leapview/pkg/duckdbsql"
)

const rowPresenceColumn = "__leapview_row_present"

func PlanModelTable(ctx context.Context, runtimeDB queryContext, model *semanticmodel.Model, tableName string, table semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTable(ctx, runtimeDB, model, tableName, table, nil)
}

type stagedRelationKind uint8

const (
	stagedRelationTable stagedRelationKind = iota + 1
	stagedRelationQuery
)

// stagedRelation keeps the relation boundary explicit. Prepared source
// sessions use either a temporary table identifier or a generated SELECT
// query; treating both as raw SQL text lets a query be emitted as `FROM
// SELECT ...`, which DuckDB rejects.
type stagedRelation struct {
	value string
	kind  stagedRelationKind
}

func planModelTable(ctx context.Context, runtimeDB queryContext, model *semanticmodel.Model, tableName string, table semanticmodel.Table, staged map[string]stagedRelation) (analyticsmaterialize.ModelTablePlan, error) {
	if err := validateIdentifier(tableName); err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	sqlText := strings.TrimSpace(table.Execution.SQL)
	if table.Execution.Source != "" && sqlText == "" {
		return planDirectSourceTable(ctx, runtimeDB, model, tableName, table, staged)
	}
	if sqlText == "" {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("model table %q requires a direct source binding or definition.sql", tableName)
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
	sqlAnalysis, err := modelsql.Analyze(ctx, sqlText)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("planning model table %q SQL AST: %w", tableName, err)
	}
	if evidence := table.SQLAnalysisEvidence; evidence != nil {
		if !evidence.Validated || !sameStringSet(sortedStrings(evidence.SourceRefs), sortedStrings(sqlAnalysis.SourceRefs)) || !sameStringSet(sortedStrings(evidence.ModelRefs), sortedStrings(sqlAnalysis.ModelRefs)) {
			return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("model table %q SQL AST analysis does not match compiled evidence", tableName)
		}
		table.SourceDependencies = append([]string(nil), evidence.SourceRefs...)
		table.ModelDependencies = append([]string(nil), evidence.ModelRefs...)
	} else {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("model table %q has no compiled SQL analysis evidence", tableName)
	}
	for _, dependency := range table.ModelDependencies {
		if dependency == tableName {
			return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("model table %q cannot read itself", tableName)
		}
		if _, ok := model.Tables[dependency]; !ok {
			return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("model table %q SQL references unknown model %q", tableName, dependency)
		}
	}
	if len(table.SourceDependencies) == 0 && len(table.ModelDependencies) == 0 {
		if err := preparePlanningDatabase(ctx, plannerDB, nil, nil); err != nil {
			return analyticsmaterialize.ModelTablePlan{}, err
		}
		if err := validateModelOutput(ctx, plannerDB, tableName, table, sqlText); err != nil {
			return analyticsmaterialize.ModelTablePlan{}, err
		}
		return materializationPlan(analyticsmaterialize.PlanModeModelSQL, tableName, sqlText), nil
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
	if err := validateModelOutput(ctx, plannerDB, tableName, table, sqlText); err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	explainAnalysis, err := duckdbsql.AnalyzePlan(ctx, plannerDB, sqlText)
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
	rewritten, err := modelsql.RewriteSources(sqlText, sqlAnalysis, replacements, true)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("rewriting model table %q source refs: %w", tableName, err)
	}
	return materializationPlan(analyticsmaterialize.PlanModeProjectedSourceInline, tableName, rewritten), nil
}

func planDirectSourceTable(ctx context.Context, runtimeDB queryContext, model *semanticmodel.Model, tableName string, table semanticmodel.Table, staged map[string]stagedRelation) (analyticsmaterialize.ModelTablePlan, error) {
	source, ok := model.Sources[table.Execution.Source]
	if !ok {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("unknown source %q", table.Execution.Source)
	}
	if len(source.Schema.Columns) == 0 {
		if columns, err := discoverSourceSchema(ctx, runtimeDB, model, source); err != nil {
			return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("discovering source %s schema: %w", table.Execution.Source, err)
		} else if len(columns) > 0 {
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			model.Sources[table.Execution.Source] = source
		}
	}
	relation, err := sourceReadRelation(model, table.Execution.Source, source, nil, modelTableReadColumns(table), false, staged)
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
	for _, statement := range []string{
		"LOAD json",
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET enable_external_access = false",
		"SET lock_configuration = true",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure DuckDB SQL analysis database (%s): %w", statement, err)
		}
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

func validateModelOutput(ctx context.Context, db *sql.DB, tableName string, table semanticmodel.Table, sqlText string) error {
	if len(table.Columns) == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, "DESCRIBE "+sqlText)
	if err != nil {
		return fmt.Errorf("describing model table %q output: %w", tableName, err)
	}
	defer rows.Close()
	type describedColumn struct{ Name, Type string }
	columns := make([]describedColumn, 0, len(table.Columns))
	for rows.Next() {
		var column describedColumn
		var nullable, key, defaultValue, extra sql.NullString
		if err := rows.Scan(&column.Name, &column.Type, &nullable, &key, &defaultValue, &extra); err != nil {
			return fmt.Errorf("reading model table %q output schema: %w", tableName, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	declared := make(map[string]semanticmodel.ModelColumn, len(table.Columns))
	for name, column := range table.Columns {
		declared[name] = column
	}
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, duplicate := seen[column.Name]; duplicate {
			return fmt.Errorf("model table %q output contains duplicate field %q", tableName, column.Name)
		}
		seen[column.Name] = struct{}{}
		declaration, ok := declared[column.Name]
		if !ok {
			return fmt.Errorf("model table %q output exposes undeclared field %q", tableName, column.Name)
		}
		if declaration.Datatype != "" {
			actual := semanticmodel.LogicalDataTypeFromPhysicalType(column.Type)
			if actual != declaration.Datatype {
				return fmt.Errorf("model table %q field %q output type %q is incompatible with declared datatype %q", tableName, column.Name, column.Type, declaration.Datatype)
			}
		}
	}
	if len(columns) != len(declared) {
		return fmt.Errorf("model table %q output fields do not exactly match declared fields", tableName)
	}
	return nil
}

func sourceReadPlansFromExplain(tableName string, table semanticmodel.Table, sourceSchemas map[string][]semanticmodel.ColumnSchema, analysis duckdbsql.Plan) ([]sourceReadPlan, error) {
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
			return nil, fmt.Errorf("model table %q SQL plan scanned source %q outside governed dependencies", tableName, scan.Table)
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
			return nil, fmt.Errorf("model table %q SQL plan did not scan governed source dependency %q", tableName, source)
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

func inlineSourceReplacements(model *semanticmodel.Model, plans []sourceReadPlan, staged map[string]stagedRelation) (map[string]string, error) {
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

func sourceReadRelation(model *semanticmodel.Model, sourceName string, source semanticmodel.Source, fields []string, columns []sourceReadColumn, rowPresenceOnly bool, staged map[string]stagedRelation) (string, error) {
	if relation, ok := staged[sourceName]; ok {
		if strings.TrimSpace(relation.value) == "" {
			return "", fmt.Errorf("staged source %q has an empty relation", sourceName)
		}
		sourceRelation := relation.value
		if relation.kind == stagedRelationQuery {
			sourceRelation = "(" + sourceRelation + ")"
		} else if relation.kind != stagedRelationTable {
			return "", fmt.Errorf("staged source %q has unknown relation kind", sourceName)
		}
		return projectedRelation(sourceRelation, fields, columns, rowPresenceOnly)
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
