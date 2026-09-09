package http

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestValidateFilterCommandPageScopeRejectsOffPageBindings(t *testing.T) {
	definition := dashboarddefinition.Definition{
		FilterBindings: map[string]dashboardfilter.Binding{"report": {Key: "report", ID: "report", Scope: dashboardfilter.ScopeReport}},
		Pages: []dashboard.Page{
			{ID: "one", FilterBindings: map[string]dashboardfilter.Binding{"status": {Key: "page-one", ID: "status", Scope: dashboardfilter.ScopePage, PageID: "one"}}},
			{ID: "two", FilterBindings: map[string]dashboardfilter.Binding{"status": {Key: "page-two", ID: "status", Scope: dashboardfilter.ScopePage, PageID: "two"}}},
		},
	}
	if err := validateFilterCommandPageScope(definition, "two", dashboardfilter.Command{Kind: dashboardfilter.CommandMutate, BindingKey: "page-one"}); err == nil {
		t.Fatal("off-page mutation was accepted")
	}
	if err := validateFilterCommandPageScope(definition, "two", dashboardfilter.Command{Kind: dashboardfilter.CommandMutate, BindingKey: "page-two"}); err != nil {
		t.Fatalf("active page mutation rejected: %v", err)
	}
	if err := validateFilterCommandPageScope(definition, "two", dashboardfilter.Command{Kind: dashboardfilter.CommandMutate, BindingKey: "report"}); err != nil {
		t.Fatalf("report mutation rejected: %v", err)
	}
	if err := validateFilterCommandPageScope(definition, "two", dashboardfilter.Command{Kind: dashboardfilter.CommandReset, ResetScope: dashboardfilter.ResetPage, BindingKeys: []string{"page-one"}}); err == nil {
		t.Fatal("off-page reset was accepted")
	}
	if err := validateFilterCommandPageScope(definition, "two", dashboardfilter.Command{Kind: dashboardfilter.CommandReset, ResetScope: dashboardfilter.ResetDashboard, BindingKeys: []string{"page-one", "page-two"}}); err != nil {
		t.Fatalf("dashboard reset rejected: %v", err)
	}
}

func TestFilterCommandResponseTombstonesOmittedCanonicalURLParameters(t *testing.T) {
	definition := dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"status": {ValueKind: dashboardfilter.ValueString},
		},
		FilterBindings: map[string]dashboardfilter.Binding{
			"status": {
				Key: "fb_status", ID: "status", Filter: "status", Scope: dashboardfilter.ScopeReport,
				Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered},
				URL:     dashboardfilter.URLPolicy{Param: "order_status", Encoding: dashboardfilter.URLEncodingTypedV1},
			},
		},
	}
	state := definition.DefaultFilterState()

	response, err := filterCommandResponse(definition, "filters", state, "clear-status")
	if err != nil {
		t.Fatalf("filter command response: %v", err)
	}
	params, ok := response["urlParams"].(map[string]any)
	if !ok {
		t.Fatalf("urlParams = %#v, want object", response["urlParams"])
	}
	value, exists := params["order_status"]
	if !exists || value != nil {
		t.Fatalf("order_status = %#v (exists %t), want explicit null tombstone", value, exists)
	}
	validation, ok := response["filterValidation"].(map[string]any)
	if !ok || validation["clientMutationID"] != "clear-status" {
		t.Fatalf("filterValidation = %#v, want mutation acknowledgement", response["filterValidation"])
	}
}
