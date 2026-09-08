package compiler

import (
	"reflect"
	"strings"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestSemanticModelLoweringSupportsFilterVariants(t *testing.T) {
	path := []string{"customer"}
	aiContext := &projectcontracts.AIContext{Instructions: stringPtr("filter context")}
	leafCases := []struct {
		name     string
		value    projectcontracts.SemanticFilter
		operator string
		literal  any
	}{
		{name: "equals", value: projectcontracts.SemanticFilter{Value: &projectcontracts.EqualsSemanticFilter{Field: "orders.status", Operator: "equals", Value: "open", Path: &path, AiContext: aiContext}}, operator: "equals", literal: "open"},
		{name: "not_equals", value: projectcontracts.SemanticFilter{Value: &projectcontracts.NotEqualsSemanticFilter{Field: "orders.status", Operator: "not_equals", Value: "closed"}}, operator: "not_equals", literal: "closed"},
		{name: "in", value: projectcontracts.SemanticFilter{Value: &projectcontracts.InSemanticFilter{Field: "orders.status", Operator: "in", Value: []any{"open", "pending"}}}, operator: "in", literal: []any{"open", "pending"}},
		{name: "not_in", value: projectcontracts.SemanticFilter{Value: &projectcontracts.NotInSemanticFilter{Field: "orders.status", Operator: "not_in", Value: []any{"cancelled"}}}, operator: "not_in", literal: []any{"cancelled"}},
		{name: "less_than", value: projectcontracts.SemanticFilter{Value: &projectcontracts.LessThanSemanticFilter{Field: "orders.amount", Operator: "less_than", Value: 10}}, operator: "less_than", literal: 10},
		{name: "less_than_or_equal", value: projectcontracts.SemanticFilter{Value: &projectcontracts.LessThanOrEqualSemanticFilter{Field: "orders.amount", Operator: "less_than_or_equal", Value: 10}}, operator: "less_than_or_equal", literal: 10},
		{name: "greater_than", value: projectcontracts.SemanticFilter{Value: &projectcontracts.GreaterThanSemanticFilter{Field: "orders.amount", Operator: "greater_than", Value: 10}}, operator: "greater_than", literal: 10},
		{name: "greater_than_or_equal", value: projectcontracts.SemanticFilter{Value: &projectcontracts.GreaterThanOrEqualSemanticFilter{Field: "orders.amount", Operator: "greater_than_or_equal", Value: 10}}, operator: "greater_than_or_equal", literal: 10},
		{name: "is_null", value: projectcontracts.SemanticFilter{Value: &projectcontracts.IsNullSemanticFilter{Field: "orders.deleted_at", Operator: "is_null"}}, operator: "is_null"},
		{name: "is_not_null", value: projectcontracts.SemanticFilter{Value: &projectcontracts.IsNotNullSemanticFilter{Field: "orders.deleted_at", Operator: "is_not_null"}}, operator: "is_not_null"},
	}
	for _, tc := range leafCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lowerSemanticFilter(tc.value)
			if err != nil {
				t.Fatalf("lower filter: %v", err)
			}
			if got.Field == "" && tc.operator != "" {
				t.Fatalf("lowered filter = %#v", got)
			}
			if got.Operator != tc.operator || !reflect.DeepEqual(got.Value, tc.literal) {
				t.Fatalf("lowered filter = %#v, want operator %q and value %#v", got, tc.operator, tc.literal)
			}
		})
	}

	all, err := lowerSemanticFilter(projectcontracts.SemanticFilter{Value: &projectcontracts.AllSemanticFilter{All: []projectcontracts.SemanticFilter{leafCases[0].value, leafCases[1].value}}})
	if err != nil || len(all.All) != 2 {
		t.Fatalf("lower all filter = %#v, err=%v", all, err)
	}
	anyFilter, err := lowerSemanticFilter(projectcontracts.SemanticFilter{Value: &projectcontracts.AnySemanticFilter{Any: []projectcontracts.SemanticFilter{leafCases[2].value}}})
	if err != nil || len(anyFilter.Any) != 1 {
		t.Fatalf("lower any filter = %#v, err=%v", anyFilter, err)
	}
	not, err := lowerSemanticFilter(projectcontracts.SemanticFilter{Value: &projectcontracts.NotSemanticFilter{Not: leafCases[3].value}})
	if err != nil || not.Not == nil || not.Not.Operator != "not_in" {
		t.Fatalf("lower not filter = %#v, err=%v", not, err)
	}
	if !reflect.DeepEqual(all.All[0].Path, path) || all.All[0].AIContext == nil {
		t.Fatalf("lowered leaf metadata = %#v", all.All[0])
	}

	_, err = lowerSemanticFilter(projectcontracts.SemanticFilter{Value: &projectcontracts.AllSemanticFilter{All: []projectcontracts.SemanticFilter{{}}}})
	if err == nil || !strings.Contains(err.Error(), "child 0: filter variant is required") {
		t.Fatalf("invalid child filter error = %v", err)
	}
	_, err = lowerSemanticFilter(projectcontracts.SemanticFilter{})
	if err == nil || !strings.Contains(err.Error(), "filter variant is required") {
		t.Fatalf("missing filter variant error = %v", err)
	}
}

