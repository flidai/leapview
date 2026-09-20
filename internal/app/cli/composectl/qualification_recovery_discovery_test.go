package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/stretchr/testify/require"
)

func TestQualificationFreshOperatorStartsWithoutCheckpointFiles(t *testing.T) {
	home := t.TempDir()
	files, err := qualificationFreshOperatorCheckpointFiles(home)
	require.NoError(t, err)
	require.Empty(t, files)
	require.NoError(t, os.WriteFile(home+"/unexpected-checkpoint", []byte("checkpoint"), 0o600))
	files, err = qualificationFreshOperatorCheckpointFiles(home)
	require.NoError(t, err)
	require.Equal(t, []string{"unexpected-checkpoint"}, files)
}

func TestSelectQualificationRollbackGenerationUsesRetainedPriorOnly(t *testing.T) {
	activeID := "active-generation"
	generations := []deploymentgen.DeliveryGenerationStatusResponse{
		{Id: activeID, Status: deploymentgen.DeliveryGenerationStatusActive},
		{Id: "non-reversible", Status: deploymentgen.DeliveryGenerationStatusRetired, RollbackClass: deploymentgen.DeliveryRollbackClassNonReversible, RollbackUntil: qualificationString("2026-09-17T00:00:00Z")},
		{Id: "prior-generation", Status: deploymentgen.DeliveryGenerationStatusRetired, RollbackClass: deploymentgen.DeliveryRollbackClassRollbackSafe, RollbackUntil: qualificationString("2026-09-17T00:00:00Z")},
	}
	selected, err := selectQualificationRollbackGeneration(generations, activeID)
	require.NoError(t, err)
	require.Equal(t, "prior-generation", selected.Id)

	_, err = selectQualificationRollbackGeneration(generations[1:], activeID)
	require.ErrorContains(t, err, "absent from retained generation collection")

	generations[2].RollbackClass = deploymentgen.DeliveryRollbackClassServingSafe
	selected, err = selectQualificationRollbackGeneration(generations, activeID)
	require.NoError(t, err)
	require.Equal(t, "prior-generation", selected.Id)
}

func TestQualifyFreshOperatorEvidenceLinksBuildApprovalAndPublication(t *testing.T) {
	planID := "plan-1"
	planDigest := "sha256:plan"
	candidateID := "candidate-1"
	publicationID := "publication-1"
	targetID := "target-1"
	activeID := "generation-1"
	plan := deploymentgen.DeliveryPlanPreviewResponse{Id: planID, PlanDigest: planDigest, TargetId: targetID}
	candidate := QualificationCandidate{ID: candidateID, PlanID: planID, PlanDigest: planDigest, TargetID: targetID}
	publication := QualificationPublication{DeploymentID: publicationID}
	evidence := qualificationFreshOperatorRecoveryEvidence{
		Plans: []deploymentgen.DeliveryPlanPreviewResponse{plan},
		Candidates: []deploymentgen.DeliveryCandidateStatusResponse{{
			Id: candidateID, PlanId: planID, Status: deploymentgen.DeliveryCandidateStatusReady,
		}},
		Builds: []deploymentgen.DeliveryBuildStatusResponse{{
			CandidateId: &candidateID, PlanId: planID, Status: deploymentgen.DeliveryBuildStatusSealed,
		}},
		Publications: []deploymentgen.DeliveryPublicationEvidenceResponse{{
			Id: publicationID, GenerationId: activeID, Status: deploymentgen.DeliveryPublicationStatusCommitted,
		}},
		Approvals: []deploymentgen.DeploymentApprovalResponse{{
			DeploymentId: publicationID, Status: deploymentgen.DeploymentApprovalStatusApproved,
		}},
	}
	require.NoError(t, qualifyFreshOperatorEvidenceLinks(evidence, candidate, publication, activeID))

	evidence.Builds[0].Status = deploymentgen.DeliveryBuildStatusFailed
	require.ErrorContains(t, qualifyFreshOperatorEvidenceLinks(evidence, candidate, publication, activeID), "sealed build evidence")
}

