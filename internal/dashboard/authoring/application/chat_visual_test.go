package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestAppendChatVisualFiltersScopesImportedStandaloneFilterToNewVisual(t *testing.T) {
	filter := testChatVisualFilter("region", "orders.region", nil)
	dashboard := document.DashboardDocument{
		Spec: document.DashboardSpec{
			Visuals: map[string]document.DashboardVisual{"existing-orders": {}},
			Filters: []document.DashboardFilter{},
		},
	}

	if err := appendChatVisualFilters(&dashboard, []document.DashboardFilter{filter}, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != 1 || dashboard.Spec.Filters[0].Targets == nil {
		t.Fatalf("imported filter targets = %#v, want an explicit target", dashboard.Spec.Filters)
	}
	targets := *dashboard.Spec.Filters[0].Targets
	if len(targets) != 1 || targets[0] != "imported-chat-visual" {
		t.Fatalf("imported filter targets = %v, want only imported-chat-visual", targets)
	}
	if chatFilterAppliesToVisual(dashboard.Spec.Filters[0], "existing-orders") {
		t.Fatal("imported source filter unexpectedly applies to a preexisting visual")
	}
	if !chatFilterAppliesToVisual(dashboard.Spec.Filters[0], "imported-chat-visual") {
		t.Fatal("imported source filter does not apply to its imported visual")
	}
}

func TestAppendChatVisualFiltersRemapsOnlyApplicableExplicitTarget(t *testing.T) {
	applicableTargets := []string{"chat-artifact"}
	inapplicableTargets := []string{"existing-orders"}
	filters := []document.DashboardFilter{
		testChatVisualFilter("applicable", "orders.region", &applicableTargets),
		testChatVisualFilter("inapplicable", "orders.status", &inapplicableTargets),
	}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{}}}

	if err := appendChatVisualFilters(&dashboard, filters, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != 1 {
		t.Fatalf("imported filters = %#v, want only the applicable source filter", dashboard.Spec.Filters)
	}
	if targets := *dashboard.Spec.Filters[0].Targets; len(targets) != 1 || targets[0] != "imported-chat-visual" {
		t.Fatalf("remapped targets = %v, want [imported-chat-visual]", targets)
	}
}

func TestAppendChatVisualFiltersRemapsSyntheticPageComponentTarget(t *testing.T) {
	targets := []string{"page/visual"}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{}}}

	if err := appendChatVisualFilters(&dashboard, []document.DashboardFilter{
		testChatVisualFilter("region", "orders.region", &targets),
	}, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != 1 {
		t.Fatalf("imported filters = %#v, want the filter scoped to the synthetic visual", dashboard.Spec.Filters)
	}
	if got := *dashboard.Spec.Filters[0].Targets; len(got) != 1 || got[0] != "imported-chat-visual" {
		t.Fatalf("remapped targets = %v, want [imported-chat-visual]", got)
	}
}

func TestAppendChatVisualFiltersRemapsDependenciesAndUniqueURLParameters(t *testing.T) {
	parameter := "region"
	childParameter := "status"
	existing := testChatVisualFilter("segment", "orders.segment", nil)
	existing.URLParameter = &parameter
	dependency := []string{"segment"}
	filters := []document.DashboardFilter{
		{
			ID: "segment", Label: "Segment", Dimension: "segment", URLParameter: &parameter,
			Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}},
		},
		{
			ID: "status", Label: "Status", Dimension: "status", URLParameter: &childParameter,
			Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: &dependency}}}},
		},
	}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{existing}}}

	if err := appendChatVisualFilters(&dashboard, filters, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != 3 {
		t.Fatalf("filter count = %d, want existing plus two imported filters", len(dashboard.Spec.Filters))
	}
	imported := map[string]document.DashboardFilter{}
	parameters := map[string]string{}
	for _, filter := range dashboard.Spec.Filters[1:] {
		imported[filter.Label] = filter
		if filter.URLParameter == nil {
			t.Fatalf("filter %q lost its URL parameter", filter.ID)
		}
		if previous, exists := parameters[*filter.URLParameter]; exists {
			t.Fatalf("filters %q and %q share URL parameter %q", previous, filter.ID, *filter.URLParameter)
		}
		parameters[*filter.URLParameter] = filter.ID
	}
	parent, ok := imported["Segment"]
	if !ok || parent.ID == "segment" {
		t.Fatalf("colliding parent filter ID = %q, want a unique imported ID", parent.ID)
	}
	child, ok := imported["Status"]
	if !ok {
		t.Fatal("dependent status filter was not imported")
	}
	options := child.Control.Value.(*document.SingleSelectDashboardFilterControl).Options.Value.(*document.DistinctDashboardFilterOptions)
	if options.DependsOn == nil || len(*options.DependsOn) != 1 || (*options.DependsOn)[0] != parent.ID {
		t.Fatalf("status option dependencies = %v, want imported parent ID %q", options.DependsOn, parent.ID)
	}
	if parent.URLParameter != nil && *parent.URLParameter == *existing.URLParameter {
		t.Fatalf("parent URL parameter = %q, collides with existing filter", *parent.URLParameter)
	}
}

