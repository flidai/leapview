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

type recordingDeploymentHandler struct {
	idempotencyKey string
	operation      string
}

func (h *recordingDeploymentHandler) PlanProjectCandidateSynchronization(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, key string) {
	h.operation, h.idempotencyKey = "sync-plan:"+project, key
}
func (h *recordingDeploymentHandler) UploadProjectCandidateSourceBlob(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, digest, _, _, _ string) {
	h.operation = "sync-upload:" + project + ":" + digest
}
