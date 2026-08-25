package app

import (
	"context"
	"errors"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

type sourceSchemaProvenanceStub struct {
	provenance releasemodule.Provenance
	err        error
}

func (stub sourceSchemaProvenanceStub) ProvenanceForServingState(context.Context, projectgraph.ServingIdentity) (releasemodule.Provenance, error) {
	return stub.provenance, stub.err
}

func TestActiveSourceSchemaEvidenceUsesExactServingProvenance(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project:test", "dev", "state:active")
	if err != nil {
		t.Fatal(err)
	}
	evaluatedAt := time.Date(2026, 8, 24, 7, 30, 0, 0, time.UTC)
	evidence := &release.GateEvidence{EvaluatedAt: evaluatedAt, Sources: []release.GateSourceEvidence{{
		ID: "source:orders", Mode: "compatible", SchemaOutcome: release.GateSuccess, SchemaDigest: "sha256:observed",
		ObservedSchema: []semanticmodel.ColumnSchema{{Name: "order_id", Ordinal: 0, PhysicalType: "VARCHAR"}},
	}}}
	reader := activeSourceSchemaEvidenceSource{
		targetID: "instance:test",
		releases: sourceSchemaProvenanceStub{provenance: release.Provenance{Plan: release.GenerationPlanProvenance{
			Identity: identity, TargetID: "instance:test", GateEvidence: evidence,
		}}},
	}
	observation, found, err := reader.SourceSchemaObservation(t.Context(), identity.ProjectID, identity.Environment, identity.GenerationID, "source:orders")
	if err != nil {
		t.Fatal(err)
	}
	if !found || observation.Mode != "compatible" || observation.Status != "success" || observation.ObservedAt != evaluatedAt || len(observation.Schema.Columns) != 1 {
		t.Fatalf("source observation = %#v, found = %v", observation, found)
	}
	observation.Schema.Columns[0].Name = "mutated"
	if evidence.Sources[0].ObservedSchema[0].Name != "order_id" {
		t.Fatal("source observation did not detach persisted schema evidence")
	}
}

func TestActiveSourceSchemaEvidenceRejectsAnotherTarget(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project:test", "dev", "state:active")
	if err != nil {
		t.Fatal(err)
	}
	reader := activeSourceSchemaEvidenceSource{
		targetID: "instance:test",
		releases: sourceSchemaProvenanceStub{provenance: release.Provenance{Plan: release.GenerationPlanProvenance{
			Identity: identity, TargetID: "instance:other", GateEvidence: &release.GateEvidence{},
		}}},
	}
	_, _, err = reader.SourceSchemaObservation(t.Context(), identity.ProjectID, identity.Environment, identity.GenerationID, "source:orders")
	if !errors.Is(err, releasemodule.ErrProvenanceInvalid) {
		t.Fatalf("target mismatch error = %v, want provenance invalid", err)
	}
}
