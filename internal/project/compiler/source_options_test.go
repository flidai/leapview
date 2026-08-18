package compiler

import (
	"encoding/json"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func pathLocation(format string, variant projectcontracts.PathSourceLocationVariant) *projectcontracts.PathSourceLocation {
	return &projectcontracts.PathSourceLocation{Value: variant}
}

func pathBase(format string) projectcontracts.PathSourceLocationBase {
	return projectcontracts.PathSourceLocationBase{Type: "path", Path: "fixture." + format, Format: format}
}

func TestResolveEffectivePathLocationUsesTypedPrecedenceForEveryFormat(t *testing.T) {
	trueValue, falseValue := true, false
	semicolon, pipe, quote, singleQuote, escape, backslash := ";", "|", `"`, "'", `\\`, "NA"
	jsonExplicit, jsonDefault, sheet, gzip, version, snapshot := "array", "newline_delimited", "Data", "gzip", "v2", "42"

	tests := []struct {
		name     string
		format   string
		location projectcontracts.PathSourceLocationVariant
		defaults *projectcontracts.ReaderDefaults
		assert   func(*testing.T, *projectcontracts.PathSourceLocation)
	}{
		{
			name:     "csv",
			format:   "csv",
			location: &projectcontracts.CSVPathSourceLocation{PathSourceLocationBase: pathBase("csv"), Format: "csv", Options: &projectcontracts.CSVReaderOptions{Header: &trueValue, Delimiter: &semicolon}},
			defaults: &projectcontracts.ReaderDefaults{Csv: &projectcontracts.CSVReaderOptions{Header: &falseValue, Delimiter: &pipe, Quote: &singleQuote, Escape: &backslash, NullString: &escape}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.CSVPathSourceLocation)
				if *v.Options.Header != true || *v.Options.Delimiter != semicolon || *v.Options.Quote != singleQuote || *v.Options.Escape != backslash || *v.Options.NullString != escape {
					t.Fatalf("csv options = %#v", v.Options)
				}
			},
		},
		{
			name:     "json",
			format:   "json",
			location: &projectcontracts.JSONPathSourceLocation{PathSourceLocationBase: pathBase("json"), Format: "json", Options: &projectcontracts.JSONReaderOptions{Format: &jsonExplicit}},
			defaults: &projectcontracts.ReaderDefaults{JSON: &projectcontracts.JSONReaderOptions{Format: &jsonDefault, MaximumDepth: &recordsDepth}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.JSONPathSourceLocation)
				if *v.Options.Format != jsonExplicit || *v.Options.MaximumDepth != recordsDepth {
					t.Fatalf("json options = %#v", v.Options)
				}
			},
		},
		{
			name:     "parquet",
			format:   "parquet",
			location: &projectcontracts.ParquetPathSourceLocation{PathSourceLocationBase: pathBase("parquet"), Format: "parquet", Options: &projectcontracts.ParquetReaderOptions{HivePartitioning: &trueValue}},
			defaults: &projectcontracts.ReaderDefaults{Parquet: &projectcontracts.ParquetReaderOptions{HivePartitioning: &falseValue, UnionByName: &trueValue}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.ParquetPathSourceLocation)
				if *v.Options.HivePartitioning != true || *v.Options.UnionByName != true {
					t.Fatalf("parquet options = %#v", v.Options)
				}
			},
		},
		{
			name:     "excel",
			format:   "excel",
			location: &projectcontracts.ExcelPathSourceLocation{PathSourceLocationBase: pathBase("excel"), Format: "excel", Options: &projectcontracts.ExcelReaderOptions{Sheet: &sheet}},
			defaults: &projectcontracts.ReaderDefaults{Excel: &projectcontracts.ExcelReaderOptions{Sheet: &quote, Header: &falseValue}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.ExcelPathSourceLocation)
				if *v.Options.Sheet != sheet || *v.Options.Header != false {
					t.Fatalf("excel options = %#v", v.Options)
				}
			},
		},
		{
			name:     "text",
			format:   "text",
			location: &projectcontracts.TextPathSourceLocation{PathSourceLocationBase: pathBase("text"), Format: "text", Options: &projectcontracts.TextReaderOptions{Delimiter: &semicolon}},
			defaults: &projectcontracts.ReaderDefaults{Text: &projectcontracts.TextReaderOptions{Delimiter: &pipe, Quote: &singleQuote, Header: &trueValue}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.TextPathSourceLocation)
				if *v.Options.Delimiter != semicolon || *v.Options.Quote != singleQuote || *v.Options.Header != true {
					t.Fatalf("text options = %#v", v.Options)
				}
			},
		},
		{
			name:     "blob",
			format:   "blob",
			location: &projectcontracts.BlobPathSourceLocation{PathSourceLocationBase: pathBase("blob"), Format: "blob", Options: &projectcontracts.BlobReaderOptions{}},
			defaults: &projectcontracts.ReaderDefaults{Blob: &projectcontracts.BlobReaderOptions{Compression: &gzip}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.BlobPathSourceLocation)
				if *v.Options.Compression != gzip {
					t.Fatalf("blob options = %#v", v.Options)
				}
			},
		},
		{
			name:     "vortex",
			format:   "vortex",
			location: &projectcontracts.VortexPathSourceLocation{PathSourceLocationBase: pathBase("vortex"), Format: "vortex", Options: &projectcontracts.VortexReaderOptions{Version: &version}},
			defaults: &projectcontracts.ReaderDefaults{Vortex: &projectcontracts.VortexReaderOptions{Version: &quote}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.VortexPathSourceLocation)
				if *v.Options.Version != version {
					t.Fatalf("vortex options = %#v", v.Options)
				}
			},
		},
		{
			name:     "delta",
			format:   "delta",
			location: &projectcontracts.DeltaPathSourceLocation{PathSourceLocationBase: pathBase("delta"), Format: "delta", Options: &projectcontracts.DeltaReaderOptions{}},
			defaults: &projectcontracts.ReaderDefaults{Delta: &projectcontracts.DeltaReaderOptions{Version: &version}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.DeltaPathSourceLocation)
				if *v.Options.Version != version {
					t.Fatalf("delta options = %#v", v.Options)
				}
			},
		},
		{
			name:     "iceberg",
			format:   "iceberg",
			location: &projectcontracts.IcebergPathSourceLocation{PathSourceLocationBase: pathBase("iceberg"), Format: "iceberg", Options: &projectcontracts.IcebergReaderOptions{Snapshot: &snapshot}},
			defaults: &projectcontracts.ReaderDefaults{Iceberg: &projectcontracts.IcebergReaderOptions{Snapshot: &quote}},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				v := got.Value.(*projectcontracts.IcebergPathSourceLocation)
				if *v.Options.Snapshot != snapshot {
					t.Fatalf("iceberg options = %#v", v.Options)
				}
			},
		},
		{
			name:     "lance",
			format:   "lance",
			location: &projectcontracts.LancePathSourceLocation{PathSourceLocationBase: pathBase("lance"), Format: "lance"},
			assert: func(t *testing.T, got *projectcontracts.PathSourceLocation) {
				if got.Value.(*projectcontracts.LancePathSourceLocation).Options != nil {
					t.Fatalf("lance options = %#v, want nil", got.Value)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveEffectivePathLocation(semanticmodel.Source{Format: tc.format, PathLocation: pathLocation(tc.format, tc.location)}, semanticmodel.Connection{ReaderDefaults: tc.defaults})
			if err != nil {
				t.Fatalf("ResolveEffectivePathLocation() error = %v", err)
			}
			if got == nil {
				t.Fatal("effective path location is nil")
			}
			tc.assert(t, got)
		})
	}
}

