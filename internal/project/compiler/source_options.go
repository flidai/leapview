package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

// LeapViewSourceDefaultsVersion identifies the versioned reader defaults used
// by compiler lowering. Effective options remain a generated typed path union.
const LeapViewSourceDefaultsVersion = "v1"

// ResolveEffectivePathLocation applies authored options over connection
// defaults over generated defaults. Each branch constructs one concrete
// generated DTO, so a format can never accidentally receive another format's
// options or lose a typed field through a map conversion.
func ResolveEffectivePathLocation(source semanticmodel.Source, connection semanticmodel.Connection) (*projectcontracts.PathSourceLocation, error) {
	if source.PathLocation == nil {
		return nil, fmt.Errorf("source path location is required")
	}
	format := strings.ToLower(strings.TrimSpace(source.Format))
	if format == "" {
		return nil, fmt.Errorf("source path format is required")
	}
	if source.PathLocation.Value == nil {
		return nil, fmt.Errorf("source path location variant is required")
	}
	if got := pathLocationFormat(source.PathLocation); got != format {
		return nil, fmt.Errorf("source path format %q disagrees with location variant %q", format, got)
	}
	defaults := connection.ReaderDefaults
	base := func(path, kind, variantFormat string) projectcontracts.PathSourceLocationBase {
		return projectcontracts.PathSourceLocationBase{Type: kind, Path: path, Format: variantFormat}
	}
	switch variant := source.PathLocation.Value.(type) {
	case *projectcontracts.CSVPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("csv path location variant is nil")
		}
		var connectionOptions *projectcontracts.CSVReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Csv
		}
		sourceOptions := variant.Options
		generated := projectcontracts.DefaultCSVReaderOptions()
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
			PathSourceLocationBase: base(variant.Path, variant.Type, "csv"), Format: "csv",
			Options: &projectcontracts.CSVReaderOptions{
				Header:     pickBool(fieldBool(sourceOptions, func(v *projectcontracts.CSVReaderOptions) *bool { return v.Header }), fieldBool(connectionOptions, func(v *projectcontracts.CSVReaderOptions) *bool { return v.Header }), generatedBool(generated, func(v *projectcontracts.CSVReaderOptions) *bool { return v.Header })),
				Delimiter:  pickString(fieldString(sourceOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.Delimiter }), fieldString(connectionOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.Delimiter }), generatedString(generated, func(v *projectcontracts.CSVReaderOptions) *string { return v.Delimiter })),
				Quote:      pickString(fieldString(sourceOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.Quote }), fieldString(connectionOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.Quote }), generatedString(generated, func(v *projectcontracts.CSVReaderOptions) *string { return v.Quote })),
				Escape:     pickString(fieldString(sourceOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.Escape }), fieldString(connectionOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.Escape }), generatedString(generated, func(v *projectcontracts.CSVReaderOptions) *string { return v.Escape })),
				NullString: pickString(fieldString(sourceOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.NullString }), fieldString(connectionOptions, func(v *projectcontracts.CSVReaderOptions) *string { return v.NullString }), nil),
			},
		}}, nil
	case *projectcontracts.JSONPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("json path location variant is nil")
		}
		var connectionOptions *projectcontracts.JSONReaderOptions
		if defaults != nil {
			connectionOptions = defaults.JSON
		}
		generated := projectcontracts.DefaultJSONReaderOptions()
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.JSONPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "json"), Format: "json", Options: &projectcontracts.JSONReaderOptions{
			Format:       pickString(fieldString(variant.Options, func(v *projectcontracts.JSONReaderOptions) *string { return v.Format }), fieldString(connectionOptions, func(v *projectcontracts.JSONReaderOptions) *string { return v.Format }), generatedString(generated, func(v *projectcontracts.JSONReaderOptions) *string { return v.Format })),
			MaximumDepth: pickInt32(fieldInt32(variant.Options, func(v *projectcontracts.JSONReaderOptions) *int32 { return v.MaximumDepth }), fieldInt32(connectionOptions, func(v *projectcontracts.JSONReaderOptions) *int32 { return v.MaximumDepth }), generatedInt32(generated, func(v *projectcontracts.JSONReaderOptions) *int32 { return v.MaximumDepth })),
		}}}, nil
	case *projectcontracts.ParquetPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("parquet path location variant is nil")
		}
		var connectionOptions *projectcontracts.ParquetReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Parquet
		}
		generated := projectcontracts.DefaultParquetReaderOptions()
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.ParquetPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "parquet"), Format: "parquet", Options: &projectcontracts.ParquetReaderOptions{
			HivePartitioning: pickBool(fieldBool(variant.Options, func(v *projectcontracts.ParquetReaderOptions) *bool { return v.HivePartitioning }), fieldBool(connectionOptions, func(v *projectcontracts.ParquetReaderOptions) *bool { return v.HivePartitioning }), generatedBool(generated, func(v *projectcontracts.ParquetReaderOptions) *bool { return v.HivePartitioning })),
			UnionByName:      pickBool(fieldBool(variant.Options, func(v *projectcontracts.ParquetReaderOptions) *bool { return v.UnionByName }), fieldBool(connectionOptions, func(v *projectcontracts.ParquetReaderOptions) *bool { return v.UnionByName }), generatedBool(generated, func(v *projectcontracts.ParquetReaderOptions) *bool { return v.UnionByName })),
		}}}, nil
	case *projectcontracts.ExcelPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("excel path location variant is nil")
		}
		var connectionOptions *projectcontracts.ExcelReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Excel
		}
		generated := projectcontracts.DefaultExcelReaderOptions()
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.ExcelPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "excel"), Format: "excel", Options: &projectcontracts.ExcelReaderOptions{
			Sheet:  pickString(fieldString(variant.Options, func(v *projectcontracts.ExcelReaderOptions) *string { return v.Sheet }), fieldString(connectionOptions, func(v *projectcontracts.ExcelReaderOptions) *string { return v.Sheet }), nil),
			Header: pickBool(fieldBool(variant.Options, func(v *projectcontracts.ExcelReaderOptions) *bool { return v.Header }), fieldBool(connectionOptions, func(v *projectcontracts.ExcelReaderOptions) *bool { return v.Header }), generatedBool(generated, func(v *projectcontracts.ExcelReaderOptions) *bool { return v.Header })),
		}}}, nil
	case *projectcontracts.TextPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("text path location variant is nil")
		}
		var connectionOptions *projectcontracts.TextReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Text
		}
		generated := projectcontracts.DefaultTextReaderOptions()
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.TextPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "text"), Format: "text", Options: &projectcontracts.TextReaderOptions{
			Delimiter: pickString(fieldString(variant.Options, func(v *projectcontracts.TextReaderOptions) *string { return v.Delimiter }), fieldString(connectionOptions, func(v *projectcontracts.TextReaderOptions) *string { return v.Delimiter }), generatedString(generated, func(v *projectcontracts.TextReaderOptions) *string { return v.Delimiter })),
			Quote:     pickString(fieldString(variant.Options, func(v *projectcontracts.TextReaderOptions) *string { return v.Quote }), fieldString(connectionOptions, func(v *projectcontracts.TextReaderOptions) *string { return v.Quote }), generatedString(generated, func(v *projectcontracts.TextReaderOptions) *string { return v.Quote })),
			Header:    pickBool(fieldBool(variant.Options, func(v *projectcontracts.TextReaderOptions) *bool { return v.Header }), fieldBool(connectionOptions, func(v *projectcontracts.TextReaderOptions) *bool { return v.Header }), generatedBool(generated, func(v *projectcontracts.TextReaderOptions) *bool { return v.Header })),
		}}}, nil
	case *projectcontracts.BlobPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("blob path location variant is nil")
		}
		var connectionOptions *projectcontracts.BlobReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Blob
		}
		generated := projectcontracts.DefaultBlobReaderOptions()
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.BlobPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "blob"), Format: "blob", Options: &projectcontracts.BlobReaderOptions{Compression: pickString(fieldString(variant.Options, func(v *projectcontracts.BlobReaderOptions) *string { return v.Compression }), fieldString(connectionOptions, func(v *projectcontracts.BlobReaderOptions) *string { return v.Compression }), generatedString(generated, func(v *projectcontracts.BlobReaderOptions) *string { return v.Compression }))}}}, nil
	case *projectcontracts.VortexPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("vortex path location variant is nil")
		}
		var connectionOptions *projectcontracts.VortexReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Vortex
		}
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.VortexPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "vortex"), Format: "vortex", Options: mergeVortex(variant.Options, connectionOptions)}}, nil
	case *projectcontracts.DeltaPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("delta path location variant is nil")
		}
		var connectionOptions *projectcontracts.DeltaReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Delta
		}
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.DeltaPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "delta"), Format: "delta", Options: mergeDelta(variant.Options, connectionOptions)}}, nil
	case *projectcontracts.IcebergPathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("iceberg path location variant is nil")
		}
		var connectionOptions *projectcontracts.IcebergReaderOptions
		if defaults != nil {
			connectionOptions = defaults.Iceberg
		}
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.IcebergPathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "iceberg"), Format: "iceberg", Options: mergeIceberg(variant.Options, connectionOptions)}}, nil
	case *projectcontracts.LancePathSourceLocation:
		if variant == nil {
			return nil, fmt.Errorf("lance path location variant is nil")
		}
		return &projectcontracts.PathSourceLocation{Value: &projectcontracts.LancePathSourceLocation{PathSourceLocationBase: base(variant.Path, variant.Type, "lance"), Format: "lance"}}, nil
	default:
		return nil, fmt.Errorf("unsupported path location variant %T", source.PathLocation.Value)
	}
}

