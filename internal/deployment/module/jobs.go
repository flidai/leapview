package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
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
	Coordinator         DeploymentCoordinator
	Authorize           func(context.Context, string, string, string) error
	Reconcile           func(context.Context) error
	ReconcileActivation func(context.Context, apiadapter.Deployment) error
	Events              jobs.EventAppender
	Logger              *slog.Logger
}

// Native activation is continuously renewed while it runs. Keeping the
// individual lease bounded to one minute limits failover delay after a worker
// is lost without imposing a one-minute execution deadline.
const activationJobLeaseTimeout = time.Minute

func (m *Module) JobHandlers() []jobs.Handler {
	return []jobs.Handler{
		jobs.HandlerFunc{JobKind: m.activationExecution().JobKind, Run: m.activate, ExecutionLeaseTimeout: activationJobLeaseTimeout},
		jobs.HandlerFunc{JobKind: "delivery.approval.activate", Run: m.activateApprovedPublication, ExecutionLeaseTimeout: activationJobLeaseTimeout},
	}
}

type approvedPublicationActivator interface {
	ActivateApprovedPublication(context.Context, string, string, string) (apiadapter.Deployment, error)
}

// activateApprovedPublication is the dedicated approval worker. The durable
// job is untrusted input: it reloads effective approval and verifies the full
// immutable publication scope before the coordinator performs its atomic
// lease/CAS activation transaction.
func (m *Module) activateApprovedPublication(ctx context.Context, job jobs.Job) error {
	if m == nil || m.persistence == nil || m.persistence.Repository == nil || m.persistence.Approval == nil || m.jobs.Coordinator == nil {
		return fmt.Errorf("native approval activation worker is unavailable")
	}
	var payload ApprovalActivationJob
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode approval activation payload: %w", err)
	}
	if payload.RequestID == "" || payload.PublicationID == "" || payload.TargetID == "" || payload.GenerationID == "" || payload.CandidateID == "" || payload.RequestDigest == "" || payload.DecisionID == "" || payload.PublicationActorID == "" || payload.RequestedBy == "" || payload.DecidedBy == "" || payload.IdempotencyKey == "" || payload.ExpectedTargetRevision <= 0 || payload.PolicyRevision <= 0 || payload.DecisionRevision <= 0 {
		return fmt.Errorf("invalid approval activation payload")
	}
	publication, err := m.persistence.Repository.Publication(ctx, payload.PublicationID)
	if err != nil {
		return err
	}
	if publication.ActorID != payload.PublicationActorID || publication.TargetID != payload.TargetID || publication.GenerationID != payload.GenerationID || publication.CandidateID != payload.CandidateID || publication.RequestDigest != payload.RequestDigest || publication.ExpectedTargetRevision != payload.ExpectedTargetRevision {
		return deployment.ErrApprovalConflict
	}
	approval, err := m.persistence.Approval.Effective(ctx, payload.RequestID)
	if err != nil && publication.State == "committed" && (errors.Is(err, nativepostgres.ErrApprovalRequired) || errors.Is(err, nativepostgres.ErrApprovalExpired)) {
		// A previous activation may have committed before runtime reconciliation
		// failed. Replay uses the immutable request/decision row (the effective
		// query intentionally excludes committed publications), then Activate
		// verifies the exact terminal operation without a second CAS advance.
		approval, err = m.persistence.Approval.RequestByID(ctx, payload.RequestID)
	}
	if err != nil {
		return err
	}
	decision := approval.LatestDecision
	if decision == nil || decision.Decision != nativepostgres.ApprovalActionApprove || decision.DecisionID != payload.DecisionID || decision.Revision != payload.DecisionRevision {
		return deployment.ErrApprovalRequired
	}
	if approval.PublicationID != payload.PublicationID || approval.TargetID != payload.TargetID || approval.GenerationID != payload.GenerationID || approval.CandidateID != payload.CandidateID || approval.RequestDigest != payload.RequestDigest || approval.ExpectedTargetRevision != payload.ExpectedTargetRevision || approval.PolicyRevision != payload.PolicyRevision || approval.RequestedBy.PrincipalID != payload.RequestedBy || decision.DecidedBy.PrincipalID != payload.DecidedBy {
		return deployment.ErrApprovalConflict
	}
	activator, ok := m.jobs.Coordinator.(approvedPublicationActivator)
	if !ok {
		return fmt.Errorf("native approval activation coordinator is unavailable")
	}
	row, err := activator.ActivateApprovedPublication(ctx, payload.PublicationID, payload.PublicationActorID, payload.IdempotencyKey)
	if err != nil {
		return err
	}
	if m.jobs.ReconcileActivation != nil {
		if err := m.jobs.ReconcileActivation(ctx, row); err != nil {
			return jobs.Retryable(err, time.Second)
		}
	}
	if m.jobs.Reconcile != nil {
		if err := m.jobs.Reconcile(ctx); err != nil {
			logger := m.jobs.Logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.WarnContext(ctx, "reconcile refresh pipelines after approval activation failed", "error", err)
			return jobs.Retryable(err, time.Second)
		}
	}
	return nil
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
	return apigencommand.AsyncExecutionContract{Mode: "async", Guarantee: "transactional", JobKind: "deployment.activate", ResourceKind: "deployment", InitialEvent: "deployment.activation_requested", InitialState: "queued", StatusOperation: "deliveryGenerationStatus", EventsOperation: "deliveryGenerationStatus", Cancellation: "supported"}
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
	row, err = m.jobs.Coordinator.Activate(ctx, apiadapter.ActivateRequest{
		Scope: apiadapter.Scope{Project: payload.Project, DeploymentID: payload.Deployment},
		Actor: payload.Actor, IdempotencyKey: payload.IdempotencyKey,
	})
	if err == nil && m.jobs.ReconcileActivation != nil {
		if reconcileErr := m.jobs.ReconcileActivation(ctx, row); reconcileErr != nil {
			// The native activation CAS commits before runtime/dashboard
			// reconciliation. Keep the durable job retryable so a transient
			// cutover failure does not mark an already-active publication as
			// terminally failed; coordinator replay is idempotent.
			return jobs.Retryable(reconcileErr, time.Second)
		}
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
		logger.ErrorContext(ctx, "deployment activation failed", "deployment", payload.Deployment, "error", err)
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
	operationIDs := []string{"delivery.activate"}
	executions := make(map[string]apigencommand.AsyncExecutionContract, len(operationIDs))
	for _, operationID := range operationIDs {
		execution := mActivationExecutionContract(operationID)
		executions[operationID] = execution
	}
	return executions, nil
}

