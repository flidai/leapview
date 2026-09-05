package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/pkg/jobs"
)

var (
	ErrPublicationForbidden = apigenfailure.New("publication_forbidden", "publication deployment forbidden")
	ErrApprovalForbidden    = apigenfailure.New("approval_forbidden", "deployment approval forbidden")
	ErrActivationForbidden  = apigenfailure.New("activation_forbidden", "deployment activation forbidden")
	// Delivery mutation sentinels map one-to-one to the public TypeSpec
	// failure contracts. Coordinators should wrap these instead of relying on
	// message text for HTTP classification.
	ErrDeliveryForbidden        = apigenfailure.New("delivery_forbidden", "delivery mutation forbidden")
	ErrDeliveryInputUnavailable = apigenfailure.New("delivery_input_unavailable", "delivery inputs unavailable")
	ErrDeliveryIdempotencyDrift = apigenfailure.New("delivery_idempotency_drift", "delivery idempotency key drift")
	ErrDeliveryApprovalRequired = apigenfailure.New("delivery_approval_required", "delivery approval required")
)

type PageParams = deploymentapi.PageParams

type ReleasePort interface {
	Get(context.Context, projectgraph.ResourceID, string) (release.Release, error)
	PublishCandidate(context.Context, release.PublishCandidateInput) (release.Release, error)
	LinkDeployment(context.Context, string, string, string, string) error
	LinkDeploymentTx(context.Context, transaction.Transaction, string, string, string, string) error
	DeploymentRelease(context.Context, string, string) (string, string, error)
	ListDeploymentIDs(context.Context, string) ([]string, error)
	PriorDeploymentRelease(context.Context, string, string) (string, error)
}

func (m *Module) getRelease(ctx context.Context, project, releaseID string) (release.Release, error) {
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		return release.Release{}, err
	}
	return m.api.Releases.Get(ctx, projectID, releaseID)
}

type JobStore interface {
	Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error)
	Cancel(context.Context, string) error
	AppendEvent(context.Context, string, string, string, []byte) (jobs.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error)
}

type APIConfig struct {
	Releases  ReleasePort
	Jobs      JobStore
	Workflow  jobplatform.WorkflowRecorder
	Committer jobs.WorkflowCommitter
	// AuditedCommitter is the explicit durable-audit workflow port. Build fills
	// it from Workflow when it owns the transaction, or validates an
	// injected implementation when durable auditing is configured.
	AuditedCommitter AuditedWorkflowCommitter
}

func deploymentAuditRequestIdentity(r *http.Request) (string, string) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = strings.TrimSpace(r.Header.Get("X-Request-Id"))
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = strings.TrimSpace(r.Header.Get("X-Correlation-Id"))
	}
	if correlationID == "" {
		correlationID = requestID
	}
	return requestID, correlationID
}

func publishEvidence(
	targetRelease release.Release,
	instanceID,
	environment string,
) (apiadapter.PublishEvidence, error) {
	if instanceID != strings.TrimSpace(instanceID) || environment != strings.TrimSpace(environment) || targetRelease.Provenance == nil || targetRelease.ServingIdentity.ProjectID == "" || targetRelease.ProjectDigest == "" || targetRelease.ProjectDigest != targetRelease.Provenance.Artifact.ProjectDigest || targetRelease.Provenance.Plan.TargetID != instanceID || targetRelease.Provenance.Plan.Identity != targetRelease.ServingIdentity || targetRelease.Provenance.Plan.Identity.Environment != environment {
		return apiadapter.PublishEvidence{}, fmt.Errorf(
			"%w: release provenance does not belong to this target",
			deployment.ErrConflict,
		)
	}
	if err := targetRelease.Provenance.Validate(); err != nil {
		return apiadapter.PublishEvidence{}, fmt.Errorf(
			"%w: invalid release provenance: %w",
			deployment.ErrConflict,
			err,
		)
	}
	if targetRelease.ActualDigest != targetRelease.ArtifactDigest || targetRelease.ServingIdentity.GenerationID != targetRelease.Provenance.Plan.Identity.GenerationID {
		return apiadapter.PublishEvidence{}, fmt.Errorf("%w: release generation artifact drifted", deployment.ErrConflict)
	}
	return apiadapter.PublishEvidence{
		ReleaseDigest:            targetRelease.Provenance.Digest,
		ArtifactContentDigest:    targetRelease.ArtifactDigest,
		ArtifactProvenanceDigest: targetRelease.Provenance.ArtifactProvenanceDigest,
		PlanDigest:               targetRelease.Provenance.PlanDigest,
		CandidateID:              targetRelease.Provenance.Candidate.ID,
		CandidateRevision:        targetRelease.Provenance.Candidate.Revision,
		TargetID:                 targetRelease.Provenance.Plan.TargetID,
		Environment:              targetRelease.Provenance.Plan.Identity.Environment,
		GenerationID:             targetRelease.Provenance.Plan.Identity.GenerationID,
		BaseGenerationID:         baseGenerationID(targetRelease.Provenance.Plan.BaseIdentity),
		RuntimeVersion:           targetRelease.Provenance.Plan.RuntimeVersion,
		PolicyDigest:             targetRelease.Provenance.Plan.PolicyDigest,
	}, nil
}

