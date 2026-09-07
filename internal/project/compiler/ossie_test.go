package compiler

import (
	"strings"
	"testing"
)

func TestCanonicalSourceOssieSurfaceRoundTripsSemanticModel(t *testing.T) {
	project, err := LoadSourceRoot("../../../dashboards/experiments/movielens")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := project.ExportOssie("genre_ratings")
	if err != nil {
		t.Fatalf("ExportOssie: %v", err)
	}
	if !strings.Contains(string(wire), `"version": "0.2.0.dev0"`) {
		t.Fatalf("export is not pinned Ossie: %s", wire[:min(len(wire), 200)])
	}
	imported, err := project.ImportOssie(wire)
	if err != nil {
		t.Fatalf("ImportOssie: %v", err)
	}
	if imported.Name != "genre_ratings" || len(imported.Datasets) == 0 || len(imported.Metrics) == 0 {
		t.Fatalf("round-tripped semantic model = %#v", imported)
	}
}
