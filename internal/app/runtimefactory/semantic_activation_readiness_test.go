package runtimefactory

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestCandidatePlanRejectsProtectedSemanticActivationWithoutReadiness(t *testing.T) {
	protected := &semanticmodel.Model{AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
		"region": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}}
	registry := completeReadinessRegistry()

	tests := []struct {
		name      string
		artifacts release.CandidateArtifactSet
	}{
		{
			name: "manifest without registry",
			artifacts: release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{
				Manifest: projectmanifest.Project{SemanticModels: map[string]*semanticmodel.Model{"semantic:readiness": protected}},
			}},
		},
		{
			name: "manifest with registry",
			artifacts: release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{
				Manifest:         projectmanifest.Project{SemanticModels: map[string]*semanticmodel.Model{"semantic:readiness": protected}},
				SemanticRegistry: registry,
			}},
		},
		{
			name:      "immutable artifact projection",
			artifacts: release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Artifact: protectedSemanticReadinessArtifact(t)}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CandidatePlanRequestWithPolicyAndReuse(
				readinessPlanInput(), test.artifacts, "runtime:v1", CandidateDeliveryPolicy{}, time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC), nil,
			)
			if !errors.Is(err, ErrSemanticActivationNotReady) {
				t.Fatalf("error = %v, want ErrSemanticActivationNotReady", err)
			}
			if !strings.Contains(err.Error(), "exact current publication, registry, graph, and policy evidence") {
				t.Fatalf("error = %v, want bounded missing-evidence details", err)
			}
		})
	}
}

func TestCandidatePlanReadinessDoesNotHonorApprovalOrRegistryOverrides(t *testing.T) {
	artifact := protectedSemanticReadinessArtifact(t)
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{
		Artifact:         artifact,
		SemanticRegistry: completeReadinessRegistry(),
	}}
	_, err := CandidatePlanRequestWithPolicyAndReuse(readinessPlanInput(), artifacts, "runtime:v1", CandidateDeliveryPolicy{RequiresApproval: true}, time.Now().UTC(), nil)
	if !errors.Is(err, ErrSemanticActivationNotReady) {
		t.Fatalf("approval policy override error = %v, want ErrSemanticActivationNotReady", err)
	}
}

func TestCandidatePlanAcceptsExactExistingPublicationReference(t *testing.T) {
	artifact := protectedSemanticReadinessArtifact(t)
	reference := identityledger.PolicyActivationReference{
		Version:      identityledger.PolicyActivationReferenceVersion,
		Publication:  identityledger.PolicyPublicationIdentity{InstanceID: "instance-a", AuthoredID: "semantic:sales", ResourceKind: projectgraph.KindSemanticModel, Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: deliveryPlanDigest('b')},
		BaselineKind: identityledger.PolicyBaselineGenesis, LifecycleSequence: 1, ActiveBundleID: "generation-base",
		GraphDigest: artifact.Graph().Digest(), PolicyEvidenceVersion: identityledger.RegistryPolicyEvidenceVersion,
		PolicyEvidenceDigest: deliveryPlanDigest('c'), ApprovalState: identityledger.PolicyApprovalNotRequired,
		RegistryTypes: &contractversion.SemanticRegistryTypes{
			InstanceID: "instance-a", ProjectID: "project:readiness", ControlRevision: 1,
			Profile: semanticvalue.Profile, Revision: 1, Digest: deliveryPlanDigest('d'),
			Definitions: []contractversion.RegisteredSemanticType{{
				ID: "definition-region", Name: "region", Type: semanticvalue.TypeString,
				Shape: "scalar", Version: 1, Enabled: true,
			}},
		},
	}
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Manifest: artifact.Manifest(), Artifact: artifact, Graph: artifact.Graph()}, Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataRefreshSources, DataRevision: "sources:1"}}
	request, err := CandidatePlanRequestWithPolicyAndReuse(readinessPlanInput(), artifacts, "runtime:v1", CandidateDeliveryPolicy{ContractActivations: []identityledger.PolicyActivationReference{reference}}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("exact publication reference rejected: %v", err)
	}
	if len(request.Evidence.ContractActivations) != 1 || request.Evidence.ContractActivations[0].PolicyEvidenceDigest != reference.PolicyEvidenceDigest {
		t.Fatalf("contract activation evidence was not retained: %#v", request.Evidence.ContractActivations)
	}
}

func TestCandidatePlanPreservesUnprotectedSemanticPlanBehavior(t *testing.T) {
	artifact := dashboardPhysicalArtifact(t, "Unprotected", false)
	artifacts := release.CandidateArtifactSet{
		Compiler:   release.CandidateCompilerEvidence{Manifest: artifact.Manifest(), Artifact: artifact},
		Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataRefreshSources, DataRevision: "sources:1"},
	}
	request, err := CandidatePlanRequestWithPolicyAndReuse(readinessPlanInput(), artifacts, "runtime:v1", CandidateDeliveryPolicy{}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("unprotected candidate plan error = %v", err)
	}
	if request.Persist != true || request.Execution.RuntimeDigest == "" {
		t.Fatalf("unprotected candidate plan = %#v", request)
	}
}

func completeReadinessRegistry() *access.SemanticRegistryContext {
	return &access.SemanticRegistryContext{
		Control: access.AuthorizationControlRevision{InstanceID: "instance-a", ProjectID: "project:readiness", Revision: 1},
		Registry: access.SemanticAttributeRegistrySnapshot{
			State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: deliveryPlanDigest('a')},
			Definitions: []access.SemanticAttributeDefinition{{
				ID: "definition-region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
				Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
			}},
		},
	}
}

func readinessPlanInput() deployment.DeliveryCandidateBuildInput {
	return deployment.DeliveryCandidateBuildInput{
		ProjectID: projectgraph.ResourceID("project:readiness"), OwnerID: "owner-1", ArtifactDigest: deliveryPlanDigest('a'),
		Candidate: deployment.Candidate{ID: "candidate-readiness", TargetID: "target-prod", Scope: deployment.CandidateScope{ProjectID: projectgraph.ResourceID("project:readiness"), Environment: "prod"}},
	}
}

func protectedSemanticReadinessArtifact(t *testing.T) projectartifact.Project {
	t.Helper()
	base := dashboardPhysicalArtifact(t, "Protected", false)
	manifest := base.Manifest()
	model := manifest.SemanticModels["semantic:sales"]
	model.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{
		"region": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}
	manifest.SemanticModels["semantic:sales"] = model
	artifact, err := projectartifact.NewProject(base.Graph(), manifest)
	if err != nil {
		t.Fatalf("protected artifact: %v", err)
	}
	return artifact
}