func pathLocationFormat(value *projectcontracts.PathSourceLocation) string {
	switch variant := value.Value.(type) {
	case *projectcontracts.CSVPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.JSONPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.ParquetPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.ExcelPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.TextPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.BlobPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.VortexPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.DeltaPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.IcebergPathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	case *projectcontracts.LancePathSourceLocation:
		if variant != nil {
			return variant.Format
		}
	}
	return ""
}

func pickBool(source, connection, generated *bool) *bool {
	if source != nil {
		return cloneBool(source)
	}
	if connection != nil {
		return cloneBool(connection)
	}
	return cloneBool(generated)
}
func pickString(source, connection, generated *string) *string {
	if source != nil {
		return cloneString(source)
	}
	if connection != nil {
		return cloneString(connection)
	}
	return cloneString(generated)
}
func pickInt32(source, connection, generated *int32) *int32 {
	if source != nil {
		value := *source
		return &value
	}
	if connection != nil {
		value := *connection
		return &value
	}
	if generated != nil {
		value := *generated
		return &value
	}
	return nil
}
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func fieldBool[T any](value *T, getter func(*T) *bool) *bool {
	if value == nil {
		return nil
	}
	return getter(value)
}
func fieldString[T any](value *T, getter func(*T) *string) *string {
	if value == nil {
		return nil
	}
	return getter(value)
}
func fieldInt32[T any](value *T, getter func(*T) *int32) *int32 {
	if value == nil {
		return nil
	}
	return getter(value)
}
func generatedBool[T any](value *T, getter func(*T) *bool) *bool { return fieldBool(value, getter) }
func generatedString[T any](value *T, getter func(*T) *string) *string {
	return fieldString(value, getter)
}
func generatedInt32[T any](value *T, getter func(*T) *int32) *int32 { return fieldInt32(value, getter) }
func mergeVortex(source, connection *projectcontracts.VortexReaderOptions) *projectcontracts.VortexReaderOptions {
	if source == nil && connection == nil {
		return nil
	}
	result := &projectcontracts.VortexReaderOptions{}
	if source != nil && source.Version != nil {
		result.Version = cloneString(source.Version)
	} else if connection != nil {
		result.Version = cloneString(connection.Version)
	}
	return result
}
func mergeDelta(source, connection *projectcontracts.DeltaReaderOptions) *projectcontracts.DeltaReaderOptions {
	if source == nil && connection == nil {
		return nil
	}
	result := &projectcontracts.DeltaReaderOptions{}
	if source != nil && source.Version != nil {
		result.Version = cloneString(source.Version)
	} else if connection != nil {
		result.Version = cloneString(connection.Version)
	}
	return result
}
func mergeIceberg(source, connection *projectcontracts.IcebergReaderOptions) *projectcontracts.IcebergReaderOptions {
	if source == nil && connection == nil {
		return nil
	}
	result := &projectcontracts.IcebergReaderOptions{}
	if source != nil && source.Snapshot != nil {
		result.Snapshot = cloneString(source.Snapshot)
	} else if connection != nil {
		result.Snapshot = cloneString(connection.Snapshot)
	}
	return result
}
