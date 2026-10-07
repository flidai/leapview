package module

import (
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func TestCandidateReleaseProvenanceRetainsLocalCredentialVersionPin(t *testing.T) {
	const versionID = "0198f2c0-7c7a-7f00-8a11-000000000301"
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	sourceDigest := digest('a')
	identity := projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "prod", GenerationID: "generation_1"}
	binding := deployment.CandidateConnectionEvidence{
		BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7,
		CredentialVersionID: versionID, EndpointConfigHash: digest('e'),
	}
	provenanceBinding := release.BindingEvidence{
		BindingID: binding.BindingID, ConnectionID: binding.ConnectionID.String(), ConnectorKind: binding.ConnectorKind,
		Revision: binding.Revision, CredentialVersionID: versionID, EndpointConfigHash: binding.EndpointConfigHash,
	}
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: "candidate_1", SourceDigest: sourceDigest,
		BindingGeneration: release.BindingFingerprint([]release.BindingEvidence{provenanceBinding}),
		RuntimeVersion:    "runtime:v1", DuckDBVersion: "duckdb:v1", Outcome: release.GateSuccess,
		EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		Bounds:      release.GateBounds{MaxRows: 10, MaxQueries: 2, MaxMillis: 100},
	}).Canonical()
	if err != nil {
		t.Fatalf("canonicalize candidate gate evidence: %v", err)
	}

	provenance, err := candidateReleaseProvenance(
		deployment.Candidate{
			ID: "candidate_1", Revision: 1, OwnerID: "owner_1", TargetID: "target_1",
			ArtifactDigest: sourceDigest,
			Scope:          deployment.CandidateScope{ProjectID: "project_1", Environment: "prod"},
		},
		release.CandidateArtifactSet{
			Artifact: release.ProjectArtifactProvenance{
				SourceDigest: sourceDigest, ProjectDigest: digest('b'), ContentDigest: digest('c'),
				CompilerVersion: "compiler:v1", SchemaVersion: 1,
			},
			AuthorizationPolicyRevision: 1, AuthorizationPolicyDigest: digest('d'), AuthorizationFingerprint: digest('f'),
			Generation: release.CandidateGenerationArtifact{
				Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources,
			},
		},
		deployment.CandidateRuntimeReceipt{RuntimeVersion: "runtime:v1", Bindings: []deployment.CandidateConnectionEvidence{binding}, GateEvidence: &gate},
		nil,
	)
	if err != nil {
		t.Fatalf("build candidate release provenance: %v", err)
	}
	if provenance.Plan.Bindings[0].CredentialVersionID != versionID {
		t.Fatalf("credential version pin=%q, want %q", provenance.Plan.Bindings[0].CredentialVersionID, versionID)
	}
}
