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
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/flidai/leapview/internal/release"
)

var (
	ErrPublicationForbidden = errors.New("publication deployment forbidden")
	ErrApprovalForbidden    = errors.New("deployment approval forbidden")
	ErrActivationForbidden  = errors.New("deployment activation forbidden")
)

type PageParams = deploymentapi.PageParams

type ReleasePort interface {
	Get(context.Context, string, string) (release.Release, error)
	PublishCandidate(context.Context, release.PublishCandidateInput) (release.Release, error)
	LinkDeployment(context.Context, string, string, string, string) error
	LinkDeploymentTx(context.Context, transaction.Transaction, string, string, string, string) error
	DeploymentRelease(context.Context, string, string) (string, string, error)
	ListDeploymentIDs(context.Context, string) ([]string, error)
	PriorDeploymentRelease(context.Context, string, string) (string, error)
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
	Workflow  jobs.WorkflowRecorder
	Committer jobs.WorkflowCommitter
}

func (m *Module) CreateDeployment(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	var body deploymentapi.CreateRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	m.createDeployment(w, r, string(deploymentgen.GenOperationCreateDeployment), project, body.ReleaseID, idempotencyKey, "")
}

func (m *Module) createDeployment(w http.ResponseWriter, r *http.Request, operationID, project, releaseID, idempotencyKey, rollbackOf string) {
	execution, err := m.execution(operationID)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "INVALID_COMMAND_CONTRACT", "Deployment execution contract is unavailable", nil)
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
			apitransport.WriteProblem(w, r, http.StatusUnauthorized, "APPROVAL_CREDENTIAL_REQUIRED", "A bounded publication credential is required", nil)
			return
		}
	}
	if m.jobs.Coordinator == nil || m.api.Releases == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	targetRelease, err := m.api.Releases.Get(r.Context(), project, releaseID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	if targetRelease.Status != release.StatusReady {
		apitransport.WriteProblem(w, r, http.StatusConflict, "RELEASE_NOT_READY", "Only ready releases can be deployed", nil)
		return
	}
	evidence, err := publishEvidence(
		targetRelease,
		m.instanceID,
		m.handlerEnvironment(),
	)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	targets := make([]apiadapter.TargetRequest, 0, len(targetRelease.Artifacts))
	for _, artifact := range targetRelease.Artifacts {
		if artifact.ServingStateID == "" {
			apitransport.WriteProblem(w, r, http.StatusConflict, "RELEASE_INCOMPLETE", "Release is missing a workspace artifact", nil)
			return
		}
		targets = append(targets, apiadapter.TargetRequest{Workspace: artifact.WorkspaceID, CandidateID: artifact.ServingStateID})
	}
	if !m.protected && m.jobs.Authorize != nil {
		if err := m.jobs.Authorize(r.Context(), principal.ID, m.handlerEnvironment(), targets); err != nil {
			if errors.Is(err, ErrPublicationForbidden) {
				apitransport.WriteProblem(w, r, http.StatusForbidden, "PUBLICATION_MANAGEMENT_REQUIRED", "MANAGE_PUBLICATIONS is required to activate a workspace containing a public dashboard publication", nil)
				return
			}
			apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "Publication activation authorization could not be evaluated", nil)
			return
		}
	}
	createRequest := apiadapter.CreateRequest{
		Project: project, Environment: m.handlerEnvironment(), Targets: targets, Actor: principal.ID, IdempotencyKey: idempotencyKey,
		ReleaseID: releaseID, Evidence: evidence, RollbackOf: rollbackOf,
	}
	createRequest.Workflow = func(deploymentID string) jobs.WorkflowIntent {
		return activationWorkflow(
			execution,
			!m.protected,
			project,
			deploymentID,
			releaseID,
			deployment.ApprovalActor{PrincipalID: principal.ID},
			deployment.Approval{},
			idempotencyKey+":cutover",
		)
	}
	created, err := m.jobs.Coordinator.Create(r.Context(), createRequest)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	response := deploymentResponse(created, targetRelease)
	if m.protected {
		approval, approvalErr := m.requestApproval(
			r.Context(),
			created,
			releaseID,
			approvalActor,
		)
		if approvalErr != nil {
			writeAPIError(w, r, approvalErr)
			return
		}
		mapped := approvalResponse(approval)
		response.Approval = &mapped
	}
	m.completePersistedExecution(r.Context(), operationID, created.ID)
	w.Header().Set("Location", deploymentLocation(project, created.ID))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func publishEvidence(
	targetRelease release.Release,
	instanceID,
	environment string,
) (apiadapter.PublishEvidence, error) {
	instanceID = strings.TrimSpace(instanceID)
	environment = strings.TrimSpace(environment)
	if targetRelease.Provenance == nil ||
		targetRelease.ProjectID == "" ||
		targetRelease.ProjectDigest == "" ||
		targetRelease.ProjectDigest != targetRelease.Provenance.Artifact.SourceDigest ||
		targetRelease.Provenance.Plan.TargetID != instanceID ||
		targetRelease.Provenance.Plan.Environment != environment {
		return apiadapter.PublishEvidence{}, fmt.Errorf(
			"%w: release provenance does not belong to this target",
			deployment.ErrConflict,
		)
	}
	if err := targetRelease.Provenance.Validate(); err != nil {
		return apiadapter.PublishEvidence{}, fmt.Errorf(
			"%w: invalid release provenance: %v",
			deployment.ErrConflict,
			err,
		)
	}
	planned := make(map[string]release.TargetWorkspacePlan, len(targetRelease.Provenance.Plan.Workspaces))
	for _, workspace := range targetRelease.Provenance.Plan.Workspaces {
		planned[workspace.WorkspaceID] = workspace
	}
	if len(planned) != len(targetRelease.Artifacts) {
		return apiadapter.PublishEvidence{}, fmt.Errorf(
			"%w: release target plan is incomplete",
			deployment.ErrConflict,
		)
	}
	for _, artifact := range targetRelease.Artifacts {
		workspace, ok := planned[artifact.WorkspaceID]
		if !ok || workspace.ServingStateID != artifact.ServingStateID ||
			workspace.ArtifactDigest != publishArtifactDigest(artifact.ActualDigest) ||
			artifact.ActualDigest != artifact.ExpectedDigest {
			return apiadapter.PublishEvidence{}, fmt.Errorf(
				"%w: release target plan drifted for workspace %q",
				deployment.ErrConflict,
				artifact.WorkspaceID,
			)
		}
	}
	return apiadapter.PublishEvidence{
		ReleaseDigest:     targetRelease.Provenance.Digest,
		ArtifactDigest:    targetRelease.Provenance.ArtifactDigest,
		PlanDigest:        targetRelease.Provenance.PlanDigest,
		CandidateID:       targetRelease.Provenance.Candidate.ID,
		CandidateRevision: targetRelease.Provenance.Candidate.Revision,
		TargetID:          targetRelease.Provenance.Plan.TargetID,
		BaseGeneration:    targetRelease.Provenance.Plan.BaseGeneration,
		RuntimeVersion:    targetRelease.Provenance.Plan.RuntimeVersion,
		PolicyDigest:      targetRelease.Provenance.Plan.PolicyDigest,
	}, nil
}

