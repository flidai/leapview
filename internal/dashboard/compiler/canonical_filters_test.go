package compiler

import (
	"slices"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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
	if binding.Scope != "report" || binding.URL.Param != "status" || binding.Pane.Order != 0 || len(binding.Targets) != 1 || binding.Targets[0] != "overview/orders-card" {
		t.Fatalf("compiled status binding = %#v", binding)
	}
	if got := compiled.Pages[0].Visuals[0].Binding; got.Scope != "report" || got.ID != "status" {
		t.Fatalf("slicer binding = %#v", got)
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
	if _, err := CompileCanonicalDashboardFilters(doc, model); err == nil || !strings.Contains(err.Error(), "used by filters") {
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
	target := "overview/orders-card"
	doc.Spec.Filters[0].Targets = &[]string{target}
	compiled, err := CompileCanonicalDashboardFilters(doc, model)
	if err != nil {
		t.Fatalf("explicit narrowing rejected: %v", err)
	}
	if got := compiled.Bindings["status"].Targets; len(got) != 1 || got[0] != target {
		t.Fatalf("narrowed targets = %#v", got)
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