func TestSemanticModelLoweringRelationshipEndpointValidation(t *testing.T) {
	named := projectcontracts.SemanticRelationshipEndpoint{Value: &projectcontracts.NamedSemanticRelationshipEndpoint{Dataset: "orders", Entity: "customer"}}
	fields := projectcontracts.SemanticRelationshipEndpoint{Value: &projectcontracts.FieldsSemanticRelationshipEndpoint{Dataset: "customers", Fields: []string{"id"}}}
	for _, tc := range []struct {
		name  string
		value projectcontracts.SemanticRelationshipEndpoint
	}{
		{name: "named", value: named},
		{name: "fields", value: fields},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := lowerSemanticRelationshipEndpoint(tc.value); err != nil {
				t.Fatalf("lower endpoint: %v", err)
			}
		})
	}
	if _, err := lowerSemanticRelationshipEndpoint(projectcontracts.SemanticRelationshipEndpoint{}); err == nil || !strings.Contains(err.Error(), "endpoint variant is required") {
		t.Fatalf("missing endpoint variant error = %v", err)
	}

	fromError := map[string]projectcontracts.SemanticRelationship{
		"broken_from": {From: projectcontracts.SemanticRelationshipEndpoint{}, To: named},
	}
	if _, err := lowerSemanticRelationships(&fromError); err == nil || !strings.Contains(err.Error(), `relationship "broken_from" from`) {
		t.Fatalf("relationship from error = %v", err)
	}
	toError := map[string]projectcontracts.SemanticRelationship{
		"broken_to": {From: named, To: projectcontracts.SemanticRelationshipEndpoint{}},
	}
	if _, err := lowerSemanticRelationships(&toError); err == nil || !strings.Contains(err.Error(), `relationship "broken_to" to`) {
		t.Fatalf("relationship to error = %v", err)
	}
}

func TestSemanticModelLoweringRetainsEveryAccessPolicyBoundary(t *testing.T) {
	required := []string{"can_view"}
	grants := map[string]projectcontracts.SemanticAccessGrant{
		"can_view": {UserAttribute: "department", AllowedValues: projectcontracts.SemanticAllowedValues{"sales"}},
	}
	dimensions := map[string]projectcontracts.SemanticDimension{"region": {RequiredAccessGrants: &required}}
	spec := projectcontracts.SemanticModelSpec{
		AccessGrants: &grants,
		Datasets: map[string]projectcontracts.SemanticDataset{"orders": {
			RequiredAccessGrants: &required,
			AccessFilters:        &[]projectcontracts.SemanticAccessFilter{{Field: "region", UserAttribute: "allowedRegions"}},
		}},
		Dimensions: &dimensions,
		Metrics: map[string]projectcontracts.SemanticMetric{
			"revenue":     {Value: &projectcontracts.SemanticMetricAggregateVariant{AggregateSemanticMetric: projectcontracts.AggregateSemanticMetric{RequiredAccessGrants: &required}}},
			"margin":      {Value: &projectcontracts.SemanticMetricDerivedVariant{DerivedSemanticMetric: projectcontracts.DerivedSemanticMetric{RequiredAccessGrants: &required}}},
			"margin_rate": {Value: &projectcontracts.SemanticMetricRatioVariant{RatioSemanticMetric: projectcontracts.RatioSemanticMetric{RequiredAccessGrants: &required}}},
		},
	}
	policy, err := lowerSemanticAccessPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	if policy.AccessGrants["can_view"].UserAttribute != "department" || len(policy.AccessGrants["can_view"].AllowedValues) != 1 {
		t.Fatalf("grant lowering = %#v", policy.AccessGrants)
	}
	if len(policy.Datasets["orders"].RequiredAccessGrants) != 1 || len(policy.Datasets["orders"].AccessFilters) != 1 || len(policy.Dimensions["region"]) != 1 {
		t.Fatalf("dataset/dimension policy lowering = %#v", policy)
	}
	for _, name := range []string{"revenue", "margin", "margin_rate"} {
		if !reflect.DeepEqual(policy.Metrics[name], required) {
			t.Fatalf("metric %q grants = %#v", name, policy.Metrics[name])
		}
	}
}

func TestSemanticModelAccessPolicyDiagnosticsAreDeterministic(t *testing.T) {
	required := []string{"missing"}
	spec := projectcontracts.SemanticModelSpec{Datasets: map[string]projectcontracts.SemanticDataset{
		"zeta": {RequiredAccessGrants: &required}, "alpha": {RequiredAccessGrants: &required},
	}}
	want := `SemanticModel dataset "alpha" references unknown access grant "missing"`
	for run := 0; run < 100; run++ {
		_, err := lowerSemanticAccessPolicy(spec)
		if err == nil || err.Error() != want {
			t.Fatalf("run %d diagnostic = %v, want %q", run, err, want)
		}
	}
}

func TestSemanticModelAccessPolicyRejectsEmptyAndDuplicateRequirements(t *testing.T) {
	empty := []string{}
	if _, err := lowerSemanticAccessPolicy(projectcontracts.SemanticModelSpec{Datasets: map[string]projectcontracts.SemanticDataset{"orders": {RequiredAccessGrants: &empty}}}); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty required grants error = %v", err)
	}
	duplicate := []string{"can_view", "can_view"}
	grants := map[string]projectcontracts.SemanticAccessGrant{"can_view": {UserAttribute: "department", AllowedValues: projectcontracts.SemanticAllowedValues{"sales"}}}
	if _, err := lowerSemanticAccessPolicy(projectcontracts.SemanticModelSpec{AccessGrants: &grants, Datasets: map[string]projectcontracts.SemanticDataset{"orders": {RequiredAccessGrants: &duplicate}}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate required grants error = %v", err)
	}
}

func stringPtr(value string) *string { return &value }
