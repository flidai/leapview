package model

import (
	"encoding/json"
	"testing"
)

func TestSemanticAccessPolicyArtifactRoundTripPreservesExactNumbers(t *testing.T) {
	literal, err := NewSemanticAccessLiteral(json.Number("9007199254740993.1250"))
	if err != nil {
		t.Fatal(err)
	}
	model := Model{Name: "sales", AccessPolicy: SemanticAccessPolicy{AccessGrants: map[string]SemanticAccessGrantSpec{
		"exact": {UserAttribute: "limit", AllowedValues: []SemanticAccessLiteral{literal}},
	}}}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var restored Model
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	value, err := restored.AccessPolicy.AccessGrants["exact"].AllowedValues[0].Value()
	if err != nil {
		t.Fatal(err)
	}
	if value != json.Number("9007199254740993.1250") {
		t.Fatalf("restored exact number = %v (%T)", value, value)
	}
}

func TestExecutionSnapshotDetachesSemanticAccessPolicy(t *testing.T) {
	model := Model{Name: "sales", AccessPolicy: SemanticAccessPolicy{
		AccessGrants: map[string]SemanticAccessGrantSpec{"view": {UserAttribute: "department", AllowedValues: []SemanticAccessLiteral{{Kind: SemanticAccessString, Text: "sales"}}}},
		Datasets:     map[string]SemanticDatasetAccessSpec{"orders": {RequiredAccessGrants: []string{"view"}, AccessFilters: []SemanticAccessFilterSpec{{Field: "region", UserAttribute: "regions"}}}},
		Dimensions:   map[string][]string{"region": {"view"}}, Metrics: map[string][]string{"revenue": {"view"}},
	}}
	snapshot := model.ExecutionSnapshot()
	grant := model.AccessPolicy.AccessGrants["view"]
	grant.AllowedValues[0].Text = "engineering"
	model.AccessPolicy.AccessGrants["view"] = grant
	dataset := model.AccessPolicy.Datasets["orders"]
	dataset.RequiredAccessGrants[0] = "other"
	dataset.AccessFilters[0].Field = "account"
	model.AccessPolicy.Datasets["orders"] = dataset
	model.AccessPolicy.Dimensions["region"][0] = "other"
	model.AccessPolicy.Metrics["revenue"][0] = "other"

	if got := snapshot.AccessPolicy.AccessGrants["view"].AllowedValues[0].Text; got != "sales" {
		t.Fatalf("snapshot grant value = %q", got)
	}
	if got := snapshot.AccessPolicy.Datasets["orders"]; got.RequiredAccessGrants[0] != "view" || got.AccessFilters[0].Field != "region" {
		t.Fatalf("snapshot dataset policy mutated: %#v", got)
	}
	if snapshot.AccessPolicy.Dimensions["region"][0] != "view" || snapshot.AccessPolicy.Metrics["revenue"][0] != "view" {
		t.Fatalf("snapshot member policy mutated: %#v", snapshot.AccessPolicy)
	}
}