func publishArtifactDigest(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "sha256:") {
		return value
	}
	return "sha256:" + value
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
	targetRelease, err := m.api.Releases.Get(r.Context(), project, releaseID)
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
		targetRelease, err := m.api.Releases.Get(r.Context(), project, releaseID)
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
	if m.jobs.Coordinator == nil || m.api.Releases == nil || m.api.Jobs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	releaseID, _, err := m.api.Releases.DeploymentRelease(r.Context(), project, deploymentID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	targetRelease, err := m.api.Releases.Get(r.Context(), project, releaseID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	if err := m.api.Jobs.Cancel(r.Context(), "deployment:"+deploymentID+":activate"); err != nil && !errors.Is(err, jobs.ErrConflict) {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_QUEUE_UNAVAILABLE", "Deployment cancellation could not be persisted", nil)
		return
	}
	row, err := m.jobs.Coordinator.Cancel(r.Context(), apiadapter.Scope{Project: project, DeploymentID: deploymentID})
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	m.recordBestEffortAPIEvent(
		r.Context(), string(deploymentgen.GenOperationCancelDeployment), deploymentID,
		deploymentCancelledAuditAction, map[string]any{"deploymentId": deploymentID, "status": "cancelled"},
	)
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
	if m.jobs.Coordinator == nil || m.api.Releases == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	releaseID, _, err := m.api.Releases.DeploymentRelease(
		r.Context(),
		project,
		deploymentID,
	)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	row, err := m.jobs.Coordinator.Get(
		r.Context(),
		apiadapter.Scope{Project: project, DeploymentID: deploymentID},
	)
	if err != nil {
		writeAPIError(w, r, err)
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
		writeAPIError(
			w,
			r,
			fmt.Errorf(
				"%w: deployment is %s and cannot be retried",
				deployment.ErrConflict,
				row.Status,
			),
		)
		return
	}
	m.createDeployment(w, r, string(deploymentgen.GenOperationRetryDeployment), project, releaseID, idempotencyKey, "")
}

func (m *Module) RollbackDeployment(w http.ResponseWriter, r *http.Request, project, deploymentID, idempotencyKey string) {
	if m.api.Releases == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_SERVICE_UNAVAILABLE", "Deployment service is unavailable", nil)
		return
	}
	releaseID, err := m.api.Releases.PriorDeploymentRelease(r.Context(), project, deploymentID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	m.createDeployment(w, r, string(deploymentgen.GenOperationRollbackDeployment), project, releaseID, idempotencyKey, deploymentID)
}

func (m *Module) RequestDeploymentApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	_ string,
) {
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "APPROVAL_CREDENTIAL_REQUIRED", "A bounded publication credential is required", nil)
		return
	}
	row, releaseID, ok := m.approvalDeployment(w, r, project, deploymentID)
	if !ok {
		return
	}
	approval, err := m.requestApproval(r.Context(), row, releaseID, actor)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	m.recordBestEffortAPIEvent(r.Context(), string(deploymentgen.GenOperationRequestDeploymentApproval), deploymentID, deploymentApprovalRequestedAuditAction, map[string]any{
		"deploymentId": deploymentID,
		"approvalId":   approval.ID,
	})
	w.Header().Set("Location", approvalLocation(project, deploymentID, approval.ID))
	apitransport.WriteJSON(w, http.StatusCreated, approvalResponse(approval))
}

