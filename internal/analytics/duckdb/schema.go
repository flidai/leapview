package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
)

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func discoverSchemas(ctx context.Context, provider analyticsresource.SessionProvider, model *semanticmodel.Model) error {
	return discoverSchemasInNamespace(ctx, provider, model, "model")
}

func discoverSchemasInNamespace(ctx context.Context, provider analyticsresource.SessionProvider, model *semanticmodel.Model, relationNamespace string) error {
	if provider == nil {
		return fmt.Errorf("schema discovery requires a DuckDB database")
	}
	db, err := provider.Session(ctx)
	if err != nil {
		return err
	}
	if model == nil {
		return fmt.Errorf("schema discovery requires a semantic model")
	}
	if err := validateRelationNamespace(relationNamespace); err != nil {
		return fmt.Errorf("relation namespace: %w", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		return err
	}
	query := `
SELECT schema_name, table_name, column_name, column_index, data_type, is_nullable, column_default, comment
FROM duckdb_columns()
WHERE database_name = ? AND schema_name IN ('source', 'model')
ORDER BY schema_name, table_name, column_index`
	args := []any{databaseName}
	if relationNamespace != "model" {
		query = `
SELECT schema_name, table_name, column_name, column_index, data_type, is_nullable, column_default, comment
FROM duckdb_columns()
WHERE database_name = ? AND (schema_name = 'source' OR schema_name = ?)
ORDER BY schema_name, table_name, column_index`
		args = append(args, relationNamespace)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	sourceColumns := map[string][]semanticmodel.ColumnSchema{}
	tableColumns := map[string][]semanticmodel.ColumnSchema{}
	for rows.Next() {
		var schemaName, tableName, columnName, dataType string
		var ordinal int
		var nullable sql.NullBool
		var defaultValue, comment sql.NullString
		if err := rows.Scan(&schemaName, &tableName, &columnName, &ordinal, &dataType, &nullable, &defaultValue, &comment); err != nil {
			return err
		}
		var nullableValue *bool
		if nullable.Valid {
			value := nullable.Bool
			nullableValue = &value
		}
		column := semanticmodel.ColumnSchema{
			Name:         columnName,
			Ordinal:      ordinal,
			PhysicalType: dataType,
			Nullable:     nullableValue,
			Default:      defaultValue.String,
			Comment:      comment.String,
		}
		switch schemaName {
		case "source":
			sourceColumns[tableName] = append(sourceColumns[tableName], column)
		case relationNamespace:
			tableColumns[tableName] = append(tableColumns[tableName], column)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for name, source := range model.Sources {
		columns := source.Schema.Columns
		var err error
		if len(columns) == 0 {
			columns, err = discoverSourceSchema(ctx, db, model, source)
			if err != nil {
				return fmt.Errorf("discovering source %s schema: %w", name, err)
			}
		}
		if len(columns) == 0 {
			columns = sortedColumns(sourceColumns[name])
		}
		if len(columns) == 0 {
			columns = source.Schema.Columns
		}
		source.Schema = semanticmodel.TableSchema{Columns: columns}
		model.Sources[name] = source
	}
	for name, table := range model.Tables {
		physicalName, err := physicalTableName(model, name)
		if err != nil {
			return fmt.Errorf("resolving physical table for semantic dataset %q: %w", name, err)
		}
		columns := sortedColumns(tableColumns[physicalName])
		if len(columns) == 0 {
			// Keep a previously discovered or authored schema when the physical
			// relation is not visible in this session (for example, a staged
			// refresh before the commit becomes readable).
			columns = sortedColumns(table.Schema.Columns)
		}
		// Authored grain/identity claims are semantic contracts, not physical
		// discovery facts. Runtime verification proves them against data; do not
		// mark discovered columns as PrimaryKey merely because they were authored.
		for index := range columns {
			columns[index].PrimaryKey = false
		}
		table.Schema = semanticmodel.TableSchema{Columns: columns}
		model.Tables[name] = table
	}
	return model.ValidateDiscoveredSchemas()
}

func discoverSourceSchema(ctx context.Context, db queryContext, model *semanticmodel.Model, source semanticmodel.Source) ([]semanticmodel.ColumnSchema, error) {
	plan, err := ResolveSourcePlan(model, source)
	if err != nil {
		return nil, err
	}
	adapter, err := sourceAdapterForPlan(plan)
	if err != nil {
		return nil, err
	}
	return adapter.Discover(ctx, db, model, source)
}

func (pathSourceAdapter) Discover(ctx context.Context, db queryContext, model *semanticmodel.Model, source semanticmodel.Source) ([]semanticmodel.ColumnSchema, error) {
	return describeSourceSchema(ctx, db, model, source)
}

func (attachedObjectSourceAdapter) Discover(ctx context.Context, db queryContext, model *semanticmodel.Model, source semanticmodel.Source) ([]semanticmodel.ColumnSchema, error) {
	return describeSourceSchema(ctx, db, model, source)
}

func (quackObjectSourceAdapter) Discover(ctx context.Context, db queryContext, model *semanticmodel.Model, source semanticmodel.Source) ([]semanticmodel.ColumnSchema, error) {
	return describeSourceSchema(ctx, db, model, source)
}

func describeSourceSchema(ctx context.Context, db queryContext, model *semanticmodel.Model, source semanticmodel.Source) ([]semanticmodel.ColumnSchema, error) {
	relation, err := SourceRelation(model, source)
	if err != nil {
		return nil, err
	}
	return describeRelationSchema(ctx, db, relation)
}

func describeRelationSchema(ctx context.Context, db queryContext, relation string) ([]semanticmodel.ColumnSchema, error) {
	rows, err := db.QueryContext(ctx, "DESCRIBE "+relation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []semanticmodel.ColumnSchema{}
	columnNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		values := make([]sql.NullString, len(columnNames))
		scan := make([]any, len(values))
		for index := range values {
			scan[index] = &values[index]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		if len(values) < 2 || !values[0].Valid {
			continue
		}
		column := semanticmodel.ColumnSchema{Name: values[0].String, Ordinal: len(result) + 1}
		if values[1].Valid {
			column.PhysicalType = values[1].String
		}
		if len(values) > 2 && values[2].Valid {
			nullable := strings.EqualFold(values[2].String, "YES") || strings.EqualFold(values[2].String, "true")
			column.Nullable = &nullable
		}
		if len(values) > 4 && values[4].Valid {
			column.Default = values[4].String
		}
		result = append(result, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sortedColumns(result), nil
}

func sortedColumns(columns []semanticmodel.ColumnSchema) []semanticmodel.ColumnSchema {
	out := append([]semanticmodel.ColumnSchema{}, columns...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ordinal != out[j].Ordinal {
			return out[i].Ordinal < out[j].Ordinal
		}
		return out[i].Name < out[j].Name
	})
	return out
}
