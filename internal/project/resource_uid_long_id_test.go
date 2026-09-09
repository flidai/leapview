package project

import (
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestResourceUIDBindingAcceptsLongAuthoredID(t *testing.T) {
	authoredID := strings.Repeat("a", 300)
	instanceID := "lvinst_" + strings.Repeat("a", 32)
	generationID := "11111111-1111-4111-8111-111111111111"
	binding := ResourceUIDBinding{
		ResourceUID:    ResourceUID("22222222-2222-4222-8222-222222222222"),
		InstanceID:     instanceID,
		ProjectID:      "project",
		Environment:    "production",
		TargetID:       instanceID,
		GenerationID:   generationID,
		AuthoredID:     authoredID,
		Kind:           projectgraph.KindConnection,
		ContractStatus: "not_contract_bearing",
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("long authored ID rejected: %v", err)
	}
}

func TestResourceUIDRecordAcceptsLongAuthoredID(t *testing.T) {
	authoredID := strings.Repeat("b", 300)
	record := ResourceUIDRecord{
		UID:               ResourceUID("33333333-3333-4333-8333-333333333333"),
		InstanceID:        "lvinst_" + strings.Repeat("b", 32),
		ProjectID:         "project",
		AuthoredID:        authoredID,
		Kind:              projectgraph.KindModel,
		State:             ResourceUIDActive,
		FirstGeneration:   "44444444-4444-4444-8444-444444444444",
		LatestGeneration:  "44444444-4444-4444-8444-444444444444",
		CurrentGeneration: "44444444-4444-4444-8444-444444444444",
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("long authored ID rejected: %v", err)
	}
}
