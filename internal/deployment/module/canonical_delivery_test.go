package module

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestEffectiveCandidateArtifactsRefreshesExecutionContextMismatch(t *testing.T) {
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	identity, err := projectgraph.NewServingIdentity("project:demo", "dev", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	baseGate := &release.GateEvidence{Digest: "sha256:base-gate"}
	inspected := release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{SourceDigest: artifactDigest},
		Generation: release.CandidateGenerationArtifact{
			Identity: identity, DataMode: release.GenerationDataReuseBase, DataRevision: "snapshot:1",
			ManagedDataPins: []release.ManagedDataPin{{ConnectionID: "orders", RevisionID: "revision-1"}}, BaseGateEvidence: baseGate,
		},
	}
	plan := deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate-1", Reason: "execution context identity changed"}}}}
	effective, err := EffectiveCandidateArtifacts(plan, "candidate-1", inspected)
	if err != nil {
		t.Fatal(err)
	}
	wantRevision, err := release.CandidateSourcesDataRevision(artifactDigest, inspected.Generation.ManagedDataPins)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Generation.DataMode != release.GenerationDataRefreshSources || effective.Generation.DataRevision != wantRevision || effective.Generation.BaseGateEvidence != nil {
		t.Fatalf("effective artifact = %#v, want refresh revision %q without base gates", effective.Generation, wantRevision)
	}
}

func TestEffectiveCandidateArtifactsRefreshesRetainedBasePartialReuse(t *testing.T) {
	artifactDigest := "sha256:" + strings.Repeat("b", 64)
	inspected := release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{SourceDigest: artifactDigest},
		Generation: release.CandidateGenerationArtifact{
			DataMode: release.GenerationDataReuseBase, DataRevision: "snapshot:2",
			ManagedDataPins:  []release.ManagedDataPin{{ConnectionID: "customers", RevisionID: "revision-2"}},
			BaseGateEvidence: &release.GateEvidence{Digest: "sha256:base-gate"},
		},
	}
	plan := deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{
		{ResourceID: "model:customers", Reusable: true},
		{ResourceID: "model:orders", RetainBase: true, Reason: "pipeline scope requires refresh"},
	}}}
	effective, err := EffectiveCandidateArtifacts(plan, "candidate-1", inspected)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Generation.DataMode != release.GenerationDataRefreshSources || effective.Generation.BaseGateEvidence != nil {
		t.Fatalf("retained-base partial reuse remained %#v", effective.Generation)
	}
	wantRevision, err := release.CandidateSourcesDataRevision(artifactDigest, inspected.Generation.ManagedDataPins)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Generation.DataRevision != wantRevision {
		t.Fatalf("effective revision = %q, want %q", effective.Generation.DataRevision, wantRevision)
	}
}