func baseGenerationID(identity *projectgraph.ServingIdentity) string {
	if identity == nil {
		return ""
	}
	return identity.GenerationID
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return deploymenthttp.DispatchAPIGenOperation(operationID, deploymentAPIGenHandler{Module: m}, logger, w, r)
}

type deploymentAPIGenHandler struct{ *Module }

func (h deploymentAPIGenHandler) RetainProjectCandidateSource(w http.ResponseWriter, r *http.Request, project, idempotencyKey, sourceSynchronizationPlan string) {
	h.Module.RetainProjectCandidateSource(w, r, project, idempotencyKey, sourceSynchronizationPlan)
}

func (h deploymentAPIGenHandler) CreateDeliveryPlan(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	h.Module.CreateDeliveryPlan(w, r, project, idempotencyKey)
}

func (h deploymentAPIGenHandler) BuildDeliveryPlan(w http.ResponseWriter, r *http.Request, project, plan, idempotencyKey string) {
	h.Module.BuildDeliveryPlan(w, r, project, plan, idempotencyKey)
}

func (h deploymentAPIGenHandler) PublishDeliveryCandidate(w http.ResponseWriter, r *http.Request, project, candidate, idempotencyKey string) {
	h.Module.PublishDeliveryCandidate(w, r, project, candidate, idempotencyKey)
}

func (h deploymentAPIGenHandler) RollbackDeliveryGeneration(w http.ResponseWriter, r *http.Request, project, generation, idempotencyKey string) {
	h.Module.RollbackDeliveryGeneration(w, r, project, generation, idempotencyKey)
}

func (h deploymentAPIGenHandler) GetDeliveryPlanPreview(w http.ResponseWriter, r *http.Request, project, plan string) {
	h.Module.GetDeliveryPlanPreview(w, r, project, plan)
}

func (h deploymentAPIGenHandler) GetDeliveryBuildStatus(w http.ResponseWriter, r *http.Request, project, build string) {
	h.Module.GetDeliveryBuildStatus(w, r, project, build)
}

func (h deploymentAPIGenHandler) GetDeliverySealStatus(w http.ResponseWriter, r *http.Request, project, seal string) {
	h.Module.GetDeliverySealStatus(w, r, project, seal)
}

func (h deploymentAPIGenHandler) GetDeliveryCandidateStatus(w http.ResponseWriter, r *http.Request, project, candidate string) {
	h.Module.GetDeliveryCandidateStatus(w, r, project, candidate)
}

func (h deploymentAPIGenHandler) GetDeliveryGenerationStatus(w http.ResponseWriter, r *http.Request, project, generation string) {
	h.Module.GetDeliveryGenerationStatus(w, r, project, generation)
}

func (h deploymentAPIGenHandler) GetDeliveryPublicationEvidence(w http.ResponseWriter, r *http.Request, project, publication string) {
	h.Module.GetDeliveryPublicationEvidence(w, r, project, publication)
}

func (h deploymentAPIGenHandler) RequestDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publication, idempotencyKey string) {
	h.Module.RequestDeliveryPublicationApproval(w, r, project, publication, idempotencyKey)
}

func (h deploymentAPIGenHandler) GetDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publication, approval string) {
	h.Module.GetDeliveryPublicationApproval(w, r, project, publication, approval)
}

func (h deploymentAPIGenHandler) ApproveDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publication, approval, idempotencyKey string) {
	h.Module.ApproveDeliveryPublicationApproval(w, r, project, publication, approval, idempotencyKey)
}

