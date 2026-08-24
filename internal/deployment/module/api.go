package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
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
	// it from Workflow when it owns the SQLite transaction, or validates an
	// injected implementation when durable auditing is configured.
	AuditedCommitter AuditedWorkflowCommitter
}

func (m *Module) CreateDeployment(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	var body deploymentapi.CreateRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	m.createDeployment(w, r, deploymentgen.GenCommandOperationCreateDeployment(), project, body.ReleaseID, idempotencyKey, "")
}

func (m *Module) createDeployment(w http.ResponseWriter, r *http.Request, operationID deploymentgen.GenCommandOperationID, project, releaseID, idempotencyKey, rollbackOf string) {
	m.createDeploymentWithBootstrap(w, r, operationID, project, releaseID, idempotencyKey, rollbackOf, false)
}

func (m *Module) createDeploymentWithBootstrap(w http.ResponseWriter, r *http.Request, operationID deploymentgen.GenCommandOperationID, project, releaseID, idempotencyKey, rollbackOf string, bootstrap bool) {
	operationIDValue := operationID.APIGenOperationID()
	execution, err := m.execution(operationIDValue)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	var approvalActor deployment.ApprovalActor
	if m.protected {
		approvalActor, ok = m.approvalActor(r, principal.ID)
		if !ok {
			m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_credential_required", "A bounded publication credential is required"))
			return
		}
	} else {
		// Ungated development deployments still need the authenticated actor on
		// their durable activation job. Approval credential evidence is only
		// required and inspected for protected targets.
		approvalActor.PrincipalID = principal.ID
	}
	if m.jobs.Coordinator == nil || m.api.Releases == nil {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("service_unavailable", "Deployment service is unavailable"))
		return
	}
	targetRelease, err := m.getRelease(r.Context(), project, releaseID)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	if targetRelease.Status != release.StatusReady {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("release_not_ready", "Only ready releases can be deployed"))
		return
	}
	evidence, err := publishEvidence(
		targetRelease,
		m.instanceID,
		m.handlerEnvironment(),
	)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	if !m.protected && m.jobs.Authorize != nil {
		if err := m.jobs.Authorize(r.Context(), principal.ID, m.handlerEnvironment(), targetRelease.ServingIdentity.GenerationID); err != nil {
			if errors.Is(err, ErrPublicationForbidden) {
				m.writeCommandFailure(w, r, operationID, err)
				return
			}
			m.writeCommandFailure(w, r, operationID, apigenfailure.Wrap("authorization_unavailable", err))
			return
		}
	}
	expectedRollbackBase, expectedRollbackRevision := "", int64(0)
	rollbackIntent := operationIDValue == string(deploymentgen.GenOperationRollbackDeployment) || strings.TrimSpace(rollbackOf) != ""
	if rollbackIntent && m.sealedCoordinator != nil {
		if m.sealedRollbackFence == nil {
			m.writeCommandFailure(w, r, operationID, apigenfailure.New("service_unavailable", "Sealed rollback fencing is unavailable"))
			return
		}
		expectedRollbackBase, expectedRollbackRevision, err = m.sealedRollbackFence(r.Context(), m.instanceID)
		if err != nil {
			m.writeCommandFailure(w, r, operationID, err)
			return
		}
	}
	createRequest := apiadapter.CreateRequest{
		Project: project, Environment: m.handlerEnvironment(), GenerationID: targetRelease.ServingIdentity.GenerationID, ArtifactDigest: targetRelease.ArtifactDigest, PriorGenerationID: evidence.BaseGenerationID, Actor: principal.ID, IdempotencyKey: idempotencyKey,
		ReleaseID: releaseID, Evidence: evidence, RollbackOf: rollbackOf,
	}
	createRequest.Workflow = func(deploymentID string) (jobs.WorkflowIntent, error) {
		return activationWorkflowForOperationWithRollbackFence(operationIDValue, execution,
			!m.protected || bootstrap,
			project,
			deploymentID,
			releaseID,
			approvalActor,
			deployment.Approval{},
			idempotencyKey+":cutover", bootstrap, expectedRollbackBase, expectedRollbackRevision, rollbackIntent,
		)
	}
	if bootstrap {
		if !m.protected || m.bootstrapPolicies == nil || m.authorizeBootstrap == nil || approvalActor.CredentialClass != deployment.CredentialClassAPIToken {
			m.writeCommandFailure(w, r, operationID, deployment.ErrApprovalRequired)
			return
		}
		requestDigest, digestErr := apiadapter.RequestDigest(createRequest)
		if digestErr != nil {
			m.writeCommandFailure(w, r, operationID, digestErr)
			return
		}
		deploymentID := apiadapter.DeploymentID(project, principal.ID, idempotencyKey)
		bootstrapPolicy := deployment.BootstrapActivationPolicy{
			ProjectID: projectgraph.ResourceID(project), Environment: servingstate.Environment(m.handlerEnvironment()), DeploymentID: deploymentID,
			RequestDigest: requestDigest, ActorID: principal.ID, CredentialID: approvalActor.CredentialID,
			CredentialExpiresAt: approvalActor.CredentialExpiresAt, ArmedAt: time.Now().UTC(),
		}
		if err := m.authorizeBootstrap(r.Context(), bootstrapPolicy); err != nil {
			m.writeCommandFailure(w, r, operationID, err)
			return
		}
		_, armErr := m.bootstrapPolicies.ArmBootstrapActivation(r.Context(), bootstrapPolicy)
		if armErr != nil {
			m.writeCommandFailure(w, r, operationID, armErr)
			return
		}
	}
	ctx := r.Context()
	migratedAudit := m.auditIntentConfigured && deploymentAuditIntentOperation(operationIDValue)
	if migratedAudit {
		deploymentID := apiadapter.DeploymentID(project, principal.ID, idempotencyKey)
		requestID, correlationID := deploymentAuditRequestIdentity(r)
		intent, intentErr := buildDeploymentAuditIntent(deploymentAuditCommandInput{
			OperationID: operationIDValue, ProjectID: project, DeploymentID: deploymentID, ReleaseID: releaseID,
			IdempotencyKey: idempotencyKey, PrincipalID: principal.ID, RequestID: requestID, CorrelationID: correlationID,
			Status: execution.InitialState,
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, operationID, intentErr)
			return
		}
		ctx = deployment.WithAuditIntent(ctx, intent)
	}
	created, err := m.jobs.Coordinator.Create(ctx, createRequest)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	response := deploymentResponse(created, targetRelease)
	if m.protected && !bootstrap {
		approval, approvalErr := m.requestApproval(
			r.Context(),
			created,
			releaseID,
			approvalActor,
		)
		if approvalErr != nil {
			m.writeCommandFailure(w, r, operationID, approvalErr)
			return
		}
		mapped := approvalResponse(approval)
		response.Approval = &mapped
	}
	m.completePersistedExecution(ctx, operationIDValue, created.ID)
	w.Header().Set("Location", deploymentLocation(project, created.ID))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func deploymentAuditIntentOperation(operationID string) bool {
	switch operationID {
	case string(deploymentgen.GenOperationCreateDeployment), string(deploymentgen.GenOperationRetryDeployment), string(deploymentgen.GenOperationRollbackDeployment):
		return true
	default:
		return false
	}
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

func (m *Module) GetDeployment(w http.ResponseWriter, r *http.Request, project, deploymentID string) {
	if m.jobs.Coordinator == nil || m.api.Releases == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	releaseID, _, err := m.api.Releases.DeploymentRelease(r.Context(), project, deploymentID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	targetRelease, err := m.getRelease(r.Context(), project, releaseID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	row, err := m.jobs.Coordinator.Get(r.Context(), apiadapter.Scope{Project: project, DeploymentID: deploymentID})
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	response := deploymentResponse(row, targetRelease)
	m.attachApproval(r.Context(), &response, row.ID)
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) ListDeployments(w http.ResponseWriter, r *http.Request, project string, params deploymentapi.PageParams) {
	if m.jobs.Coordinator == nil || m.api.Releases == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	ids, err := m.api.Releases.ListDeploymentIDs(r.Context(), project)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	items := make([]deploymentapi.Response, 0, len(ids))
	for _, id := range ids {
		releaseID, _, err := m.api.Releases.DeploymentRelease(r.Context(), project, id)
		if err != nil {
			continue
		}
		targetRelease, err := m.getRelease(r.Context(), project, releaseID)
		if err != nil {
			continue
		}
		row, err := m.jobs.Coordinator.Get(r.Context(), apiadapter.Scope{Project: project, DeploymentID: id})
		if err != nil {
			continue
		}
		response := deploymentResponse(row, targetRelease)
		m.attachApproval(r.Context(), &response, row.ID)
		items = append(items, response)
	}
	page, next, err := apitransport.KeysetPage(items, params.Limit, params.PageToken, func(item deploymentapi.Response) string { return item.CreatedAt + "\x00" + item.ID })
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, deploymentapi.ListResponse{Items: page, Page: deploymentapi.PageInfo{NextCursor: next}})
}

func (m *Module) CancelDeployment(w http.ResponseWriter, r *http.Request, project, deploymentID string) {
	operationID := deploymentgen.GenCommandOperationCancelDeployment()
	if m.jobs.Coordinator == nil || m.api.Releases == nil || m.api.Jobs == nil {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("service_unavailable", "Deployment service is unavailable"))
		return
	}
	principal, ok := m.principal(r)
	if !ok {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("authentication_required", "Bearer authentication is required"))
		return
	}
	releaseID, _, err := m.api.Releases.DeploymentRelease(r.Context(), project, deploymentID)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	targetRelease, err := m.getRelease(r.Context(), project, releaseID)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	ctx := r.Context()
	if m.auditIntentConfigured {
		requestID, correlationID := deploymentAuditRequestIdentity(r)
		intent, intentErr := buildDeploymentAuditIntent(deploymentAuditCommandInput{
			OperationID: operationID.APIGenOperationID(), ProjectID: project, DeploymentID: deploymentID, ReleaseID: releaseID,
			IdempotencyKey: r.Header.Get("Idempotency-Key"), PrincipalID: principal.ID,
			RequestID: requestID, CorrelationID: correlationID, Status: string(apiadapter.StatusCancelled),
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, operationID, intentErr)
			return
		}
		ctx = deployment.WithAuditIntent(ctx, intent)
	}
	row, err := m.jobs.Coordinator.Cancel(ctx, apiadapter.Scope{Project: project, DeploymentID: deploymentID})
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	m.completePersistedExecution(ctx, operationID.APIGenOperationID(), deploymentID)
	if !m.auditIntentConfigured {
		m.recordBestEffortAPIEvent(
			r.Context(), operationID.APIGenOperationID(), deploymentID,
			deploymentCancelledAuditAction, map[string]any{"deploymentId": deploymentID, "status": "cancelled"},
		)
	}
	w.Header().Set("Location", deploymentLocation(project, deploymentID))
	apitransport.WriteJSON(w, http.StatusAccepted, deploymentResponse(row, targetRelease))
}