func (m *Module) ApproveDeployment(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID,
	_ string,
) {
	m.transitionApproval(w, r, project, deploymentID, approvalID, approvalDecisionApprove)
}

func (m *Module) DenyDeploymentApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID,
	_ string,
) {
	m.transitionApproval(w, r, project, deploymentID, approvalID, approvalDecisionDeny)
}

func (m *Module) RevokeDeploymentApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID,
	_ string,
) {
	m.transitionApproval(w, r, project, deploymentID, approvalID, approvalDecisionRevoke)
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
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "ACTIVATION_CREDENTIAL_REQUIRED", "A bounded activation credential is required", nil)
		return
	}
	row, releaseID, ok := m.approvalDeployment(w, r, project, deploymentID)
	if !ok {
		return
	}
	approval, err := m.authorizeApprovedActivation(r.Context(), row, releaseID, actor)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	execution, err := m.execution(string(deploymentgen.GenOperationActivateDeployment))
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "INVALID_COMMAND_CONTRACT", "Deployment execution contract is unavailable", nil)
		return
	}
	workflow := activationWorkflow(
		execution,
		true,
		project,
		deploymentID,
		releaseID,
		actor,
		approval,
		idempotencyKey,
	)
	if m.api.Committer == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_QUEUE_UNAVAILABLE", "Deployment activation queue is unavailable", nil)
		return
	}
	if err := m.api.Committer.CommitWorkflow(r.Context(), workflow); err != nil && !errors.Is(err, jobs.ErrConflict) {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_QUEUE_UNAVAILABLE", "Deployment activation could not be persisted", nil)
		return
	}
	targetRelease, err := m.api.Releases.Get(r.Context(), project, releaseID)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	response := deploymentResponse(row, targetRelease)
	mapped := approvalResponse(approval)
	response.Approval = &mapped
	m.completePersistedExecution(r.Context(), string(deploymentgen.GenOperationActivateDeployment), deploymentID)
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

