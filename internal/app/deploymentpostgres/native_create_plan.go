package deploymentpostgres

// Native CreatePlan is the application-owned orchestration boundary for the
// clean PostgreSQL delivery authority. Planning intentionally stops at
// immutable compiler and durable non-secret binding evidence: no serving
// artifact is materialized, no credential pool or DuckLake writer is opened,
// and no physical work is started here.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	nativePlanOperationType   = "delivery.plan.create"
	maxNativePlanOutcomeBytes = 16 << 10
)

// NativeReleaseArtifactInspector is deliberately narrower than
// release.CandidateArtifactPreparer. A planner may inspect immutable retained
// compiler evidence, but cannot receive a materialization or hydration method.
type NativeReleaseArtifactInspector interface {
	InspectCandidateArtifacts(context.Context, release.CandidateArtifactRequest) (release.CandidateArtifactSet, error)
}

// NativeDeliveryPolicyResolver selects target-owned governance for the exact
// operation being planned. Protected code and policy changes may require an
// approval even when an already-authorized restatement does not.
type NativeDeliveryPolicyResolver func(deployment.DeliveryOperationKind) (runtimefactory.CandidateDeliveryPolicy, error)

type nativeDeliveryEventReader interface {
	GetDeliveryEvent(context.Context, deploymentnative.Tx, deploymentmodule.NativeDeliveryEventInput) (deploymentnative.Event, error)
}

type nativeDeliveryAuditReader interface {
	GetMutationAudit(context.Context, deploymentnative.Tx, deploymentmodule.NativeDeliveryAuditInput) (deploymentnative.AuditEvent, error)
}

type nativeOperationLookup interface {
	Lookup(context.Context, deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationRecord, bool, error)
}

// NativeCreatePlanConfig contains the authorities needed for one native plan
// command. Events, audit, operations, and the plan repository all receive the
// exact same caller-owned PostgreSQL transaction.
type NativeCreatePlanConfig struct {
	Repository      *deploymentnative.Repository
	Sources         project.CandidateSourceAttestationReader
	Artifacts       NativeReleaseArtifactInspector
	BindingEvidence deployment.CandidateConnectionEvidenceResolver

	// ArtifactInspector is an expressive alias retained for composition code
	// that names the read-only phase explicitly. Artifacts takes precedence.
	ArtifactInspector NativeReleaseArtifactInspector
	RuntimeVersion    string
	Policy            runtimefactory.CandidateDeliveryPolicy
	PolicyResolver    NativeDeliveryPolicyResolver
	Clock             func() time.Time

	Events     deploymentmodule.NativeDeliveryEventAppender
	Audit      deploymentmodule.NativeDeliveryAuditAppender
	Workflow   deploymentmodule.NativeDeliveryWorkflowRecorder
	Operations deploymentmodule.NativeOperationAuthority
	// WorkflowFactory is optional. If supplied, its intent is recorded in the
	// same transaction after the plan event and audit have been appended.
	WorkflowFactory func(NativePlanWorkflowInput) (jobs.WorkflowIntent, error)
}

// NativePlanWorkflowInput is the value-only workflow projection. It prevents
// workflow callbacks from receiving a repository, transaction, or artifact
// authority while still allowing resource-specific follow-up work.
type NativePlanWorkflowInput struct {
	Plan        deploymentmodule.NativeDeliveryPlan
	OperationID string
	ProjectID   projectgraph.ResourceID
	TargetID    string
}

// NativeCreatePlanCoordinator implements the module's native mutation port.
// BuildPlan is present so composition can install one bounded port today; it
// fails closed until native physical build orchestration is wired.
type NativeCreatePlanCoordinator struct {
	repository      *deploymentnative.Repository
	sources         project.CandidateSourceAttestationReader
	artifacts       NativeReleaseArtifactInspector
	bindingEvidence deployment.CandidateConnectionEvidenceResolver
	runtimeVersion  string
	policy          runtimefactory.CandidateDeliveryPolicy
	policyResolver  NativeDeliveryPolicyResolver
	clock           func() time.Time
	events          deploymentmodule.NativeDeliveryEventAppender
	eventReader     nativeDeliveryEventReader
	audit           deploymentmodule.NativeDeliveryAuditAppender
	auditReader     nativeDeliveryAuditReader
	workflow        deploymentmodule.NativeDeliveryWorkflowRecorder
	operations      deploymentmodule.NativeOperationAuthority
	operationLookup nativeOperationLookup
	workflowFactory func(NativePlanWorkflowInput) (jobs.WorkflowIntent, error)
}

var _ deploymentmodule.NativeDeliveryMutationPort = (*NativeCreatePlanCoordinator)(nil)
var _ deploymentmodule.NativeDeliveryCommandCompleter = (*NativeCreatePlanCoordinator)(nil)

// NewNativeCreatePlanCoordinator validates composition without performing I/O.
func NewNativeCreatePlanCoordinator(config NativeCreatePlanConfig) (*NativeCreatePlanCoordinator, error) {
	if config.Repository == nil || !config.Repository.Configured() || !config.Repository.TransactionCapable() {
		return nil, errors.New("native create-plan requires a configured transaction-capable PostgreSQL repository")
	}
	if config.Sources == nil {
		return nil, errors.New("native create-plan source attestation reader is required")
	}
	inspector := config.Artifacts
	if inspector == nil {
		inspector = config.ArtifactInspector
	}
	if inspector == nil {
		return nil, errors.New("native create-plan read-only artifact inspector is required")
	}
	if strings.TrimSpace(config.RuntimeVersion) == "" {
		return nil, errors.New("native create-plan runtime version is required")
	}
	if config.Events == nil || config.Audit == nil || config.Operations == nil {
		return nil, errors.New("native create-plan event, audit, and operation authorities are required")
	}
	operationLookup, ok := config.Operations.(nativeOperationLookup)
	if !ok {
		return nil, errors.New("native create-plan operation replay reader is required")
	}
	eventReader, ok := config.Events.(nativeDeliveryEventReader)
	if !ok {
		return nil, errors.New("native create-plan durable event reader is required")
	}
	auditReader, ok := config.Audit.(nativeDeliveryAuditReader)
	if !ok {
		return nil, errors.New("native create-plan durable audit reader is required")
	}
	clock := config.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &NativeCreatePlanCoordinator{
		repository: config.Repository, sources: config.Sources, artifacts: inspector, bindingEvidence: config.BindingEvidence,
		runtimeVersion: strings.TrimSpace(config.RuntimeVersion), policy: config.Policy, policyResolver: config.PolicyResolver, clock: clock,
		events: config.Events, eventReader: eventReader, audit: config.Audit, auditReader: auditReader, workflow: config.Workflow,
		operations: config.Operations, operationLookup: operationLookup, workflowFactory: config.WorkflowFactory,
	}, nil
}