func (m *Module) RetryDeployment(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	idempotencyKey string,
) {
	operationID := deploymentgen.GenCommandOperationRetryDeployment()
	if m.jobs.Coordinator == nil || m.api.Releases == nil {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("service_unavailable", "Deployment service is unavailable"))
		return
	}
	releaseID, rollbackOf, err := m.api.Releases.DeploymentRelease(
		r.Context(),
		project,
		deploymentID,
	)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	row, err := m.jobs.Coordinator.Get(
		r.Context(),
		apiadapter.Scope{Project: project, DeploymentID: deploymentID},
	)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	retryable := row.Status == apiadapter.StatusFailed ||
		row.Status == apiadapter.StatusCancelled
	if row.Status == apiadapter.StatusPending && m.approvals != nil {
		if approval, approvalErr := m.approvals.Current(
			r.Context(),
			deploymentID,
		); approvalErr == nil {
			retryable = approval.Status == deployment.ApprovalDenied ||
				approval.Status == deployment.ApprovalRevoked ||
				approval.Status == deployment.ApprovalExpired
		}
	}
	if !retryable {
		m.writeCommandFailure(
			w,
			r,
			operationID,
			fmt.Errorf(
				"%w: deployment is %s and cannot be retried",
				deployment.ErrConflict,
				row.Status,
			),
		)
		return
	}
	m.createDeployment(w, r, deploymentgen.GenCommandOperationRetryDeployment(), project, releaseID, idempotencyKey, rollbackOf)
}