func (h deploymentAPIGenHandler) ListDeployments(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	h.Module.ListDeployments(w, r, project, deploymentapi.PageParams{Limit: limit, PageToken: pageToken})
}

func (h deploymentAPIGenHandler) ListDeploymentEvents(w http.ResponseWriter, r *http.Request, project, deploymentID string, limit *int32, pageToken *string) {
	h.Module.ListDeploymentEvents(w, r, project, deploymentID, deploymentapi.PageParams{Limit: limit, PageToken: pageToken})
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
) (apiadapter.Deployment, string, bool) {
	if m == nil || m.approvals == nil ||
		m.jobs.Coordinator == nil || m.api.Releases == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEPLOYMENT_APPROVAL_UNAVAILABLE", "Deployment approvals are unavailable", nil)
		return apiadapter.Deployment{}, "", false
	}
	releaseID, _, err := m.api.Releases.DeploymentRelease(
		r.Context(),
		project,
		deploymentID,
	)
	if err != nil {
		writeAPIError(w, r, err)
		return apiadapter.Deployment{}, "", false
	}
	row, err := m.jobs.Coordinator.Get(
		r.Context(),
		apiadapter.Scope{Project: project, DeploymentID: deploymentID},
	)
	if err != nil {
		writeAPIError(w, r, err)
		return apiadapter.Deployment{}, "", false
	}
	if row.Status != apiadapter.StatusPending {
		writeAPIError(
			w,
			r,
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
		return deployment.Approval{}, errors.New(
			"deployment approvals are unavailable",
		)
	}
	return m.approvals.Request(ctx, deployment.ApprovalRequest{
		ProjectID: row.Project, DeploymentID: row.ID,
		Environment:   row.Environment,
		RequestDigest: row.RequestDigest,
		ReleaseID:     releaseID, RequestedBy: actor,
	})
}

type approvalDecision uint8

const (
	approvalDecisionApprove approvalDecision = iota + 1
	approvalDecisionDeny
	approvalDecisionRevoke
)

