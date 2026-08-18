package compiler

import (
	"encoding/json"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

// decodeConnectionResource is the sole compiler boundary from the generated
// typed authoring DTO into the runtime semantic model. No legacy structural
// translator is kept: schema validation and this lowering consume the same
// generated document.
func decodeConnectionResource(path string, content []byte, metadata metadata) (semanticmodel.Connection, error) {
	var authored projectcontracts.Connection
	if err := configschema.DecodeResource(configschema.KindConnection, path, content, &authored); err != nil {
		return semanticmodel.Connection{}, err
	}
	// Generated variants intentionally expose no shared option interface. The
	// type switch keeps the closed union explicit and mechanically extracts only
	// portable fields.
	kind, access, defaults, err := connectionVariant(authored.Spec.Value)
	if err != nil {
		return semanticmodel.Connection{}, err
	}
	readerDefaults := make(map[string]map[string]any, len(defaults))
	for format, value := range defaults {
		options, err := structMap(value)
		if err != nil {
			return semanticmodel.Connection{}, fmt.Errorf("decode %s defaults: %w", format, err)
		}
		readerDefaults[format] = options
	}
	return semanticmodel.Connection{
		Kind: kind, Description: metadata.Description,
		Access:         access,
		ReaderDefaults: readerDefaults,
		Defaults:       semanticmodel.ConnectionDefaults{Options: map[string]any{}},
		Options:        map[string]any{},
	}, nil
}

func connectionVariant(value projectcontracts.ConnectionSpecVariant) (string, semanticmodel.ConnectionAccess, map[string]any, error) {
	defaults := map[string]any{}
	switch variant := value.(type) {
	case *projectcontracts.ManagedConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, lowerConnectionAccess(variant.Access), defaults, nil
	case *projectcontracts.S3Connection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, lowerConnectionAccess(variant.Access), defaults, nil
	case *projectcontracts.R2Connection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, lowerConnectionAccess(variant.Access), defaults, nil
	case *projectcontracts.GCSConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, lowerConnectionAccess(variant.Access), defaults, nil
	case *projectcontracts.HTTPConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, lowerConnectionAccess(variant.Access), defaults, nil
	case *projectcontracts.AzureBlobConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, lowerConnectionAccess(variant.Access), defaults, nil
	case *projectcontracts.PostgresConnection:
		return variant.Type, "", defaults, nil
	case *projectcontracts.MySQLConnection:
		return variant.Type, "", defaults, nil
	case *projectcontracts.SQLiteConnection:
		return variant.Type, "", defaults, nil
	case *projectcontracts.DuckLakeConnection:
		return variant.Type, "", defaults, nil
	case *projectcontracts.QuackConnection:
		return variant.Type, "", defaults, nil
	case nil:
		return "", "", nil, fmt.Errorf("connection spec variant is required")
	default:
		return "", "", nil, fmt.Errorf("unsupported connection spec variant %T", value)
	}
}

func lowerConnectionAccess(value *projectcontracts.PublicAccess) semanticmodel.ConnectionAccess {
	if value == nil {
		return ""
	}
	return semanticmodel.ConnectionAccess(*value)
}

func readerDefaults(value *projectcontracts.ReaderDefaults) map[string]any {
	result := map[string]any{}
	if value.Csv != nil {
		result["csv"] = value.Csv
	}
	if value.JSON != nil {
		result["json"] = value.JSON
	}
	if value.Parquet != nil {
		result["parquet"] = value.Parquet
	}
	if value.Excel != nil {
		result["excel"] = value.Excel
	}
	if value.Text != nil {
		result["text"] = value.Text
	}
	if value.Blob != nil {
		result["blob"] = value.Blob
	}
	if value.Vortex != nil {
		result["vortex"] = value.Vortex
	}
	if value.Delta != nil {
		result["delta"] = value.Delta
	}
	if value.Iceberg != nil {
		result["iceberg"] = value.Iceberg
	}
	if value.Lance != nil {
		result["lance"] = value.Lance
	}
	return result
}

func decodeSourceResource(path string, content []byte, metadata metadata) (semanticmodel.Source, error) {
	var authored projectcontracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, path, content, &authored); err != nil {
		return semanticmodel.Source{}, err
	}
	source := semanticmodel.Source{Connection: authored.Spec.Connection, Description: metadata.Description, Fields: map[string]semanticmodel.SourceField{}, Options: map[string]any{}}
	switch location := authored.Spec.Location.Value.(type) {
	case *projectcontracts.SourceLocationPathVariant:
		path, format, rawOptions, err := lowerPathLocation(&location.PathSourceLocation)
		if err != nil {
			return semanticmodel.Source{}, fmt.Errorf("decode path location: %w", err)
		}
		source.LocationType, source.Path, source.Format = semanticmodel.KindPath, path, format
		options, err := pathOptions(rawOptions)
		if err != nil {
			return semanticmodel.Source{}, fmt.Errorf("decode path options: %w", err)
		}
		source.Options = options
	case *projectcontracts.SourceLocationRelationVariant:
		source.LocationType, source.Catalog, source.SchemaName, source.RelationName = semanticmodel.KindObject, optionalString(location.Catalog), optionalString(location.Schema), location.Name
		parts := make([]string, 0, 3)
		for _, part := range []string{source.Catalog, source.SchemaName, source.RelationName} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		source.Object = strings.Join(parts, ".")
	default:
		return semanticmodel.Source{}, fmt.Errorf("source location variant is required")
	}
	if authored.Spec.Schema != nil {
		mode, fields, err := sourceSchema(authored.Spec.Schema)
		if err != nil {
			return semanticmodel.Source{}, err
		}
		source.SchemaMode, source.Fields = mode, fields
	}
	return source, nil
}

