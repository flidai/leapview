package compiler

import (
	"slices"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func canonicalFilterTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
				"amount": {Field: "orders.amount", Type: "number", Datatype: semanticmodel.DataTypeInteger},
			}},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"status": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
			"amount": {Type: "number", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.amount"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}}},
	}
}

func canonicalVisual(id string) document.DashboardVisual {
	return document.DashboardVisual{Type: document.DashboardVisualTypeKpi, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: &id}}}}}
}

func canonicalStringValue(value string) document.DashboardFilterValue {
	return document.DashboardFilterValue{Value: &document.StringDashboardFilterValue{Type: "string", Value: value}}
}

func TestCompileCanonicalDashboardFiltersPreservesOrderDefaultsAndPlacement(t *testing.T) {
	model := canonicalFilterTestModel()
	url := "status"
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"}, Spec: document.DashboardSpec{
		SemanticModel: "sales",
		Filters: []document.DashboardFilter{
			{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}, Default: &document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{canonicalStringValue("paid")}}}, URLParameter: &url},
			{ID: "amount", Label: "Amount", Dimension: "amount", Control: document.DashboardFilterControl{Value: &document.NumericRangeDashboardFilterControl{Type: "numericRange"}}},
		},
		Visuals: map[string]document.DashboardVisual{"orders": canonicalVisual("order_count")},
		Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{
			{Value: &document.FilterDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "status-control", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 3, RowSpan: 1}}, Type: "filter", Filter: "status"}},
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders-card", Placement: document.DashboardPlacement{Column: 4, Row: 1, ColumnSpan: 9, RowSpan: 4}}, Type: "visual", Visual: "orders"}},
		}}},
	}}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(compiled.Order, ","); got != "status,amount" {
		t.Fatalf("filter order = %q", got)
	}
	binding := compiled.Bindings["status"]
	if binding.Scope != "report" || binding.URL.Param != "status" || binding.URL.Encoding != "" || binding.Pane.Order != 0 || len(binding.Targets) != 1 || binding.Targets[0] != "overview/orders-card" {
		t.Fatalf("compiled status binding = %#v", binding)
	}
	if got := compiled.Pages[0].Visuals[0].Binding; got.Scope != "report" || got.ID != "status" {
		t.Fatalf("slicer binding = %#v", got)
	}
	if got := compiled.Pages[0].Visuals[0].Presentation.Style; got != dashboardfilter.PresentationDropdown {
		t.Fatalf("slicer presentation = %q, want dropdown", got)
	}
	definition := dashboarddefinition.Definition{Pages: []dashboard.Page{{ID: "overview", Visuals: []dashboard.PageVisual{{ID: "status-control", Kind: "slicer"}, {ID: "orders-card", Kind: "visual"}}}}}
	if err := compiled.ApplyToDefinition(&definition); err != nil {
		t.Fatal(err)
	}
	if got := definition.Pages[0].Visuals[0].Presentation.Style; got != dashboardfilter.PresentationDropdown {
		t.Fatalf("attached slicer presentation = %q, want dropdown", got)
	}
}

func TestCanonicalFilterPresentationLowersTypedControls(t *testing.T) {
	tests := []struct {
		name    string
		control document.DashboardFilterControlVariant
		want    dashboardfilter.PresentationStyle
	}{
		{name: "single default", control: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}, want: dashboardfilter.PresentationDropdown},
		{name: "single distinct", control: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders"}}}, want: dashboardfilter.PresentationList},
		{name: "single static", control: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.StaticDashboardFilterOptions{Type: "static", Values: []document.DashboardFilterOption{}}}}, want: dashboardfilter.PresentationButtons},
		{name: "multi", control: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}, want: dashboardfilter.PresentationDropdown},
		{name: "text", control: &document.TextDashboardFilterControl{Type: "text"}, want: dashboardfilter.PresentationInput},
		{name: "numeric", control: &document.NumericRangeDashboardFilterControl{Type: "numericRange"}, want: dashboardfilter.PresentationNumericRange},
		{name: "date", control: &document.DateRangeDashboardFilterControl{Type: "dateRange"}, want: dashboardfilter.PresentationDateRange},
		{name: "relative", control: &document.RelativePeriodDashboardFilterControl{Type: "relativePeriod"}, want: dashboardfilter.PresentationRelativePeriod},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalFilterPresentation(document.DashboardFilterControl{Value: test.control})
			if err != nil {
				t.Fatal(err)
			}
			if got.Style != test.want {
				t.Fatalf("presentation style = %q, want %q", got.Style, test.want)
			}
		})
	}
}

