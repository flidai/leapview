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
		{"text", map[string]any{}},
		{"blob", map[string]any{}},
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

func TestDuckDBPathOptionsRejectsNilAndKeepsLanceOptionless(t *testing.T) {
	if _, err := duckDBPathOptions(nil); err == nil {
		t.Fatal("nil path location was accepted")
	}
	location := &projectcontracts.PathSourceLocation{Value: &projectcontracts.LancePathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "fixture.lance", Format: "lance"},
		Format:                 "lance",
	}}
	if got, err := duckDBPathOptions(location); err != nil || len(got) != 0 {
		t.Fatalf("lance options = %#v, err = %v; want empty", got, err)
	}
}

func TestTableReaderIDsRejectInvalidValuesBeforeSQL(t *testing.T) {
	for _, format := range []string{"delta", "iceberg"} {
		t.Run(format, func(t *testing.T) {
			location := func(value string) *projectcontracts.PathSourceLocation {
				got := testPathLocation(format, "fixture")
				switch variant := got.Value.(type) {
				case *projectcontracts.DeltaPathSourceLocation:
					variant.Options = &projectcontracts.DeltaReaderOptions{Version: &value}
				case *projectcontracts.IcebergPathSourceLocation:
					variant.Options = &projectcontracts.IcebergReaderOptions{Snapshot: &value}
				}
				return got
			}
			for _, value := range []string{"0", "5298355539581857556", "9223372036854775807"} {
				got, err := duckDBPathOptions(location(value))
				if err != nil {
					t.Fatalf("valid nonnegative ID %s: %v", value, err)
				}
				key := "version"
				if format == "iceberg" {
					key = "snapshot_from_id"
				}
				if got[key] != value {
					t.Fatalf("ID options = %#v, want exact decimal %s", got, value)
				}
			}
			for _, value := range []string{"", "-1", "+1", "01", "1.5", "1e3", "not-an-id", "9223372036854775808", "18446744073709551615", "18446744073709551616"} {
				if _, err := duckDBPathOptions(location(value)); err == nil {
					t.Fatalf("invalid ID %q reached SQL options", value)
				}
			}
		})
	}
}
