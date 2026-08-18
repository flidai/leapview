package release

import (
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestProvenanceBindsExactGenerationAndBaseIdentity(t *testing.T) {
	sha := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_0")
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProvenance(ProvenanceInput{
		Artifact:  ProjectArtifactProvenance{SourceDigest: sha('a'), ProjectDigest: sha('b'), ContentDigest: sha('c'), CompilerVersion: "compiler", SchemaVersion: 1},
		Candidate: CandidateProvenance{ID: "candidate_1", Revision: 1, OwnerID: "principal_1"},
		Plan:      GenerationPlanProvenance{Identity: identity, BaseIdentity: &base, TargetID: "target_1", RuntimeVersion: "runtime", PolicyDigest: sha('d'), DataRevision: "snapshot:1", DataMode: GenerationDataReuseBase},
	})
	if err != nil {
		t.Fatalf("new provenance: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("validate provenance: %v", err)
	}
	p.Plan.BaseIdentity.GenerationID = ""
	if err := p.Validate(); err == nil {
		t.Fatal("partial base identity accepted")
	}
	if p, err = NewProvenance(ProvenanceInput{
		Artifact:  ProjectArtifactProvenance{SourceDigest: sha('a'), ProjectDigest: sha('b'), ContentDigest: sha('c'), CompilerVersion: "compiler", SchemaVersion: 1},
		Candidate: CandidateProvenance{ID: "candidate_1", Revision: 1, OwnerID: "principal_1"},
		Plan:      GenerationPlanProvenance{Identity: identity, BaseIdentity: &projectgraph.ServingIdentity{ProjectID: "other_project", Environment: "prod", GenerationID: "generation_0"}, TargetID: "target_1", RuntimeVersion: "runtime", PolicyDigest: sha('d'), DataRevision: "snapshot:1", DataMode: GenerationDataReuseBase},
	}); err == nil {
		t.Fatal("cross-project base identity accepted")
	}
	nonCanonical := identity
	nonCanonical.GenerationID = " generation_1"
	if p, err = NewProvenance(ProvenanceInput{
		Artifact:  ProjectArtifactProvenance{SourceDigest: sha('a'), ProjectDigest: sha('b'), ContentDigest: sha('c'), CompilerVersion: "compiler", SchemaVersion: 1},
		Candidate: CandidateProvenance{ID: "candidate_1", Revision: 1, OwnerID: "principal_1"},
		Plan:      GenerationPlanProvenance{Identity: nonCanonical, BaseIdentity: &base, TargetID: "target_1", RuntimeVersion: "runtime", PolicyDigest: sha('d'), DataRevision: "snapshot:1", DataMode: GenerationDataReuseBase},
	}); err == nil {
		t.Fatal("noncanonical generation identity accepted")
	}
}

func TestProvenanceAllowsInitialGenerationWithoutBaseIdentity(t *testing.T) {
	sha := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_initial")
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProvenance(ProvenanceInput{
		Artifact:  ProjectArtifactProvenance{SourceDigest: sha('a'), ProjectDigest: sha('b'), ContentDigest: sha('c'), CompilerVersion: "compiler", SchemaVersion: 1},
		Candidate: CandidateProvenance{ID: "candidate_initial", Revision: 1, OwnerID: "principal_1"},
		Plan:      GenerationPlanProvenance{Identity: identity, TargetID: "target_initial", RuntimeVersion: "runtime", PolicyDigest: sha('d'), DataRevision: "snapshot:1", DataMode: GenerationDataReuseBase},
	})
	if err != nil {
		t.Fatalf("initial generation provenance: %v", err)
	}
	if p.Plan.BaseIdentity != nil {
		t.Fatal("initial generation unexpectedly acquired a base identity")
	}
}