func (m *Module) RollbackDeployment(w http.ResponseWriter, r *http.Request, project, deploymentID, idempotencyKey string) {
	operationID := deploymentgen.GenCommandOperationRollbackDeployment()
	if m.api.Releases == nil {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("service_unavailable", "Deployment service is unavailable"))
		return
	}
	releaseID, err := m.api.Releases.PriorDeploymentRelease(r.Context(), project, deploymentID)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	m.createDeployment(w, r, deploymentgen.GenCommandOperationRollbackDeployment(), project, releaseID, idempotencyKey, deploymentID)
}

func (m *Module) RequestDeploymentApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	idempotencyKey string,
) {
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationRequestDeploymentApproval(), apigenfailure.New("approval_credential_required", "A bounded publication credential is required"))
		return
	}
	row, releaseID, ok := m.approvalDeployment(w, r, project, deploymentID, deploymentgen.GenCommandOperationRequestDeploymentApproval())
	if !ok {
		return
	}
	ctx := r.Context()
	migratedAudit := m.auditIntentConfigured
	if migratedAudit {
		requestID, correlationID := deploymentAuditRequestIdentity(r)
		intent, intentErr := buildDeploymentAuditIntent(deploymentAuditCommandInput{
			OperationID: string(deploymentgen.GenOperationRequestDeploymentApproval), ProjectID: project, DeploymentID: deploymentID,
			IdempotencyKey: idempotencyKey, PrincipalID: principal.ID, RequestID: requestID, CorrelationID: correlationID,
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationRequestDeploymentApproval(), intentErr)
			return
		}
		ctx = deployment.WithAuditIntent(ctx, intent)
	}
	approval, err := m.requestApproval(ctx, row, releaseID, actor)
	if err != nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationRequestDeploymentApproval(), err)
		return
	}
	m.completePersistedExecution(ctx, deploymentgen.GenCommandOperationRequestDeploymentApproval().APIGenOperationID(), deploymentID)
	if !migratedAudit {
		m.recordBestEffortAPIEvent(r.Context(), deploymentgen.GenCommandOperationRequestDeploymentApproval().APIGenOperationID(), deploymentID, deploymentApprovalRequestedAuditAction, map[string]any{
			"deploymentId": deploymentID,
			"approvalId":   approval.ID,
		})
	}
	w.Header().Set("Location", approvalLocation(project, deploymentID, approval.ID))
	apitransport.WriteJSON(w, http.StatusCreated, approvalResponse(approval))
}

