package module

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

type ActivateJob struct {
	Project                  string
	Deployment               string
	Actor                    string
	Credential               deployment.ApprovalActor
	ApprovalID               string
	ApprovalRevision         int64
	IdempotencyKey           string
	Bootstrap                bool
	Rollback                 bool
	ExpectedBaseGenerationID string
	ExpectedTargetRevision   int64
}

type DeploymentCoordinator interface {
	Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error)
	Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error)
	Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error)
	CancelRequest(context.Context, apiadapter.CancelRequest) (apiadapter.Deployment, error)
}

// JobConfig contains deployment-owned workflow ports. Authorization is a
// consumer-defined port; schedule reconciliation is an explicit downstream
// notification rather than repository reach-through.
type JobConfig struct {
	Coordinator DeploymentCoordinator
	Authorize   func(context.Context, string, string, string) error
	Reconcile   func(context.Context) error
	Events      jobs.EventAppender
	Logger      *slog.Logger
}

func (m *Module) JobHandlers() []jobs.Handler {
	return []jobs.Handler{jobs.HandlerFunc{JobKind: m.activationExecution().JobKind, Run: m.activate, ExecutionLeaseTimeout: 5 * time.Minute}}
}

func (m *Module) execution(operationID string) (apigencommand.AsyncExecutionContract, error) {
	if m == nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("deployment module is unavailable")
	}
	if m.executions == nil {
		executions, err := loadDeploymentExecutionContracts()
		if err != nil {
			return apigencommand.AsyncExecutionContract{}, err
		}
		m.executions = executions
	}
	execution, ok := m.executions[operationID]
	if !ok {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("deployment execution contract %q is unavailable", operationID)
	}
	return execution, nil
}

func (m *Module) activationExecution() apigencommand.AsyncExecutionContract {
	execution, _ := m.execution(string(deploymentgen.GenOperationActivateDeployment))
	return execution
}

