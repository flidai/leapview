package app

import (
	"context"
	"errors"
	"testing"
	"time"

	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/google/uuid"
)

type committedCredentialGenerationStub struct {
	evidence appdeploymentpostgres.CommittedGenerationEvidence
	err      error
	calls    int
	target   string
	identity projectgraph.ServingIdentity
}

func (s *committedCredentialGenerationStub) CommittedGeneration(_ context.Context, target string, identity projectgraph.ServingIdentity) (appdeploymentpostgres.CommittedGenerationEvidence, error) {
	s.calls++
	s.target, s.identity = target, identity
	return s.evidence, s.err
}

func activeCredentialPinFixture(t *testing.T) (activeConnectionEvidenceSource, release.Provenance, *committedCredentialGenerationStub) {
	t.Helper()
	identity := projectgraph.ServingIdentity{ProjectID: "project_credential", Environment: "prod", GenerationID: uuid.NewString()}
	bindings := []release.BindingEvidence{{
		BindingID: "binding_warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 2,
		CredentialVersionID: uuid.NewString(), EndpointConfigHash: activeResultIdentityDigest('e'),
	}}
	candidate := release.CandidateProvenance{ID: uuid.NewString(), Revision: 4, OwnerID: "builder"}
	artifact := release.ProjectArtifactProvenance{
		SourceDigest: activeResultIdentityDigest('a'), ProjectDigest: activeResultIdentityDigest('b'),
		ContentDigest: activeResultIdentityDigest('c'), CompilerVersion: "compiler:v1", SchemaVersion: 1,
	}
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: candidate.ID, SourceDigest: artifact.SourceDigest,
		BindingGeneration: release.BindingFingerprint(bindings), RuntimeVersion: "runtime:v1", DuckDBVersion: "duckdb:test",
		Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
		Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 1, MaxMillis: 1000},
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := release.NewProvenance(release.ProvenanceInput{
		Artifact: artifact, Candidate: candidate,
		Plan: release.GenerationPlanProvenance{
			Identity: identity, TargetID: "instance", RuntimeVersion: "runtime:v1",
			PolicyDigest: activeResultIdentityDigest('d'), PolicyRevision: 1, AuthorizationDigest: activeResultIdentityDigest('f'),
			DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Bindings: bindings, GateEvidence: &gate,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitments := &committedCredentialGenerationStub{evidence: appdeploymentpostgres.CommittedGenerationEvidence{
		TargetID: "instance", Identity: identity, PublicationID: uuid.NewString(), SnapshotSealID: uuid.NewString(),
		CandidateID: candidate.ID, CandidateRevision: candidate.Revision,
		ServingArtifactDigest: artifact.ContentDigest, BindingFingerprint: release.BindingFingerprint(bindings),
	}}
	return activeConnectionEvidenceSource{
		releases: sourceSchemaProvenanceStub{provenance: provenance}, commitments: commitments,
		targetID: "instance", environment: "prod",
	}, provenance, commitments
}

func TestActiveCredentialPinRequiresCommittedPublicationEvidence(t *testing.T) {
	for _, mode := range []string{"missing authority", "candidate only", "ready release only"} {
		t.Run(mode, func(t *testing.T) {
			source, provenance, commitments := activeCredentialPinFixture(t)
			if mode == "missing authority" {
				source.commitments = nil
			} else {
				commitments.err = errors.New("generation has no committed publication")
			}
			if _, err := source.BindingEvidence(t.Context(), provenance.Plan.Identity.GenerationID, provenance.Plan.Identity.ProjectID.String()); err == nil {
				t.Fatal("uncommitted local credential pin reached serving evidence")
			}
			if _, err := source.ResultIdentityEvidence(t.Context(), provenance.Plan.Identity); err == nil {
				t.Fatal("uncommitted local credential pin reached result identity")
			}
		})
	}
}

func TestActiveCredentialPinPreservesExactCommittedGeneration(t *testing.T) {
	source, provenance, commitments := activeCredentialPinFixture(t)
	got, err := source.BindingEvidence(t.Context(), provenance.Plan.Identity.GenerationID, provenance.Plan.Identity.ProjectID.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CredentialVersionID != provenance.Plan.Bindings[0].CredentialVersionID || got[0].ValidatedVersion != "" {
		t.Fatal("serving evidence lost or reinterpreted the local credential version")
	}
	if commitments.calls != 1 || commitments.target != source.targetID || commitments.identity != provenance.Plan.Identity {
		t.Fatal("commitment lookup did not use the exact serving scope")
	}
	identity, err := source.ResultIdentityEvidence(t.Context(), provenance.Plan.Identity)
	if err != nil || identity.BindingFingerprint != commitments.evidence.BindingFingerprint {
		t.Fatalf("committed binding fingerprint changed: %v", err)
	}
}

func TestActiveCredentialPinRejectsSubstitutedCommitEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*appdeploymentpostgres.CommittedGenerationEvidence)
	}{
		{"target", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.TargetID = "foreign" }},
		{"generation", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.Identity.GenerationID = uuid.NewString() }},
		{"project", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.Identity.ProjectID = "foreign" }},
		{"environment", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.Identity.Environment = "foreign" }},
		{"candidate", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.CandidateID = uuid.NewString() }},
		{"candidate revision", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.CandidateRevision++ }},
		{"artifact", func(e *appdeploymentpostgres.CommittedGenerationEvidence) {
			e.ServingArtifactDigest = activeResultIdentityDigest('d')
		}},
		{"binding pin", func(e *appdeploymentpostgres.CommittedGenerationEvidence) {
			e.BindingFingerprint = activeResultIdentityDigest('d')
		}},
		{"publication absent", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.PublicationID = "" }},
		{"seal absent", func(e *appdeploymentpostgres.CommittedGenerationEvidence) { e.SnapshotSealID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, provenance, commitments := activeCredentialPinFixture(t)
			test.mutate(&commitments.evidence)
			if _, err := source.BindingEvidence(t.Context(), provenance.Plan.Identity.GenerationID, provenance.Plan.Identity.ProjectID.String()); err == nil {
				t.Fatal("substituted committed generation evidence was accepted")
			}
		})
	}
}

func TestActiveCredentialPinRejectsTamperedProvenanceBeforeCommitLookup(t *testing.T) {
	source, provenance, commitments := activeCredentialPinFixture(t)
	provenance.Plan.Bindings[0].CredentialVersionID = uuid.NewString()
	source.releases = sourceSchemaProvenanceStub{provenance: provenance}
	if _, err := source.BindingEvidence(t.Context(), provenance.Plan.Identity.GenerationID, provenance.Plan.Identity.ProjectID.String()); err == nil {
		t.Fatal("tampered credential pin was accepted")
	}
	if commitments.calls != 0 {
		t.Fatal("invalid provenance reached committed generation lookup")
	}
}
