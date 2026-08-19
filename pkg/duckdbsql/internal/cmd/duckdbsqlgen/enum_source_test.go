package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnumSnapshotIsClosedAndCanonical(t *testing.T) {
	schema, err := loadEnumSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AggregateHandling", "SetOperationType", "CTEMaterialize", "OrderType", "OrderByNullType", "WindowBoundary", "WindowExcludeMode", "ExpressionType", "ExpressionClass", "LogicalTypeId"} {
		if len(schema[name]) == 0 {
			t.Fatalf("enum %s is empty", name)
		}
	}
	if got := schema["SetOperationType"][0]; got != "NONE" {
		t.Fatalf("SetOperationType first value = %q, want NONE", got)
	}
	if got := schema["LogicalTypeId"][0]; got != "INVALID" {
		t.Fatalf("LogicalTypeId first value = %q, want INVALID", got)
	}
}

func TestPinnedEnumExtractionMatchesSnapshot(t *testing.T) {
	source := os.Getenv("DUCKDB_SOURCE")
	if source == "" {
		t.Skip("DUCKDB_SOURCE is not set")
	}
	if _, err := os.Stat(filepath.Join(source, "src/include/duckdb/common/types.hpp")); err != nil {
		t.Skipf("pinned DuckDB source is unavailable: %v", err)
	}
	want, err := loadEnumSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadEnumSchema(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned enum schema differs from snapshot")
	}
}

func TestCXXEnumParserRejectsMissingDeclaration(t *testing.T) {
	if _, err := parseCXXEnum([]byte("enum class Present { VALUE };"), "Missing"); err == nil {
		t.Fatal("missing enum declaration unexpectedly accepted")
	}
}