func (m *Module) transitionApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	deploymentID,
	approvalID string,
	decision approvalDecision,
) {
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
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "APPROVAL_CREDENTIAL_REQUIRED", "A bounded approval credential is required", nil)
		return
	}
	if _, _, ok := m.approvalDeployment(w, r, project, deploymentID); !ok {
		return
	}
	transition := deployment.ApprovalTransition{
		ProjectID: project, DeploymentID: deploymentID,
		ApprovalID:       approvalID,
		ExpectedRevision: body.ExpectedRevision,
		Actor:            actor,
	}
	var (
		approval deployment.Approval
		err      error
	)
	switch decision {
	case approvalDecisionApprove:
		approval, err = m.approvals.Approve(r.Context(), transition)
	case approvalDecisionDeny:
		approval, err = m.approvals.Deny(r.Context(), transition)
	case approvalDecisionRevoke:
		approval, err = m.approvals.Revoke(r.Context(), transition)
	default:
		err = deployment.ErrApprovalInvalid
	}
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	eventType := deploymentApprovalRevokedAuditAction
	operationID := string(deploymentgen.GenOperationRevokeDeploymentApproval)
	switch decision {
	case approvalDecisionApprove:
		eventType = deploymentApprovedAuditAction
		operationID = string(deploymentgen.GenOperationApproveDeployment)
	case approvalDecisionDeny:
		eventType = deploymentDeniedAuditAction
		operationID = string(deploymentgen.GenOperationDenyDeploymentApproval)
	}
	m.recordBestEffortAPIEvent(r.Context(), operationID, deploymentID, eventType, map[string]any{
		"deploymentId":     deploymentID,
		"approvalId":       approval.ID,
		"approvalRevision": approval.Revision,
	})
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
		return deployment.Approval{}, err
	}
	if m.authorizeApproval == nil {
		return deployment.Approval{}, errors.New(
			"approval authorization is unavailable",
		)
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
		return deployment.Approval{}, err
	}
	if m.authorizeActivation == nil {
		return deployment.Approval{}, errors.New(
			"activation authorization is unavailable",
		)
	}
	if err := m.authorizeActivation(
		ctx,
		actor,
		row.Project,
		row.Environment,
	); err != nil {
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
			return m.appendAPIEvent(ctx, deploymentID, contract.AuditAction, data)
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
		Evidence: publishEvidenceResponse(targetRelease), Status: status,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, Targets: make([]deploymentapi.TargetResponse, 0, len(row.Targets)),
		Connections: make([]deploymentapi.ConnectionResponse, 0, len(row.Connections)),
	}
	for _, target := range row.Targets {
		stateID := target.CandidateID
		mapped := deploymentapi.TargetResponse{WorkspaceID: target.Workspace, ServingStateID: &stateID, Status: string(target.Status)}
		if target.PriorCandidateID != "" {
			mapped.PriorServingStateID = &target.PriorCandidateID
		}
		if target.Error != "" {
			mapped.Error = &target.Error
		}
		result.Targets = append(result.Targets, mapped)
	}
	for _, connection := range row.Connections {
		mapped := deploymentapi.ConnectionResponse{ConnectionID: connection.Connection, RevisionID: connection.RevisionID}
		if connection.PriorRevisionID != "" {
			mapped.PriorRevisionID = &connection.PriorRevisionID
		}
		result.Connections = append(result.Connections, mapped)
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
		return deploymentapi.PublishEvidenceResponse{
			Workspaces: []deploymentapi.WorkspacePublishEvidence{},
		}
	}
	response := deploymentapi.PublishEvidenceResponse{
		ReleaseDigest:     targetRelease.Provenance.Digest,
		ArtifactDigest:    targetRelease.Provenance.ArtifactDigest,
		PlanDigest:        targetRelease.Provenance.PlanDigest,
		CandidateID:       targetRelease.Provenance.Candidate.ID,
		CandidateRevision: targetRelease.Provenance.Candidate.Revision,
		TargetID:          targetRelease.Provenance.Plan.TargetID,
		BaseGeneration:    targetRelease.Provenance.Plan.BaseGeneration,
		RuntimeVersion:    targetRelease.Provenance.Plan.RuntimeVersion,
		PolicyDigest:      targetRelease.Provenance.Plan.PolicyDigest,
		Workspaces: make(
			[]deploymentapi.WorkspacePublishEvidence,
			0,
			len(targetRelease.Provenance.Plan.Workspaces),
		),
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
	for _, workspace := range targetRelease.Provenance.Plan.Workspaces {
		mapped := deploymentapi.WorkspacePublishEvidence{
			WorkspaceID: workspace.WorkspaceID, ServingStateID: workspace.ServingStateID,
			ArtifactDigest: workspace.ArtifactDigest,
			DataRevision:   workspace.DataRevision, DataMode: string(workspace.DataMode),
			ManagedDataPins: make(
				[]deploymentapi.ManagedDataPinEvidence,
				len(workspace.ManagedDataPins),
			),
			Bindings: make(
				[]deploymentapi.BindingEvidence,
				len(workspace.Bindings),
			),
		}
		for index, pin := range workspace.ManagedDataPins {
			mapped.ManagedDataPins[index] = deploymentapi.ManagedDataPinEvidence{
				ConnectionID: pin.ConnectionID, RevisionID: pin.RevisionID,
			}
		}
		for index, binding := range workspace.Bindings {
			mapped.Bindings[index] = deploymentapi.BindingEvidence{
				BindingID: binding.BindingID, LogicalConnection: binding.LogicalConnection,
				ConnectorKind: binding.ConnectorKind, Revision: binding.Revision,
				ValidatedVersion: binding.ValidatedVersion, EndpointConfigHash: binding.EndpointConfigHash,
			}
		}
		response.Workspaces = append(response.Workspaces, mapped)
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

func writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
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