var recordsDepth int32 = 17

func TestResolveEffectivePathLocationUsesGeneratedDefaultsForEveryFormat(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		location projectcontracts.PathSourceLocationVariant
		want     projectcontracts.PathSourceLocationVariant
	}{
		{"csv", "csv", &projectcontracts.CSVPathSourceLocation{PathSourceLocationBase: pathBase("csv"), Format: "csv"}, &projectcontracts.CSVPathSourceLocation{PathSourceLocationBase: pathBase("csv"), Format: "csv", Options: projectcontracts.DefaultCSVReaderOptions()}},
		{"json", "json", &projectcontracts.JSONPathSourceLocation{PathSourceLocationBase: pathBase("json"), Format: "json"}, &projectcontracts.JSONPathSourceLocation{PathSourceLocationBase: pathBase("json"), Format: "json", Options: projectcontracts.DefaultJSONReaderOptions()}},
		{"parquet", "parquet", &projectcontracts.ParquetPathSourceLocation{PathSourceLocationBase: pathBase("parquet"), Format: "parquet"}, &projectcontracts.ParquetPathSourceLocation{PathSourceLocationBase: pathBase("parquet"), Format: "parquet", Options: projectcontracts.DefaultParquetReaderOptions()}},
		{"excel", "excel", &projectcontracts.ExcelPathSourceLocation{PathSourceLocationBase: pathBase("excel"), Format: "excel"}, &projectcontracts.ExcelPathSourceLocation{PathSourceLocationBase: pathBase("excel"), Format: "excel", Options: projectcontracts.DefaultExcelReaderOptions()}},
		{"text", "text", &projectcontracts.TextPathSourceLocation{PathSourceLocationBase: pathBase("text"), Format: "text"}, &projectcontracts.TextPathSourceLocation{PathSourceLocationBase: pathBase("text"), Format: "text", Options: projectcontracts.DefaultTextReaderOptions()}},
		{"blob", "blob", &projectcontracts.BlobPathSourceLocation{PathSourceLocationBase: pathBase("blob"), Format: "blob"}, &projectcontracts.BlobPathSourceLocation{PathSourceLocationBase: pathBase("blob"), Format: "blob", Options: projectcontracts.DefaultBlobReaderOptions()}},
		{"vortex", "vortex", &projectcontracts.VortexPathSourceLocation{PathSourceLocationBase: pathBase("vortex"), Format: "vortex"}, &projectcontracts.VortexPathSourceLocation{PathSourceLocationBase: pathBase("vortex"), Format: "vortex"}},
		{"delta", "delta", &projectcontracts.DeltaPathSourceLocation{PathSourceLocationBase: pathBase("delta"), Format: "delta"}, &projectcontracts.DeltaPathSourceLocation{PathSourceLocationBase: pathBase("delta"), Format: "delta"}},
		{"iceberg", "iceberg", &projectcontracts.IcebergPathSourceLocation{PathSourceLocationBase: pathBase("iceberg"), Format: "iceberg"}, &projectcontracts.IcebergPathSourceLocation{PathSourceLocationBase: pathBase("iceberg"), Format: "iceberg"}},
		{"lance", "lance", &projectcontracts.LancePathSourceLocation{PathSourceLocationBase: pathBase("lance"), Format: "lance"}, &projectcontracts.LancePathSourceLocation{PathSourceLocationBase: pathBase("lance"), Format: "lance"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveEffectivePathLocation(semanticmodel.Source{Format: tc.format, PathLocation: pathLocation(tc.format, tc.location)}, semanticmodel.Connection{})
			if err != nil {
				t.Fatalf("ResolveEffectivePathLocation() error = %v", err)
			}
			if !reflect.DeepEqual(got.Value, tc.want) {
				t.Fatalf("effective variant = %#v, want %#v", got.Value, tc.want)
			}
		})
	}
}

func TestEffectivePathLocationAndSchemaEvidencePersistInManifestJSON(t *testing.T) {
	nonNull := false
	location := pathLocation("csv", &projectcontracts.CSVPathSourceLocation{PathSourceLocationBase: pathBase("csv"), Format: "csv", Options: projectcontracts.DefaultCSVReaderOptions()})
	source := semanticmodel.Source{
		LocationType: semanticmodel.KindPath, Path: "orders.csv", Format: "csv", Connection: "files", PathLocation: location, EffectivePathLocation: location, SchemaMode: "compatible",
		Fields: map[string]semanticmodel.SourceField{"id": {Datatype: semanticmodel.DataTypeInteger, Nullable: &nonNull}},
	}
	manifest := projectmanifest.Project{Sources: map[string]semanticmodel.Source{"source:orders": source}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var roundTrip projectmanifest.Project
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	got := roundTrip.Sources["source:orders"]
	if got.SchemaMode != source.SchemaMode || got.Fields["id"].Datatype != semanticmodel.DataTypeInteger || got.Fields["id"].Nullable == nil || *got.Fields["id"].Nullable {
		t.Fatalf("manifest round-trip lost compiled source evidence: %#v", got)
	}
}