func TestRunQualificationFreshOperatorDeliveryRecoveryDrillsCollectionsAndRollback(t *testing.T) {
	const (
		projectID          = "project-1"
		targetID           = "target-1"
		activeGenerationID = "generation-active"
		priorGenerationID  = "generation-prior"
		planID             = "plan-1"
		planDigest         = "sha256:plan"
		candidateID        = "candidate-active"
		publicationID      = "publication-active"
		rollbackID         = "publication-rollback"
	)
	var mu sync.Mutex
	operatorCalls := 0
	paths := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths[request.URL.Path]++
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/api/v1/projects/"+projectID+"/delivery/publications/"+rollbackID && request.Header.Get("Authorization") != "Bearer read-token" && request.Method != http.MethodPost {
			t.Fatalf("read request authorization = %q for %s", request.Header.Get("Authorization"), request.URL.Path)
		}
		switch {
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/operator":
			mu.Lock()
			operatorCalls++
			active := activeGenerationID
			if operatorCalls > 1 {
				active = priorGenerationID
			}
			mu.Unlock()
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryOperatorSnapshotResponse{
				ProjectId: projectID, TargetId: targetID, Environment: "evaluation",
				ActiveGeneration: &active,
			})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/plans":
			assertQualificationCollectionLimit(t, request)
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryPlanListResponse{Items: []deploymentgen.DeliveryPlanPreviewResponse{{Id: planID, PlanDigest: planDigest, TargetId: targetID}}})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/builds":
			assertQualificationCollectionLimit(t, request)
			candidate := candidateID
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryBuildAttemptListResponse{Items: []deploymentgen.DeliveryBuildStatusResponse{{CandidateId: &candidate, PlanId: planID, Status: deploymentgen.DeliveryBuildStatusSealed}}})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/candidates":
			assertQualificationCollectionLimit(t, request)
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryCandidateListResponse{Items: []deploymentgen.DeliveryCandidateStatusResponse{{Id: candidateID, PlanId: planID, Status: deploymentgen.DeliveryCandidateStatusReady}}})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/approval-requests":
			assertQualificationCollectionLimit(t, request)
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryApprovalRequestListResponse{Items: []deploymentgen.DeploymentApprovalResponse{{DeploymentId: publicationID, Status: deploymentgen.DeploymentApprovalStatusApproved}}})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/publications":
			assertQualificationCollectionLimit(t, request)
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryPublicationListResponse{Items: []deploymentgen.DeliveryPublicationEvidenceResponse{{Id: publicationID, GenerationId: activeGenerationID, Status: deploymentgen.DeliveryPublicationStatusCommitted}}})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/generations":
			assertQualificationCollectionLimit(t, request)
			rollbackUntil := "2026-09-17T00:00:00Z"
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryRetainedGenerationListResponse{Items: []deploymentgen.DeliveryGenerationStatusResponse{
				{Id: activeGenerationID, Status: deploymentgen.DeliveryGenerationStatusActive},
				{Id: priorGenerationID, Status: deploymentgen.DeliveryGenerationStatusRetired, RollbackClass: deploymentgen.DeliveryRollbackClassRollbackSafe, RollbackUntil: &rollbackUntil},
			}})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/generations/"+priorGenerationID+"/rollback":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer control-token" || request.Header.Get("Idempotency-Key") == "" {
				t.Fatalf("rollback request = method=%s authorization=%q idempotency=%q", request.Method, request.Header.Get("Authorization"), request.Header.Get("Idempotency-Key"))
			}
			writer.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryPublicationEvidenceResponse{
				Id: rollbackID, TargetId: targetID, GenerationId: priorGenerationID,
				CandidateId: "candidate-prior", PlanId: "plan-prior", PlanDigest: "sha256:prior",
				Status: deploymentgen.DeliveryPublicationStatusPending,
			})
		case request.URL.Path == "/api/v1/projects/"+projectID+"/delivery/publications/"+rollbackID:
			if request.Header.Get("Authorization") != "Bearer control-token" {
				t.Fatalf("rollback publication authorization = %q", request.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(writer).Encode(deploymentgen.DeliveryPublicationEvidenceResponse{
				Id: rollbackID, TargetId: targetID, GenerationId: priorGenerationID,
				CandidateId: "candidate-prior", PlanId: "plan-prior", PlanDigest: "sha256:prior",
				Status: deploymentgen.DeliveryPublicationStatusCommitted,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	evidence, err := runQualificationFreshOperatorDeliveryRecovery(
		t.Context(), server.Client(), server.URL,
		qualificationRecoveryOptions{
			ProjectID: projectID, Target: server.URL,
			ProjectDataToken: "read-token", RecoveryControlToken: "control-token",
		},
		QualificationCandidate{ID: candidateID, PlanID: planID, PlanDigest: planDigest, TargetID: targetID},
		QualificationPublication{DeploymentID: publicationID},
		t.TempDir(),
	)
	require.NoError(t, err)
	require.Empty(t, evidence.CheckpointFiles)
	require.Equal(t, priorGenerationID, evidence.SelectedGenerationID)
	require.Equal(t, priorGenerationID, evidence.RollbackPublication.GenerationId)
	require.Equal(t, deploymentgen.DeliveryPublicationStatusCommitted, evidence.RollbackPublication.Status)
	require.Equal(t, priorGenerationID, *evidence.PostRollbackOperator.ActiveGeneration)
	require.GreaterOrEqual(t, operatorCalls, 2)
	for _, suffix := range []string{"/delivery/plans", "/delivery/builds", "/delivery/candidates", "/delivery/approval-requests", "/delivery/publications", "/delivery/generations"} {
		require.Equal(t, 1, paths["/api/v1/projects/"+projectID+suffix], suffix)
	}
}

func assertQualificationCollectionLimit(t *testing.T, request *http.Request) {
	t.Helper()
	if request.URL.Query().Get("limit") != "100" || request.URL.Query().Get("pageToken") != "" {
		t.Fatalf("collection query = %s", request.URL.RawQuery)
	}
	if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		t.Fatalf("collection request is unauthenticated")
	}
}

func qualificationString(value string) *string {
	return &value
}