func (h deploymentAPIGenHandler) DenyDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publication, approval, idempotencyKey string) {
	h.Module.DenyDeliveryPublicationApproval(w, r, project, publication, approval, idempotencyKey)
}

func (h deploymentAPIGenHandler) RevokeDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publication, approval, idempotencyKey string) {
	h.Module.RevokeDeliveryPublicationApproval(w, r, project, publication, approval, idempotencyKey)
}

func (h deploymentAPIGenHandler) GetDeliveryOperatorSnapshot(w http.ResponseWriter, r *http.Request, project string) {
	h.Module.GetDeliveryOperatorSnapshot(w, r, project)
}

func (m *Module) principal(r *http.Request) (deploymenthttp.Principal, bool) {
	if m == nil || m.handler == nil {
		return deploymenthttp.Principal{}, false
	}
	return m.handler.Principal(r)
}

func (m *Module) handlerEnvironment() string {
	if m == nil || m.handler == nil {
		return ""
	}
	return m.handler.Environment()
}

func (m *Module) approvalActor(
	r *http.Request,
	principalID string,
) (deployment.ApprovalActor, bool) {
	if m == nil || m.currentApprovalActor == nil {
		return deployment.ApprovalActor{}, false
	}
	actor, ok := m.currentApprovalActor(r)
	if !ok || actor.PrincipalID != principalID || actor.PrincipalID == "" || actor.CredentialID == "" || actor.CredentialExpiresAt.IsZero() {
		return deployment.ApprovalActor{}, false
	}
	if m.nativeDeliveryApproval != nil {
		return actor, true
	}
	return actor, m.approvals != nil && m.approvals.ValidateActor(actor) == nil
}

func (m *Module) authorizeApprovedActivation(
	ctx context.Context,
	row apiadapter.Deployment,
	releaseID string,
	actor deployment.ApprovalActor,
) (deployment.Approval, error) {
	if m == nil || m.approvals == nil {
		return deployment.Approval{}, deployment.ErrApprovalRequired
	}
	if err := m.approvals.ValidateActor(actor); err != nil {
		return deployment.Approval{}, err
	}
	approval, err := m.approvals.AuthorizeActivation(
		ctx,
		deployment.ApprovalActivation{
			ProjectID: row.Project, DeploymentID: row.ID,
			Environment:   row.Environment,
			RequestDigest: row.RequestDigest,
			ReleaseID:     releaseID,
		},
	)
	if err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("approval_unavailable", err)
		}
		return deployment.Approval{}, err
	}
	if m.authorizeApproval == nil {
		return deployment.Approval{}, apigenfailure.New("approval_unavailable", "approval authorization is unavailable")
	}
	if err := m.authorizeApproval(
		ctx,
		deployment.ApprovalActor{
			PrincipalID:         approval.ApprovedBy,
			CredentialClass:     approval.ApprovalCredentialClass,
			CredentialID:        approval.ApprovalCredentialID,
			CredentialExpiresAt: approval.ApprovalCredentialExpiresAt,
		},
		row.Project,
		row.Environment,
	); err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("authorization_unavailable", err)
		}
		return deployment.Approval{}, err
	}
	if m.authorizeActivation == nil {
		return deployment.Approval{}, apigenfailure.New("authorization_unavailable", "activation authorization is unavailable")
	}
	if err := m.authorizeActivation(
		ctx,
		actor,
		row.Project,
		row.Environment,
	); err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("authorization_unavailable", err)
		}
		return deployment.Approval{}, err
	}
	return approval, nil
}

func deploymentResponse(
	row apiadapter.Deployment,
	targetRelease release.Release,
) deploymentapi.Response {
	status := deploymentapi.Status(row.Status)
	if row.Status == apiadapter.StatusPending {
		status = deploymentapi.StatusQueued
	}
	result := deploymentapi.Response{
		ID: row.ID, ProjectID: row.Project, ReleaseID: targetRelease.ID,
		Environment: row.Environment, RequestDigest: row.RequestDigest,
		GenerationID: row.GenerationID, ArtifactDigest: row.ArtifactDigest,
		Evidence: publishEvidenceResponse(targetRelease), Status: status,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}
	if row.PriorGenerationID != "" {
		result.PriorGenerationID = &row.PriorGenerationID
	}
	if row.ActivatedAt != "" {
		result.StartedAt = &row.ActivatedAt
		result.FinishedAt = &row.ActivatedAt
	}
	if row.ActivationPrincipal != "" {
		result.ActivationPrincipal = &row.ActivationPrincipal
	}
	if row.VerificationDigest != "" && row.VerifiedAt != "" {
		result.Verification = &deploymentapi.VerificationResponse{
			Digest: row.VerificationDigest, VerifiedAt: row.VerifiedAt,
		}
	}
	if row.Error != "" {
		result.Error = &row.Error
	}
	return result
}

