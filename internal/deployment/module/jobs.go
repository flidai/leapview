package module

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/platform/jobs"
)

type ActivateJob struct {
	Project          string
	Deployment       string
	Actor            string
	Credential       deployment.ApprovalActor
	ApprovalID       string
	ApprovalRevision int64
	IdempotencyKey   string
}

type DeploymentCoordinator interface {
	Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error)
	Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error)
	Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error)
	Cancel(context.Context, apiadapter.Scope) (apiadapter.Deployment, error)
}

// JobConfig contains deployment-owned workflow ports. Authorization is a
// consumer-defined port; schedule reconciliation is an explicit downstream
// notification rather than repository reach-through.
type JobConfig struct {
	Coordinator DeploymentCoordinator
	Authorize   func(context.Context, string, string, []apiadapter.TargetRequest) error
	Reconcile   func(context.Context) error
	Events      jobs.EventAppender
	Logger      *slog.Logger
}

func (m *Module) JobHandlers() []jobs.Handler {
	return []jobs.Handler{jobs.HandlerFunc{JobKind: m.activationExecution().JobKind, Run: m.activate}}
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
	var payload ActivateJob
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	pending, err := m.jobs.Coordinator.Get(ctx, apiadapter.Scope{Project: payload.Project, DeploymentID: payload.Deployment})
	if err != nil {
		return err
	}
	if m.protected {
		if m.api.Releases == nil ||
			payload.Credential.PrincipalID != payload.Actor {
			return deployment.ErrApprovalRequired
		}
		releaseID, _, releaseErr := m.api.Releases.DeploymentRelease(
			ctx,
			payload.Project,
			payload.Deployment,
		)
		if releaseErr != nil {
			return releaseErr
		}
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
	if m.jobs.Authorize != nil {
		if err := m.jobs.Authorize(ctx, payload.Actor, pending.Environment, nil); err != nil {
			m.appendEvent(ctx, payload.Deployment, "deployment.failed", "failed")
			return err
		}
	}
	row, err := m.jobs.Coordinator.Activate(ctx, apiadapter.ActivateRequest{
		Scope: apiadapter.Scope{Project: payload.Project, DeploymentID: payload.Deployment},
		Actor: payload.Actor, IdempotencyKey: payload.IdempotencyKey,
	})
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

func activationWorkflow(
	execution apigencommand.AsyncExecutionContract,
	enqueue bool,
	project,
	deploymentID,
	releaseID string,
	actor deployment.ApprovalActor,
	approval deployment.Approval,
	idempotencyKey string,
) jobs.WorkflowIntent {
	workflow, _ := activationWorkflowForOperation(string(deploymentgen.GenOperationCreateDeployment), execution, enqueue, project, deploymentID, releaseID, actor, approval, idempotencyKey)
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
) (jobs.WorkflowIntent, error) {
	payload, _ := json.Marshal(ActivateJob{
		Project: project, Deployment: deploymentID,
		Actor: actor.PrincipalID, Credential: actor,
		ApprovalID:       approval.ID,
		ApprovalRevision: approval.Revision,
		IdempotencyKey:   idempotencyKey,
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
			WorkloadClass: "control", WorkspaceID: "_node",
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