func TestAppendChatVisualFiltersReservesFutureIDsWhenRenamingDependencies(t *testing.T) {
	filters := []document.DashboardFilter{
		{ID: "parent", Label: "Parent", Dimension: "parent", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}},
		{ID: "parent_1", Label: "Parent one", Dimension: "parent_one", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}},
		{ID: "parent_1_1", Label: "Parent one one", Dimension: "parent_one_one", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}},
		{ID: "parent_1_2", Label: "Parent one two", Dimension: "parent_one_two", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}},
		{ID: "parent_1_3", Label: "Parent one three", Dimension: "parent_one_three", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}},
		{ID: "child", Label: "Child", Dimension: "child", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: &[]string{"parent_1"}}}}}},
	}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{
		testChatVisualFilter("parent", "existing.parent", nil),
		testChatVisualFilter("parent_1", "existing.parent_one", nil),
	}}}

	if err := appendChatVisualFilters(&dashboard, filters, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	byLabel := make(map[string]document.DashboardFilter, len(filters))
	for _, filter := range dashboard.Spec.Filters[2:] {
		byLabel[filter.Label] = filter
	}
	if got := byLabel["Parent one"].ID; got != "parent_1_4" {
		t.Fatalf("renamed source parent_1 ID = %q, want parent_1_4 after reserving future source IDs", got)
	}
	child := byLabel["Child"]
	options := child.Control.Value.(*document.SingleSelectDashboardFilterControl).Options.Value.(*document.DistinctDashboardFilterOptions)
	if options.DependsOn == nil || len(*options.DependsOn) != 1 || (*options.DependsOn)[0] != "parent_1_4" {
		t.Fatalf("child option dependencies = %v, want [parent_1_4]", options.DependsOn)
	}
}

func TestAppendChatVisualFiltersReplaysCyclicOptionDependencies(t *testing.T) {
	dependsOnB := []string{"b"}
	dependsOnA := []string{"a"}
	filters := []document.DashboardFilter{
		{ID: "a", Label: "A", Dimension: "a", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: &dependsOnB}}}}},
		{ID: "b", Label: "B", Dimension: "b", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: &dependsOnA}}}}},
	}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{
		testChatVisualFilter("a", "existing.a", nil),
		testChatVisualFilter("b", "existing.b", nil),
	}}}

	if err := appendChatVisualFilters(&dashboard, filters, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("first appendChatVisualFilters() error = %v", err)
	}
	firstImportCount := len(dashboard.Spec.Filters)
	if err := appendChatVisualFilters(&dashboard, filters, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("replayed appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != firstImportCount {
		t.Fatalf("filter count after cyclic replay = %d, want %d", len(dashboard.Spec.Filters), firstImportCount)
	}
	for _, filter := range dashboard.Spec.Filters[2:] {
		options := filter.Control.Value.(*document.SingleSelectDashboardFilterControl).Options.Value.(*document.DistinctDashboardFilterOptions)
		if options.DependsOn == nil || len(*options.DependsOn) != 1 {
			t.Fatalf("filter %q dependencies = %v, want one remapped dependency", filter.ID, options.DependsOn)
		}
		want := "a_1"
		if filter.ID == "a_1" {
			want = "b_1"
		}
		if (*options.DependsOn)[0] != want {
			t.Fatalf("filter %q dependency = %q, want %q", filter.ID, (*options.DependsOn)[0], want)
		}
	}
}

