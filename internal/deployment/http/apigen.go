package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type APIGenHandler interface {
	UploadProjectCandidateSourceBlob(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string, string)
	CommitProjectCandidateSynchronization(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	PlanProjectCandidateSynchronization(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	StartProjectCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetProjectCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ReplaceProjectCandidateArtifact(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	RetryProjectCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	CancelProjectCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	CancelProjectCandidateByKey(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	PublishProjectCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ReviewProjectCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ListDeployments(stdhttp.ResponseWriter, *stdhttp.Request, string, *int32, *string)
	CreateDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	CancelDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	RetryDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ListDeploymentEvents(stdhttp.ResponseWriter, *stdhttp.Request, string, string, *int32, *string)
	RollbackDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	RequestDeploymentApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ApproveDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	DenyDeploymentApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	RevokeDeploymentApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	ActivateDeployment(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
}

// DeliveryAPIGenHandler is optional to preserve the small legacy test and
// embedding surface of APIGenHandler while generated delivery operations are
// rolled out. Production deployment modules implement both interfaces.
type DeliveryAPIGenHandler interface {
	RetainProjectCandidateSource(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	CreateDeliveryPlan(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	BuildDeliveryPlan(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	PublishDeliveryCandidate(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	RollbackDeliveryGeneration(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	GetDeliveryPlanPreview(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDeliveryBuildStatus(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDeliverySealStatus(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDeliveryCandidateStatus(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDeliveryGenerationStatus(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDeliveryPublicationEvidence(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	RequestDeliveryPublicationApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	GetDeliveryPublicationApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ApproveDeliveryPublicationApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	DenyDeliveryPublicationApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	RevokeDeliveryPublicationApproval(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	GetDeliveryOperatorSnapshot(stdhttp.ResponseWriter, *stdhttp.Request, string)
}

func (d *APIGenDispatcher) RetainProjectCandidateSource(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenRetainProjectCandidateSourceHeaders) {
	if handler, ok := d.handler.(interface {
		RetainProjectCandidateSource(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	}); ok {
		handler.RetainProjectCandidateSource(w, r, project, headers.IdempotencyKey, headers.SourceSynchronizationPlan)
		return
	}
	apitransport.WriteProblem(w, r, stdhttp.StatusServiceUnavailable, "CANDIDATE_SERVICE_UNAVAILABLE", "Candidate source retention is unavailable", nil)
}

func (d *APIGenDispatcher) UploadProjectCandidateSourceBlob(w stdhttp.ResponseWriter, r *stdhttp.Request, project, digest string, headers deploymentgen.GenUploadProjectCandidateSourceBlobHeaders) {
	d.handler.UploadProjectCandidateSourceBlob(w, r, project, digest, headers.ContentType, headers.ContentDigest, headers.SourceSynchronizationPlan)
}

func (d *APIGenDispatcher) CommitProjectCandidateSynchronization(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenCommitProjectCandidateSynchronizationHeaders) {
	d.handler.CommitProjectCandidateSynchronization(w, r, project, headers.IdempotencyKey, headers.SourceSynchronizationPlan)
}

func (d *APIGenDispatcher) PlanProjectCandidateSynchronization(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenPlanProjectCandidateSynchronizationHeaders) {
	d.handler.PlanProjectCandidateSynchronization(w, r, project, headers.IdempotencyKey)
}

type APIGenDispatcher struct{ handler APIGenHandler }

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) StartProjectCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenStartProjectCandidateHeaders) {
	d.handler.StartProjectCandidate(w, r, project, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) GetProjectCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string) {
	d.handler.GetProjectCandidate(w, r, project, candidate)
}

func (d *APIGenDispatcher) ReplaceProjectCandidateArtifact(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string, headers deploymentgen.GenReplaceProjectCandidateArtifactHeaders) {
	d.handler.ReplaceProjectCandidateArtifact(w, r, project, candidate, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) RetryProjectCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string, headers deploymentgen.GenRetryProjectCandidateHeaders) {
	d.handler.RetryProjectCandidate(w, r, project, candidate, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) CancelProjectCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string, headers deploymentgen.GenCancelProjectCandidateHeaders) {
	d.handler.CancelProjectCandidate(w, r, project, candidate, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) CancelProjectCandidateByKey(
	w stdhttp.ResponseWriter,
	r *stdhttp.Request,
	project,
	candidateKey string,
	headers deploymentgen.GenCancelProjectCandidateByKeyHeaders,
) {
	d.handler.CancelProjectCandidateByKey(
		w,
		r,
		project,
		candidateKey,
		headers.IdempotencyKey,
	)
}

func (d *APIGenDispatcher) PublishProjectCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string, headers deploymentgen.GenPublishProjectCandidateHeaders) {
	d.handler.PublishProjectCandidate(w, r, project, candidate, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) ReviewProjectCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string) {
	d.handler.ReviewProjectCandidate(w, r, project, candidate)
}

func (d *APIGenDispatcher) ListDeployments(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, params deploymentgen.GenListDeploymentsParams) {
	d.handler.ListDeployments(w, r, project, params.Limit, params.PageToken)
}

func (d *APIGenDispatcher) CreateDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenCreateDeploymentHeaders) {
	d.handler.CreateDeployment(w, r, project, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) GetDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string) {
	d.handler.GetDeployment(w, r, project, deployment)
}

func (d *APIGenDispatcher) CancelDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string, _ deploymentgen.GenCancelDeploymentHeaders) {
	d.handler.CancelDeployment(w, r, project, deployment)
}

func (d *APIGenDispatcher) RetryDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string, headers deploymentgen.GenRetryDeploymentHeaders) {
	d.handler.RetryDeployment(w, r, project, deployment, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) ListDeploymentEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string, params deploymentgen.GenListDeploymentEventsParams, _ deploymentgen.GenListDeploymentEventsHeaders) {
	d.handler.ListDeploymentEvents(w, r, project, deployment, params.Limit, params.PageToken)
}

func (d *APIGenDispatcher) RollbackDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string, headers deploymentgen.GenRollbackDeploymentHeaders) {
	d.handler.RollbackDeployment(w, r, project, deployment, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) RequestDeploymentApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string, headers deploymentgen.GenRequestDeploymentApprovalHeaders) {
	d.handler.RequestDeploymentApproval(w, r, project, deployment, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) ApproveDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment, approval string, headers deploymentgen.GenApproveDeploymentHeaders) {
	d.handler.ApproveDeployment(w, r, project, deployment, approval, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) DenyDeploymentApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment, approval string, headers deploymentgen.GenDenyDeploymentApprovalHeaders) {
	d.handler.DenyDeploymentApproval(w, r, project, deployment, approval, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) RevokeDeploymentApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment, approval string, headers deploymentgen.GenRevokeDeploymentApprovalHeaders) {
	d.handler.RevokeDeploymentApproval(w, r, project, deployment, approval, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) ActivateDeployment(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deployment string, headers deploymentgen.GenActivateDeploymentHeaders) {
	d.handler.ActivateDeployment(w, r, project, deployment, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) GetDeliveryPlanPreview(w stdhttp.ResponseWriter, r *stdhttp.Request, project, plan string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryPlanPreview(w, r, project, plan)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) CreateDeliveryPlan(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenCreateDeliveryPlanHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.CreateDeliveryPlan(w, r, project, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) BuildDeliveryPlan(w stdhttp.ResponseWriter, r *stdhttp.Request, project, plan string, headers deploymentgen.GenBuildDeliveryPlanHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.BuildDeliveryPlan(w, r, project, plan, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) PublishDeliveryCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string, headers deploymentgen.GenPublishDeliveryCandidateHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.PublishDeliveryCandidate(w, r, project, candidate, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) RollbackDeliveryGeneration(w stdhttp.ResponseWriter, r *stdhttp.Request, project, generation string, headers deploymentgen.GenRollbackDeliveryGenerationHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.RollbackDeliveryGeneration(w, r, project, generation, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliveryBuildStatus(w stdhttp.ResponseWriter, r *stdhttp.Request, project, build string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryBuildStatus(w, r, project, build)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliverySealStatus(w stdhttp.ResponseWriter, r *stdhttp.Request, project, seal string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliverySealStatus(w, r, project, seal)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliveryCandidateStatus(w stdhttp.ResponseWriter, r *stdhttp.Request, project, candidate string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryCandidateStatus(w, r, project, candidate)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliveryGenerationStatus(w stdhttp.ResponseWriter, r *stdhttp.Request, project, generation string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryGenerationStatus(w, r, project, generation)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliveryPublicationEvidence(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryPublicationEvidence(w, r, project, publication)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) RequestDeliveryPublicationApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication string, headers deploymentgen.GenRequestDeliveryPublicationApprovalHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.RequestDeliveryPublicationApproval(w, r, project, publication, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliveryPublicationApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication, approval string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryPublicationApproval(w, r, project, publication, approval)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) ApproveDeliveryPublicationApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication, approval string, headers deploymentgen.GenApproveDeliveryPublicationApprovalHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.ApproveDeliveryPublicationApproval(w, r, project, publication, approval, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) DenyDeliveryPublicationApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication, approval string, headers deploymentgen.GenDenyDeliveryPublicationApprovalHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.DenyDeliveryPublicationApproval(w, r, project, publication, approval, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) RevokeDeliveryPublicationApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication, approval string, headers deploymentgen.GenRevokeDeliveryPublicationApprovalHeaders) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.RevokeDeliveryPublicationApproval(w, r, project, publication, approval, headers.IdempotencyKey)
		return
	}
	writeDeliveryUnavailable(w)
}

func (d *APIGenDispatcher) GetDeliveryOperatorSnapshot(w stdhttp.ResponseWriter, r *stdhttp.Request, project string) {
	if h, ok := d.handler.(DeliveryAPIGenHandler); ok {
		h.GetDeliveryOperatorSnapshot(w, r, project)
		return
	}
	writeDeliveryUnavailable(w)
}

func writeDeliveryUnavailable(w stdhttp.ResponseWriter) {
	writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]any{"code": "DELIVERY_READ_UNAVAILABLE", "message": "Delivery status is temporarily unavailable"})
}

type APIGenTransportErrorResponder struct{ Logger *slog.Logger }

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure deploymentgen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(operationID string, handler APIGenHandler, logger *slog.Logger, w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	return deploymentgen.DispatchAPIGenOperation(
		operationID, NewAPIGenDispatcher(handler), APIGenTransportErrorResponder{Logger: logger}, w, r,
	)
}
