package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestChangingFilterControlMigratesPageBindingDefaults(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	doc := revision.Document
	if err := addCanonicalFilter(&doc, AddFilterPayload{FilterID: "status", Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	defaultValue := document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{{Value: &document.StringDashboardFilterValue{Type: "string", Value: "paid"}}, {Value: &document.StringDashboardFilterValue{Type: "string", Value: "pending"}}}}}
	required := true
	bindings := []document.DashboardPageFilterBinding{{ID: "status", Filter: "status", Default: &defaultValue, Required: &required}}
	doc.Spec.Pages[0].FilterBindings = &bindings
	if err := updateCanonicalFilter(&doc, UpdateFilterPayload{FilterID: "status", Label: "Status", Dataset: "orders", ControlType: "singleSelect"}); err != nil {
		t.Fatal(err)
	}
	pageDefault := (*doc.Spec.Pages[0].FilterBindings)[0].Default.Value.(*document.SetDashboardFilterExpression)
	if len(pageDefault.Values) != 1 {
		t.Fatalf("single-select retained %d page defaults", len(pageDefault.Values))
	}
	if len(defaultValue.Value.(*document.SetDashboardFilterExpression).Values) != 2 {
		t.Fatal("control change mutated prior revision's default")
	}
	if err := updateCanonicalFilter(&doc, UpdateFilterPayload{FilterID: "status", Label: "Status", Dataset: "orders", ControlType: "text"}); err != nil {
		t.Fatal(err)
	}
	binding := (*doc.Spec.Pages[0].FilterBindings)[0]
	if binding.Default != nil || binding.Required == nil || *binding.Required {
		t.Fatalf("text control retained incompatible page defaults: %#v", binding)
	}
}
