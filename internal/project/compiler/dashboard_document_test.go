package compiler

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

func TestLoadDashboardDocumentForProjectExpandsIncludes(t *testing.T) {
	root := t.TempDir()
	dashboardDir := filepath.Join(root, "dashboards")
	if err := os.MkdirAll(dashboardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dashboardPath := filepath.Join(dashboardDir, "sales.yaml")
	dashboard := `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales}
spec:
  semanticModel: sales
  filters: []
  includes: {visuals: [visuals.yaml], pages: [pages.yaml]}
  visuals: {}
  pages: []
`
	if err := os.WriteFile(dashboardPath, []byte(dashboard), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dashboardDir, "visuals.yaml"), []byte(`visuals:
  revenue:
    type: bar
    query: {type: aggregate, dimensions: [], metrics: [revenue]}
    presentation: {type: cartesian}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dashboardDir, "pages.yaml"), []byte("pages:\n  - id: overview\n    title: Overview\n    components: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value, err := LoadDashboardDocumentForProject(dashboardPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if value.Spec.Includes != nil || len(value.Spec.Visuals) != 1 || len(value.Spec.Pages) != 1 || value.Spec.Pages[0].ID != "overview" {
		t.Fatalf("expanded document = %#v", value.Spec)
	}
}

func TestLoadDashboardDocumentUsesCanonicalGeneratedDTO(t *testing.T) {
	path := filepath.Join("..", "..", "dashboard", "document", "testdata", "canonical.yaml")
	document, err := LoadDashboardDocument(path)
	if err != nil {
		t.Fatalf("LoadDashboardDocument() error = %v", err)
	}
	if string(document.APIVersion) != "leapview.dev/v1" || string(document.Kind) != "Dashboard" {
		t.Fatalf("document envelope = %#v", document)
	}
	if document.Metadata.Name != "sales" || document.Spec.SemanticModel != "sales" {
		t.Fatalf("document identity = %#v", document)
	}
	if len(document.Spec.Visuals) != 1 || len(document.Spec.Pages) != 1 {
		t.Fatalf("document collections = %#v", document.Spec)
	}
}

func TestExportDashboardRoundTripsGeneratedDTO(t *testing.T) {
	path := filepath.Join("..", "..", "dashboard", "document", "testdata", "canonical.yaml")
	want, err := LoadDashboardDocument(path)
	if err != nil {
		t.Fatalf("LoadDashboardDocument() error = %v", err)
	}
	encoded, err := ExportDashboard(want)
	if err != nil {
		t.Fatalf("ExportDashboard() error = %v", err)
	}
	var got document.DashboardDocument
	if err := configschema.DecodeResource(configschema.KindDashboard, "roundtrip.yaml", encoded, &got); err != nil {
		t.Fatalf("DecodeResource(roundtrip) error = %v\n%s", err, encoded)
	}
	if got.Metadata.ID != want.Metadata.ID || got.Metadata.Name != want.Metadata.Name || got.Spec.SemanticModel != want.Spec.SemanticModel {
		t.Fatalf("round-trip identity = %#v, want %#v", got.Metadata, want.Metadata)
	}
	if len(got.Spec.Visuals) != len(want.Spec.Visuals) || len(got.Spec.Pages) != len(want.Spec.Pages) {
		t.Fatalf("round-trip collections = %#v, want %#v", got.Spec, want.Spec)
	}
}

func TestExportDashboardPreservesZeroDomainAndEmptyFilters(t *testing.T) {
	path := filepath.Join("..", "..", "dashboard", "document", "testdata", "canonical.yaml")
	value, err := LoadDashboardDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	value.Spec.Filters = []document.DashboardFilter{}
	minimum, maximum := 0.0, 100.0
	metric := "revenue"
	value.Spec.Visuals = map[string]document.DashboardVisual{"distribution": {
		Type:         document.DashboardVisualTypeHistogram,
		Query:        document.DashboardQuery{Value: &document.HistogramDashboardQuery{Type: "histogram", Field: document.DashboardMetricSelection{String: &metric}, Bins: 10, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact, Domain: &document.DashboardHistogramDomain{Minimum: &minimum, Maximum: &maximum}}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}},
	}}
	encoded, err := ExportDashboard(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, "filters: []") || !strings.Contains(text, "minimum: 0") {
		t.Fatalf("generated export dropped meaningful zero/empty values:\n%s", text)
	}
	var roundTrip document.DashboardDocument
	if err := configschema.DecodeResource(configschema.KindDashboard, "roundtrip.yaml", encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Spec.Filters == nil || roundTrip.Spec.Visuals["distribution"].Query.Value.(*document.HistogramDashboardQuery).Domain.Minimum == nil || *roundTrip.Spec.Visuals["distribution"].Query.Value.(*document.HistogramDashboardQuery).Domain.Minimum != 0 {
		t.Fatal("round-trip did not retain empty filters and zero domain minimum")
	}
}

func TestExportDashboardReliesOnCanonicalSchemaForEnvelopeAndName(t *testing.T) {
	path := filepath.Join("..", "..", "dashboard", "document", "testdata", "canonical.yaml")
	value, err := LoadDashboardDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*document.DashboardDocument){
		"kind": func(value *document.DashboardDocument) {
			value.Kind = document.DashboardResourceKind("NotDashboard")
		},
		"visualType": func(value *document.DashboardDocument) {
			visual := value.Spec.Visuals["revenue"]
			visual.Type = document.DashboardVisualType("unsupported")
			value.Spec.Visuals["revenue"] = visual
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := value
			mutate(&invalid)
			if _, err := ExportDashboard(invalid); err == nil {
				t.Fatal("ExportDashboard accepted an invalid generated document")
			}
		})
	}
}

func TestCanonicalDashboardYAMLJSONFingerprintMatches(t *testing.T) {
	path := filepath.Join("..", "..", "dashboard", "document", "testdata", "canonical.yaml")
	want, err := LoadDashboardDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ExportDashboard(want)
	if err != nil {
		t.Fatal(err)
	}
	var got document.DashboardDocument
	if err := configschema.DecodeResource(configschema.KindDashboard, "cross-origin.json", encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical document changed across YAML/JSON boundary:\n got=%#v\nwant=%#v", got, want)
	}
}