func (m *Module) ApproveDeployment(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID,
	idempotencyKey string,
) {
	m.transitionApproval(w, r, project, deploymentID, approvalID, idempotencyKey, approvalDecisionApprove)
}

func (m *Module) DenyDeploymentApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID,
	idempotencyKey string,
) {
	m.transitionApproval(w, r, project, deploymentID, approvalID, idempotencyKey, approvalDecisionDeny)
}

func (m *Module) RevokeDeploymentApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID,
	idempotencyKey string,
) {
	m.transitionApproval(w, r, project, deploymentID, approvalID, idempotencyKey, approvalDecisionRevoke)
}

func (m *Module) ActivateDeployment(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	idempotencyKey string,
) {
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), apigenfailure.New("approval_credential_required", "A bounded activation credential is required"))
		return
	}
	row, releaseID, ok := m.approvalDeployment(w, r, project, deploymentID, deploymentgen.GenCommandOperationActivateDeployment())
	if !ok {
		return
	}
	approval, err := m.authorizeApprovedActivation(r.Context(), row, releaseID, actor)
	if err != nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), err)
		return
	}
	execution, err := m.execution(deploymentgen.GenCommandOperationActivateDeployment().APIGenOperationID())
	if err != nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), err)
		return
	}
	workflow, err := activationWorkflowForOperation(string(deploymentgen.GenOperationActivateDeployment), execution,
		true,
		project,
		deploymentID,
		releaseID,
		actor,
		approval,
		idempotencyKey,
	)
	if err != nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), err)
		return
	}
	if m.api.Committer == nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), apigenfailure.New("queue_unavailable", "Deployment activation queue is unavailable"))
		return
	}
	var activationAudit *access.AuditIntent
	if m.auditIntentConfigured {
		requestID, correlationID := deploymentAuditRequestIdentity(r)
		intent, intentErr := buildDeploymentAuditIntent(deploymentAuditCommandInput{
			OperationID: deploymentgen.GenCommandOperationActivateDeployment().APIGenOperationID(), ProjectID: project,
			DeploymentID: deploymentID, ReleaseID: releaseID, IdempotencyKey: idempotencyKey, PrincipalID: principal.ID,
			RequestID: requestID, CorrelationID: correlationID, Status: execution.InitialState, Outcome: "accepted",
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), intentErr)
			return
		}
		activationAudit = &intent
	}
	var commitErr error
	if activationAudit != nil {
		if m.api.AuditedCommitter == nil {
			m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), apigenfailure.New("queue_unavailable", "Deployment activation audit is unavailable"))
			return
		}
		commitErr = m.api.AuditedCommitter.CommitWorkflowWithAudit(r.Context(), workflow, *activationAudit)
	} else {
		commitErr = m.api.Committer.CommitWorkflow(r.Context(), workflow)
	}
	if commitErr != nil && !errors.Is(commitErr, jobs.ErrConflict) {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), apigenfailure.Wrap("queue_unavailable", commitErr))
		return
	}
	targetRelease, err := m.getRelease(r.Context(), project, releaseID)
	if err != nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationActivateDeployment(), err)
		return
	}
	response := deploymentResponse(row, targetRelease)
	mapped := approvalResponse(approval)
	response.Approval = &mapped
	m.completePersistedExecution(r.Context(), deploymentgen.GenCommandOperationActivateDeployment().APIGenOperationID(), deploymentID)
	w.Header().Set("Location", deploymentLocation(project, deploymentID))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func (m *Module) ListDeploymentEvents(w http.ResponseWriter, r *http.Request, project, deploymentID string, params deploymentapi.PageParams) {
	if m.api.Releases == nil || m.jobs.Coordinator == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	if _, _, err := m.api.Releases.DeploymentRelease(r.Context(), project, deploymentID); err != nil {
		writeAPIError(w, r, err)
		return
	}
	if _, err := m.jobs.Coordinator.Get(r.Context(), apiadapter.Scope{Project: project, DeploymentID: deploymentID}); err != nil {
		writeAPIError(w, r, err)
		return
	}
	if m.api.Jobs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_EVENT_STORE_UNAVAILABLE", "Deployment events are unavailable", nil)
		return
	}
	jobhttp.WriteEventPage(w, r, m.api.Jobs, "deployment", deploymentID, params.Limit, params.PageToken, "deployment:"+project+":"+deploymentID)
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return deploymenthttp.DispatchAPIGenOperation(operationID, deploymentAPIGenHandler{Module: m}, logger, w, r)
}

