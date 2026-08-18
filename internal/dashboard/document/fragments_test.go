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
	if err := os.WriteFile(dashboardPath, []byte("dashboard"), 0o644); err != nil {
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
	// Relocating the checkout must not alter source evidence or the canonical
	// fingerprint inputs.
	root2 := t.TempDir()
	dashboardPath2 := filepath.Join(root2, "dashboards", "sales.yaml")
	if err := os.MkdirAll(filepath.Dir(dashboardPath2), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sales.yaml", visualPath, pagePath, componentPath} {
		source := filepath.Join(filepath.Dir(dashboardPath), name)
		target := filepath.Join(filepath.Dir(dashboardPath2), name)
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second, err := ExpandDashboardFragments(input, dashboardPath2, root2)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(t, result.Document)) != string(mustJSON(t, second.Document)) || strings.Join(result.Paths, "|") != strings.Join(second.Paths, "|") || string(mustJSON(t, result.Layout)) != string(mustJSON(t, second.Layout)) {
		t.Fatalf("relocated fragment expansion changed canonical evidence: %#v / %#v", result, second)
	}
}

func TestExpandDashboardFragmentsRejectsUnsafePathsCyclesAndDuplicates(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "sales.yaml")
	if err := os.WriteFile(dashboardPath, []byte("dashboard"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	identity := filepath.Join(root, "identity.yaml")
	if err := os.WriteFile(identity, []byte("apiVersion: leapview.dev/v1\nkind: Dashboard\nmetadata: {id: dashboard:fragment, name: fragment}\nspec: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ExpandDashboardFragments(fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice("identity.yaml")}), dashboardPath, root)
	if err == nil || !strings.Contains(err.Error(), "project resource envelope") {
		t.Fatalf("fragment resource identity accepted: %v", err)
	}
}

func TestExpandDashboardFragmentsReportsFragmentLine(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "sales.yaml")
	if err := os.WriteFile(dashboardPath, []byte("dashboard"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.yaml"), []byte("visuals:\n  broken:\n    type: bar\n    unexpected: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExpandDashboardFragments(fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice("bad.yaml")}), dashboardPath, root)
	var fragmentErr *FragmentError
	if err == nil || !strings.Contains(err.Error(), "bad.yaml") || !asFragmentError(err, &fragmentErr) || fragmentErr.Line == 0 {
		t.Fatalf("fragment diagnostic = %v, want bad.yaml source line", err)
	}
}

func TestExpandDashboardFragmentsUsesStrictJSONBoundary(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "sales.yaml")
	if err := os.WriteFile(dashboardPath, []byte("dashboard"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{"duplicate keys", "visuals:\n  revenue: {}\n  revenue: {}\n", "schema.duplicate_key"},
		{"anchor", "visuals: &visuals {}\n", "schema.alias"},
		{"alias", "visuals: &visuals {}\nother: *visuals\n", "schema.alias"},
		{"explicit tag", "visuals: !!map {}\n", "schema.tag"},
		{"multiple documents", "visuals: {}\n---\nvisuals: {}\n", "schema.document"},
		{"non-string key", "visuals:\n  ? [revenue]\n  : {}\n", "schema.key"},
		{"non-finite number", "visuals:\n  revenue: {value: .nan}\n", "schema.number"},
		{"overflow number", "visuals:\n  revenue: {value: 1e400}\n", "schema.number"},
		{"underflow number", "visuals:\n  revenue: {value: 1e-400}\n", "schema.number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name+".yaml")
			if err := os.WriteFile(path, []byte(test.doc), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ExpandDashboardFragments(fragmentTestDocument(&DashboardIncludes{Visuals: ptrSlice(filepath.Base(path))}), dashboardPath, root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("strict fragment error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExpandDashboardFragmentsReportsSequenceDuplicateOrigins(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "sales.yaml")
	if err := os.WriteFile(dashboardPath, []byte("dashboard"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	filter := "- id: state\n  label: State\n  dimension: state\n  control: {type: text}\n"
	write("filter-one.yaml", filter)
	write("filter-two.yaml", "\n"+filter)
	input := fragmentTestDocument(&DashboardIncludes{Filters: ptrStrings("filter-one.yaml", "filter-two.yaml")})
	if _, err := ExpandDashboardFragments(input, dashboardPath, root); err == nil || !strings.Contains(err.Error(), "filter-two.yaml") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate filter diagnostic = %v, want second fragment origin", err)
	}
	write("page-one.yaml", "pages:\n  - id: overview\n    title: Overview\n    components: []\n")
	write("page-two.yaml", "pages:\n  - id: overview\n    title: Duplicate\n    components: []\n")
	input = fragmentTestDocument(&DashboardIncludes{Pages: ptrStrings("page-one.yaml", "page-two.yaml")})
	if _, err := ExpandDashboardFragments(input, dashboardPath, root); err == nil || !strings.Contains(err.Error(), "page-two.yaml") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate page diagnostic = %v, want second fragment origin", err)
	}
	component := "components:\n  local:\n    - type: visual\n      id: duplicate-component\n      visual: local\n      placement: {column: 1, row: 1, columnSpan: 1, rowSpan: 1}\n"
	write("component-one.yaml", component)
	write("component-two.yaml", "\n"+component)
	input = fragmentTestDocument(&DashboardIncludes{Components: ptrStrings("component-one.yaml", "component-two.yaml")})
	input.Spec.Pages = []DashboardPage{{ID: "local", Title: "Local", Components: []DashboardPageComponent{}}}
	if _, err := ExpandDashboardFragments(input, dashboardPath, root); err == nil || !strings.Contains(err.Error(), "component-two.yaml") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate component diagnostic = %v, want second fragment origin", err)
	}
	scoped := "components:\n  local:\n    - type: visual\n      id: mixed-duplicate\n      visual: local\n      placement: {column: 1, row: 1, columnSpan: 1, rowSpan: 1}\n"
	unscoped := "- type: visual\n  id: mixed-duplicate\n  visual: local\n  placement: {column: 1, row: 1, columnSpan: 1, rowSpan: 1}\n"
	write("scoped.yaml", scoped)
	write("unscoped.yaml", unscoped)
	input = fragmentTestDocument(&DashboardIncludes{Components: ptrStrings("scoped.yaml", "unscoped.yaml")})
	input.Spec.Pages = []DashboardPage{{ID: "local", Title: "Local", Components: []DashboardPageComponent{{Value: &VisualDashboardPageComponent{DashboardPageComponentBase: DashboardPageComponentBase{ID: "mixed-duplicate", Type: "visual", Placement: DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 1, RowSpan: 1}}, Type: "visual", Visual: "local"}}}}}
	if _, err := ExpandDashboardFragments(input, dashboardPath, root); err == nil || !strings.Contains(err.Error(), "scoped.yaml") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("mixed component diagnostic = %v, want scoped second origin", err)
	}
	pageKeyOne := "components:\n  local:\n    - type: visual\n      id: page-key-one\n      visual: local\n      placement: {column: 1, row: 1, columnSpan: 1, rowSpan: 1}\n"
	pageKeyTwo := "components:\n  local:\n    - type: visual\n      id: page-key-two\n      visual: local\n      placement: {column: 2, row: 1, columnSpan: 1, rowSpan: 1}\n"
	write("page-key-one.yaml", pageKeyOne)
	write("page-key-two.yaml", pageKeyTwo)
	input = fragmentTestDocument(&DashboardIncludes{Components: ptrStrings("page-key-one.yaml", "page-key-two.yaml")})
	input.Spec.Pages = []DashboardPage{{ID: "local", Title: "Local", Components: []DashboardPageComponent{}}}
	if _, err := ExpandDashboardFragments(input, dashboardPath, root); err == nil || !strings.Contains(err.Error(), "page-key-two.yaml") || !strings.Contains(err.Error(), "page key") {
		t.Fatalf("duplicate component page key accepted: %v", err)
	}
}

func canonicalTestVisual() DashboardVisual {
	return DashboardVisual{Type: DashboardVisualTypeBar, Query: DashboardQuery{Value: &AggregateDashboardQuery{Type: "aggregate", Dimensions: []DashboardDimensionSelection{}, Metrics: []DashboardMetricSelection{metric("revenue")}}}, Presentation: DashboardPresentation{Value: &CartesianDashboardPresentation{Type: "cartesian"}}}
}

func fragmentTestDocument(includes *DashboardIncludes) DashboardDocument {
	return DashboardDocument{APIVersion: DashboardApiVersionLeapviewDevV1, Kind: DashboardResourceKindDashboard, Metadata: DashboardMetadata{ID: "dashboard:sales", Name: "sales"}, Spec: DashboardSpec{SemanticModel: "semantic:sales", Visuals: map[string]DashboardVisual{"local": canonicalTestVisual()}, Pages: []DashboardPage{{ID: "local", Title: "Local", Components: []DashboardPageComponent{}}}, Includes: includes}}
}

func ptrSlice(value string) *[]string { values := []string{value}; return &values }

func ptrStrings(values ...string) *[]string { return &values }

func asFragmentError(err error, target **FragmentError) bool {
	value, ok := err.(*FragmentError)
	if ok {
		*target = value
	}
	return ok
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
