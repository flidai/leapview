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
	PlanProjectCandidateSynchronization(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
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
	apitransport.WriteProblem(w, r, stdhttp.StatusServiceUnavailable, "CANDIDATE_UNAVAILABLE", "Candidate is unavailable", nil)
}

func (d *APIGenDispatcher) UploadProjectCandidateSourceBlob(w stdhttp.ResponseWriter, r *stdhttp.Request, project, digest string, headers deploymentgen.GenUploadProjectCandidateSourceBlobHeaders) {
	d.handler.UploadProjectCandidateSourceBlob(w, r, project, digest, headers.ContentType, headers.ContentDigest, headers.SourceSynchronizationPlan)
}

func (d *APIGenDispatcher) PlanProjectCandidateSynchronization(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers deploymentgen.GenPlanProjectCandidateSynchronizationHeaders) {
	d.handler.PlanProjectCandidateSynchronization(w, r, project, headers.IdempotencyKey)
}

type APIGenDispatcher struct{ handler APIGenHandler }

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
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
