package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

var _ deploymentgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ deploymentgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherMapsDeploymentCreateIdempotency(t *testing.T) {
	handler := &recordingDeploymentHandler{}
	NewAPIGenDispatcher(handler).CreateDeployment(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/p1/deployments", nil),
		"p1",
		deploymentgen.GenCreateDeploymentHeaders{IdempotencyKey: "request-1"},
	)
	if got, want := handler.idempotencyKey, "request-1"; got != want {
		t.Fatalf("idempotency key = %q, want %q", got, want)
	}
}

func TestAPIGenDispatcherMapsCandidateOperationsAndIdempotency(t *testing.T) {
	handler := &recordingDeploymentHandler{}
	dispatcher := NewAPIGenDispatcher(handler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/p1/candidates", nil)

	dispatcher.StartProjectCandidate(recorder, request, "p1", deploymentgen.GenStartProjectCandidateHeaders{IdempotencyKey: "start-1"})
	if handler.operation != "start:p1" || handler.idempotencyKey != "start-1" {
		t.Fatalf("start mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
	dispatcher.GetProjectCandidate(recorder, request, "p1", "cand_1")
	if handler.operation != "get:p1:cand_1" {
		t.Fatalf("get mapping = %q", handler.operation)
	}
	dispatcher.ReplaceProjectCandidateArtifact(recorder, request, "p1", "cand_1", deploymentgen.GenReplaceProjectCandidateArtifactHeaders{IdempotencyKey: "replace-1"})
	if handler.operation != "replace:p1:cand_1" || handler.idempotencyKey != "replace-1" {
		t.Fatalf("replace mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
	dispatcher.RetryProjectCandidate(recorder, request, "p1", "cand_1", deploymentgen.GenRetryProjectCandidateHeaders{IdempotencyKey: "retry-1"})
	if handler.operation != "retry:p1:cand_1" || handler.idempotencyKey != "retry-1" {
		t.Fatalf("retry mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
	dispatcher.CancelProjectCandidate(recorder, request, "p1", "cand_1", deploymentgen.GenCancelProjectCandidateHeaders{IdempotencyKey: "cancel-1"})
	if handler.operation != "cancel:p1:cand_1" || handler.idempotencyKey != "cancel-1" {
		t.Fatalf("cancel mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
	dispatcher.CancelProjectCandidateByKey(
		recorder,
		request,
		"p1",
		"github:pull/42",
		deploymentgen.GenCancelProjectCandidateByKeyHeaders{
			IdempotencyKey: "cancel-key-1",
		},
	)
	if handler.operation != "cancel-key:p1:github:pull/42" ||
		handler.idempotencyKey != "cancel-key-1" {
		t.Fatalf("cancel-by-key mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
	dispatcher.PublishProjectCandidate(recorder, request, "p1", "cand_1", deploymentgen.GenPublishProjectCandidateHeaders{IdempotencyKey: "publish-1"})
	if handler.operation != "publish:p1:cand_1" || handler.idempotencyKey != "publish-1" {
		t.Fatalf("publish mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
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
	dispatcher.CommitProjectCandidateSynchronization(recorder, request, "p1", deploymentgen.GenCommitProjectCandidateSynchronizationHeaders{
		IdempotencyKey: "sync-1", SourceSynchronizationPlan: "plan-1",
	})
	if handler.operation != "sync-commit:p1" || handler.idempotencyKey != "sync-1" {
		t.Fatalf("sync commit mapping = operation:%q key:%q", handler.operation, handler.idempotencyKey)
	}
}

type recordingDeploymentHandler struct {
	idempotencyKey string
	operation      string
}

func (h *recordingDeploymentHandler) StartProjectCandidate(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, key string) {
	h.operation, h.idempotencyKey = "start:"+project, key
}
func (h *recordingDeploymentHandler) GetProjectCandidate(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidate string) {
	h.operation = "get:" + project + ":" + candidate
}
func (h *recordingDeploymentHandler) ReplaceProjectCandidateArtifact(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidate, key string) {
	h.operation, h.idempotencyKey = "replace:"+project+":"+candidate, key
}
func (h *recordingDeploymentHandler) RetryProjectCandidate(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidate, key string) {
	h.operation, h.idempotencyKey = "retry:"+project+":"+candidate, key
}
func (h *recordingDeploymentHandler) CancelProjectCandidate(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidate, key string) {
	h.operation, h.idempotencyKey = "cancel:"+project+":"+candidate, key
}
func (h *recordingDeploymentHandler) CancelProjectCandidateByKey(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidateKey, key string) {
	h.operation, h.idempotencyKey = "cancel-key:"+project+":"+candidateKey, key
}
func (h *recordingDeploymentHandler) PublishProjectCandidate(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidate, key string) {
	h.operation, h.idempotencyKey = "publish:"+project+":"+candidate, key
}
func (h *recordingDeploymentHandler) ReviewProjectCandidate(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, candidate string) {
	h.operation = "review:" + project + ":" + candidate
}
func (h *recordingDeploymentHandler) PlanProjectCandidateSynchronization(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, key string) {
	h.operation, h.idempotencyKey = "sync-plan:"+project, key
}
func (h *recordingDeploymentHandler) UploadProjectCandidateSourceBlob(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, digest, _, _, _ string) {
	h.operation = "sync-upload:" + project + ":" + digest
}
func (h *recordingDeploymentHandler) CommitProjectCandidateSynchronization(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, key, _ string) {
	h.operation, h.idempotencyKey = "sync-commit:"+project, key
}

func (*recordingDeploymentHandler) ListDeployments(stdhttp.ResponseWriter, *stdhttp.Request, string, *int32, *string) {
}
func (h *recordingDeploymentHandler) CreateDeployment(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _ string, key string) {
	h.idempotencyKey = key
}
func (*recordingDeploymentHandler) GetDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string) {
}
func (*recordingDeploymentHandler) CancelDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string) {
}
func (*recordingDeploymentHandler) RetryDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string) {
}
func (*recordingDeploymentHandler) ListDeploymentEvents(stdhttp.ResponseWriter, *stdhttp.Request, string, string, *int32, *string) {
}
func (*recordingDeploymentHandler) RollbackDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string) {
}
func (*recordingDeploymentHandler) RequestDeploymentApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string) {
}
func (*recordingDeploymentHandler) ApproveDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string) {
}
func (*recordingDeploymentHandler) DenyDeploymentApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string) {
}
func (*recordingDeploymentHandler) RevokeDeploymentApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string) {
}
func (*recordingDeploymentHandler) ActivateDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string) {
}