type deploymentAPIGenHandler struct{ *Module }

func (h deploymentAPIGenHandler) RetainProjectCandidateSource(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	h.Module.RetainProjectCandidateSource(w, r, project, idempotencyKey)
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

func (h deploymentAPIGenHandler) ListDeployments(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	h.Module.ListDeployments(w, r, project, deploymentapi.PageParams{Limit: limit, PageToken: pageToken})
}

func (h deploymentAPIGenHandler) ListDeploymentEvents(w http.ResponseWriter, r *http.Request, project, deploymentID string, limit *int32, pageToken *string) {
	h.Module.ListDeploymentEvents(w, r, project, deploymentID, deploymentapi.PageParams{Limit: limit, PageToken: pageToken})
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
	return actor, ok &&
		actor.PrincipalID == principalID &&
		m.approvals != nil &&
		m.approvals.ValidateActor(actor) == nil
}

func (m *Module) approvalDeployment(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID string,
	operationID deploymentgen.GenCommandOperationID,
) (apiadapter.Deployment, string, bool) {
	if m == nil || m.approvals == nil ||
		m.jobs.Coordinator == nil || m.api.Releases == nil {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_unavailable", "Deployment approvals are unavailable"))
		return apiadapter.Deployment{}, "", false
	}
	releaseID, _, err := m.api.Releases.DeploymentRelease(
		r.Context(),
		project,
		deploymentID,
	)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return apiadapter.Deployment{}, "", false
	}
	row, err := m.jobs.Coordinator.Get(
		r.Context(),
		apiadapter.Scope{Project: project, DeploymentID: deploymentID},
	)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return apiadapter.Deployment{}, "", false
	}
	if row.Status != apiadapter.StatusPending {
		m.writeCommandFailure(
			w,
			r,
			operationID,
			fmt.Errorf(
				"%w: deployment is %s",
				deployment.ErrConflict,
				row.Status,
			),
		)
		return apiadapter.Deployment{}, "", false
	}
	return row, releaseID, true
}