func (m *Module) activate(ctx context.Context, job jobs.Job) error {
	if m.jobs.Coordinator == nil {
		return fmt.Errorf("deployment coordinator is unavailable")
	}
	logger := m.jobs.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var payload ActivateJob
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	logger.InfoContext(ctx, "deployment activation started", "deployment", payload.Deployment, "bootstrap", payload.Bootstrap, "rollback", payload.Rollback)
	pending, err := m.jobs.Coordinator.Get(ctx, apiadapter.Scope{Project: payload.Project, DeploymentID: payload.Deployment})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "deployment activation loaded pending row", "deployment", payload.Deployment, "status", pending.Status, "generation", pending.GenerationID)
	releaseID := ""
	if m.sealedCoordinator != nil && m.api.Releases != nil {
		resolved, _, resolveErr := m.api.Releases.DeploymentRelease(ctx, payload.Project, payload.Deployment)
		if resolveErr != nil {
			return resolveErr
		}
		releaseID = resolved
	}
	if payload.Bootstrap {
		if !m.protected || m.bootstrapPolicies == nil || m.authorizeBootstrap == nil || payload.Credential.CredentialClass != deployment.CredentialClassAPIToken || payload.Credential.PrincipalID != payload.Actor {
			return deployment.ErrApprovalRequired
		}
		policy, policyErr := m.bootstrapPolicies.BootstrapActivationPolicy(ctx, payload.Deployment)
		if policyErr != nil || policy.ProjectID.String() != pending.Project || string(policy.Environment) != pending.Environment || policy.DeploymentID != pending.ID || policy.RequestDigest != pending.RequestDigest || policy.ActorID != payload.Credential.PrincipalID || policy.CredentialID != payload.Credential.CredentialID || !policy.CredentialExpiresAt.Equal(payload.Credential.CredentialExpiresAt) {
			if policyErr != nil {
				return policyErr
			}
			return deployment.ErrBootstrapPolicyConflict
		}
		if err := m.authorizeBootstrap(ctx, policy); err != nil {
			m.appendEvent(ctx, payload.Deployment, "deployment.authorization_failed", "failed")
			return err
		}
	} else if m.protected {
		if m.api.Releases == nil ||
			payload.Credential.PrincipalID != payload.Actor {
			return deployment.ErrApprovalRequired
		}
		resolvedReleaseID, _, releaseErr := m.api.Releases.DeploymentRelease(
			ctx,
			payload.Project,
			payload.Deployment,
		)
		if releaseErr != nil {
			return releaseErr
		}
		releaseID = resolvedReleaseID
		approval, approvalErr := m.authorizeApprovedActivation(
			ctx,
			pending,
			releaseID,
			payload.Credential,
		)
		if approvalErr != nil {
			m.appendEvent(
				ctx,
				payload.Deployment,
				"deployment.authorization_failed",
				"failed",
			)
			return approvalErr
		}
		if approval.ID != payload.ApprovalID ||
			approval.Revision != payload.ApprovalRevision {
			return deployment.ErrApprovalConflict
		}
	}
	if !payload.Bootstrap && m.jobs.Authorize != nil {
		if err := m.jobs.Authorize(ctx, payload.Actor, pending.Environment, pending.GenerationID); err != nil {
			m.appendEvent(ctx, payload.Deployment, "deployment.failed", "failed")
			return err
		}
	}
	var row apiadapter.Deployment
	if m.sealedCoordinator != nil {
		if m.sealedPublishRequest == nil || m.sealedRollbackRequest == nil || m.sealedActivationMarker == nil {
			return fmt.Errorf("sealed publication lifecycle is incomplete")
		}
		if payload.Rollback {
			request, resolveErr := m.sealedRollbackRequest(ctx, pending, releaseID, payload.Credential, payload.ExpectedBaseGenerationID, payload.ExpectedTargetRevision)
			if resolveErr != nil {
				return resolveErr
			}
			result, publishErr := m.sealedCoordinator.Rollback(ctx, request)
			if publishErr != nil {
				m.appendEvent(ctx, payload.Deployment, "deployment.failed", "failed")
				return publishErr
			}
			activation := sealedActivationInput(pending, payload.Actor, result.CatalogDigest)
			activated, markErr := m.sealedActivationMarker(ctx, activation)
			if markErr != nil {
				return markErr
			}
			if m.sealedReconcile != nil {
				if reconcileErr := m.sealedReconcile(ctx, pending.GenerationID); reconcileErr != nil {
					return reconcileErr
				}
			}
			row = mapSealedDeployment(activated)
		} else {
			logger.InfoContext(ctx, "deployment activation sealed publish starting", "deployment", payload.Deployment, "bootstrap", payload.Bootstrap)
			request, resolveErr := m.sealedPublishRequest(ctx, pending, releaseID, payload.Credential, payload.Bootstrap)
			if resolveErr != nil {
				return resolveErr
			}
			_, publishErr := m.sealedCoordinator.Publish(ctx, request)
			if publishErr != nil {
				logger.ErrorContext(ctx, "deployment activation sealed publish failed", "deployment", payload.Deployment, "error", publishErr)
				m.appendEvent(ctx, payload.Deployment, "deployment.failed", "failed")
				return publishErr
			}
			logger.InfoContext(ctx, "deployment activation sealed publish committed", "deployment", payload.Deployment)
			activation := sealedActivationInput(pending, payload.Actor, request.Seal.CatalogDigest)
			logger.InfoContext(ctx, "deployment activation sealed marker starting", "deployment", payload.Deployment)
			activated, markErr := m.sealedActivationMarker(ctx, activation)
			if markErr != nil {
				logger.ErrorContext(ctx, "deployment activation sealed marker failed", "deployment", payload.Deployment, "error", markErr)
				return markErr
			}
			logger.InfoContext(ctx, "deployment activation sealed marker committed", "deployment", payload.Deployment)
			if m.sealedReconcile != nil {
				logger.InfoContext(ctx, "deployment activation sealed reconcile starting", "deployment", payload.Deployment)
				if reconcileErr := m.sealedReconcile(ctx, pending.GenerationID); reconcileErr != nil {
					logger.ErrorContext(ctx, "deployment activation sealed reconcile failed", "deployment", payload.Deployment, "error", reconcileErr)
					return reconcileErr
				}
				logger.InfoContext(ctx, "deployment activation sealed reconcile completed", "deployment", payload.Deployment)
			}
			row = mapSealedDeployment(activated)
		}
	} else {
		if m.requireSealedCoordinator {
			return fmt.Errorf("sealed publication coordinator is unavailable")
		}
		row, err = m.jobs.Coordinator.Activate(ctx, apiadapter.ActivateRequest{
			Scope: apiadapter.Scope{Project: payload.Project, DeploymentID: payload.Deployment},
			Actor: payload.Actor, IdempotencyKey: payload.IdempotencyKey,
		})
	}
	if err == nil && m.jobs.Reconcile != nil {
		if reconcileErr := m.jobs.Reconcile(ctx); reconcileErr != nil {
			logger := m.jobs.Logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.WarnContext(ctx, "reconcile refresh pipelines after deployment activation failed", "error", reconcileErr)
		}
	}
	event := "deployment.active"
	if err != nil {
		event = "deployment.failed"
	}
	m.appendActivationEvent(
		ctx,
		payload.Deployment,
		event,
		payload.Actor,
		row,
	)
	return err
}

