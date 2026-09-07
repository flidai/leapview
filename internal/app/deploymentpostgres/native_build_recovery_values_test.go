package deploymentpostgres

import (
	"strings"
	"testing"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDeriveNativeBuildRecoveryArtifactValues(t *testing.T) {
	request := deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID:   projectgraph.ResourceID("project-recovery-values"),
		Environment: "prod",
	}
	plan := nativeBuildPlan{
		DeliveryPlan:   deploymentdomain.DeliveryPlan{Digest: recoveryValueDigest('a'), SourceDigest: recoveryValueDigest('b')},
		ArtifactDigest: recoveryValueDigest('c'),
	}
	prepared := NativeBuildRecoveryPreparationResult{
		Operation:       deploymentmodule.NativeOperationRecord{OperationID: "0198f2c0-7c7a-7f00-8a11-000000001001"},
		DeliveryAttempt: deploymentnative.DeliveryBuildAttempt{FencingEpoch: 17},
		CandidateID:     "0198f2c0-7c7a-7f00-8a11-000000001002",
		GenerationID:    "0198f2c0-7c7a-7f00-8a11-000000001003",
		AttemptID:       "0198f2c0-7c7a-7f00-8a11-000000001004",
	}

	t.Run("synthesizes absent immutable binding", func(t *testing.T) {
		artifactRequest, binding, marker, err := deriveNativeBuildRecoveryArtifactValues(request, recoveryValueDigest('d'), plan, prepared, "pool-recovery")
		if err != nil {
			t.Fatal(err)
		}
		wantArtifactID := "artifact-" + strings.TrimPrefix(plan.ArtifactDigest, "sha256:")
		if artifactRequest.CandidateID != prepared.CandidateID || artifactRequest.ServingIdentity.ProjectID != request.ProjectID || artifactRequest.ServingIdentity.Environment != request.Environment || artifactRequest.ServingIdentity.GenerationID != prepared.GenerationID || artifactRequest.SourceDigest != plan.SourceDigest {
			t.Fatalf("artifact recovery request = %#v", artifactRequest)
		}
		if artifactRequest.Artifact.ServingArtifactID != wantArtifactID || artifactRequest.Artifact.ServingArtifactDigest != plan.ArtifactDigest || artifactRequest.Artifact.ServingStateID != prepared.GenerationID {
			t.Fatalf("synthesized artifact identity = %#v", artifactRequest.Artifact)
		}
		if binding.AttemptID != prepared.AttemptID || binding.ServingArtifactID != wantArtifactID || binding.ServingArtifactDigest != plan.ArtifactDigest || binding.ServingStateID != prepared.GenerationID || !binding.BoundAt.IsZero() {
			t.Fatalf("synthesized binding = %#v", binding)
		}
		if marker.LeaseEpoch != 17 || marker.FencingToken != "17" || marker.DeliveryID != prepared.Operation.OperationID || marker.GenerationID != prepared.GenerationID || marker.AttemptID != prepared.AttemptID || marker.PhysicalPoolID != "pool-recovery" {
			t.Fatalf("recovery marker = %#v", marker)
		}
	})

	t.Run("preserves existing immutable binding", func(t *testing.T) {
		bound := prepared
		bound.Artifact = deploymentnative.BuildArtifactBinding{
			AttemptID:             prepared.AttemptID,
			ServingArtifactID:     "artifact-existing",
			ServingArtifactDigest: plan.ArtifactDigest,
			ServingStateID:        prepared.GenerationID,
		}
		artifactRequest, binding, marker, err := deriveNativeBuildRecoveryArtifactValues(request, recoveryValueDigest('d'), plan, bound, "pool-recovery")
		if err != nil {
			t.Fatal(err)
		}
		if artifactRequest.Artifact.ServingArtifactID != bound.Artifact.ServingArtifactID || artifactRequest.Artifact.ServingArtifactDigest != bound.Artifact.ServingArtifactDigest || artifactRequest.Artifact.ServingStateID != bound.Artifact.ServingStateID {
			t.Fatalf("existing artifact identity = %#v", artifactRequest.Artifact)
		}
		if binding.AttemptID != prepared.AttemptID || binding.ServingArtifactID != bound.Artifact.ServingArtifactID || binding.ServingArtifactDigest != bound.Artifact.ServingArtifactDigest || binding.ServingStateID != bound.Artifact.ServingStateID {
			t.Fatalf("existing binding = %#v", binding)
		}
		if marker.LeaseEpoch != bound.DeliveryAttempt.FencingEpoch || marker.FencingToken != "17" {
			t.Fatalf("existing-binding marker = %#v", marker)
		}
	})
}

func recoveryValueDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}