func publishEvidenceResponse(
	targetRelease release.Release,
) deploymentapi.PublishEvidenceResponse {
	if targetRelease.Provenance == nil {
		return deploymentapi.PublishEvidenceResponse{}
	}
	response := deploymentapi.PublishEvidenceResponse{
		ReleaseDigest:            targetRelease.Provenance.Digest,
		ArtifactContentDigest:    targetRelease.ArtifactDigest,
		ArtifactProvenanceDigest: targetRelease.Provenance.ArtifactProvenanceDigest,
		PlanDigest:               targetRelease.Provenance.PlanDigest,
		CandidateID:              targetRelease.Provenance.Candidate.ID,
		CandidateRevision:        targetRelease.Provenance.Candidate.Revision,
		TargetID:                 targetRelease.Provenance.Plan.TargetID,
		Environment:              targetRelease.Provenance.Plan.Identity.Environment,
		GenerationID:             targetRelease.Provenance.Plan.Identity.GenerationID,
		RuntimeVersion:           targetRelease.Provenance.Plan.RuntimeVersion,
		PolicyDigest:             targetRelease.Provenance.Plan.PolicyDigest,
	}
	if source := targetRelease.Provenance.SourceRevision; source != nil {
		response.SourceRevision = &deploymentapi.CandidateSourceRevision{
			Revision: source.Revision,
		}
		if source.Repository != "" {
			value := source.Repository
			response.SourceRevision.Repository = &value
		}
		if source.Ref != "" {
			value := source.Ref
			response.SourceRevision.Ref = &value
		}
		if source.ChangeID != "" {
			value := source.ChangeID
			response.SourceRevision.ChangeID = &value
		}
	}
	return response
}

func approvalResponse(approval deployment.Approval) deploymentapi.ApprovalResponse {
	response := deploymentapi.ApprovalResponse{
		ID: approval.ID, ProjectID: approval.ProjectID,
		DeploymentID:  approval.DeploymentID,
		Environment:   approval.Environment,
		RequestDigest: approval.RequestDigest,
		ReleaseID:     approval.ReleaseID,
		Status:        string(approval.Status),
		RequestedBy:   approval.RequestedBy,
		RequestedAt:   approval.RequestedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:     approval.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Revision:      approval.Revision,
	}
	if approval.Status == deployment.ApprovalDenied && approval.ApprovedBy != "" {
		response.DeniedBy = &approval.ApprovedBy
	} else if approval.ApprovedBy != "" {
		response.ApprovedBy = &approval.ApprovedBy
	}
	if approval.Status == deployment.ApprovalDenied && !approval.ApprovedAt.IsZero() {
		value := approval.ApprovedAt.UTC().Format(time.RFC3339Nano)
		response.DeniedAt = &value
	} else if !approval.ApprovedAt.IsZero() {
		value := approval.ApprovedAt.UTC().Format(time.RFC3339Nano)
		response.ApprovedAt = &value
	}
	if approval.RevokedBy != "" {
		response.RevokedBy = &approval.RevokedBy
	}
	if !approval.RevokedAt.IsZero() {
		value := approval.RevokedAt.UTC().Format(time.RFC3339Nano)
		response.RevokedAt = &value
	}
	return response
}