func TestCompileCanonicalDashboardFiltersRejectsPhysicalAndReservedURL(t *testing.T) {
	model := canonicalFilterTestModel()
	reserved := "page"
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{{ID: "bad", Label: "Bad", Dimension: "orders.status", Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: "text"}}, URLParameter: &reserved}}}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "semantic dimension") {
		t.Fatalf("physical dimension error = %v", err)
	}
	doc.Spec.Filters[0].Dimension = "status"
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved URL error = %v", err)
	}
	otherURL := "shared"
	doc.Spec.Filters = append(doc.Spec.Filters, document.DashboardFilter{ID: "other", Label: "Other", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: "text"}}, URLParameter: &otherURL})
	// The first filter still has the reserved name; replace it with a valid
	// parameter before checking duplicate detection.
	validURL := "shared"
	doc.Spec.Filters[0].URLParameter = &validURL
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "URL parameter") {
		t.Fatalf("duplicate URL error = %v", err)
	}
}

func TestCompileCanonicalDashboardFiltersRejectsSecondInCanvasPlacement(t *testing.T) {
	model := canonicalFilterTestModel()
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: "text"}}}}, Pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{
		{Value: &document.FilterDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "one", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 2, RowSpan: 1}}, Type: "filter", Filter: "status"}},
		{Value: &document.FilterDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "two", Placement: document.DashboardPlacement{Column: 3, Row: 1, ColumnSpan: 2, RowSpan: 1}}, Type: "filter", Filter: "status"}},
	}}}}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate placement error = %v", err)
	}
}

func TestCompileCanonicalDashboardFiltersRequiresExplicitNarrowingForIncompatibleVisibleTarget(t *testing.T) {
	model := canonicalFilterTestModel()
	model.Datasets["customers"] = semanticmodel.SemanticDatasetSpec{Model: "customers"}
	model.Tables["customers"] = semanticmodel.Table{ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"name": {Field: "customers.name", Type: "string", Datatype: semanticmodel.DataTypeString}}}
	model.Metrics["customer_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.name"}}
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		Filters: []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Visuals: map[string]document.DashboardVisual{"orders": canonicalVisual("order_count"), "customers": canonicalVisual("customer_count")},
		Pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders-card", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 2}}, Type: "visual", Visual: "orders"}},
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "customers-card", Placement: document.DashboardPlacement{Column: 5, Row: 1, ColumnSpan: 4, RowSpan: 2}}, Type: "visual", Visual: "customers"}},
		}}},
	}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "narrow targets") {
		t.Fatalf("all-target incompatibility error = %v", err)
	}
	target := "orders"
	doc.Spec.Filters[0].Targets = &[]string{target}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatalf("explicit narrowing rejected: %v", err)
	}
	if got := compiled.Bindings["status"].Targets; len(got) != 1 || got[0] != "overview/orders-card" {
		t.Fatalf("narrowed targets = %#v", got)
	}
	doc.Spec.Filters[0].Targets = &[]string{"overview/orders-card"}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err != nil {
		t.Fatalf("qualified report target rejected: %v", err)
	}
	doc.Spec.Filters[0].Targets = &[]string{"orders-card"}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("unqualified report component target was accepted: %v", err)
	}
}