// decodeModelResource is the sole model authoring boundary. The generated
// TypeSpec DTO owns the closed definition union; compiler state receives only
// the lowered runtime representation. SQL dependencies are intentionally not
// accepted here and are derived later by the canonical AST analyzer.
func decodeModelResource(path string, content []byte, metadata metadata) (semanticmodel.Table, *semanticmodel.AIContext, error) {
	var authored projectcontracts.Model
	if err := configschema.DecodeResource(configschema.KindModel, path, content, &authored); err != nil {
		return semanticmodel.Table{}, nil, err
	}
	table := semanticmodel.Table{
		Entities:    map[string]semanticmodel.EntityDefinition{},
		Dimensions:  map[string]semanticmodel.MetricDimension{},
		Columns:     map[string]semanticmodel.ModelColumn{},
		Description: metadata.Description,
	}
	for name, entity := range authored.Spec.Entities {
		table.Entities[name] = semanticmodel.EntityDefinition{
			Type:        entity.Type,
			Fields:      append([]string(nil), entity.Fields...),
			Description: optionalString(entity.Description),
			AIContext:   lowerAIContext(entity.AiContext),
		}
	}
	table.GrainEntity = authored.Spec.Grain.Entity
	for name, field := range authored.Spec.Fields {
		table.Dimensions[name] = semanticmodel.MetricDimension{
			Label:       optionalString(field.Label),
			Description: optionalString(field.Description),
			Type:        canonicalDimensionTypeName(field.Datatype),
			Datatype:    semanticmodel.LogicalDataType(field.Datatype),
			AIContext:   lowerAIContext(field.AiContext),
		}
		table.Columns[name] = semanticmodel.ModelColumn{
			Name:        name,
			Field:       name,
			Type:        canonicalDimensionTypeName(field.Datatype),
			Datatype:    semanticmodel.LogicalDataType(field.Datatype),
			Description: optionalString(field.Description),
			AIContext:   lowerAIContext(field.AiContext),
		}
	}
	definition, err := modelDefinition(authored.Spec.Definition)
	if err != nil {
		return semanticmodel.Table{}, nil, err
	}
	table.Execution.Source = definition.source
	table.Execution.SQL = definition.sql
	if definition.source != "" {
		table.SourceDependencies = []string{definition.source}
	}
	table.Checks, err = lowerModelChecks(authored.Spec.Checks)
	if err != nil {
		return semanticmodel.Table{}, nil, err
	}
	return table, lowerAIContext(authored.AiContext), nil
}

type loweredModelDefinition struct {
	source string
	sql    string
}

func modelDefinition(value projectcontracts.ModelDefinition) (loweredModelDefinition, error) {
	switch variant := value.Value.(type) {
	case *projectcontracts.DirectModelDefinition:
		source := strings.TrimSpace(variant.Source)
		if source == "" {
			return loweredModelDefinition{}, fmt.Errorf("direct model definition source is required")
		}
		return loweredModelDefinition{source: source}, nil
	case *projectcontracts.SQLModelDefinition:
		sql := strings.TrimSpace(variant.SQL)
		if sql == "" {
			return loweredModelDefinition{}, fmt.Errorf("sql model definition sql is required")
		}
		return loweredModelDefinition{sql: sql}, nil
	case nil:
		return loweredModelDefinition{}, fmt.Errorf("model definition variant is required")
	default:
		return loweredModelDefinition{}, fmt.Errorf("unsupported model definition variant %T", value.Value)
	}
}

func lowerAIContext(value *projectcontracts.AIContext) *semanticmodel.AIContext {
	if value == nil {
		return nil
	}
	return &semanticmodel.AIContext{
		Instructions: optionalString(value.Instructions),
		Synonyms:     optionalStrings(value.Synonyms),
		Examples:     optionalStrings(value.Examples),
	}
}

func optionalStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return append([]string(nil), (*value)...)
}

func lowerModelChecks(value *[]projectcontracts.ModelCheck) ([]semanticmodel.ModelCheck, error) {
	if value == nil {
		return nil, nil
	}
	checks := make([]semanticmodel.ModelCheck, 0, len(*value))
	for _, check := range *value {
		lowered := semanticmodel.ModelCheck{}
		switch variant := check.Value.(type) {
		case *projectcontracts.ModelCheckNonNullVariant:
			lowered.Type, lowered.Field, lowered.Severity = variant.Type, variant.Field, optionalString(variant.Severity)
		case *projectcontracts.ModelCheckUniqueVariant:
			lowered.Type, lowered.Fields, lowered.Severity = variant.Type, append([]string(nil), variant.Fields...), optionalString(variant.Severity)
		case *projectcontracts.ModelCheckAcceptedValuesVariant:
			lowered.Type, lowered.Field, lowered.Values, lowered.Severity = variant.Type, variant.Field, append([]string(nil), variant.Values...), optionalString(variant.Severity)
		case *projectcontracts.ModelCheckRelationshipVariant:
			lowered.Type, lowered.Field, lowered.To, lowered.Severity = variant.Type, variant.Field, variant.To, optionalString(variant.Severity)
		case *projectcontracts.ModelCheckRowCountVariant:
			lowered.Type, lowered.Minimum, lowered.Maximum, lowered.Severity = variant.Type, variant.Minimum, variant.Maximum, optionalString(variant.Severity)
		case nil:
			return nil, fmt.Errorf("model check variant is required")
		default:
			return nil, fmt.Errorf("unsupported model check variant %T", check.Value)
		}
		checks = append(checks, lowered)
	}
	return checks, nil
}

func sourceSchema(value *projectcontracts.SourceSchema) (string, map[string]semanticmodel.SourceField, error) {
	if value == nil {
		return "inferred", map[string]semanticmodel.SourceField{}, nil
	}
	fields := map[string]semanticmodel.SourceField{}
	switch variant := value.Value.(type) {
	case *projectcontracts.SourceSchemaInferredVariant:
		return variant.Mode, fields, nil
	case *projectcontracts.SourceSchemaCompatibleVariant:
		for name, field := range variant.Fields {
			fields[name] = lowerSourceField(name, field)
		}
		return variant.Mode, fields, nil
	case *projectcontracts.SourceSchemaStrictVariant:
		for name, field := range variant.Fields {
			fields[name] = lowerSourceField(name, field)
		}
		return variant.Mode, fields, nil
	default:
		return "", nil, fmt.Errorf("unsupported source schema variant %T", value.Value)
	}
}

func lowerSourceField(name string, field projectcontracts.SourceSchemaField) semanticmodel.SourceField {
	return semanticmodel.SourceField{Field: name, Name: name, Datatype: semanticmodel.LogicalDataType(field.Datatype), Nullable: field.Nullable, Description: optionalString(field.Description)}
}

func lowerPathLocation(value *projectcontracts.PathSourceLocation) (string, string, any, error) {
	if value == nil {
		return "", "", nil, fmt.Errorf("path location is required")
	}
	switch variant := value.Value.(type) {
	case *projectcontracts.CSVPathSourceLocation:
		return variant.Path, "csv", variant.Options, nil
	case *projectcontracts.JSONPathSourceLocation:
		return variant.Path, "json", variant.Options, nil
	case *projectcontracts.ParquetPathSourceLocation:
		return variant.Path, "parquet", variant.Options, nil
	case *projectcontracts.ExcelPathSourceLocation:
		return variant.Path, "excel", variant.Options, nil
	case *projectcontracts.TextPathSourceLocation:
		return variant.Path, "text", variant.Options, nil
	case *projectcontracts.BlobPathSourceLocation:
		return variant.Path, "blob", variant.Options, nil
	case *projectcontracts.VortexPathSourceLocation:
		return variant.Path, "vortex", variant.Options, nil
	case *projectcontracts.DeltaPathSourceLocation:
		return variant.Path, "delta", variant.Options, nil
	case *projectcontracts.IcebergPathSourceLocation:
		return variant.Path, "iceberg", variant.Options, nil
	case *projectcontracts.LancePathSourceLocation:
		return variant.Path, "lance", variant.Options, nil
	case nil:
		return "", "", nil, fmt.Errorf("path location variant is required")
	default:
		return "", "", nil, fmt.Errorf("unsupported path location variant %T", variant)
	}
}

func pathOptions(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	return structMap(value)
}

func structMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