func (m *Module) requestApproval(
	ctx context.Context,
	row apiadapter.Deployment,
	releaseID string,
	actor deployment.ApprovalActor,
) (deployment.Approval, error) {
	if m == nil || m.approvals == nil {
		return deployment.Approval{}, apigenfailure.New("approval_unavailable", "deployment approvals are unavailable")
	}
	approval, err := m.approvals.Request(ctx, deployment.ApprovalRequest{
		ProjectID: row.Project, DeploymentID: row.ID,
		Environment:   row.Environment,
		RequestDigest: row.RequestDigest,
		ReleaseID:     releaseID, RequestedBy: actor,
	})
	if err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("approval_unavailable", err)
		}
		return deployment.Approval{}, err
	}
	return approval, nil
}

type approvalDecision uint8

const (
	approvalDecisionApprove approvalDecision = iota + 1
	approvalDecisionDeny
	approvalDecisionRevoke
)

func operationIDForDecision(decision approvalDecision) deploymentgen.GenCommandOperationID {
	switch decision {
	case approvalDecisionApprove:
		return deploymentgen.GenCommandOperationApproveDeployment()
	case approvalDecisionDeny:
		return deploymentgen.GenCommandOperationDenyDeploymentApproval()
	default:
		return deploymentgen.GenCommandOperationRevokeDeploymentApproval()
	}
}