func sealedActivationInput(pending apiadapter.Deployment, actor, verificationDigest string) deployment.ActivationInput {
	identity := projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(pending.Project), Environment: pending.Environment, GenerationID: pending.GenerationID}
	return deployment.ActivationInput{
		DeploymentID: pending.ID, ServingIdentity: identity, ArtifactDigest: pending.ArtifactDigest,
		PriorGenerationID: pending.PriorGenerationID, ActivationPrincipal: actor, VerificationDigest: verificationDigest,
	}
}

func mapSealedDeployment(row deployment.Deployment) apiadapter.Deployment {
	return apiadapter.Deployment{
		ID: row.ID, Project: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment,
		GenerationID: row.ServingIdentity.GenerationID, ArtifactDigest: row.ArtifactDigest,
		PriorGenerationID: row.PriorGenerationID, RequestDigest: row.RequestDigest, Status: apiadapter.Status(row.Status),
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ActivatedAt: row.ActivatedAt,
		ActivationPrincipal: row.ActivationPrincipal, VerificationDigest: row.VerificationDigest, VerifiedAt: row.VerifiedAt, Error: row.Error,
	}
}

func activationWorkflow(
	execution apigencommand.AsyncExecutionContract,
	enqueue bool,
	project,
	deploymentID,
	releaseID string,
	actor deployment.ApprovalActor,
	approval deployment.Approval,
	idempotencyKey string,
	environment string,
) jobs.WorkflowIntent {
	workflow, _ := activationWorkflowForOperation(string(deploymentgen.GenOperationCreateDeployment), execution, enqueue, project, deploymentID, releaseID, actor, approval, idempotencyKey, environment)
	return workflow
}

func activationWorkflowForOperation(
	operationID string,
	execution apigencommand.AsyncExecutionContract,
	enqueue bool,
	project,
	deploymentID,
	releaseID string,
	actor deployment.ApprovalActor,
	approval deployment.Approval,
	idempotencyKey string,
	environment string,
) (jobs.WorkflowIntent, error) {
	return activationWorkflowForOperationWithBootstrap(operationID, execution, enqueue, project, deploymentID, releaseID, actor, approval, idempotencyKey, false, environment)
}

func activationWorkflowForOperationWithBootstrap(
	operationID string,
	execution apigencommand.AsyncExecutionContract,
	enqueue bool,
	project,
	deploymentID,
	releaseID string,
	actor deployment.ApprovalActor,
	approval deployment.Approval,
	idempotencyKey string,
	bootstrap bool,
	environment string,
) (jobs.WorkflowIntent, error) {
	return activationWorkflowForOperationWithRollbackFence(operationID, execution, enqueue, project, deploymentID, releaseID, actor, approval, idempotencyKey, bootstrap, "", 0, false, environment)
}

func activationWorkflowForOperationWithRollbackFence(
	operationID string,
	execution apigencommand.AsyncExecutionContract,
	enqueue bool,
	project,
	deploymentID,
	releaseID string,
	actor deployment.ApprovalActor,
	approval deployment.Approval,
	idempotencyKey string,
	bootstrap bool,
	expectedBaseGenerationID string,
	expectedTargetRevision int64,
	rollbackIntent bool,
	environment string,
) (jobs.WorkflowIntent, error) {
	if err := servingstate.ValidateEnvironment(servingstate.Environment(environment)); err != nil || strings.TrimSpace(environment) != environment {
		return jobs.WorkflowIntent{}, fmt.Errorf("deployment environment is required")
	}
	payload, _ := json.Marshal(ActivateJob{
		Project: project, Deployment: deploymentID,
		Actor: actor.PrincipalID, Credential: actor,
		ApprovalID:       approval.ID,
		ApprovalRevision: approval.Revision,
		IdempotencyKey:   idempotencyKey, Bootstrap: bootstrap,
		Rollback:                 operationID == string(deploymentgen.GenOperationRollbackDeployment) || rollbackIntent,
		ExpectedBaseGenerationID: expectedBaseGenerationID,
		ExpectedTargetRevision:   expectedTargetRevision,
	})
	queuedPayload := deploymentgen.GenSchemaDeploymentQueuedAuditPayload{
		DeploymentId: deploymentID, ProjectId: project, ReleaseId: releaseID, Status: execution.InitialState,
	}
	var eventString string
	var eventErr error
	switch operationID {
	case string(deploymentgen.GenOperationCreateDeployment):
		eventString, eventErr = deploymentgen.EncodeGenCreateDeploymentAuditPayload(queuedPayload)
	case string(deploymentgen.GenOperationRetryDeployment):
		eventString, eventErr = deploymentgen.EncodeGenRetryDeploymentAuditPayload(queuedPayload)
	case string(deploymentgen.GenOperationRollbackDeployment):
		eventString, eventErr = deploymentgen.EncodeGenRollbackDeploymentAuditPayload(queuedPayload)
	case string(deploymentgen.GenOperationActivateDeployment):
		eventString, eventErr = deploymentgen.EncodeGenActivateDeploymentAuditPayload(queuedPayload)
	case string(deploymentgen.GenOperationPublishProjectCandidate):
		eventString, eventErr = deploymentgen.EncodeGenPublishProjectCandidateAuditPayload(queuedPayload)
	default:
		return jobs.WorkflowIntent{}, fmt.Errorf("deployment operation %q has no queued audit payload encoder", operationID)
	}
	if eventErr != nil {
		return jobs.WorkflowIntent{}, eventErr
	}
	event := []byte(eventString)
	workflow := jobs.WorkflowIntent{
		Event: jobs.EventInput{
			Key:          execution.InitialEvent,
			ResourceKind: execution.ResourceKind, ResourceID: deploymentID,
			EventType: execution.InitialEvent, Data: event,
		},
	}
	if enqueue {
		workflow.Job = jobs.EnqueueInput{
			ID:            execution.ResourceKind + ":" + deploymentID + ":activate",
			Kind:          execution.JobKind,
			WorkloadClass: "control", PrincipalID: actor.PrincipalID, GroupIDs: nil, EstimatedMemoryBytes: 16 << 20,
			PartitionKey: "deployment:" + project + ":" + environment,
			ResourceKind: execution.ResourceKind, ResourceID: deploymentID,
			Payload: payload,
		}
	}
	return workflow, nil
}