func TestCompileCanonicalDashboardFiltersRetainsOneReportBindingAcrossPageRelocation(t *testing.T) {
	model := canonicalFilterTestModel()
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		Filters: []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Visuals: map[string]document.DashboardVisual{"orders": canonicalVisual("order_count")},
		Pages:   []document.DashboardPage{{ID: "one", Components: []document.DashboardPageComponent{{Value: &document.FilterDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "bar", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 2, RowSpan: 1}}, Type: "filter", Filter: "status"}}, {Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders", Placement: document.DashboardPlacement{Column: 3, Row: 1, ColumnSpan: 2, RowSpan: 1}}, Type: "visual", Visual: "orders"}}}}, {ID: "two", Components: []document.DashboardPageComponent{{Value: &document.FilterDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "bar", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 2, RowSpan: 1}}, Type: "filter", Filter: "status"}}, {Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders", Placement: document.DashboardPlacement{Column: 3, Row: 1, ColumnSpan: 2, RowSpan: 1}}, Type: "visual", Visual: "orders"}}}}},
	}}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Bindings) != 1 || compiled.Pages[0].Visuals[0].Binding != compiled.Pages[1].Visuals[0].Binding {
		t.Fatalf("filter binding was not stable across pages: %#v", compiled.Bindings)
	}
}

func TestCompileCanonicalDashboardFiltersCreatesFirstClassPageBinding(t *testing.T) {
	model := canonicalFilterTestModel()
	required, editable, pageURL := true, false, "page_status"
	pageDefault := document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{canonicalStringValue("paid")}}}
	pageTargets := []string{"orders_card"}
	pageBindings := []document.DashboardPageFilterBinding{{ID: "page_status", Filter: "status", Default: &pageDefault, Required: &required, ReaderEditable: &editable, Targets: &pageTargets, URLParameter: &pageURL}}
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		SemanticModel: "sales",
		Filters:       []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Visuals:       map[string]document.DashboardVisual{"orders": canonicalVisual("order_count")},
		Pages: []document.DashboardPage{{ID: "overview", FilterBindings: &pageBindings, Components: []document.DashboardPageComponent{
			{Value: &document.FilterDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "status_control", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 3, RowSpan: 1}}, Type: "filter", Filter: "status"}},
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders_card", Placement: document.DashboardPlacement{Column: 4, Row: 1, ColumnSpan: 9, RowSpan: 4}}, Type: "visual", Visual: "orders"}},
		}}},
	}}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := compiled.Bindings["status"]; exists {
		t.Fatalf("page-scoped filter retained report binding: %#v", compiled.Bindings)
	}
	pageBinding := compiled.Pages[0].FilterBindings["page_status"]
	if pageBinding.Scope != dashboardfilter.ScopePage || pageBinding.PageID != "overview" || pageBinding.Filter != "status" || pageBinding.Key == "" {
		t.Fatalf("compiled page binding = %#v", pageBinding)
	}
	if !pageBinding.Required || pageBinding.Editable() || pageBinding.URL.Param != pageURL || pageBinding.Default.Kind != dashboardfilter.ExpressionSet {
		t.Fatalf("page binding overrides = %#v", pageBinding)
	}
	if got := pageBinding.Targets; len(got) != 1 || got[0] != "overview/orders_card" {
		t.Fatalf("page binding targets = %#v", got)
	}
	if got := compiled.Pages[0].Visuals[0].Binding; got.Scope != dashboardfilter.ScopePage || got.ID != "page_status" {
		t.Fatalf("page slicer binding = %#v", got)
	}
	definition := dashboarddefinition.Definition{Pages: []dashboard.Page{{ID: "overview", Visuals: []dashboard.PageVisual{{ID: "status_control", Kind: "slicer"}, {ID: "orders_card", Kind: "visual"}}}}}
	if err := compiled.ApplyToDefinition(&definition); err != nil {
		t.Fatal(err)
	}
	if got := definition.CompiledFilterBindings()[pageBinding.Key]; got.Scope != dashboardfilter.ScopePage || got.PageID != "overview" {
		t.Fatalf("attached page binding = %#v", got)
	}
}

