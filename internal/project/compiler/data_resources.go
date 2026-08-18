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
	kind, defaults, err := connectionVariant(authored.Spec.Value)
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
		ReaderDefaults: readerDefaults,
		Defaults:       semanticmodel.ConnectionDefaults{Options: map[string]any{}},
		Options:        map[string]any{},
	}, nil
}

func connectionVariant(value projectcontracts.ConnectionSpecVariant) (string, map[string]any, error) {
	defaults := map[string]any{}
	switch variant := value.(type) {
	case *projectcontracts.ManagedConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, defaults, nil
	case *projectcontracts.S3Connection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, defaults, nil
	case *projectcontracts.R2Connection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, defaults, nil
	case *projectcontracts.GCSConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, defaults, nil
	case *projectcontracts.HTTPConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, defaults, nil
	case *projectcontracts.AzureBlobConnection:
		if variant.Defaults != nil {
			defaults = readerDefaults(variant.Defaults)
		}
		return variant.Type, defaults, nil
	case *projectcontracts.PostgresConnection:
		return variant.Type, defaults, nil
	case *projectcontracts.MySQLConnection:
		return variant.Type, defaults, nil
	case *projectcontracts.SQLiteConnection:
		return variant.Type, defaults, nil
	case *projectcontracts.DuckLakeConnection:
		return variant.Type, defaults, nil
	case *projectcontracts.QuackConnection:
		return variant.Type, defaults, nil
	case nil:
		return "", nil, fmt.Errorf("connection spec variant is required")
	default:
		return "", nil, fmt.Errorf("unsupported connection spec variant %T", value)
	}
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
		source.LocationType, source.Path, source.Format = semanticmodel.KindPath, location.Path, location.Format
		source.Options = pathOptions(location.Options)
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

func pathOptions(value *projectcontracts.SourcePathOptions) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result, _ := structMap(value)
	return result
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
