package app

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/flidai/leapview/internal/release"
)

type rejectingLatestLifecycleReader struct {
	err error
}

func (r rejectingLatestLifecycleReader) ReadLatestLifecycleEvidence(context.Context, string, projectgraph.ResourceID, projectgraph.Kind) (identityledger.LifecycleEvidence, error) {
	return identityledger.LifecycleEvidence{}, r.err
}

func TestResolveContractActivationReferencesRejectsChangedOrFirstProtectedModel(t *testing.T) {
	base := protectedContractActivationArtifact(t)
	manifest := base.Manifest()
	changed := manifest.SemanticModels["semantic:sales"]
	changed.AccessGrants["region"] = semanticmodel.SemanticAccessGrantSpec{UserAttribute: "region", AllowedValues: []any{"eu"}}
	manifest.SemanticModels["semantic:sales"] = changed
	candidate, err := projectartifact.NewProject(base.Graph(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	readerErr := errors.New("publication reader reached")
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{
		Graph: candidate.Graph(), Manifest: candidate.Manifest(), Artifact: candidate, BaseArtifact: base,
	}}
	if _, err := resolveContractActivationReferences(t.Context(), rejectingLatestLifecycleReader{err: readerErr}, "instance-a", "generation-base", artifacts); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) || errors.Is(err, readerErr) {
		t.Fatalf("changed protected model error=%v, want pre-publication fail-closed conflict", err)
	}

	artifacts.Compiler.Graph = base.Graph()
	artifacts.Compiler.Manifest = base.Manifest()
	artifacts.Compiler.Artifact = base
	if _, err := resolveContractActivationReferences(t.Context(), rejectingLatestLifecycleReader{err: readerErr}, "instance-a", "generation-base", artifacts); !errors.Is(err, readerErr) {
		t.Fatalf("unchanged protected model error=%v, want existing publication authority read", err)
	}

	artifacts.Compiler.BaseArtifact = projectartifact.Project{}
	if _, err := resolveContractActivationReferences(t.Context(), rejectingLatestLifecycleReader{err: readerErr}, "instance-a", "", artifacts); !errors.Is(err, ErrIdentityLifecycleUnavailable) {
		t.Fatalf("first protected activation error=%v, want ErrIdentityLifecycleUnavailable", err)
	}
}

func protectedContractActivationArtifact(t *testing.T) projectartifact.Project {
	t.Helper()
	base := materializationDeltaFixture(t)
	manifest := base.Manifest()
	model := manifest.SemanticModels["semantic:sales"]
	model.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{
		"region": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}
	manifest.SemanticModels["semantic:sales"] = model
	artifact, err := projectartifact.NewProject(base.Graph(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
