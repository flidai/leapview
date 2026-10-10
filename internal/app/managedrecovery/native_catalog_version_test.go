package managedrecovery

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestNativeCatalogReadbackPreservesExactFormat(t *testing.T) {
	set := recoveryset.RecoverySet{Catalog: recoveryset.CatalogCommit{CatalogVersion: 1}, Serving: recoveryset.SnapshotSeal{CatalogSchemaVersion: "1.0"}, Compatibility: physicalpool.Compatibility{CatalogFormat: "ducklake:1.0"}}
	for _, version := range []string{"1", "1.0"} {
		if !nativeCatalogVersionMatches(version, set) {
			t.Fatalf("actual DuckLake format %q denied", version)
		}
	}
	for _, version := range []string{"", "1.1", "1.0-dev1", "2", "01.0"} {
		if nativeCatalogVersionMatches(version, set) {
			t.Fatalf("foreign DuckLake format %q accepted", version)
		}
	}
	set.Serving.CatalogSchemaVersion = "2"
	if nativeCatalogVersionMatches("1.0", set) {
		t.Fatal("foreign sealed schema accepted")
	}
	set.Serving.CatalogSchemaVersion = "1.0"
	set.Compatibility.CatalogFormat = "ducklake:1.1"
	if nativeCatalogVersionMatches("1.0", set) {
		t.Fatal("foreign compatibility tuple accepted")
	}
}