func (m *Module) transitionApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID string,
	idempotencyKey string,
	decision approvalDecision,
) {
	operationID := operationIDForDecision(decision)
	var body deploymentapi.ApprovalDecisionRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_credential_required", "A bounded approval credential is required"))
		return
	}
	if _, _, ok := m.approvalDeployment(w, r, project, deploymentID, operationID); !ok {
		return
	}
	transition := deployment.ApprovalTransition{
		ProjectID: project, DeploymentID: deploymentID,
		ApprovalID:       approvalID,
		ExpectedRevision: body.ExpectedRevision,
		Actor:            actor,
	}
	ctx := r.Context()
	migratedAudit := m.auditIntentConfigured
	if migratedAudit {
		requestID, correlationID := deploymentAuditRequestIdentity(r)
		intent, intentErr := buildDeploymentAuditIntent(deploymentAuditCommandInput{
			OperationID: operationID.APIGenOperationID(), ProjectID: project, DeploymentID: deploymentID, ApprovalID: approvalID,
			ApprovalRev: body.ExpectedRevision + 1, IdempotencyKey: idempotencyKey, PrincipalID: principal.ID,
			RequestID: requestID, CorrelationID: correlationID,
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, operationID, intentErr)
			return
		}
		ctx = deployment.WithAuditIntent(ctx, intent)
	}
	var (
		approval deployment.Approval
		err      error
	)
	switch decision {
	case approvalDecisionApprove:
		approval, err = m.approvals.Approve(ctx, transition)
	case approvalDecisionDeny:
		approval, err = m.approvals.Deny(ctx, transition)
	case approvalDecisionRevoke:
		approval, err = m.approvals.Revoke(ctx, transition)
	default:
		err = deployment.ErrApprovalInvalid
	}
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	m.completePersistedExecution(ctx, operationID.APIGenOperationID(), deploymentID)
	eventType := deploymentApprovalRevokedAuditAction
	switch decision {
	case approvalDecisionApprove:
		eventType = deploymentApprovedAuditAction
	case approvalDecisionDeny:
		eventType = deploymentDeniedAuditAction
	}
	if !migratedAudit {
		m.recordBestEffortAPIEvent(r.Context(), operationID.APIGenOperationID(), deploymentID, eventType, map[string]any{
			"deploymentId":     deploymentID,
			"approvalId":       approval.ID,
			"approvalRevision": approval.Revision,
		})
	}
	apitransport.WriteJSON(w, http.StatusOK, approvalResponse(approval))
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

func (m *Module) attachApproval(
	ctx context.Context,
	response *deploymentapi.Response,
	deploymentID string,
) {
	if m == nil || m.approvals == nil || response == nil {
		return
	}
	approval, err := m.approvals.Current(ctx, deploymentID)
	if err != nil {
		return
	}
	mapped := approvalResponse(approval)
	response.Approval = &mapped
}

func (m *Module) appendAPIEvent(ctx context.Context, deploymentID, eventType string, data any) error {
	if m == nil || m.api.Jobs == nil {
		return errors.New("deployment event store is unavailable")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = m.api.Jobs.AppendEvent(ctx, "deployment", deploymentID, eventType, encoded)
	return err
}

func (m *Module) appendEncodedAPIEvent(ctx context.Context, deploymentID, eventType, data string) error {
	if m == nil || m.api.Jobs == nil {
		return errors.New("deployment event store is unavailable")
	}
	_, err := m.api.Jobs.AppendEvent(ctx, "deployment", deploymentID, eventType, []byte(data))
	return err
}

func (m *Module) recordBestEffortAPIEvent(
	ctx context.Context,
	operationID, deploymentID, eventType string,
	data any,
) {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		logger.ErrorContext(ctx, "deployment command executor is unavailable", "operation_id", operationID, "error", err)
		return
	}
	err = executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if eventType != contract.AuditAction {
				return fmt.Errorf("deployment audit action %q does not match generated action %q", eventType, contract.AuditAction)
			}
			encoded, err := encodeDeploymentAuditPayload(operationID, data)
			if err != nil {
				return err
			}
			return m.appendEncodedAPIEvent(ctx, deploymentID, contract.AuditAction, encoded)
		},
		LogMessage: "deployment audit failed",
		LogAttributes: []slog.Attr{
			slog.String("deployment_id", deploymentID),
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "deployment command contract execution failed", "operation_id", operationID, "error", err)
	}
}