func TestCompileCanonicalDashboardFiltersRequiresPageLocalComponentTargets(t *testing.T) {
	model := canonicalFilterTestModel()
	pageTargets := []string{"orders"}
	pageBindings := []document.DashboardPageFilterBinding{{ID: "page_status", Filter: "status", Targets: &pageTargets}}
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		SemanticModel: "sales",
		Filters:       []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Visuals:       map[string]document.DashboardVisual{"orders": canonicalVisual("order_count")},
		Pages: []document.DashboardPage{{ID: "overview", FilterBindings: &pageBindings, Components: []document.DashboardPageComponent{
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders_card", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "orders"}},
		}}},
	}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "outside its page") {
		t.Fatalf("visual definition id was accepted as page-local target: %v", err)
	}
}

func TestCompileCanonicalDashboardFiltersRejectsAmbiguousComponentTarget(t *testing.T) {
	model := canonicalFilterTestModel()
	pageTargets := []string{"orders_card"}
	pageBindings := []document.DashboardPageFilterBinding{{ID: "page_status", Filter: "status", Targets: &pageTargets}}
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		SemanticModel: "sales",
		Filters:       []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Visuals:       map[string]document.DashboardVisual{"orders": canonicalVisual("order_count")},
		Pages: []document.DashboardPage{{ID: "overview", FilterBindings: &pageBindings, Components: []document.DashboardPageComponent{
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders_card", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "orders"}},
			{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders_copy", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "orders"}},
		}}},
	}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "cannot have independent component state") {
		t.Fatalf("ambiguous component target error = %v", err)
	}
}

func TestCanonicalOptionDependenciesRequireConsumerOverlap(t *testing.T) {
	pages := []dashboard.Page{{ID: "overview", Visuals: []dashboard.PageVisual{
		{ID: "orders_card", Kind: "visual", Visual: "orders"},
		{ID: "revenue_card", Kind: "visual", Visual: "revenue"},
	}}}
	orders := dashboardfilter.Binding{Scope: dashboardfilter.ScopePage, PageID: "overview", TargetPolicy: dashboardfilter.TargetPolicy{Include: []string{"orders_card"}}}
	revenue := dashboardfilter.Binding{Scope: dashboardfilter.ScopePage, PageID: "overview", TargetPolicy: dashboardfilter.TargetPolicy{Include: []string{"revenue_card"}}}
	if canonicalBindingTargetPoliciesOverlap(orders, revenue, pages) {
		t.Fatal("disjoint page component policies produced an option dependency edge")
	}
	reportOrders := dashboardfilter.Binding{Scope: dashboardfilter.ScopeReport, TargetPolicy: dashboardfilter.TargetPolicy{Include: []string{"orders"}}}
	if !canonicalBindingTargetPoliciesOverlap(orders, reportOrders, pages) {
		t.Fatal("page component and matching report visual policy did not overlap")
	}
}

