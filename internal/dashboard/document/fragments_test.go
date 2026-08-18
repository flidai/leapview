package document

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandDashboardFragmentsCanonicalEquivalenceAndLayout(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "dashboards", "sales.yaml")
	if err := os.MkdirAll(filepath.Dir(dashboardPath), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(filepath.Dir(dashboardPath), name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return name
	}
	visualPath := write("visuals.yaml", "visuals:\n  revenue:\n    type: bar\n    query:\n      type: aggregate\n      dimensions: []\n      metrics: [revenue]\n    presentation: {type: cartesian}\n")
	pagePath := write("pages.yaml", "pages:\n  - id: overview\n    title: Overview\n    components: []\n")
	componentPath := write("components.yaml", "components:\n  overview:\n    - type: visual\n      id: revenue-component\n      placement: {column: 1, row: 1, columnSpan: 4, rowSpan: 3}\n      visual: revenue\n")

	input := DashboardDocument{
		APIVersion: DashboardApiVersionLeapviewDevV1, Kind: DashboardResourceKindDashboard,
		Metadata: DashboardMetadata{ID: "dashboard:sales", Name: "sales"},
		Spec: DashboardSpec{
			SemanticModel: "semantic:sales",
			Visuals:       map[string]DashboardVisual{"local": canonicalTestVisual()},
			Pages:         []DashboardPage{{ID: "local", Title: "Local", Components: []DashboardPageComponent{}}},
			Includes:      &DashboardIncludes{Visuals: ptrSlice(visualPath), Pages: ptrSlice(pagePath), Components: ptrSlice(componentPath)},
		},
	}
	result, err := ExpandDashboardFragments(input, dashboardPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Spec.Includes != nil {
		t.Fatal("expanded canonical document retained includes")
	}
	if len(result.Paths) != 3 || len(result.Layout.Visuals) != 1 || len(result.Layout.Pages) != 1 || len(result.Layout.Components) != 1 {
		t.Fatalf("fragment source evidence = %#v / %#v", result.Paths, result.Layout)
	}
	if got := result.Document.Spec.Pages[0].ID; got != "overview" {
		t.Fatalf("expanded page sequence starts with %q, want overview", got)
	}
	if got := len(result.Document.Spec.Pages[0].Components); got != 1 {
		t.Fatalf("expanded page components = %d, want 1", got)
	}

	canonical := input
	canonical.Spec.Includes = nil
	canonical.Spec.Visuals = map[string]DashboardVisual{"revenue": canonicalTestVisual(), "local": canonicalTestVisual()}
	canonical.Spec.Pages = []DashboardPage{{ID: "overview", Title: "Overview", Components: result.Document.Spec.Pages[0].Components}, {ID: "local", Title: "Local", Components: []DashboardPageComponent{}}}
	left, err := json.Marshal(result.Document)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("expanded document differs from direct canonical document:\n%s\n%s", left, right)
	}
}

func TestExpandDashboardFragmentsRejectsUnsafePathsCyclesAndDuplicates(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "sales.yaml")
	if err := os.WriteFile(filepath.Join(root, "fragment.yaml"), []byte("visuals: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, pattern, want string }{
		{"absolute", filepath.Join(root, "fragment.yaml"), "must be relative"},
		{"traversal", "../fragment.yaml", "escapes"},
		{"glob", "**/*.yaml", "unsupported"},
		{"missing", "missing.yaml", "matched no files"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice(test.pattern)})
			_, err := ExpandDashboardFragments(input, dashboardPath, root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expansion error = %v, want %q", err, test.want)
			}
		})
	}
	cycle := filepath.Join(root, "cycle.yaml")
	if err := os.WriteFile(cycle, []byte("includes:\n  visuals: [cycle.yaml]\nvisuals: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExpandDashboardFragments(fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice("cycle.yaml")}), dashboardPath, root)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle accepted: %v", err)
	}
	duplicate := filepath.Join(root, "duplicate.yaml")
	if err := os.WriteFile(duplicate, []byte("visuals:\n  local:\n    type: bar\n    query: {type: aggregate, dimensions: [], metrics: [revenue]}\n    presentation: {type: cartesian}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ExpandDashboardFragments(fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice("duplicate.yaml")}), dashboardPath, root)
	if err == nil || (!strings.Contains(err.Error(), "defined more than once") && !strings.Contains(err.Error(), "redefined")) {
		t.Fatalf("duplicate visual accepted: %v", err)
	}
}

func TestExpandDashboardFragmentsReportsFragmentLine(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "sales.yaml")
	if err := os.WriteFile(filepath.Join(root, "bad.yaml"), []byte("visuals:\n  broken:\n    type: bar\n    unexpected: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExpandDashboardFragments(fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice("bad.yaml")}), dashboardPath, root)
	var fragmentErr *FragmentError
	if err == nil || !strings.Contains(err.Error(), "bad.yaml") || !asFragmentError(err, &fragmentErr) || fragmentErr.Line == 0 {
		t.Fatalf("fragment diagnostic = %v, want bad.yaml source line", err)
	}
}

func canonicalTestVisual() DashboardVisual {
	return DashboardVisual{Type: DashboardVisualTypeBar, Query: DashboardQuery{Value: &AggregateDashboardQuery{Type: "aggregate", Dimensions: []DashboardDimensionSelection{}, Metrics: []DashboardMetricSelection{metric("revenue")}}}, Presentation: DashboardPresentation{Value: &CartesianDashboardPresentation{Type: "cartesian"}}}
}

func fragmentTestDocument(includes *DashboardIncludes) DashboardDocument {
	return DashboardDocument{APIVersion: DashboardApiVersionLeapviewDevV1, Kind: DashboardResourceKindDashboard, Metadata: DashboardMetadata{ID: "dashboard:sales", Name: "sales"}, Spec: DashboardSpec{SemanticModel: "semantic:sales", Visuals: map[string]DashboardVisual{"local": canonicalTestVisual()}, Pages: []DashboardPage{{ID: "local", Title: "Local", Components: []DashboardPageComponent{}}}, Includes: includes}}
}

func ptrSlice(value string) *[]string { values := []string{value}; return &values }

func asFragmentError(err error, target **FragmentError) bool {
	value, ok := err.(*FragmentError)
	if ok {
		*target = value
	}
	return ok
}