func nativeApprovalResponse(project, environment string, approval nativepostgres.ApprovalRequest) deploymentapi.ApprovalResponse {
	response := deploymentapi.ApprovalResponse{
		ID: approval.RequestID, ProjectID: project, DeploymentID: approval.PublicationID,
		Environment: environment, RequestDigest: approval.RequestDigest, ReleaseID: "",
		Status: "pending", RequestedBy: approval.RequestedBy.PrincipalID,
		RequestedAt: approval.RequestedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: approval.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	if approval.LatestDecision != nil {
		response.Revision = approval.LatestDecision.Revision
		switch approval.LatestDecision.Decision {
		case nativepostgres.ApprovalActionApprove:
			response.Status = "approved"
			value := approval.LatestDecision.DecidedBy.PrincipalID
			response.ApprovedBy = &value
			at := approval.LatestDecision.DecidedAt.UTC().Format(time.RFC3339Nano)
			response.ApprovedAt = &at
		case nativepostgres.ApprovalActionDeny:
			response.Status = "denied"
			value := approval.LatestDecision.DecidedBy.PrincipalID
			response.DeniedBy = &value
			at := approval.LatestDecision.DecidedAt.UTC().Format(time.RFC3339Nano)
			response.DeniedAt = &at
		case nativepostgres.ApprovalActionRevoke:
			response.Status = "revoked"
			value := approval.LatestDecision.DecidedBy.PrincipalID
			response.RevokedBy = &value
			at := approval.LatestDecision.DecidedAt.UTC().Format(time.RFC3339Nano)
			response.RevokedAt = &at
		}
	}
	return response
}

func approvalLocation(project, publicationID, approvalID string) string {
	return "/api/v1/projects/" + project + "/delivery/publications/" + publicationID +
		"/approval-requests/" + approvalID
}

func (m *Module) writeCommandFailure(w http.ResponseWriter, r *http.Request, operationID deploymentgen.GenCommandOperationID, err error) {
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, m.logger, operationID, deploymentgen.GetAPIGenCommandFailureContracts, err)
}

func writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
	if kind, ok := apigenfailure.KindOf(err); ok {
		switch kind {
		case "service_unavailable":
			status, code = http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE"
		case "queue_unavailable":
			status, code = http.StatusServiceUnavailable, "ASYNC_QUEUE_UNAVAILABLE"
		case "authorization_unavailable":
			status, code = http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE"
		case "approval_unavailable":
			status, code = http.StatusServiceUnavailable, "DEPLOYMENT_APPROVAL_UNAVAILABLE"
		case "release_not_ready":
			status, code = http.StatusConflict, "RELEASE_NOT_READY"
		case "release_incomplete":
			status, code = http.StatusConflict, "RELEASE_INCOMPLETE"
		case "approval_credential_required":
			status, code = http.StatusUnauthorized, "APPROVAL_CREDENTIAL_REQUIRED"
		case "activation_forbidden":
			status, code = http.StatusForbidden, "ACTIVATION_FORBIDDEN"
		case "publication_forbidden":
			status, code = http.StatusForbidden, "PUBLICATION_MANAGEMENT_REQUIRED"
		case "invalid":
			status, code = http.StatusUnprocessableEntity, "INVALID_DEPLOYMENT"
		}
	}
	switch {
	case errors.Is(err, release.ErrNotFound), errors.Is(err, deployment.ErrNotFound):
		status, code = http.StatusNotFound, "DEPLOYMENT_NOT_FOUND"
	case errors.Is(err, deployment.ErrApprovalNotFound),
		errors.Is(err, deployment.ErrApprovalScope):
		status, code = http.StatusNotFound, "DEPLOYMENT_APPROVAL_NOT_FOUND"
	case errors.Is(err, deployment.ErrApprovalCredentialExpired):
		status, code = http.StatusUnauthorized, "APPROVAL_CREDENTIAL_EXPIRED"
	case errors.Is(err, deployment.ErrApprovalSeparationOfDuty):
		status, code = http.StatusConflict, "SEPARATION_OF_DUTY_REQUIRED"
	case errors.Is(err, ErrActivationForbidden):
		status, code = http.StatusForbidden, "ACTIVATION_FORBIDDEN"
	case errors.Is(err, ErrApprovalForbidden):
		status, code = http.StatusForbidden, "APPROVAL_FORBIDDEN"
	case errors.Is(err, deployment.ErrApprovalRequired),
		errors.Is(err, deployment.ErrApprovalExpired),
		errors.Is(err, deployment.ErrApprovalConflict):
		status, code = http.StatusConflict, "DEPLOYMENT_APPROVAL_REQUIRED"
	case errors.Is(err, release.ErrConflict), errors.Is(err, deployment.ErrConflict):
		status, code = http.StatusConflict, "DEPLOYMENT_CONFLICT"
	case errors.Is(err, apiadapter.ErrInvalid):
		status, code = http.StatusUnprocessableEntity, "INVALID_DEPLOYMENT"
	}
	detail := err.Error()
	if status == http.StatusInternalServerError {
		detail = "The deployment request could not be completed"
	}
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}