func TestCompileCanonicalDashboardFiltersRejectsInvalidPageBindings(t *testing.T) {
	model := canonicalFilterTestModel()
	base := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		SemanticModel: "sales",
		Filters:       []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Pages:         []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{}}},
	}}
	tests := []struct {
		name     string
		bindings []document.DashboardPageFilterBinding
		want     string
	}{
		{name: "unknown filter", bindings: []document.DashboardPageFilterBinding{{ID: "missing", Filter: "missing"}}, want: "unknown filter"},
		{name: "duplicate filter", bindings: []document.DashboardPageFilterBinding{{ID: "one", Filter: "status"}, {ID: "two", Filter: "status"}}, want: "more than once"},
		{name: "duplicate id", bindings: []document.DashboardPageFilterBinding{{ID: "same", Filter: "status"}, {ID: "same", Filter: "status"}}, want: "duplicate filter binding id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := base
			doc.Spec.Pages = append([]document.DashboardPage(nil), base.Spec.Pages...)
			doc.Spec.Pages[0].FilterBindings = &test.bindings
			if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("compile error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileCanonicalDashboardFiltersValidatesURLParametersPerPageRoute(t *testing.T) {
	model := canonicalFilterTestModel()
	shared := "shared"
	one := []document.DashboardPageFilterBinding{{ID: "page_status", Filter: "status"}}
	two := []document.DashboardPageFilterBinding{{ID: "page_other", Filter: "other"}}
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{
		SemanticModel: "sales",
		Filters: []document.DashboardFilter{
			{ID: "status", Label: "Status", Dimension: "status", URLParameter: &shared, Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: "text"}}},
			{ID: "other", Label: "Other", Dimension: "status", URLParameter: &shared, Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: "text"}}},
		},
		Pages: []document.DashboardPage{{ID: "one", FilterBindings: &one, Components: []document.DashboardPageComponent{}}, {ID: "two", FilterBindings: &two, Components: []document.DashboardPageComponent{}}},
	}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err != nil {
		t.Fatalf("separate page URL parameters collided: %v", err)
	}
	combined := append(append([]document.DashboardPageFilterBinding(nil), one...), two...)
	doc.Spec.Pages[0].FilterBindings = &combined
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "on page \"one\"") {
		t.Fatalf("same-route URL collision error = %v", err)
	}
}

func TestCompileCanonicalDistinctOptionsRebindDependencyToQueriedDataset(t *testing.T) {
	model := canonicalFilterTestModel()
	model.Datasets["customers"] = semanticmodel.SemanticDatasetSpec{Model: "customers"}
	model.Tables["customers"] = semanticmodel.Table{ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"segment": {Field: "customers.segment", Type: "string", Datatype: semanticmodel.DataTypeString}}}
	model.Dimensions["segment"] = semanticmodel.SemanticDimension{Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"customers": {Field: "customers.segment"}, "orders": {Field: "orders.status"}}}
	limit := int32(20)
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{
		{ID: "segment", Label: "Segment", Dimension: "segment", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "customers", Limit: &limit}}}}},
		{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: &[]string{"segment"}}}}}},
	}}}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatal(err)
	}
	deps := compiled.Bindings["status"].OptionDependencies
	if len(deps) != 1 || deps[0].ID != "segment" || deps[0].Scope != "report" {
		t.Fatalf("status option dependencies = %#v", deps)
	}
}

func TestCompileCanonicalDistinctOptionsRejectsSelfDependency(t *testing.T) {
	model := canonicalFilterTestModel()
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{
		{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: &[]string{"status"}}}}}},
	}}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "dependency \"status\" is invalid") {
		t.Fatalf("self dependency error = %v", err)
	}
}

func TestCompileCanonicalDistinctIncludeNullAllowsNullPredicate(t *testing.T) {
	model := canonicalFilterTestModel()
	includeNull := true
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{
		{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", IncludeNull: &includeNull}}}}},
	}}}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatal(err)
	}
	operators := compiled.Definitions["status"].Predicates[0].Operators
	if !slices.Contains(operators, dashboardfilter.OperatorIsNull) {
		t.Fatalf("include-null operators = %#v", operators)
	}
}

func TestCompileCanonicalDashboardFiltersRejectsRequiredEmptyAndHonorsEditability(t *testing.T) {
	model := canonicalFilterTestModel()
	required := true
	locked := false
	doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard:sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}, Required: &required, ReaderEditable: &locked}}}}
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "non-empty default") {
		t.Fatalf("required default error = %v", err)
	}
	doc.Spec.Filters[0].Default = &document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{canonicalStringValue("paid")}}}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.Bindings["status"].Required || compiled.Bindings["status"].Editable() {
		t.Fatalf("required/editability = %#v", compiled.Bindings["status"])
	}
}