func (m *Module) appendEvent(ctx context.Context, deploymentID, event, status string) {
	if m.jobs.Events == nil {
		return
	}
	data, _ := json.Marshal(map[string]any{"deploymentId": deploymentID, "status": status})
	_, _ = m.jobs.Events.AppendEvent(context.WithoutCancel(ctx), m.activationExecution().ResourceKind, deploymentID, event, data)
}

func (m *Module) appendActivationEvent(
	ctx context.Context,
	deploymentID,
	event,
	actor string,
	row apiadapter.Deployment,
) {
	if m.jobs.Events == nil {
		return
	}
	data, _ := json.Marshal(map[string]any{
		"deploymentId":        deploymentID,
		"status":              row.Status,
		"activationPrincipal": actor,
		"verificationDigest":  row.VerificationDigest,
	})
	_, _ = m.jobs.Events.AppendEvent(
		context.WithoutCancel(ctx),
		m.activationExecution().ResourceKind,
		deploymentID,
		event,
		data,
	)
}

func loadDeploymentExecutionContracts() (map[string]apigencommand.AsyncExecutionContract, error) {
	operationIDs := []string{
		string(deploymentgen.GenOperationCreateDeployment),
		string(deploymentgen.GenOperationRetryDeployment),
		string(deploymentgen.GenOperationRollbackDeployment),
		string(deploymentgen.GenOperationActivateDeployment),
		string(deploymentgen.GenOperationPublishProjectCandidate),
	}
	executions := make(map[string]apigencommand.AsyncExecutionContract, len(operationIDs))
	for _, operationID := range operationIDs {
		contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(operationID)
		if !ok {
			return nil, fmt.Errorf("deployment command contract %q is unavailable", operationID)
		}
		if err := contract.Validate(); err != nil {
			return nil, fmt.Errorf("validate deployment command contract %q: %w", operationID, err)
		}
		if contract.Execution == nil {
			return nil, fmt.Errorf("deployment command %q async execution contract is unavailable", operationID)
		}
		execution := *contract.Execution
		if execution.Guarantee != "transactional" {
			return nil, fmt.Errorf("deployment command %q requires transactional execution, got %q", operationID, execution.Guarantee)
		}
		if execution.ResourceKind != "deployment" || execution.InitialState != "queued" {
			return nil, fmt.Errorf("deployment command %q has incompatible initial lifecycle %q/%q", operationID, execution.ResourceKind, execution.InitialState)
		}
		if execution.StatusOperation != string(deploymentgen.GenOperationGetDeployment) || execution.EventsOperation != string(deploymentgen.GenOperationListDeploymentEvents) {
			return nil, fmt.Errorf("deployment command %q has incompatible lifecycle operations", operationID)
		}
		if execution.Cancellation != "supported" {
			return nil, fmt.Errorf("deployment command %q cancellation policy %q is not implemented", operationID, execution.Cancellation)
		}
		executions[operationID] = execution
	}
	return executions, nil
}

func validateDeploymentJobHandlers(executions map[string]apigencommand.AsyncExecutionContract, handlers []jobs.Handler) error {
	if len(handlers) != 1 {
		return fmt.Errorf("deployment execution requires exactly one job handler, got %d", len(handlers))
	}
	kind := handlers[0].Kind()
	for operationID, execution := range executions {
		if execution.JobKind != kind {
			return fmt.Errorf("deployment command %q job kind %q does not match registered handler %q", operationID, execution.JobKind, kind)
		}
	}
	return nil
}