func encodeDeploymentAuditPayload(operationID string, data any) (string, error) {
	values, ok := data.(map[string]any)
	if !ok {
		return "", fmt.Errorf("deployment audit payload has type %T", data)
	}
	str := func(key string) string { value, _ := values[key].(string); return value }
	int64Value := func(key string) int64 {
		switch value := values[key].(type) {
		case int64:
			return value
		case int:
			return int64(value)
		case float64:
			return int64(value)
		default:
			return 0
		}
	}
	switch operationID {
	case string(deploymentgen.GenOperationCancelDeployment):
		return deploymentgen.EncodeGenCancelDeploymentAuditPayload(deploymentgen.GenSchemaDeploymentCancelledAuditPayload{DeploymentId: str("deploymentId"), Status: str("status")})
	case string(deploymentgen.GenOperationRequestDeploymentApproval):
		return deploymentgen.EncodeGenRequestDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalRequestedAuditPayload{DeploymentId: str("deploymentId"), ApprovalId: str("approvalId")})
	case string(deploymentgen.GenOperationApproveDeployment):
		return deploymentgen.EncodeGenApproveDeploymentAuditPayload(deploymentgen.GenSchemaDeploymentApprovalDecisionAuditPayload{DeploymentId: str("deploymentId"), ApprovalId: str("approvalId"), ApprovalRevision: int64Value("approvalRevision")})
	case string(deploymentgen.GenOperationDenyDeploymentApproval):
		return deploymentgen.EncodeGenDenyDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalDecisionAuditPayload{DeploymentId: str("deploymentId"), ApprovalId: str("approvalId"), ApprovalRevision: int64Value("approvalRevision")})
	case string(deploymentgen.GenOperationRevokeDeploymentApproval):
		return deploymentgen.EncodeGenRevokeDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalDecisionAuditPayload{DeploymentId: str("deploymentId"), ApprovalId: str("approvalId"), ApprovalRevision: int64Value("approvalRevision")})
	default:
		return "", fmt.Errorf("deployment operation %q has no map audit payload encoder", operationID)
	}
}

func (m *Module) completePersistedExecution(ctx context.Context, operationID, deploymentID string) {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		logger.ErrorContext(ctx, "deployment command executor is unavailable", "operation_id", operationID, "error", err)
		return
	}
	err = executor.Execute(ctx, operationID, apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error { return nil },
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if contract.Execution == nil || contract.Execution.InitialEvent != contract.AuditAction {
				return fmt.Errorf("deployment command %q audit and initial lifecycle event disagree", operationID)
			}
			if m.api.Jobs == nil {
				return errors.New("deployment event store is unavailable")
			}
			events, err := m.api.Jobs.ListEvents(ctx, contract.Execution.ResourceKind, deploymentID, 0, 200)
			if err != nil {
				return err
			}
			for _, event := range events {
				if event.EventType == contract.Execution.InitialEvent {
					return nil
				}
			}
			return fmt.Errorf("deployment initial lifecycle event %q is unavailable", contract.Execution.InitialEvent)
		},
		LogMessage:    "deployment persisted audit verification failed",
		LogAttributes: []slog.Attr{slog.String("deployment_id", deploymentID)},
	})
	if err != nil {
		logger.ErrorContext(ctx, "deployment command contract execution failed", "operation_id", operationID, "error", err)
	}
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

func deploymentLocation(project, deploymentID string) string {
	return "/api/v1/projects/" + project + "/deployments/" + deploymentID
}

func approvalLocation(project, deploymentID, approvalID string) string {
	return deploymentLocation(project, deploymentID) +
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
