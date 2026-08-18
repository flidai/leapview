package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var discoveredFieldRefPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\b`)

// LogicalDataTypeFromPhysicalType is the explicit DuckDB/source type mapping
// used when a serving generation validates discovered schemas. Unknown
// physical types intentionally map to Opaque instead of being guessed.
func LogicalDataTypeFromPhysicalType(physical string) LogicalDataType {
	typeName := strings.ToUpper(strings.TrimSpace(physical))
	for {
		start := strings.IndexByte(typeName, '(')
		if start < 0 {
			break
		}
		end := strings.IndexByte(typeName[start:], ')')
		if end < 0 {
			typeName = typeName[:start]
			break
		}
		typeName = typeName[:start] + typeName[start+end+1:]
	}
	typeName = strings.Join(strings.Fields(typeName), " ")
	switch typeName {
	case "VARCHAR", "CHAR", "BPCHAR", "TEXT", "UUID", "ENUM":
		return DataTypeString
	case "TINYINT", "SMALLINT", "INTEGER", "INT", "INT2", "INT4", "INT8", "BIGINT", "HUGEINT",
		"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT":
		return DataTypeInteger
	case "DECIMAL", "NUMERIC":
		return DataTypeDecimal
	case "FLOAT", "FLOAT4", "REAL", "DOUBLE", "FLOAT8", "DOUBLE PRECISION":
		return DataTypeFloat
	case "BOOLEAN", "BOOL":
		return DataTypeBoolean
	case "DATE":
		return DataTypeDate
	case "TIME", "TIME WITHOUT TIME ZONE", "TIMETZ", "TIME WITH TIME ZONE":
		return DataTypeTime
	case "TIMESTAMP", "TIMESTAMP WITHOUT TIME ZONE", "TIMESTAMP_NS", "TIMESTAMP_MS", "TIMESTAMP_S":
		return DataTypeDateTime
	case "TIMESTAMPTZ", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP_TZ":
		return DataTypeDateTimeTZ
	default:
		return DataTypeOpaque
	}
}

func (m *Model) ValidateDiscoveredSchemas() error {
	if m == nil {
		return fmt.Errorf("semantic model is required")
	}
	if err := m.validateSemanticDefinitions(); err != nil {
		return err
	}
	if err := m.ValidateDiscoveredSourceSchemas(); err != nil {
		return err
	}
	tableNames := make([]string, 0, len(m.Tables))
	for tableName := range m.Tables {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		table := m.Tables[tableName]
		columns := map[string]ColumnSchema{}
		for _, column := range table.Schema.Columns {
			columns[column.Name] = column
		}
		if len(columns) == 0 {
			return fmt.Errorf("model table %q has no discovered schema", tableName)
		}
		entityNames := make([]string, 0, len(table.Entities))
		for entityName := range table.Entities {
			entityNames = append(entityNames, entityName)
		}
		sort.Strings(entityNames)
		for _, entityName := range entityNames {
			entity := table.Entities[entityName]
			for _, field := range entity.Fields {
				if _, ok := columns[field]; !ok {
					return fmt.Errorf("model table %q entity %q field %q is not in discovered schema", tableName, entityName, field)
				}
			}
		}
		dimensionNames := make([]string, 0, len(table.Dimensions))
		for field := range table.Dimensions {
			dimensionNames = append(dimensionNames, field)
		}
		sort.Strings(dimensionNames)
		for _, field := range dimensionNames {
			dimension := table.Dimensions[field]
			column, ok := columns[field]
			if !ok {
				return fmt.Errorf("model table %q field %q is not in discovered schema", tableName, field)
			}
			if err := validateDiscoveredDatatype(tableName, field, dimension.Datatype, column.PhysicalType); err != nil {
				return err
			}
		}
		columnNames := make([]string, 0, len(table.Columns))
		for field := range table.Columns {
			columnNames = append(columnNames, field)
		}
		sort.Strings(columnNames)
		for _, field := range columnNames {
			columnSpec := table.Columns[field]
			column, ok := columns[field]
			if !ok {
				return fmt.Errorf("model table %q column %q is not in discovered schema", tableName, field)
			}
			if err := validateDiscoveredDatatype(tableName, field, columnSpec.Datatype, column.PhysicalType); err != nil {
				return err
			}
		}
	}
	metricNames := make([]string, 0, len(m.Metrics))
	for metricName := range m.Metrics {
		metricNames = append(metricNames, metricName)
	}
	sort.Strings(metricNames)
	for _, metricName := range metricNames {
		metric := m.Metrics[metricName]
		if metric.Type != "aggregate" || metric.Input == nil {
			continue
		}
		for _, ref := range []string{metric.Input.Field} {
			if ref == "" {
				continue
			}
			if _, err := m.ResolveDimension(ref); err != nil {
				return fmt.Errorf("metric %q references unknown field %q", metricName, ref)
			}
		}
	}
	return nil
}

func validateDiscoveredDatatype(tableName, field string, authored LogicalDataType, physicalType string) error {
	if authored == "" {
		return nil
	}
	discovered := LogicalDataTypeFromPhysicalType(physicalType)
	if authored == DataTypeOpaque {
		return fmt.Errorf("model table %q field %q uses Opaque datatype but discovered physical type %q requires an explicit mapping", tableName, field, physicalType)
	}
	if discovered == DataTypeOpaque {
		return fmt.Errorf("model table %q field %q datatype %q cannot be validated against unknown discovered physical type %q", tableName, field, authored, physicalType)
	}
	if authored != discovered {
		return fmt.Errorf("model table %q field %q datatype %q is incompatible with discovered physical type %q (mapped to %q)", tableName, field, authored, physicalType, discovered)
	}
	return nil
}

func (m *Model) ValidateDiscoveredSourceSchemas() error {
	if m == nil {
		return fmt.Errorf("semantic model is required")
	}
	sourceNames := make([]string, 0, len(m.Sources))
	for sourceName := range m.Sources {
		sourceNames = append(sourceNames, sourceName)
	}
	sort.Strings(sourceNames)
	for _, sourceName := range sourceNames {
		source := m.Sources[sourceName]
		columns := map[string]ColumnSchema{}
		for _, column := range source.Schema.Columns {
			columns[column.Name] = column
		}
		if len(columns) == 0 {
			return fmt.Errorf("source %q has no discovered schema", sourceName)
		}
		mode := strings.ToLower(strings.TrimSpace(source.SchemaMode))
		if mode == "" {
			mode = "inferred"
		}
		if mode != "inferred" && mode != "compatible" && mode != "strict" {
			return fmt.Errorf("source %q has unsupported schema mode %q", sourceName, source.SchemaMode)
		}
		if mode == "inferred" {
			continue
		}
		for field, declaration := range source.Fields {
			column, ok := columns[field]
			if !ok {
				return fmt.Errorf("source %q field %q is not in discovered schema", sourceName, field)
			}
			declared := declaration.Datatype
			if declared == "" {
				declared = canonicalSourceDatatype(LogicalDataType(strings.TrimSpace(declaration.Type)))
			}
			if declared != "" {
				observed := LogicalDataTypeFromPhysicalType(column.PhysicalType)
				if declared != observed || observed == DataTypeOpaque && declared != DataTypeOpaque {
					return fmt.Errorf("source %q field %q datatype %q is incompatible with discovered physical type %q (mapped to %q)", sourceName, field, declared, column.PhysicalType, observed)
				}
			}
			if declaration.Nullable != nil {
				if column.Nullable == nil {
					return fmt.Errorf("source %q field %q nullability could not be established from discovered schema", sourceName, field)
				}
				if !*declaration.Nullable && *column.Nullable {
					return fmt.Errorf("source %q field %q nullability is incompatible: declared non-null but discovered nullable", sourceName, field)
				}
			}
		}
		if mode == "strict" {
			if len(source.Fields) != len(columns) {
				return fmt.Errorf("source %q strict schema has %d declared fields but discovered %d", sourceName, len(source.Fields), len(columns))
			}
			for field := range columns {
				if _, ok := source.Fields[field]; !ok {
					return fmt.Errorf("source %q strict schema has undeclared discovered field %q", sourceName, field)
				}
			}
		}
	}
	return nil
}

func canonicalSourceDatatype(value LogicalDataType) LogicalDataType {
	switch strings.ToLower(strings.TrimSpace(string(value))) {
	case "string":
		return DataTypeString
	case "integer", "int":
		return DataTypeInteger
	case "decimal", "number":
		return DataTypeDecimal
	case "float", "double":
		return DataTypeFloat
	case "boolean", "bool":
		return DataTypeBoolean
	case "date":
		return DataTypeDate
	case "time":
		return DataTypeTime
	case "datetime", "timestamp":
		return DataTypeDateTime
	case "datetimetz", "timestamptz":
		return DataTypeDateTimeTZ
	case "opaque":
		return DataTypeOpaque
	default:
		return value
	}
}

func ExpressionFieldRefs(expression string) []string {
	matches := discoveredFieldRefPattern.FindAllStringSubmatch(expression, -1)
	seen := map[string]struct{}{}
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		ref := match[1] + "." + match[2]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func discoveredFieldRefs(expression string) []string {
	return ExpressionFieldRefs(expression)
}
