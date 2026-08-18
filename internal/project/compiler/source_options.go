package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

// LeapViewSourceDefaultsVersion identifies the versioned option defaults used
// by compiler lowering. The effective map is recorded on Source once and is
// consumed verbatim by discovery and runtime preparation.
const LeapViewSourceDefaultsVersion = "v1"

var versionedSourceDefaults = map[string]any{
	"csv":     projectcontracts.CSVReaderOptions{Header: boolPtr(false), Delimiter: stringPtr(","), Quote: stringPtr(`"`), Escape: stringPtr(`"`)},
	"json":    projectcontracts.JSONReaderOptions{Format: stringPtr("auto")},
	"parquet": projectcontracts.ParquetReaderOptions{HivePartitioning: boolPtr(false), UnionByName: boolPtr(false)},
	"excel":   projectcontracts.ExcelReaderOptions{Header: boolPtr(true)},
	"text":    projectcontracts.TextReaderOptions{Delimiter: stringPtr("\t"), Quote: stringPtr(`"`), Header: boolPtr(false)},
	"blob":    projectcontracts.BlobReaderOptions{Compression: stringPtr("auto")},
	"vortex":  projectcontracts.VortexReaderOptions{},
	"delta":   projectcontracts.DeltaReaderOptions{},
	"iceberg": projectcontracts.IcebergReaderOptions{},
	"lance":   projectcontracts.LanceReaderOptions{},
}

// ResolveEffectiveSourceOptions applies the only supported precedence order:
// Source options > Connection defaults > versioned LeapView defaults.
func ResolveEffectiveSourceOptions(source semanticmodel.Source, connection semanticmodel.Connection) (map[string]any, error) {
	format := strings.ToLower(strings.TrimSpace(source.Format))
	defaults, ok := versionedSourceDefaults[format]
	if !ok {
		return nil, fmt.Errorf("unsupported source format %q", source.Format)
	}
	result, err := structMap(defaults)
	if err != nil {
		return nil, fmt.Errorf("resolve %s defaults: %w", format, err)
	}
	for key, value := range connection.ReaderDefaults[format] {
		result[key] = value
	}
	for key, value := range connection.Defaults.Options {
		result[key] = value
	}
	for key, value := range source.Options {
		result[key] = value
	}
	for key, value := range result {
		if value == nil {
			return nil, fmt.Errorf("%s source option %q cannot be null", format, key)
		}
	}
	return result, nil
}

func boolPtr(value bool) *bool { return &value }

func stringPtr(value string) *string { return &value }
