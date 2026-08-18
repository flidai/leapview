package duckdb

import (
	"reflect"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestDuckDBPathOptionsRendersEveryTypedFormat(t *testing.T) {
	tests := []struct {
		format string
		want   map[string]any
	}{
		{"csv", map[string]any{"header": false, "delim": ",", "quote": `"`, "escape": `"`}},
		{"json", map[string]any{"format": "auto"}},
		{"parquet", map[string]any{"hive_partitioning": false, "union_by_name": false}},
		{"excel", map[string]any{"header": true}},
		{"text", map[string]any{"delim": "\t", "quote": `"`, "header": false}},
		{"blob", map[string]any{"compression": "auto"}},
		{"vortex", map[string]any{}},
		{"delta", map[string]any{}},
		{"iceberg", map[string]any{}},
		{"lance", map[string]any{}},
	}
	for _, tc := range tests {
		t.Run(tc.format, func(t *testing.T) {
			location := testPathLocation(tc.format, "fixture."+tc.format)
			got, err := duckDBPathOptions(location)
			if err != nil {
				t.Fatalf("duckDBPathOptions() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DuckDB options = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDuckDBPathOptionsRejectsNilAndUnsupportedLanceOptions(t *testing.T) {
	if _, err := duckDBPathOptions(nil); err == nil {
		t.Fatal("nil path location was accepted")
	}
	version := "v1"
	location := &projectcontracts.PathSourceLocation{Value: &projectcontracts.LancePathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "fixture.lance", Format: "lance"},
		Format:                 "lance", Options: &projectcontracts.LanceReaderOptions{Version: &version},
	}}
	if _, err := duckDBPathOptions(location); err == nil {
		t.Fatal("lance options were accepted despite replacement scan not supporting options")
	}
}
