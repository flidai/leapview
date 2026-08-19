package compiler

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	return semanticmodel.Connection{
		Kind: kind, Description: metadata.Description,
		Access:         access,
		ReaderDefaults: defaults,
	}, nil
}

func connectionVariant(value projectcontracts.ConnectionSpecVariant) (string, semanticmodel.ConnectionAccess, *projectcontracts.ReaderDefaults, error) {
	switch variant := value.(type) {
	case *projectcontracts.ManagedConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.S3Connection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.R2Connection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.GCSConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.HTTPConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.AzureBlobConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.PostgresConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.MySQLConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.SQLiteConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.DuckLakeConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.QuackConnection:
		return variant.Type, "", nil, nil
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

func decodeSourceResource(path string, content []byte, metadata metadata) (semanticmodel.Source, error) {
	var authored projectcontracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, path, content, &authored); err != nil {
		return semanticmodel.Source{}, err
	}
	source := semanticmodel.Source{Connection: authored.Spec.Connection, Description: metadata.Description, Fields: map[string]semanticmodel.SourceField{}}
	switch location := authored.Spec.Location.Value.(type) {
	case *projectcontracts.SourceLocationPathVariant:
		path, format, err := lowerPathLocation(&location.PathSourceLocation)
		if err != nil {
			return semanticmodel.Source{}, fmt.Errorf("decode path location: %w", err)
		}
		source.LocationType, source.Path, source.Format = semanticmodel.KindPath, path, format
		source.PathLocation = &location.PathSourceLocation
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
	if authored.Spec.Freshness != nil {
		freshness, err := lowerSourceFreshness(authored.Spec.Freshness)
		if err != nil {
			return semanticmodel.Source{}, err
		}
		schemaMode := strings.ToLower(strings.TrimSpace(source.SchemaMode))
		if schemaMode == "" {
			schemaMode = "inferred"
		}
		if freshness != nil && freshness.Basis == "field" && schemaMode != "inferred" {
			if _, ok := source.Fields[freshness.Field]; !ok {
				return semanticmodel.Source{}, fmt.Errorf("source freshness field %q is not declared in source schema", freshness.Field)
			}
		}
		source.Freshness = freshness
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
	for index, check := range *value {
		lowered := semanticmodel.ModelCheck{}
		switch variant := check.Value.(type) {
		case *projectcontracts.ModelCheckNonNullVariant:
			if strings.TrimSpace(variant.Field) == "" {
				return nil, fmt.Errorf("checks[%d] non_null requires field", index)
			}
			lowered.Type, lowered.Field, lowered.Severity = variant.Type, variant.Field, optionalString(variant.Severity)
		case *projectcontracts.ModelCheckUniqueVariant:
			if len(variant.Fields) == 0 {
				return nil, fmt.Errorf("checks[%d] unique requires fields", index)
			}
			seenFields := make(map[string]struct{}, len(variant.Fields))
			for _, field := range variant.Fields {
				if strings.TrimSpace(field) == "" {
					return nil, fmt.Errorf("checks[%d] unique fields must be non-empty", index)
				}
				if _, exists := seenFields[field]; exists {
					return nil, fmt.Errorf("checks[%d] unique contains duplicate field %q", index, field)
				}
				seenFields[field] = struct{}{}
			}
			lowered.Type, lowered.Fields, lowered.Severity = variant.Type, append([]string(nil), variant.Fields...), optionalString(variant.Severity)
		case *projectcontracts.ModelCheckAcceptedValuesVariant:
			if strings.TrimSpace(variant.Field) == "" || len(variant.Values) == 0 {
				return nil, fmt.Errorf("checks[%d] accepted_values requires field and values", index)
			}
			seenValues := make(map[string]struct{}, len(variant.Values))
			for _, accepted := range variant.Values {
				if _, exists := seenValues[accepted]; exists {
					return nil, fmt.Errorf("checks[%d] accepted_values contains duplicate value %q", index, accepted)
				}
				seenValues[accepted] = struct{}{}
			}
			lowered.Type, lowered.Field, lowered.Values, lowered.Severity = variant.Type, variant.Field, append([]string(nil), variant.Values...), optionalString(variant.Severity)
		case *projectcontracts.ModelCheckRelationshipVariant:
			if strings.TrimSpace(variant.Field) == "" || strings.TrimSpace(variant.To) == "" {
				return nil, fmt.Errorf("checks[%d] relationship requires field and to", index)
			}
			lowered.Type, lowered.Field, lowered.To, lowered.Severity = variant.Type, variant.Field, variant.To, optionalString(variant.Severity)
		case *projectcontracts.ModelCheckRowCountVariant:
			if variant.Minimum == nil && variant.Maximum == nil {
				return nil, fmt.Errorf("checks[%d] row_count requires minimum or maximum", index)
			}
			if variant.Minimum != nil && *variant.Minimum < 0 {
				return nil, fmt.Errorf("checks[%d] row_count minimum must be non-negative", index)
			}
			if variant.Maximum != nil && *variant.Maximum < 0 {
				return nil, fmt.Errorf("checks[%d] row_count maximum must be non-negative", index)
			}
			if variant.Minimum != nil && variant.Maximum != nil && *variant.Minimum > *variant.Maximum {
				return nil, fmt.Errorf("checks[%d] row_count minimum exceeds maximum", index)
			}
			lowered.Type, lowered.Minimum, lowered.Maximum, lowered.Severity = variant.Type, variant.Minimum, variant.Maximum, optionalString(variant.Severity)
		case nil:
			return nil, fmt.Errorf("model check variant is required")
		default:
			return nil, fmt.Errorf("unsupported model check variant %T", check.Value)
		}
		if lowered.Severity != "" && !strings.EqualFold(lowered.Severity, "warning") && !strings.EqualFold(lowered.Severity, "error") {
			return nil, fmt.Errorf("checks[%d] severity must be warning or error", index)
		}
		checks = append(checks, lowered)
	}
	return checks, nil
}

func lowerSourceFreshness(value *projectcontracts.SourceFreshness) (*semanticmodel.SourceFreshnessSpec, error) {
	if value == nil {
		return nil, nil
	}
	result := &semanticmodel.SourceFreshnessSpec{}
	switch variant := value.Value.(type) {
	case *projectcontracts.SourceFreshnessFieldVariant:
		if strings.TrimSpace(variant.Field) == "" {
			return nil, fmt.Errorf("source freshness field is required")
		}
		result.Basis, result.Field = "field", variant.Field
		var err error
		result.WarningAfter, err = lowerFreshnessDuration(variant.WarningAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness warningAfter: %w", err)
		}
		result.ErrorAfter, err = lowerFreshnessDuration(variant.ErrorAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness errorAfter: %w", err)
		}
	case *projectcontracts.SourceFreshnessRevisionVariant:
		if strings.TrimSpace(variant.Revision) == "" {
			return nil, fmt.Errorf("source freshness revision is required")
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, variant.Revision)
		if parseErr != nil {
			return nil, fmt.Errorf("source freshness revision must be RFC3339 utcDateTime: %w", parseErr)
		}
		if parsed.Location() != time.UTC {
			return nil, fmt.Errorf("source freshness revision must use UTC")
		}
		parsed = parsed.UTC()
		result.Basis, result.Revision, result.RevisionAt = "revision", parsed.Format(time.RFC3339Nano), &parsed
		var err error
		result.WarningAfter, err = lowerFreshnessDuration(variant.WarningAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness warningAfter: %w", err)
		}
		result.ErrorAfter, err = lowerFreshnessDuration(variant.ErrorAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness errorAfter: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported source freshness variant %T", value.Value)
	}
	if result.WarningAfter == nil && result.ErrorAfter == nil {
		return nil, fmt.Errorf("source freshness requires warningAfter or errorAfter")
	}
	if result.WarningAfter != nil && result.ErrorAfter != nil && result.WarningAfter.Duration() >= result.ErrorAfter.Duration() {
		return nil, fmt.Errorf("source freshness warningAfter must be shorter than errorAfter")
	}
	return result, nil
}

func lowerFreshnessDuration(value *projectcontracts.FreshnessDuration) (*semanticmodel.FreshnessDurationSpec, error) {
	if value == nil {
		return nil, nil
	}
	if value.Amount <= 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}
	unit := strings.TrimSpace(strings.ToLower(value.Unit))
	switch unit {
	case "second", "minute", "hour", "day":
	default:
		return nil, fmt.Errorf("unit %q is not supported", value.Unit)
	}
	multiplier := int64(time.Second)
	switch unit {
	case "minute":
		multiplier *= 60
	case "hour":
		multiplier *= 60 * 60
	case "day":
		multiplier *= 24 * 60 * 60
	}
	if value.Amount > int64(^uint64(0)>>1)/multiplier {
		return nil, fmt.Errorf("amount overflows time.Duration")
	}
	return &semanticmodel.FreshnessDurationSpec{Amount: value.Amount, Unit: unit}, nil
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

func lowerPathLocation(value *projectcontracts.PathSourceLocation) (string, string, error) {
	if value == nil {
		return "", "", fmt.Errorf("path location is required")
	}
	switch variant := value.Value.(type) {
	case *projectcontracts.CSVPathSourceLocation:
		return variant.Path, "csv", nil
	case *projectcontracts.JSONPathSourceLocation:
		return variant.Path, "json", nil
	case *projectcontracts.ParquetPathSourceLocation:
		return variant.Path, "parquet", nil
	case *projectcontracts.ExcelPathSourceLocation:
		return variant.Path, "excel", nil
	case *projectcontracts.TextPathSourceLocation:
		return variant.Path, "text", nil
	case *projectcontracts.BlobPathSourceLocation:
		return variant.Path, "blob", nil
	case *projectcontracts.VortexPathSourceLocation:
		return variant.Path, "vortex", nil
	case *projectcontracts.DeltaPathSourceLocation:
		return variant.Path, "delta", nil
	case *projectcontracts.IcebergPathSourceLocation:
		return variant.Path, "iceberg", nil
	case *projectcontracts.LancePathSourceLocation:
		return variant.Path, "lance", nil
	case nil:
		return "", "", fmt.Errorf("path location variant is required")
	default:
		return "", "", fmt.Errorf("unsupported path location variant %T", variant)
	}
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