// NewNativeCreatePlan is a concise constructor alias for application
// composition code.
func NewNativeCreatePlan(config NativeCreatePlanConfig) (*NativeCreatePlanCoordinator, error) {
	return NewNativeCreatePlanCoordinator(config)
}

// BuildPlan deliberately fails closed. Implementing the module interface is
// safe because no caller can mistake an unimplemented physical path for a
// successful build result.
func (c *NativeCreatePlanCoordinator) BuildPlan(context.Context, deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
}

// CreatePlan verifies the exact retained source attestation, performs a
// read-only release artifact inspection, computes the canonical rich plan,
// and atomically persists operation, plan, event, audit, and optional workflow
// consequences. Every database mutation is committed once at the end.
func (c *NativeCreatePlanCoordinator) CreatePlan(ctx context.Context, request deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	if c == nil || c.repository == nil || c.sources == nil || c.artifacts == nil || c.events == nil || c.eventReader == nil || c.audit == nil || c.auditReader == nil || c.operations == nil || c.operationLookup == nil {
		return deploymentmodule.NativeDeliveryPlan{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	ctx = contextOrBackground(ctx)
	if err := validateNativeCreatePlanRequest(request); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	sourceOwner := strings.TrimSpace(request.SourceOwnerID)
	if sourceOwner == "" {
		sourceOwner = strings.TrimSpace(request.PrincipalID)
	}
	if sourceOwner == "" {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: source owner is required", deployment.ErrDeliveryInvalid)
	}
	request.SourceOwnerID = sourceOwner

	requestDigest, err := nativePlanRequestDigest(request)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	commandOwner, err := uuid.NewV7()
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("allocate native plan command owner: %w", err)
	}
	operationInput := deploymentmodule.NativeOperationAcquireInput{
		Scope: request.TargetID, OperationType: nativePlanOperationType,
		IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: commandOwner.String(),
	}
	known, found, err := c.operationLookup.Lookup(ctx, operationInput)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if found && len(known.Outcome) > 0 {
		return c.replayNativePlan(ctx, request, requestDigest, operationInput)
	}
	now := c.clock().UTC()
	if now.IsZero() {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: planner clock returned zero time", deployment.ErrDeliveryInvalid)
	}

	// Retained source and compiler objects may require remote object-store I/O.
	// Inspect them before opening the control transaction, against a fresh
	// snapshot of the target fence. The target is locked and compared again
	// below before any consequence is written.
	preflightTarget, err := c.repository.Target(ctx, request.TargetID)
	freshTarget := errors.Is(err, deploymentnative.ErrNotFound)
	if freshTarget {
		preflightTarget = deploymentnative.DeliveryTarget{
			TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment, TargetRevision: 1,
		}
	} else {
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if err := validateNativePlanTarget(preflightTarget, request); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
	}
	source, err := c.sources.SnapshotAttestation(ctx, project.CandidateSourceScope{ProjectID: request.ProjectID, OwnerID: sourceOwner}, request.SourceDigest, request.SourceAttestationDigest)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("verify retained source attestation: %w", err)
	}
	if source.ProjectID != request.ProjectID || source.ArtifactDigest != request.SourceDigest || source.SourceAttestationDigest != request.SourceAttestationDigest {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: retained source attestation identity changed", deployment.ErrDeliveryConflict)
	}
	inspectID := "inspect-" + strings.TrimPrefix(requestDigest, "sha256:")
	inspected, err := c.artifacts.InspectCandidateArtifacts(ctx, release.CandidateArtifactRequest{
		CandidateID: inspectID,
		Scope: deployment.CandidateScope{
			ProjectID: request.ProjectID, Environment: request.Environment,
			BaseGenerationID: preflightTarget.ActiveGenerationID,
		},
		OwnerID: sourceOwner, ArtifactDigest: request.SourceDigest, Source: source,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("inspect retained release artifact: %w", err)
	}
	if err := validateNativePlanInspection(request, source, inspected, inspectID); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	bindingDigest, err := resolveNativeCandidateBindingDigest(ctx, c.bindingEvidence, nativeCandidateConnectionRequest(
		inspectID, request.PrincipalID, request.TargetID, inspected,
	))
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}

	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	acquired, err := c.operations.AcquireTx(ctx, tx, operationInput)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if replay, err := nativePlanOperationDisposition(acquired, operationInput); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	} else if replay {
		outcome, err := decodeNativePlanOutcome(acquired.Operation.Outcome, operationInput)
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if outcome.OperationID != acquired.Operation.OperationID {
			return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: replay outcome operation identity differs", deployment.ErrDeliveryConflict)
		}
		stored, err := c.repository.PlanTx(ctx, tx, outcome.PlanID)
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("replay plan is unavailable: %w", err)
		}
		rich, err := stored.RichPlan()
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if err := validateNativePlanReplay(rich, request, outcome); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		projection, err := nativeDeliveryPlanProjection(rich)
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if err := bindNativePlanConsequences(&projection, rich.ActorID, request.IdempotencyKey, requestDigest, outcome.EventID, outcome.AuditID); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		committed = true
		return projection, nil
	}

	if freshTarget {
		if _, err := c.repository.ClaimProjectTx(ctx, tx, deployment.ProjectClaimInput{
			ProjectID: request.ProjectID, Environment: servingstate.Environment(request.Environment), ClaimedBy: request.PrincipalID, ClaimedAt: now,
		}); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if _, err := c.repository.CreateTargetTx(ctx, tx, deploymentnative.TargetInput{
			TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment, TargetRevision: 1,
		}); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
	}
	target, err := c.repository.TargetForShareTx(ctx, tx, request.TargetID)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := validateNativePlanTarget(target, request); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if !sameNativePlanTargetFence(preflightTarget, target) {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: target fence changed during source inspection", deployment.ErrDeliveryConflict)
	}
	_, reuse, err := c.readBaseTx(ctx, tx, target, request.ProjectID)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}

	operationID, err := canonicalUUIDv7(acquired.Operation.OperationID)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: operation authority returned invalid plan identity: %v", deployment.ErrDeliveryConflict, err)
	}
	candidate := deployment.Candidate{
		ID: operationID, Key: operationID, TargetID: request.TargetID, OwnerID: sourceOwner,
		Scope:          deployment.CandidateScope{ProjectID: request.ProjectID, Environment: request.Environment, BaseGenerationID: target.ActiveGenerationID},
		ArtifactDigest: request.SourceDigest, Revision: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: request.ProjectID, OwnerID: sourceOwner, ArtifactDigest: request.SourceDigest,
		Operation: deployment.DeliveryOperationKind(request.Operation), CandidateKey: operationID,
		Candidate: candidate, Source: source,
	}
	policy := c.policy
	if c.policyResolver != nil {
		policy, err = c.policyResolver(input.Operation)
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("resolve native delivery policy: %w", err)
		}
	}
	planRequest, err := runtimefactory.CandidatePlanRequestWithPolicyAndReuse(input, inspected, c.runtimeVersion, policy, now, reuse)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("compute canonical delivery plan: %w", err)
	}
	// Runtimefactory's legacy candidate projection prefixes IDs with plan-;
	// native operation identity is authoritative and must itself be the plan ID.
	planRequest.ID = operationID
	planRequest.ActorID = request.PrincipalID
	planRequest.SourceAttestationDigest = request.SourceAttestationDigest
	planRequest.ServingArtifactDigest = inspected.Generation.ArtifactDigest
	planRequest.Persist = true
	planRequest.TargetID, planRequest.ProjectID, planRequest.Environment = request.TargetID, request.ProjectID.String(), request.Environment
	planRequest.CreatedAt = now
	// BindingDigest is the exact validated provider/binding evidence selected
	// during planning, not merely the authored connector requirement shape.
	planRequest.Execution.BindingDigest = bindingDigest
	rich, err := richPlanFromRequest(planRequest, sourceOwner, operationID, target.ActiveGenerationID, target.TargetRevision)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	planDocument, err := json.Marshal(rich)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("encode canonical plan document: %w", err)
	}
	evidence, err := json.Marshal(rich.Evidence)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("encode plan evidence: %w", err)
	}
	stored, err := c.repository.CreatePlanAllocatedTx(ctx, tx, deploymentnative.PlanInput{
		PlanID: operationID, TargetID: request.TargetID, PlanRevision: 0, PlanDigest: rich.Digest,
		CompiledGraphDigest: inspected.Compiler.Graph.Digest(), CompiledConfigDigest: rich.Execution.ConfigDigest,
		SecurityDomainFingerprint: inspected.AuthorizationFingerprint, ArtifactDigest: inspected.Generation.ArtifactDigest,
		QualificationDigest: rich.Governance.QualificationDigest, QualificationRequired: true,
		PlanDocument: planDocument, Evidence: evidence, CreatedAt: rich.CreatedAt,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	storedRich, err := stored.RichPlan()
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := validateNativePlanReplay(storedRich, request, nativePlanOutcome{OperationID: operationID, PlanID: operationID, ProjectID: request.ProjectID.String(), TargetID: request.TargetID, SourceDigest: request.SourceDigest, SourceAttestationDigest: request.SourceAttestationDigest, Status: "accepted", PlanDigest: storedRich.Digest}); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	projection, err := nativeDeliveryPlanProjection(storedRich)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	eventID, err := nativePlanConsequenceID(operationID, "event")
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	auditID, err := nativePlanConsequenceID(operationID, "audit")
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := bindNativePlanConsequences(&projection, storedRich.ActorID, request.IdempotencyKey, requestDigest, eventID, auditID); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	payload := nativePlanEventPayload{OperationID: operationID, ProjectID: request.ProjectID.String(), ResourceID: operationID, Status: "accepted"}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	event, err := c.events.AppendDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{
		EventID: eventID, ScopeID: request.TargetID, AggregateType: "delivery_plan", AggregateID: operationID,
		EventType: "delivery.plan.created", SchemaVersion: 1, CorrelationID: operationID, Payload: payloadJSON,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if event.EventID != eventID || event.ScopeID != request.TargetID || event.AggregateType != "delivery_plan" || event.AggregateID != operationID || event.EventType != "delivery.plan.created" || event.SchemaVersion != 1 || event.CorrelationID != operationID || event.AggregateVersion <= 0 || !sameNativeJSON(event.Payload, payloadJSON) {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: plan event identity differs", deployment.ErrDeliveryConflict)
	}
	audit, err := c.audit.AppendMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{
		AuditID: auditID, DomainEventID: event.EventID, ScopeID: request.TargetID, ActorID: request.PrincipalID,
		Action: "delivery.plan.created", ResourceKind: "plan", ResourceID: operationID, Outcome: "accepted",
		Operation: "plan", RequestDigest: requestDigest, CorrelationID: operationID, AggregateKey: event.AggregateID,
		AggregateSequence: event.AggregateVersion, Metadata: payloadJSON,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if audit.AuditID != auditID || audit.EventID != event.EventID || audit.ScopeID != request.TargetID || audit.ActorID != request.PrincipalID || audit.Action != "delivery.plan.created" || audit.ResourceKind != "plan" || audit.ResourceID != operationID || audit.Outcome != "accepted" || audit.RequestDigest != requestDigest || !sameNativeJSON(audit.Metadata, payloadJSON) {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: plan audit identity differs", deployment.ErrDeliveryConflict)
	}
	if c.workflowFactory != nil {
		if c.workflow == nil {
			return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: plan workflow recorder is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
		}
		intent, err := c.workflowFactory(NativePlanWorkflowInput{Plan: projection, OperationID: operationID, ProjectID: request.ProjectID, TargetID: request.TargetID})
		if err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
		if err := c.workflow.RecordWorkflow(ctx, tx, intent); err != nil {
			return deploymentmodule.NativeDeliveryPlan{}, err
		}
	}
	outcome := nativePlanOutcome{OperationID: operationID, PlanID: operationID, EventID: eventID, AuditID: auditID, ProjectID: request.ProjectID.String(), TargetID: request.TargetID, SourceDigest: request.SourceDigest, SourceAttestationDigest: request.SourceAttestationDigest, Status: "accepted", PlanDigest: storedRich.Digest}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := c.operations.CompleteTx(ctx, tx, acquired.Lease, outcomeJSON); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	committed = true
	return projection, nil
}

func (c *NativeCreatePlanCoordinator) replayNativePlan(ctx context.Context, request deploymentmodule.NativeDeliveryPlanRequest, requestDigest string, operationInput deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeDeliveryPlan, error) {
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	acquired, err := c.operations.AcquireTx(ctx, tx, operationInput)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	replay, err := nativePlanOperationDisposition(acquired, operationInput)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if !replay {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: terminal operation lookup did not replay", deployment.ErrDeliveryConflict)
	}
	outcome, err := decodeNativePlanOutcome(acquired.Operation.Outcome, operationInput)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if outcome.OperationID != acquired.Operation.OperationID {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: replay outcome operation identity differs", deployment.ErrDeliveryConflict)
	}
	stored, err := c.repository.PlanTx(ctx, tx, outcome.PlanID)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("replay plan is unavailable: %w", err)
	}
	rich, err := stored.RichPlan()
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := validateNativePlanReplay(rich, request, outcome); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	projection, err := nativeDeliveryPlanProjection(rich)
	if err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := bindNativePlanConsequences(&projection, rich.ActorID, request.IdempotencyKey, requestDigest, outcome.EventID, outcome.AuditID); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryPlan{}, err
	}
	committed = true
	return projection, nil
}

// CompleteNativePlanCommand verifies that the transactional plan evidence
// returned to generated HTTP handlers is internally complete. The mutation
// itself has already committed before this guard executes.
func (c *NativeCreatePlanCoordinator) CompleteNativePlanCommand(ctx context.Context, plan deploymentmodule.NativeDeliveryPlan) error {
	if c == nil || c.repository == nil || c.eventReader == nil || c.auditReader == nil || c.operationLookup == nil {
		return deploymentmodule.ErrDeliveryInputUnavailable
	}
	if plan.ID == uuid.Nil || plan.Status != string(deploymentgen.DeliveryPlanStatusPlanned) || plan.PlanDigest == "" || plan.ProjectID.Validate() != nil || plan.ActorID == "" || plan.SourceOwnerID == "" || plan.IdempotencyKey == "" || platformdigest.ValidateSHA256Identity(plan.RequestDigest) != nil || plan.EventID == uuid.Nil || plan.AuditID == uuid.Nil {
		return fmt.Errorf("%w: native plan completion evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	ctx = contextOrBackground(ctx)
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	stored, err := c.repository.PlanTx(ctx, tx, plan.ID.String())
	if err != nil {
		return fmt.Errorf("%w: persisted native plan is unavailable: %v", deployment.ErrDeliveryConflict, err)
	}
	rich, err := stored.RichPlan()
	if err != nil {
		return err
	}
	verified, err := nativeDeliveryPlanProjection(rich)
	if err != nil {
		return err
	}
	if verified.ID != plan.ID || rich.ActorID != plan.ActorID || rich.SourceOwnerID != plan.SourceOwnerID || verified.ProjectID != plan.ProjectID || verified.TargetID != plan.TargetID || verified.Environment != plan.Environment || verified.Operation != plan.Operation || verified.SourceDigest != plan.SourceDigest || verified.SourceAttestationDigest != plan.SourceAttestationDigest || verified.BaseGenerationID != plan.BaseGenerationID || verified.BaseTargetRevision != plan.BaseTargetRevision || verified.ExecutionDigest != plan.ExecutionDigest || verified.ProvenanceDigest != plan.ProvenanceDigest || verified.GovernanceDigest != plan.GovernanceDigest || verified.EvidenceDigest != plan.EvidenceDigest || verified.PlanDigest != plan.PlanDigest || verified.Status != plan.Status || !verified.CreatedAt.Equal(plan.CreatedAt.UTC()) || !verified.ExpiresAt.Equal(plan.ExpiresAt.UTC()) {
		return fmt.Errorf("%w: returned native plan differs from persisted canonical plan", deployment.ErrDeliveryConflict)
	}
	operationInput := deploymentmodule.NativeOperationAcquireInput{Scope: plan.TargetID, OperationType: nativePlanOperationType, IdempotencyKey: plan.IdempotencyKey, RequestDigest: plan.RequestDigest, OwnerID: plan.ActorID}
	operation, found, err := c.operationLookup.Lookup(ctx, operationInput)
	if err != nil || !found || operation.OperationID != plan.ID.String() {
		return fmt.Errorf("%w: persisted native plan operation is unavailable", deployment.ErrDeliveryConflict)
	}
	outcome, err := decodeNativePlanOutcome(operation.Outcome, operationInput)
	if err != nil || outcome.OperationID != plan.ID.String() || outcome.PlanDigest != plan.PlanDigest || outcome.ProjectID != plan.ProjectID.String() || outcome.EventID != plan.EventID.String() || outcome.AuditID != plan.AuditID.String() {
		return fmt.Errorf("%w: persisted native plan operation outcome differs", deployment.ErrDeliveryConflict)
	}
	payloadJSON, err := json.Marshal(nativePlanEventPayload{OperationID: plan.ID.String(), ProjectID: plan.ProjectID.String(), ResourceID: plan.ID.String(), Status: "accepted"})
	if err != nil {
		return err
	}
	event, err := c.eventReader.GetDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{
		EventID: plan.EventID.String(), ScopeID: plan.TargetID, AggregateType: "delivery_plan", AggregateID: plan.ID.String(),
		EventType: "delivery.plan.created", SchemaVersion: 1, CorrelationID: plan.ID.String(), Payload: payloadJSON,
	})
	if err != nil {
		return fmt.Errorf("%w: persisted native plan event is unavailable: %v", deployment.ErrDeliveryConflict, err)
	}
	if _, err := c.auditReader.GetMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{
		AuditID: plan.AuditID.String(), DomainEventID: event.EventID, ScopeID: plan.TargetID, ActorID: plan.ActorID,
		Operation: "plan", Action: "delivery.plan.created", ResourceKind: "plan", ResourceID: plan.ID.String(), Outcome: "accepted",
		RequestDigest: plan.RequestDigest, CorrelationID: plan.ID.String(), AggregateKey: event.AggregateID,
		AggregateSequence: event.AggregateVersion, Metadata: payloadJSON,
	}); err != nil {
		return fmt.Errorf("%w: persisted native plan audit is unavailable: %v", deployment.ErrDeliveryConflict, err)
	}
	return nil
}

func (c *NativeCreatePlanCoordinator) CompleteNativeBuildCommand(context.Context, deploymentmodule.NativeDeliveryBuild) error {
	return deploymentmodule.ErrDeliveryInputUnavailable
}

type nativePlanEventPayload struct {
	OperationID string `json:"operationId"`
	ProjectID   string `json:"projectId"`
	ResourceID  string `json:"resourceId"`
	Status      string `json:"status"`
}

type nativePlanOutcome struct {
	OperationID             string `json:"operationId"`
	PlanID                  string `json:"planId"`
	EventID                 string `json:"eventId"`
	AuditID                 string `json:"auditId"`
	ProjectID               string `json:"projectId"`
	TargetID                string `json:"targetId"`
	SourceDigest            string `json:"sourceDigest"`
	SourceAttestationDigest string `json:"sourceAttestationDigest"`
	Status                  string `json:"status"`
	PlanDigest              string `json:"planDigest"`
}

func nativePlanOperationDisposition(result deploymentmodule.NativeOperationAcquireResult, input deploymentmodule.NativeOperationAcquireInput) (bool, error) {
	switch result.Status {
	case deploymentmodule.NativeOperationAcquired:
		if err := validateNativeOperation(result, input, false); err != nil {
			return false, err
		}
		if result.Lease.OperationID != result.Operation.OperationID || result.Lease.OwnerID != input.OwnerID || result.Lease.FencingGeneration <= 0 || result.Lease.LeaseExpiresAt.IsZero() {
			return false, fmt.Errorf("%w: acquired operation lease identity differs", deployment.ErrDeliveryConflict)
		}
		return false, nil
	case deploymentmodule.NativeOperationReplay:
		if err := validateNativeOperation(result, input, true); err != nil {
			return false, err
		}
		return true, nil
	case deploymentmodule.NativeOperationBusy, deploymentmodule.NativeOperationIndeterminate:
		return false, fmt.Errorf("%w: operation is not available for planning", deployment.ErrDeliveryConflict)
	default:
		return false, fmt.Errorf("%w: unknown operation status %q", deployment.ErrDeliveryConflict, result.Status)
	}
}

func validateNativeOperation(result deploymentmodule.NativeOperationAcquireResult, input deploymentmodule.NativeOperationAcquireInput, replay bool) error {
	if _, err := canonicalUUIDv7(result.Operation.OperationID); err != nil {
		return fmt.Errorf("%w: %v", deployment.ErrDeliveryConflict, err)
	}
	if result.Operation.Scope != input.Scope || result.Operation.OperationType != input.OperationType || result.Operation.IdempotencyKey != input.IdempotencyKey || result.Operation.RequestDigest != input.RequestDigest {
		return fmt.Errorf("%w: operation identity differs", deployment.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(result.Operation.OwnerID); err != nil {
		return fmt.Errorf("%w: operation owner identity is invalid", deployment.ErrDeliveryConflict)
	}
	if !replay && result.Operation.OwnerID != input.OwnerID {
		return fmt.Errorf("%w: acquired operation owner identity differs", deployment.ErrDeliveryConflict)
	}
	if replay && len(result.Operation.Outcome) == 0 {
		return fmt.Errorf("%w: replay operation outcome is empty", deployment.ErrDeliveryConflict)
	}
	return nil
}

func decodeNativePlanOutcome(raw json.RawMessage, input deploymentmodule.NativeOperationAcquireInput) (nativePlanOutcome, error) {
	var outcome nativePlanOutcome
	if len(raw) == 0 || strictjson.DecodeWithOptions(raw, &outcome, strictjson.Options{MaxBytes: maxNativePlanOutcomeBytes}) != nil || outcome.Status != "accepted" {
		return nativePlanOutcome{}, fmt.Errorf("%w: replay operation outcome is invalid", deployment.ErrDeliveryConflict)
	}
	for label, value := range map[string]string{"operation id": outcome.OperationID, "plan id": outcome.PlanID, "event id": outcome.EventID, "audit id": outcome.AuditID} {
		if _, err := canonicalUUIDv7(value); err != nil {
			return nativePlanOutcome{}, fmt.Errorf("%w: replay %s: %v", deployment.ErrDeliveryConflict, label, err)
		}
	}
	if outcome.OperationID != outcome.PlanID || outcome.ProjectID == "" || outcome.TargetID != input.Scope || platformdigest.ValidateSHA256Identity(outcome.SourceDigest) != nil || platformdigest.ValidateSHA256Identity(outcome.SourceAttestationDigest) != nil || platformdigest.ValidateSHA256Identity(outcome.PlanDigest) != nil {
		return nativePlanOutcome{}, fmt.Errorf("%w: replay outcome identity differs", deployment.ErrDeliveryConflict)
	}
	expectedEvent, err := nativePlanConsequenceID(outcome.OperationID, "event")
	if err != nil || outcome.EventID != expectedEvent {
		return nativePlanOutcome{}, fmt.Errorf("%w: replay event identity differs", deployment.ErrDeliveryConflict)
	}
	expectedAudit, err := nativePlanConsequenceID(outcome.OperationID, "audit")
	if err != nil || outcome.AuditID != expectedAudit {
		return nativePlanOutcome{}, fmt.Errorf("%w: replay audit identity differs", deployment.ErrDeliveryConflict)
	}
	return outcome, nil
}

func validateNativePlanTarget(target deploymentnative.DeliveryTarget, request deploymentmodule.NativeDeliveryPlanRequest) error {
	if target.TargetID != request.TargetID || target.ProjectID != request.ProjectID.String() || target.Environment != request.Environment || target.TargetRevision <= 0 {
		return fmt.Errorf("%w: target project, environment, or revision differs from request", deployment.ErrDeliveryConflict)
	}
	return nil
}

func sameNativePlanTargetFence(left, right deploymentnative.DeliveryTarget) bool {
	return left.TargetID == right.TargetID && left.ProjectID == right.ProjectID && left.Environment == right.Environment &&
		left.TargetRevision == right.TargetRevision && left.ActiveGenerationID == right.ActiveGenerationID && left.ActivePublicationID == right.ActivePublicationID
}

func validateNativePlanInspection(request deploymentmodule.NativeDeliveryPlanRequest, source project.CandidateSourceSnapshot, inspected release.CandidateArtifactSet, inspectID string) error {
	compilerGraph := inspected.Compiler.Graph
	compilerArtifact := inspected.Compiler.Artifact
	expectedInspectDigest := sha256.Sum256([]byte(inspectID))
	expectedGenerationID := "inspect-" + hex.EncodeToString(expectedInspectDigest[:])
	identity := inspected.Generation.Identity
	if inspected.Artifact.SourceDigest != request.SourceDigest || inspected.Artifact.SourceDigest != source.ArtifactDigest ||
		inspected.Artifact.ProjectDigest != source.ProjectDigest || platformdigest.ValidateSHA256Identity(inspected.Artifact.ProjectDigest) != nil ||
		compilerArtifact.ProjectID() != request.ProjectID || compilerArtifact.Digest() != source.ProjectDigest ||
		compilerGraph.ProjectID() != request.ProjectID || compilerGraph.Digest() != compilerArtifact.Graph().Digest() || compilerGraph.Validate() != nil ||
		inspected.Compiler.Plan.Project != request.ProjectID.String() || !sameNativeValue(inspected.Compiler.Manifest, compilerArtifact.Manifest()) ||
		identity.Validate() != nil || identity.ProjectID != request.ProjectID || identity.Environment != request.Environment || identity.GenerationID != expectedGenerationID ||
		platformdigest.ValidateSHA256Identity(inspected.AuthorizationFingerprint) != nil ||
		platformdigest.ValidateSHA256Identity(inspected.Artifact.ContentDigest) != nil ||
		inspected.Generation.ArtifactDigest != inspected.Artifact.ContentDigest ||
		inspected.Generation.ServingArtifactID != nativePlannedServingArtifactID(inspected.Generation.ArtifactDigest) ||
		inspected.Generation.BundleManifestJSON == "" || inspected.Generation.BundleManifestJSON != strings.TrimSpace(inspected.Generation.BundleManifestJSON) {
		return fmt.Errorf("%w: inspected compiler artifact identity differs from retained source and target scope", deployment.ErrDeliveryConflict)
	}
	// Recompute the deterministic serving bundle identity from the exact
	// compiler evidence supplied by the inspector. This prevents a forged
	// ContentDigest/manifest from being persisted merely because its format
	// looks valid; later materialization must reproduce these same bytes.
	manifest, servingDigest, err := projectbundle.PackCompiledProject(compilerArtifact, inspected.Compiler.Plan, io.Discard)
	if err != nil {
		return fmt.Errorf("%w: recompute planned serving artifact: %v", deployment.ErrDeliveryConflict, err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("%w: encode planned serving artifact manifest: %v", deployment.ErrDeliveryConflict, err)
	}
	if servingDigest != inspected.Generation.ArtifactDigest || servingDigest != inspected.Artifact.ContentDigest || string(manifestJSON) != inspected.Generation.BundleManifestJSON {
		return fmt.Errorf("%w: planned serving artifact identity does not match compiler evidence", deployment.ErrDeliveryConflict)
	}
	return nil
}

func nativePlannedServingArtifactID(digest string) string {
	if platformdigest.ValidateSHA256Identity(digest) != nil {
		return ""
	}
	return "artifact-" + strings.TrimPrefix(digest, "sha256:")
}

func nativePlanConsequenceID(planID, role string) (string, error) {
	canonical, err := canonicalUUIDv7(planID)
	if err != nil {
		return "", err
	}
	if role != "event" && role != "audit" {
		return "", errors.New("native plan consequence role is invalid")
	}
	id, _ := uuid.Parse(canonical)
	digest := sha256.Sum256([]byte("leapview/native-plan/" + canonical + "/" + role))
	copy(id[6:], digest[:10])
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id.String(), nil
}

func bindNativePlanConsequences(plan *deploymentmodule.NativeDeliveryPlan, actorID, idempotencyKey, requestDigest, eventID, auditID string) error {
	if plan == nil || actorID == "" || actorID != strings.TrimSpace(actorID) || idempotencyKey == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) || platformdigest.ValidateSHA256Identity(requestDigest) != nil {
		return fmt.Errorf("%w: native plan consequence evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	event, err := uuid.Parse(eventID)
	if err != nil || event.String() != eventID || event.Version() != 7 {
		return fmt.Errorf("%w: native plan event identity is invalid", deployment.ErrDeliveryConflict)
	}
	audit, err := uuid.Parse(auditID)
	if err != nil || audit.String() != auditID || audit.Version() != 7 {
		return fmt.Errorf("%w: native plan audit identity is invalid", deployment.ErrDeliveryConflict)
	}
	expectedEvent, err := nativePlanConsequenceID(plan.ID.String(), "event")
	if err != nil || expectedEvent != eventID {
		return fmt.Errorf("%w: native plan event identity differs", deployment.ErrDeliveryConflict)
	}
	expectedAudit, err := nativePlanConsequenceID(plan.ID.String(), "audit")
	if err != nil || expectedAudit != auditID {
		return fmt.Errorf("%w: native plan audit identity differs", deployment.ErrDeliveryConflict)
	}
	plan.ActorID = actorID
	plan.IdempotencyKey = idempotencyKey
	plan.RequestDigest = requestDigest
	plan.EventID = event
	plan.AuditID = audit
	return nil
}

func validateNativePlanReplay(plan deployment.DeliveryPlan, request deploymentmodule.NativeDeliveryPlanRequest, outcome nativePlanOutcome) error {
	if plan.ID != outcome.PlanID || plan.ProjectID != request.ProjectID || plan.TargetID != request.TargetID || plan.Environment != request.Environment || plan.Operation != deployment.DeliveryOperationKind(request.Operation) || plan.SourceDigest != request.SourceDigest || plan.Provenance.AttestationDigest != request.SourceAttestationDigest || plan.ActorID != request.PrincipalID || plan.SourceOwnerID != request.SourceOwnerID || plan.Status != deployment.DeliveryPlanPlanned {
		return fmt.Errorf("%w: replayed plan identity differs", deployment.ErrDeliveryConflict)
	}
	if outcome.ProjectID != request.ProjectID.String() || outcome.SourceDigest != request.SourceDigest || outcome.SourceAttestationDigest != request.SourceAttestationDigest || outcome.PlanDigest != plan.Digest {
		return fmt.Errorf("%w: replay outcome does not match plan", deployment.ErrDeliveryConflict)
	}
	return nil
}

func (c *NativeCreatePlanCoordinator) readBaseTx(ctx context.Context, tx deploymentnative.Tx, target deploymentnative.DeliveryTarget, projectID projectgraph.ResourceID) (*deployment.DeliveryPlan, *deployment.DeliveryReuseInput, error) {
	if strings.TrimSpace(target.ActiveGenerationID) == "" {
		return nil, nil, nil
	}
	activeID, err := canonicalNonNilUUID(target.ActiveGenerationID, "active generation id")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: active generation identity: %v", deployment.ErrDeliveryConflict, err)
	}
	generation, err := c.repository.GenerationTx(ctx, tx, activeID)
	if err != nil {
		return nil, nil, err
	}
	if generation.TargetID != target.TargetID || generation.PlanID == "" || generation.SnapshotSealID == "" {
		return nil, nil, fmt.Errorf("%w: active generation identity is incomplete", deployment.ErrDeliveryConflict)
	}
	basePlanRow, err := c.repository.PlanTx(ctx, tx, generation.PlanID)
	if err != nil {
		return nil, nil, err
	}
	basePlan, err := basePlanRow.RichPlan()
	if err != nil {
		return nil, nil, err
	}
	if basePlan.ProjectID != projectID || basePlan.TargetID != target.TargetID || basePlan.Digest != generation.PlanDigest || basePlan.ID != generation.PlanID {
		return nil, nil, fmt.Errorf("%w: active generation plan identity differs", deployment.ErrDeliveryConflict)
	}
	sealID, err := canonicalNonNilUUID(generation.SnapshotSealID, "active snapshot seal id")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: active snapshot seal identity: %v", deployment.ErrDeliveryConflict, err)
	}
	seal, err := c.repository.SnapshotSealTx(ctx, tx, sealID)
	if err != nil {
		return nil, nil, err
	}
	if seal.CandidateID != generation.CandidateID || seal.PlanDigest != generation.PlanDigest || seal.PhysicalPoolID == "" || seal.ClosureDigest == "" || seal.CompatibilityDigest == "" {
		return nil, nil, fmt.Errorf("%w: active snapshot seal identity is incomplete", deployment.ErrDeliveryConflict)
	}
	baseContext, err := basePlan.Execution.ContextDigest()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: active plan execution context: %v", deployment.ErrDeliveryConflict, err)
	}
	reuse := &deployment.DeliveryReuseInput{
		BaseExecutionDigest: basePlan.ExecutionDigest, CatalogDigest: seal.ClosureDigest, BaseCatalogDigest: seal.ClosureDigest,
		PhysicalPoolID: seal.PhysicalPoolID, BasePhysicalPoolID: seal.PhysicalPoolID,
		CompatibilityDigest: seal.CompatibilityDigest, BaseCompatibilityDigest: seal.CompatibilityDigest,
		BaseContextDigest: baseContext, Deterministic: true,
	}
	return &basePlan, reuse, nil
}

func validateNativeCreatePlanRequest(request deploymentmodule.NativeDeliveryPlanRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	for label, value := range map[string]string{
		"target": request.TargetID, "environment": request.Environment, "principal": request.PrincipalID,
		"source digest": request.SourceDigest, "source attestation digest": request.SourceAttestationDigest,
		"idempotency key": request.IdempotencyKey,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
	}
	if request.SourceOwnerID != strings.TrimSpace(request.SourceOwnerID) {
		return fmt.Errorf("%w: source owner is not canonical", deployment.ErrDeliveryInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(request.SourceDigest); err != nil {
		return fmt.Errorf("%w: source digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if err := platformdigest.ValidateSHA256Identity(request.SourceAttestationDigest); err != nil {
		return fmt.Errorf("%w: source attestation digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	switch deployment.DeliveryOperationKind(request.Operation) {
	case deployment.DeliveryOperationCodeChange, deployment.DeliveryOperationRestatement, deployment.DeliveryOperationBindingChange, deployment.DeliveryOperationPolicyChange:
	default:
		return fmt.Errorf("%w: unsupported delivery operation %q", deployment.ErrDeliveryInvalid, request.Operation)
	}
	return nil
}

func richPlanFromRequest(request deployment.DeliveryPlanRequest, sourceOwner, planID, baseGeneration string, baseRevision int64) (deployment.DeliveryPlan, error) {
	projectID, err := projectgraph.NewResourceID(request.ProjectID)
	if err != nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	plan := deployment.DeliveryPlan{
		ID: planID, ActorID: request.ActorID, SourceOwnerID: sourceOwner,
		TargetID: request.TargetID, ProjectID: projectID, Environment: request.Environment,
		Operation: request.Operation, SourceDigest: request.SourceDigest, ServingArtifactDigest: request.ServingArtifactDigest,
		BaseGenerationID: baseGeneration, BaseTargetRevision: baseRevision,
		Execution: request.Execution, Provenance: request.Provenance, Governance: request.Governance,
		Evidence: request.Evidence, PipelinePlan: request.PipelinePlan, CreatedAt: request.CreatedAt,
	}
	return deployment.NewDeliveryPlan(plan)
}

func nativePlanRequestDigest(request deploymentmodule.NativeDeliveryPlanRequest) (string, error) {
	canonical := struct {
		ProjectID, TargetID, Environment, PrincipalID, SourceOwnerID, Operation, SourceDigest, SourceAttestationDigest, IdempotencyKey string
	}{request.ProjectID.String(), request.TargetID, request.Environment, request.PrincipalID, request.SourceOwnerID, request.Operation, request.SourceDigest, request.SourceAttestationDigest, request.IdempotencyKey}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sameNativeJSON(left, right []byte) bool {
	var a, b any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&a) != nil || rightDecoder.Decode(&b) != nil {
		return false
	}
	var trailing any
	if !errors.Is(leftDecoder.Decode(&trailing), io.EOF) || !errors.Is(rightDecoder.Decode(&trailing), io.EOF) {
		return false
	}
	leftCanonical, leftErr := json.Marshal(a)
	rightCanonical, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func sameNativeValue(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func canonicalUUIDv7(value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		return "", errors.New("must be a canonical UUIDv7")
	}
	return value, nil
}

func canonicalNonNilUUID(value, label string) (string, error) {
	canonical, err := canonicalUUID(value, label)
	if err != nil {
		return "", err
	}
	parsed, _ := uuid.Parse(canonical)
	if parsed == uuid.Nil {
		return "", fmt.Errorf("%w: %s must be a non-nil UUID", deploymentnative.ErrInvalid, label)
	}
	return canonical, nil
}

func nativeDeliveryPlanProjection(plan deployment.DeliveryPlan) (deploymentmodule.NativeDeliveryPlan, error) {
	id, err := uuid.Parse(plan.ID)
	if err != nil || id.String() != plan.ID || id.Version() != 7 {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: plan id is not a canonical UUIDv7", deployment.ErrDeliveryConflict)
	}
	var evidence deploymentgen.DeliveryPlanEvidenceView
	redacted := deployment.RedactedDeliveryPlanEvidence(plan)
	encoded, err := json.Marshal(redacted)
	if err != nil || json.Unmarshal(encoded, &evidence) != nil {
		return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: plan evidence projection is invalid", deployment.ErrDeliveryConflict)
	}
	baseID := uuid.Nil
	if plan.BaseGenerationID != "" {
		baseID, err = uuid.Parse(plan.BaseGenerationID)
		if err != nil || baseID == uuid.Nil || baseID.String() != plan.BaseGenerationID {
			return deploymentmodule.NativeDeliveryPlan{}, fmt.Errorf("%w: base generation identity is not canonical", deployment.ErrDeliveryConflict)
		}
	}
	return deploymentmodule.NativeDeliveryPlan{
		ID: id, SourceOwnerID: plan.SourceOwnerID, ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment,
		Operation: string(plan.Operation), SourceDigest: plan.SourceDigest, SourceAttestationDigest: plan.Provenance.AttestationDigest,
		BaseGenerationID: baseID, BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest,
		ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, EvidenceDigest: plan.EvidenceDigest,
		PlanDigest: plan.Digest, Status: string(plan.Status), ExpiresAt: plan.Governance.ExpiresAt.UTC(), CreatedAt: plan.CreatedAt.UTC(), Evidence: evidence,
	}, nil
}
