package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

var _ deploymentgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ deploymentgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherMapsCandidateSynchronizationAndIdempotency(t *testing.T) {
	handler := &recordingDeploymentHandler{}
	dispatcher := NewAPIGenDispatcher(handler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/p1/candidates", nil)

	dispatcher.PlanProjectCandidateSynchronization(recorder, request, "p1", deploymentgen.GenPlanProjectCandidateSynchronizationHeaders{
		IdempotencyKey: "plan-1",
	})
	if handler.operation != "sync-plan:p1" {
		t.Fatalf("sync plan mapping = %q", handler.operation)
	}
	dispatcher.UploadProjectCandidateSourceBlob(recorder, request, "p1", "sha256:blob", deploymentgen.GenUploadProjectCandidateSourceBlobHeaders{
		ContentType: "application/octet-stream", ContentDigest: "sha-256=:blob:", SourceSynchronizationPlan: "plan-1",
	})
	if handler.operation != "sync-upload:p1:sha256:blob" {
		t.Fatalf("sync upload mapping = %q", handler.operation)
	}
}

func TestAPIGenDispatcherMapsDeliveryCollectionParams(t *testing.T) {
	handler := &recordingDeliveryCollectionHandler{}
	dispatcher := NewAPIGenDispatcher(handler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/p1/publications", nil)
	limit := int32(17)
	token := "k1.cursor"

	dispatcher.ListDeliveryPublications(recorder, request, "p1", deploymentgen.GenListDeliveryPublicationsParams{
		Limit: &limit, PageToken: &token,
	})
	if handler.publicationProject != "p1" || handler.publicationLimit != &limit || handler.publicationToken != &token {
		t.Fatalf("publication collection mapping = project %q limit %v token %v", handler.publicationProject, handler.publicationLimit, handler.publicationToken)
	}

	dispatcher.ListRetainedDeliveryGenerations(recorder, request, "p1", deploymentgen.GenListRetainedDeliveryGenerationsParams{
		Limit: &limit, PageToken: &token,
	})
	if handler.generationProject != "p1" || handler.generationLimit != &limit || handler.generationToken != &token {
		t.Fatalf("generation collection mapping = project %q limit %v token %v", handler.generationProject, handler.generationLimit, handler.generationToken)
	}

	dispatcher.ListDeliveryPlans(recorder, request, "p1", deploymentgen.GenListDeliveryPlansParams{Limit: &limit, PageToken: &token})
	if handler.planProject != "p1" || handler.planLimit != &limit || handler.planToken != &token {
		t.Fatalf("plan collection mapping = project %q limit %v token %v", handler.planProject, handler.planLimit, handler.planToken)
	}

	dispatcher.ListDeliveryBuildAttempts(recorder, request, "p1", deploymentgen.GenListDeliveryBuildAttemptsParams{Limit: &limit, PageToken: &token})
	if handler.buildProject != "p1" || handler.buildLimit != &limit || handler.buildToken != &token {
		t.Fatalf("build collection mapping = project %q limit %v token %v", handler.buildProject, handler.buildLimit, handler.buildToken)
	}

	dispatcher.ListDeliveryCandidates(recorder, request, "p1", deploymentgen.GenListDeliveryCandidatesParams{Limit: &limit, PageToken: &token})
	if handler.candidateProject != "p1" || handler.candidateLimit != &limit || handler.candidateToken != &token {
		t.Fatalf("candidate collection mapping = project %q limit %v token %v", handler.candidateProject, handler.candidateLimit, handler.candidateToken)
	}

	dispatcher.ListDeliveryApprovalRequests(recorder, request, "p1", deploymentgen.GenListDeliveryApprovalRequestsParams{Limit: &limit, PageToken: &token})
	if handler.approvalProject != "p1" || handler.approvalLimit != &limit || handler.approvalToken != &token {
		t.Fatalf("approval collection mapping = project %q limit %v token %v", handler.approvalProject, handler.approvalLimit, handler.approvalToken)
	}
}

type recordingDeploymentHandler struct {
	idempotencyKey string
	operation      string
}

type recordingDeliveryCollectionHandler struct {
	recordingDeploymentHandler
	publicationProject string
	publicationLimit   *int32
	publicationToken   *string
	generationProject  string
	generationLimit    *int32
	generationToken    *string
	planProject        string
	planLimit          *int32
	planToken          *string
	buildProject       string
	buildLimit         *int32
	buildToken         *string
	candidateProject   string
	candidateLimit     *int32
	candidateToken     *string
	approvalProject    string
	approvalLimit      *int32
	approvalToken      *string
}

func (h *recordingDeliveryCollectionHandler) ListDeliveryPublications(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string, limit *int32, token *string) {
	h.publicationProject, h.publicationLimit, h.publicationToken = project, limit, token
}

func (h *recordingDeliveryCollectionHandler) ListRetainedDeliveryGenerations(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string, limit *int32, token *string) {
	h.generationProject, h.generationLimit, h.generationToken = project, limit, token
}

func (h *recordingDeliveryCollectionHandler) ListDeliveryPlans(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string, limit *int32, token *string) {
	h.planProject, h.planLimit, h.planToken = project, limit, token
}

func (h *recordingDeliveryCollectionHandler) ListDeliveryBuildAttempts(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string, limit *int32, token *string) {
	h.buildProject, h.buildLimit, h.buildToken = project, limit, token
}

func (h *recordingDeliveryCollectionHandler) ListDeliveryCandidates(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string, limit *int32, token *string) {
	h.candidateProject, h.candidateLimit, h.candidateToken = project, limit, token
}

func (h *recordingDeliveryCollectionHandler) ListDeliveryApprovalRequests(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string, limit *int32, token *string) {
	h.approvalProject, h.approvalLimit, h.approvalToken = project, limit, token
}

func (h *recordingDeploymentHandler) PlanProjectCandidateSynchronization(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, key string) {
	h.operation, h.idempotencyKey = "sync-plan:"+project, key
}
func (h *recordingDeploymentHandler) UploadProjectCandidateSourceBlob(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, digest, _, _, _ string) {
	h.operation = "sync-upload:" + project + ":" + digest
}
