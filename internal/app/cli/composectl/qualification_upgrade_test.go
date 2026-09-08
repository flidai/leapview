package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

func TestQualificationDeliveryPersistenceEvidenceBindsCandidateGenerationAndSeal(t *testing.T) {
	const (
		candidateID  = "candidate-1"
		generationID = "generation-1"
		sealID       = "seal-1"
		planID       = "plan-1"
		planDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	candidate, generation := qualificationDeliveryEvidenceFixture(candidateID, generationID, sealID, planID, planDigest)
	server := qualificationDeliveryEvidenceServer(t, candidate, generation)
	defer server.Close()
	evidence, err := (&Controller{}).qualificationDeliveryPersistenceEvidence(
		t.Context(), server.URL, "project-1", candidateID, generationID, "token",
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CandidateID != candidateID || evidence.GenerationID != generationID || evidence.SnapshotSealID != sealID || evidence.PlanID != planID {
		t.Fatalf("delivery persistence evidence = %#v", evidence)
	}
}

func TestQualificationDeliveryPersistenceEvidenceDiagnosesMismatchedFieldsWithoutValues(t *testing.T) {
	const (
		candidateID  = "candidate-1"
		generationID = "generation-1"
		sealID       = "seal-1"
		planID       = "plan-1"
		planDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		secret       = "qualification-secret-value"
	)
	mutations := []struct {
		name   string
		field  string
		mutate func(*deploymentgen.DeliveryCandidateStatusResponse, *deploymentgen.DeliveryGenerationStatusResponse)
	}{
		{name: "candidate id", field: "candidate.id", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.Id = secret
		}},
		{name: "candidate status", field: "candidate.status", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.Status = deploymentgen.DeliveryCandidateStatusFailed
		}},
		{name: "candidate snapshot seal", field: "candidate.snapshotSealId", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.SnapshotSealId = nil
		}},
		{name: "serving state", field: "candidate.servingStateId", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.ServingStateId = secret
		}},
		{name: "plan id", field: "candidate.planId", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.PlanId = secret
		}},
		{name: "plan digest", field: "candidate.planDigest", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.PlanDigest = secret
		}},
		{name: "target id", field: "candidate.targetId", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.TargetId = secret
		}},
		{name: "physical pool", field: "candidate.physicalPoolId", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.PhysicalPoolId = secret
		}},
		{name: "closure digest", field: "candidate.closureDigest", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.ClosureDigest = strptr(secret)
		}},
		{name: "compatibility digest", field: "candidate.compatibilityDigest", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.CompatibilityDigest = secret
		}},
		{name: "serving artifact id", field: "candidate.servingArtifactId", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.ServingArtifactId = secret
		}},
		{name: "serving artifact digest", field: "candidate.servingArtifactDigest", mutate: func(candidate *deploymentgen.DeliveryCandidateStatusResponse, _ *deploymentgen.DeliveryGenerationStatusResponse) {
			candidate.ServingArtifactDigest = secret
		}},
		{name: "generation candidate", field: "generation.candidateId", mutate: func(_ *deploymentgen.DeliveryCandidateStatusResponse, generation *deploymentgen.DeliveryGenerationStatusResponse) {
			generation.CandidateId = secret
		}},
		{name: "generation id", field: "generation.id", mutate: func(_ *deploymentgen.DeliveryCandidateStatusResponse, generation *deploymentgen.DeliveryGenerationStatusResponse) {
			generation.Id = secret
		}},
		{name: "generation status", field: "generation.status", mutate: func(_ *deploymentgen.DeliveryCandidateStatusResponse, generation *deploymentgen.DeliveryGenerationStatusResponse) {
			generation.Status = deploymentgen.DeliveryGenerationStatusRetired
		}},
		{name: "generation serving state", field: "generation.servingStateId", mutate: func(_ *deploymentgen.DeliveryCandidateStatusResponse, generation *deploymentgen.DeliveryGenerationStatusResponse) {
			generation.ServingStateId = secret
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate, generation := qualificationDeliveryEvidenceFixture(candidateID, generationID, sealID, planID, planDigest)
			test.mutate(&candidate, &generation)
			server := qualificationDeliveryEvidenceServer(t, candidate, generation)
			defer server.Close()

			_, err := (&Controller{}).qualificationDeliveryPersistenceEvidence(
				t.Context(), server.URL, "project-1", candidateID, generationID, "token",
			)
			if err == nil {
				t.Fatal("mismatched delivery evidence unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("error = %q, want field %q", err, test.field)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "token") {
				t.Fatalf("error exposed a secret value: %q", err)
			}
		})
	}
}

func qualificationDeliveryEvidenceFixture(
	candidateID, generationID, sealID, planID, planDigest string,
) (deploymentgen.DeliveryCandidateStatusResponse, deploymentgen.DeliveryGenerationStatusResponse) {
	return deploymentgen.DeliveryCandidateStatusResponse{
			Id: candidateID, Status: deploymentgen.DeliveryCandidateStatusReady, SnapshotSealId: &sealID,
			ServingStateId: generationID, PlanId: planID, PlanDigest: planDigest,
			TargetId: "target-1", PhysicalPoolId: "pool-1", DucklakeSnapshotId: int64ptr(42), RelationManifestDigest: strptr("relation-1"), ClosureDigest: strptr("closure-1"),
			CompatibilityDigest: "compat-1", ServingArtifactId: "artifact-1", ServingArtifactDigest: "artifact-digest",
		}, deploymentgen.DeliveryGenerationStatusResponse{
			Id: generationID, CandidateId: candidateID, Status: deploymentgen.DeliveryGenerationStatusActive,
			ServingStateId: generationID, PlanId: planID, PlanDigest: planDigest,
			TargetId: "target-1", PhysicalPoolId: "pool-1", SnapshotSealId: sealID, DucklakeSnapshotId: 42, RelationManifestDigest: "relation-1", ClosureDigest: "closure-1",
			CompatibilityDigest: "compat-1", ServingArtifactId: "artifact-1", ServingArtifactDigest: "artifact-digest",
		}
}

func strptr(value string) *string { return &value }

func int64ptr(value int64) *int64 { return &value }

func qualificationDeliveryEvidenceServer(
	t *testing.T,
	candidate deploymentgen.DeliveryCandidateStatusResponse,
	generation deploymentgen.DeliveryGenerationStatusResponse,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch {
		case strings.Contains(r.URL.Path, "/candidates/"):
			response = candidate
		case strings.Contains(r.URL.Path, "/generations/"):
			response = generation
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode delivery evidence: %v", err)
		}
	}))
}
