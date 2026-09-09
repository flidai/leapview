package app

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/flidai/leapview/internal/release"
)

type rejectingLatestLifecycleReader struct {
	err error
}

func TestLegacyPlanWithoutReferencesCannotActivateProtectedArtifact(t *testing.T) {
	protected := protectedContractActivationArtifact(t)
	compiled := projectbundle.CompiledProjectArtifact{Manifest: protected.Manifest()}
	if err := validateCanonicalContractArtifact(compiled, deployment.DeliveryPlan{}); !errors.Is(err, ErrIdentityLifecycleUnavailable) {
		t.Fatalf("legacy protected plan error=%v, want ErrIdentityLifecycleUnavailable", err)
	}
	exact := deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{
		ContractActivations: []identityledger.PolicyActivationReference{{Version: identityledger.PolicyActivationReferenceVersion}},
	}}
	if err := validateCanonicalContractArtifact(compiled, exact); err != nil {
		t.Fatalf("canonical protected plan rejected before exact fence: %v", err)
	}
	if err := validateUnsupportedContractArtifact(compiled, exact, "asynchronous sealed publication"); !errors.Is(err, ErrIdentityLifecycleUnavailable) {
		t.Fatalf("legacy exact-reference plan error=%v, want canonical-path rejection", err)
	}

	unprotected := materializationDeltaFixture(t)
	unprotectedCompiled := projectbundle.CompiledProjectArtifact{Manifest: unprotected.Manifest()}
	if err := validateUnsupportedContractArtifact(unprotectedCompiled, deployment.DeliveryPlan{}, "asynchronous sealed publication"); err != nil {
		t.Fatalf("unprotected legacy plan rejected: %v", err)
	}
	if err := validateCanonicalContractArtifact(unprotectedCompiled, exact); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("unprotected plan with contract references error=%v, want policy conflict", err)
	}
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