func TestAddChatVisualToDocumentUsesUniqueFilterURLParametersAcrossImportsAndReplays(t *testing.T) {
	parameter := "period"
	pageParameter := "period"
	pageBindings := []document.DashboardPageFilterBinding{{ID: "existing-control", Filter: "existing", URLParameter: &pageParameter}}
	source := ChatVisualImport{
		ArtifactID: "chat-artifact", SemanticModelID: "sales",
		Visual: document.DashboardVisual{},
		Filters: []document.DashboardFilter{
			{ID: "period", Label: "Period", Dimension: "period", URLParameter: &parameter, Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}}},
		},
	}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{}, FilterBindings: &pageBindings}}, Visuals: map[string]document.DashboardVisual{}, Filters: []document.DashboardFilter{}}}
	dashboard.Spec.SemanticModel = "sales"
	firstCommand := authoring.CommandID("018f5c8a-9d40-7c50-9e31-1c8a7b1a0e0b")
	secondCommand := authoring.CommandID("018f5c8a-9d40-7c50-9e31-1c8a7b1a0e0c")

	if _, err := AddChatVisualToDocument(&dashboard, "overview", source, firstCommand); err != nil {
		t.Fatalf("first AddChatVisualToDocument() error = %v", err)
	}
	firstCount := len(dashboard.Spec.Filters)
	if !chatVisualReplayMatches(dashboard, "overview", source, firstCommand) {
		t.Fatal("unchanged source does not match its imported filter replay")
	}
	changedSource := source
	changedSource.Filters = append([]document.DashboardFilter(nil), source.Filters...)
	changedParameter := "changed_period"
	changedSource.Filters[0].URLParameter = &changedParameter
	if chatVisualReplayMatches(dashboard, "overview", changedSource, firstCommand) {
		t.Fatal("changed source URL parameter matched its imported replay")
	}
	changedSource = source
	changedSource.Filters = append([]document.DashboardFilter(nil), source.Filters...)
	changedSource.Filters[0].ID = "renamed_period"
	if chatVisualReplayMatches(dashboard, "overview", changedSource, firstCommand) {
		t.Fatal("changed source filter ID matched its imported replay")
	}
	if _, err := AddChatVisualToDocument(&dashboard, "overview", source, firstCommand); err != nil {
		t.Fatalf("replayed AddChatVisualToDocument() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != firstCount {
		t.Fatalf("filter count after replay = %d, want %d", len(dashboard.Spec.Filters), firstCount)
	}
	if _, err := AddChatVisualToDocument(&dashboard, "overview", source, secondCommand); err != nil {
		t.Fatalf("second AddChatVisualToDocument() error = %v", err)
	}
	parameters := map[string]string{}
	for _, filter := range dashboard.Spec.Filters {
		if filter.URLParameter == nil {
			continue
		}
		if previous, exists := parameters[*filter.URLParameter]; exists {
			t.Fatalf("filters %q and %q share URL parameter %q", previous, filter.ID, *filter.URLParameter)
		}
		parameters[*filter.URLParameter] = filter.ID
	}
	if parameters[pageParameter] != "" {
		t.Fatalf("imported filter retained page-binding URL parameter %q", pageParameter)
	}
}

func testChatVisualFilter(id, dimension string, targets *[]string) document.DashboardFilter {
	return document.DashboardFilter{
		ID: id, Label: id, Dimension: dimension, Targets: targets,
		Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}},
	}
}

func chatFilterAppliesToVisual(filter document.DashboardFilter, visualID string) bool {
	if filter.Targets == nil {
		return true
	}
	for _, target := range *filter.Targets {
		if target == visualID {
			return true
		}
	}
	return false
}

func TestChatVisualReplayRejectsSourceChangingToImportedFilterAliases(t *testing.T) {
	parameter := "period"
	source := ChatVisualImport{ArtifactID: strings.Repeat("long_artifact_", 20), SemanticModelID: "sales", Filters: []document.DashboardFilter{{ID: "period", Label: "Period", Dimension: "period", URLParameter: &parameter, Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}}}}}
	existing := source.Filters[0]
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Pages: []document.DashboardPage{{ID: "overview"}}, Visuals: map[string]document.DashboardVisual{}, Filters: []document.DashboardFilter{existing}}}
	dashboard.Spec.SemanticModel = "sales"
	command := authoring.CommandID("018f5c8a-9d40-7c50-9e31-1c8a7b1a0e0b")
	if _, err := AddChatVisualToDocument(&dashboard, "overview", source, command); err != nil {
		t.Fatal(err)
	}
	for id := range dashboard.Spec.Visuals {
		if len(id+"_component") > 128 {
			t.Fatalf("generated component ID exceeds schema limit: %d", len(id+"_component"))
		}
	}
	imported := dashboard.Spec.Filters[1]
	for name, mutate := range map[string]func(*document.DashboardFilter){
		"renamed URL alias": func(filter *document.DashboardFilter) { filter.URLParameter = imported.URLParameter },
		"renamed ID alias":  func(filter *document.DashboardFilter) { filter.ID = imported.ID },
	} {
		t.Run(name, func(t *testing.T) {
			changed := source
			changed.Filters = append([]document.DashboardFilter(nil), source.Filters...)
			mutate(&changed.Filters[0])
			if chatVisualReplayMatches(dashboard, "overview", changed, command) {
				t.Fatal("changed source intent matched a retained imported alias")
			}
			if _, err := AddChatVisualToDocument(&dashboard, "overview", changed, command); !errors.Is(err, authoring.ErrCommandReuse) {
				t.Fatalf("changed source intent error=%v, want ErrCommandReuse", err)
			}
		})
	}
	if !chatVisualReplayMatches(dashboard, "overview", source, command) {
		t.Fatal("original retry no longer matches")
	}
}