func mActivationExecutionContract(operationID string) apigencommand.AsyncExecutionContract {
	initialEvent := "deployment.queued"
	if operationID == "delivery.activate" {
		initialEvent = "deployment.activation_requested"
	}
	return apigencommand.AsyncExecutionContract{Mode: "async", Guarantee: "transactional", JobKind: "deployment.activate", ResourceKind: "deployment", InitialEvent: initialEvent, InitialState: "queued", StatusOperation: "deliveryGenerationStatus", EventsOperation: "deliveryGenerationStatus", Cancellation: "supported"}
}

func validateDeploymentJobHandlers(executions map[string]apigencommand.AsyncExecutionContract, handlers []jobs.Handler) error {
	if len(handlers) != 2 {
		return fmt.Errorf("deployment execution requires activation and approval job handlers, got %d", len(handlers))
	}
	kinds := make(map[string]struct{}, len(handlers))
	for _, handler := range handlers {
		if handler == nil || handler.Kind() == "" {
			return fmt.Errorf("deployment job handler kind is required")
		}
		kinds[handler.Kind()] = struct{}{}
	}
	for operationID, execution := range executions {
		if _, ok := kinds[execution.JobKind]; !ok {
			return fmt.Errorf("deployment command %q job kind %q does not match registered handlers", operationID, execution.JobKind)
		}
	}
	if _, ok := kinds["delivery.approval.activate"]; !ok {
		return fmt.Errorf("approval activation job handler is not registered")
	}
	return nil
}
