package module

// Native delivery contracts intentionally stop at the module boundary. They
// carry PostgreSQL delivery identities as UUID values and do not expose
// compatibility lifecycle, file-catalog, or candidate models. The
// application composition root can therefore assemble compiler/physical
// build work independently and inject one implementation here.

import (
	"context"
	"fmt"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// NativeDeliveryMutationPort is the clean-slate PostgreSQL plan/build seam
// used by the HTTP handlers. Publication and rollback intentionally use the
// distinct NativeDeliveryPublicationPort below so a plan/build implementation
// cannot accidentally acquire publication authority.
type NativeDeliveryMutationPort interface {
	CreatePlan(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error)
	BuildPlan(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error)
}

// NativeDeliveryCommandCompleter is optional for ports used outside the
// generated HTTP transport. Native PostgreSQL ports should implement it so the
// APIGen command guard can verify native event/audit evidence without forcing
// the module to depend on a compatibility delivery reader.
type NativeDeliveryCommandCompleter interface {
	CompleteNativePlanCommand(context.Context, NativeDeliveryPlan) error
	CompleteNativeBuildCommand(context.Context, NativeDeliveryBuild) error
}

// NativeDeliveryPublicationPort owns creation of pending publish and rollback
// publications. Implementations resolve the candidate/generation and current
// target fence in the native PostgreSQL authority, then persist the operation,
// event, audit, and publication atomically. They must not approve or activate
// a request inline.
type NativeDeliveryPublicationPort interface {
	PublishCandidate(context.Context, NativeDeliveryPublishRequest) (NativeDeliveryPublication, error)
	RollbackGeneration(context.Context, NativeDeliveryRollbackRequest) (NativeDeliveryPublication, error)
}

// NativeDeliveryPublicationCommandCompleter is implemented by native PostgreSQL
// publication authorities. The generated command guard calls it after
// the mutation and it must verify the durable operation, event, and audit
// consequences before the HTTP command is acknowledged.
type NativeDeliveryPublicationCommandCompleter interface {
	CompleteNativePublishCommand(context.Context, NativeDeliveryPublication) error
	CompleteNativeRollbackCommand(context.Context, NativeDeliveryPublication) error
}

type NativeDeliveryPublishRequest struct {
	ProjectID      projectgraph.ResourceID
	TargetID       string
	Environment    string
	CandidateID    uuid.UUID
	PrincipalID    string
	IdempotencyKey string
}

type NativeDeliveryRollbackRequest struct {
	ProjectID      projectgraph.ResourceID
	TargetID       string
	Environment    string
	GenerationID   uuid.UUID
	PrincipalID    string
	IdempotencyKey string
}

// NativeDeliveryPublication is the canonical native publication evidence
// projection. UUID identities remain typed here and are stringified only by
// the generated HTTP response adapter.
type NativeDeliveryPublication struct {
	ID                       uuid.UUID
	OperationID              uuid.UUID
	EventID                  uuid.UUID
	AuditID                  uuid.UUID
	ActorID                  string
	IdempotencyKey           string
	RequestDigest            string
	ProjectID                projectgraph.ResourceID
	TargetID                 string
	Environment              string
	PlanID                   uuid.UUID
	PlanDigest               string
	CandidateID              uuid.UUID
	GenerationID             uuid.UUID
	ExpectedBaseGenerationID uuid.UUID
	ExpectedTargetRevision   int64
	ResultTargetRevision     int64
	Status                   string
	CreatedAt                time.Time
	CompletedAt              time.Time
}

type NativeDeliveryPublicationFuncs struct {
	Publish  func(context.Context, NativeDeliveryPublishRequest) (NativeDeliveryPublication, error)
	Rollback func(context.Context, NativeDeliveryRollbackRequest) (NativeDeliveryPublication, error)
}

func (f NativeDeliveryPublicationFuncs) PublishCandidate(ctx context.Context, request NativeDeliveryPublishRequest) (NativeDeliveryPublication, error) {
	if f.Publish == nil {
		return NativeDeliveryPublication{}, ErrDeliveryInputUnavailable
	}
	return f.Publish(ctx, request)
}

func (f NativeDeliveryPublicationFuncs) RollbackGeneration(ctx context.Context, request NativeDeliveryRollbackRequest) (NativeDeliveryPublication, error) {
	if f.Rollback == nil {
		return NativeDeliveryPublication{}, ErrDeliveryInputUnavailable
	}
	return f.Rollback(ctx, request)
}

// NativeDeliveryPlanRequest contains only authoring intent. Plan identity is
// allocated by the native authority; callers must not supply a plan UUID.
type NativeDeliveryPlanRequest struct {
	ProjectID   projectgraph.ResourceID
	TargetID    string
	Environment string
	PrincipalID string
	// SourceOwnerID is the retained-source namespace. It is deliberately
	// separate from PrincipalID so scheduler/reviewer initiated plans can
	// attest an author-owned source without changing actor evidence.
	SourceOwnerID           string
	Operation               string
	SourceDigest            string
	SourceAttestationDigest string
	IdempotencyKey          string
	// PipelinePlan carries the immutable generation-bound refresh selection
	// into native delivery planning. The PostgreSQL authority persists this
	// rich plan document; callers cannot reconstruct it from a digest later.
	PipelinePlan *projectpipelineplan.Plan
}

// NativeDeliveryPlan is the value-only response projection returned by a
// native planner. IDs are UUID-native; target and project identities retain
// their domain-native forms.
type NativeDeliveryPlan struct {
	ID uuid.UUID
	// ActorID, RequestDigest, EventID, and AuditID are internal completion
	// evidence. The HTTP response intentionally does not expose them; the
	// native PostgreSQL command completer uses them to re-read the exact durable
	// consequences before acknowledging the mutation.
	ActorID                 string
	SourceOwnerID           string
	IdempotencyKey          string
	RequestDigest           string
	EventID                 uuid.UUID
	AuditID                 uuid.UUID
	ProjectID               projectgraph.ResourceID
	TargetID                string
	Environment             string
	Operation               string
	SourceDigest            string
	SourceAttestationDigest string
	BaseGenerationID        uuid.UUID
	BaseTargetRevision      int64
	ExecutionDigest         string
	ProvenanceDigest        string
	GovernanceDigest        string
	EvidenceDigest          string
	PlanDigest              string
	Status                  string
	ExpiresAt               time.Time
	CreatedAt               time.Time
	Evidence                deploymentgen.DeliveryPlanEvidenceView
}

// NativeDeliveryBuildRequest identifies one exact persisted plan and build
// operation. PlanID is required to be the canonical UUID returned by plan
// creation; no text-ID generation is performed by the module.
type NativeDeliveryBuildRequest struct {
	ProjectID      projectgraph.ResourceID
	TargetID       string
	Environment    string
	PlanID         uuid.UUID
	PrincipalID    string
	IdempotencyKey string
}

// NativeDeliveryBuild is the value-only build response projection. Optional
// native identities remain UUIDs (zero means omitted); physical pool IDs are
// authority-owned opaque text because the PostgreSQL schema intentionally
// treats them as pool identity strings rather than UUIDs.
type NativeDeliveryBuild struct {
	// ActorID, IdempotencyKey, RequestDigest, OperationID, EventID, and
	// AuditID are internal completion evidence. The HTTP response deliberately
	// omits them; the native PostgreSQL command completer uses them to re-read the
	// exact durable build consequences before acknowledging the mutation.
	ActorID string
	// OperationOwnerID is the server-allocated owner of the durable operation;
	// it is internal completion evidence and is not exposed in the HTTP DTO.
	OperationOwnerID      string
	IdempotencyKey        string
	RequestDigest         string
	OperationID           uuid.UUID
	EventID               uuid.UUID
	AuditID               uuid.UUID
	ID                    uuid.UUID
	PlanID                uuid.UUID
	PlanDigest            string
	SourceDigest          string
	ExecutionDigest       string
	BaseGenerationID      uuid.UUID
	BasePhysicalPoolID    string
	PhysicalPoolID        string
	WriterLeaseID         uuid.UUID
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        uuid.UUID
	Status                string
	SealID                uuid.UUID
	CandidateID           uuid.UUID
	FailureCode           string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	TerminalAt            time.Time
	Revision              int64
	CandidateRevision     int64
}

// NativeDeliveryMutationFuncs is a small adapter for composition and tests.
// The callbacks are expected to call the native PostgreSQL repository and any
// injected compiler/physical builder; nil callbacks fail closed.
type NativeDeliveryMutationFuncs struct {
	Plan  func(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error)
	Build func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error)
}

// executeNativeDeliveryCommand applies the generated command contract to a
// native mutation that has already committed. The durable completer remains
// the source of verification evidence; the generated executor owns the
// guarantee and marks the transport guard complete only after verification
// succeeds. Calls made outside a generated transport retain their historical
// no-op behavior so refresh/non-HTTP workers can invoke coordinators directly.
func executeNativeDeliveryCommand(
	ctx context.Context,
	operationID, auditAction string,
	verify func(context.Context) error,
	execute func(context.Context, *apigencommand.Executor, apigencommand.Execution) error,
) error {
	activeOperation, generated := apigencommand.OperationID(ctx)
	if !generated {
		return nil
	}
	if activeOperation != operationID {
		return fmt.Errorf("%w: active %q, completing %q", apigencommand.ErrOperationMismatch, activeOperation, operationID)
	}
	if verify == nil || execute == nil {
		return ErrDeliveryInputUnavailable
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return execute(ctx, executor, apigencommand.Execution{Transactional: func(verifyCtx context.Context, contract apigencommand.Contract) error {
		if contract.Guarantee != apigencommand.GuaranteeTransactional {
			return fmt.Errorf("%w: native delivery command %q does not provide transactional auditing", apigencommand.ErrInvalidContract, operationID)
		}
		if contract.AuditAction != auditAction {
			return fmt.Errorf("%w: native delivery command %q audit action is %q, want %q", apigencommand.ErrInvalidContract, operationID, contract.AuditAction, auditAction)
		}
		return verify(verifyCtx)
	}})
}

func completeNativePlanCommand(ctx context.Context, port NativeDeliveryMutationPort, plan NativeDeliveryPlan) error {
	operationID := deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID()
	completer, generated := port.(NativeDeliveryCommandCompleter)
	if !generated {
		if _, active := apigencommand.OperationID(ctx); active {
			return ErrDeliveryInputUnavailable
		}
		return nil
	}
	return executeNativeDeliveryCommand(ctx, operationID, "delivery.plan.created", func(verifyCtx context.Context) error {
		return completer.CompleteNativePlanCommand(verifyCtx, plan)
	}, func(execCtx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return deploymentgen.ExecuteGenCreateDeliveryPlanCommand(execCtx, executor, deploymentgen.GenCreateDeliveryPlanCommandInvocation{}, execution)
	})
}

func completeNativeBuildCommand(ctx context.Context, port NativeDeliveryMutationPort, build NativeDeliveryBuild) error {
	operationID := deploymentgen.GenCommandOperationBuildDeliveryPlan().APIGenOperationID()
	completer, generated := port.(NativeDeliveryCommandCompleter)
	if !generated {
		if _, active := apigencommand.OperationID(ctx); active {
			return ErrDeliveryInputUnavailable
		}
		return nil
	}
	return executeNativeDeliveryCommand(ctx, operationID, "delivery.build.sealed", func(verifyCtx context.Context) error {
		return completer.CompleteNativeBuildCommand(verifyCtx, build)
	}, func(execCtx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return deploymentgen.ExecuteGenBuildDeliveryPlanCommand(execCtx, executor, deploymentgen.GenBuildDeliveryPlanCommandInvocation{}, execution)
	})
}

func completeNativePublishCommand(ctx context.Context, port NativeDeliveryPublicationPort, publication NativeDeliveryPublication) error {
	operationID := deploymentgen.GenCommandOperationPublishDeliveryCandidate().APIGenOperationID()
	completer, generated := port.(NativeDeliveryPublicationCommandCompleter)
	if !generated {
		if _, active := apigencommand.OperationID(ctx); active {
			return ErrDeliveryInputUnavailable
		}
		return nil
	}
	return executeNativeDeliveryCommand(ctx, operationID, "delivery.publication.requested", func(verifyCtx context.Context) error {
		return completer.CompleteNativePublishCommand(verifyCtx, publication)
	}, func(execCtx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return deploymentgen.ExecuteGenPublishDeliveryCandidateCommand(execCtx, executor, deploymentgen.GenPublishDeliveryCandidateCommandInvocation{}, execution)
	})
}

func completeNativeRollbackCommand(ctx context.Context, port NativeDeliveryPublicationPort, publication NativeDeliveryPublication) error {
	operationID := deploymentgen.GenCommandOperationRollbackDeliveryGeneration().APIGenOperationID()
	completer, generated := port.(NativeDeliveryPublicationCommandCompleter)
	if !generated {
		if _, active := apigencommand.OperationID(ctx); active {
			return ErrDeliveryInputUnavailable
		}
		return nil
	}
	return executeNativeDeliveryCommand(ctx, operationID, "delivery.rollback.requested", func(verifyCtx context.Context) error {
		return completer.CompleteNativeRollbackCommand(verifyCtx, publication)
	}, func(execCtx context.Context, executor *apigencommand.Executor, execution apigencommand.Execution) error {
		return deploymentgen.ExecuteGenRollbackDeliveryGenerationCommand(execCtx, executor, deploymentgen.GenRollbackDeliveryGenerationCommandInvocation{}, execution)
	})
}

var _ NativeDeliveryMutationPort = NativeDeliveryMutationFuncs{}

func (f NativeDeliveryMutationFuncs) CreatePlan(ctx context.Context, request NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
	if f.Plan == nil {
		return NativeDeliveryPlan{}, ErrDeliveryInputUnavailable
	}
	return f.Plan(ctx, request)
}

func (f NativeDeliveryMutationFuncs) BuildPlan(ctx context.Context, request NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
	if f.Build == nil {
		return NativeDeliveryBuild{}, ErrDeliveryInputUnavailable
	}
	return f.Build(ctx, request)
}

// NewNativeDeliveryMutationAdapter makes callback wiring explicit while
// retaining a stable interface for application composition.
func NewNativeDeliveryMutationAdapter(plan func(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error), build func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error)) NativeDeliveryMutationPort {
	return NativeDeliveryMutationFuncs{Plan: plan, Build: build}
}

func (r NativeDeliveryPlanRequest) validate(environment string) error {
	if err := r.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	for label, value := range map[string]string{
		"target": r.TargetID, "environment": r.Environment, "principal": r.PrincipalID,
		"source digest": r.SourceDigest, "source attestation digest": r.SourceAttestationDigest,
		"idempotency key": r.IdempotencyKey,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: native delivery %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
	}
	if r.SourceOwnerID != "" && r.SourceOwnerID != strings.TrimSpace(r.SourceOwnerID) {
		return fmt.Errorf("%w: native delivery source owner is required and canonical", deployment.ErrDeliveryInvalid)
	}
	if environment != "" && r.Environment != environment {
		return fmt.Errorf("%w: environment does not match instance", deployment.ErrDeliveryInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(r.SourceDigest); err != nil {
		return fmt.Errorf("%w: source digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if err := platformdigest.ValidateSHA256Identity(r.SourceAttestationDigest); err != nil {
		return fmt.Errorf("%w: source attestation digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if r.PipelinePlan != nil {
		canonical := r.PipelinePlan.Canonical()
		if err := canonical.Validate(); err != nil {
			return fmt.Errorf("%w: pipeline plan: %v", deployment.ErrDeliveryInvalid, err)
		}
		if canonical.ProjectID != r.ProjectID.String() || canonical.Environment != r.Environment || canonical.ArtifactDigest != r.SourceDigest {
			return fmt.Errorf("%w: pipeline plan identity differs from native delivery request", deployment.ErrDeliveryConflict)
		}
	}
	switch deployment.DeliveryOperationKind(r.Operation) {
	case deployment.DeliveryOperationCodeChange, deployment.DeliveryOperationRestatement, deployment.DeliveryOperationBindingChange, deployment.DeliveryOperationPolicyChange:
	default:
		return fmt.Errorf("%w: native delivery operation is unsupported", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func (p NativeDeliveryPlan) validate(request NativeDeliveryPlanRequest, environment string) error {
	if p.ID == uuid.Nil || p.ID.String() != strings.TrimSpace(p.ID.String()) {
		return fmt.Errorf("%w: native plan identity is missing", deployment.ErrDeliveryInvalid)
	}
	if err := p.ProjectID.Validate(); err != nil || p.ProjectID != request.ProjectID {
		return fmt.Errorf("%w: native plan project identity differs from request", deployment.ErrDeliveryConflict)
	}
	if p.TargetID != request.TargetID || p.Environment != request.Environment || (environment != "" && p.Environment != environment) {
		return fmt.Errorf("%w: native plan scope differs from request", deployment.ErrDeliveryConflict)
	}
	if p.SourceDigest != request.SourceDigest || p.SourceAttestationDigest != request.SourceAttestationDigest {
		return fmt.Errorf("%w: native plan source identity differs from request", deployment.ErrDeliveryConflict)
	}
	if p.Operation != request.Operation {
		return fmt.Errorf("%w: native plan operation differs from request", deployment.ErrDeliveryConflict)
	}
	if p.Status != string(deploymentgen.DeliveryPlanStatusPlanned) {
		return fmt.Errorf("%w: native plan is not planned", deployment.ErrDeliveryConflict)
	}
	for label, value := range map[string]string{"plan digest": p.PlanDigest, "execution digest": p.ExecutionDigest, "provenance digest": p.ProvenanceDigest, "governance digest": p.GovernanceDigest, "evidence digest": p.EvidenceDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: %s: %v", deployment.ErrDeliveryInvalid, label, err)
		}
	}
	if p.BaseGenerationID != uuid.Nil && p.BaseGenerationID.String() != strings.TrimSpace(p.BaseGenerationID.String()) {
		return fmt.Errorf("%w: native base generation identity is not canonical", deployment.ErrDeliveryInvalid)
	}
	if p.BaseTargetRevision < 0 || p.CreatedAt.IsZero() || p.ExpiresAt.IsZero() || !p.CreatedAt.Equal(p.CreatedAt.UTC()) || !p.ExpiresAt.Equal(p.ExpiresAt.UTC()) || !p.ExpiresAt.After(p.CreatedAt) {
		return fmt.Errorf("%w: native plan timestamps are incomplete", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func (r NativeDeliveryBuildRequest) validate(environment string) error {
	if err := r.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	if r.PlanID == uuid.Nil || r.PlanID.String() != strings.TrimSpace(r.PlanID.String()) {
		return fmt.Errorf("%w: plan identity must be a canonical UUID", deployment.ErrDeliveryInvalid)
	}
	for label, value := range map[string]string{"target": r.TargetID, "environment": r.Environment, "principal": r.PrincipalID, "idempotency key": r.IdempotencyKey} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: native delivery %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: native delivery %s is invalid", deployment.ErrDeliveryInvalid, label)
		}
	}
	if environment != "" && r.Environment != environment {
		return fmt.Errorf("%w: environment does not match instance", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func (r NativeDeliveryPublishRequest) validate(environment string) error {
	if err := r.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	if r.CandidateID == uuid.Nil || r.CandidateID.String() != strings.TrimSpace(r.CandidateID.String()) {
		return fmt.Errorf("%w: candidate identity must be a canonical UUID", deployment.ErrDeliveryInvalid)
	}
	for label, value := range map[string]string{"target": r.TargetID, "environment": r.Environment, "principal": r.PrincipalID, "idempotency key": r.IdempotencyKey} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: native delivery %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: native delivery %s is invalid", deployment.ErrDeliveryInvalid, label)
		}
	}
	if environment != "" && r.Environment != environment {
		return fmt.Errorf("%w: environment does not match instance", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func (r NativeDeliveryRollbackRequest) validate(environment string) error {
	if err := r.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	if r.GenerationID == uuid.Nil || r.GenerationID.String() != strings.TrimSpace(r.GenerationID.String()) {
		return fmt.Errorf("%w: generation identity must be a canonical UUID", deployment.ErrDeliveryInvalid)
	}
	for label, value := range map[string]string{"target": r.TargetID, "environment": r.Environment, "principal": r.PrincipalID, "idempotency key": r.IdempotencyKey} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: native delivery %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: native delivery %s is invalid", deployment.ErrDeliveryInvalid, label)
		}
	}
	if environment != "" && r.Environment != environment {
		return fmt.Errorf("%w: environment does not match instance", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func (p NativeDeliveryPublication) validate(project projectgraph.ResourceID, target, environment string) error {
	if p.ID == uuid.Nil || p.OperationID == uuid.Nil || p.EventID == uuid.Nil || p.AuditID == uuid.Nil || p.PlanID == uuid.Nil || p.CandidateID == uuid.Nil || p.GenerationID == uuid.Nil {
		return fmt.Errorf("%w: native publication identities are incomplete", deployment.ErrDeliveryInvalid)
	}
	if p.ProjectID != project || p.TargetID != target || p.Environment != environment {
		return fmt.Errorf("%w: native publication scope differs from request", deployment.ErrDeliveryConflict)
	}
	if p.ID != p.OperationID || p.ID.String() != strings.TrimSpace(p.ID.String()) || p.OperationID.String() != strings.TrimSpace(p.OperationID.String()) || p.EventID.String() != strings.TrimSpace(p.EventID.String()) || p.AuditID.String() != strings.TrimSpace(p.AuditID.String()) {
		return fmt.Errorf("%w: native publication identities are not canonical", deployment.ErrDeliveryInvalid)
	}
	if p.Status != "pending" {
		return fmt.Errorf("%w: native publication must remain pending", deployment.ErrDeliveryConflict)
	}
	if err := platformdigest.ValidateSHA256Identity(p.PlanDigest); err != nil {
		return fmt.Errorf("%w: plan digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if err := platformdigest.ValidateSHA256Identity(p.RequestDigest); err != nil {
		return fmt.Errorf("%w: request digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if p.ExpectedTargetRevision <= 0 || p.CreatedAt.IsZero() || !p.CreatedAt.Equal(p.CreatedAt.UTC()) || !p.CompletedAt.IsZero() {
		return fmt.Errorf("%w: native publication lifecycle evidence is invalid", deployment.ErrDeliveryInvalid)
	}
	if p.ExpectedBaseGenerationID != uuid.Nil && p.ExpectedBaseGenerationID.String() != strings.TrimSpace(p.ExpectedBaseGenerationID.String()) {
		return fmt.Errorf("%w: native base generation identity is not canonical", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func (b NativeDeliveryBuild) validate(request NativeDeliveryBuildRequest) error {
	if b.ID == uuid.Nil || b.PlanID == uuid.Nil || b.ID.String() != strings.TrimSpace(b.ID.String()) || b.PlanID.String() != strings.TrimSpace(b.PlanID.String()) {
		return fmt.Errorf("%w: native build and plan identities are incomplete", deployment.ErrDeliveryInvalid)
	}
	if b.PlanID != request.PlanID {
		return fmt.Errorf("%w: native build plan identity differs from request", deployment.ErrDeliveryConflict)
	}
	for label, value := range map[string]string{"plan digest": b.PlanDigest, "source digest": b.SourceDigest, "execution digest": b.ExecutionDigest, "physical pool": b.PhysicalPoolID} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil && label != "physical pool" {
			return fmt.Errorf("%w: %s: %v", deployment.ErrDeliveryInvalid, label, err)
		}
		if label == "physical pool" && (value == "" || value != strings.TrimSpace(value)) {
			return fmt.Errorf("%w: physical pool identity is incomplete", deployment.ErrDeliveryInvalid)
		}
	}
	if b.WriterLeaseID == uuid.Nil {
		return fmt.Errorf("%w: native writer lease identity is missing", deployment.ErrDeliveryInvalid)
	}
	if b.Status != string(deploymentgen.DeliveryBuildStatusSealed) || b.SealID == uuid.Nil || b.CandidateID == uuid.Nil || b.ServingStateID == uuid.Nil {
		return fmt.Errorf("%w: native build is not completely sealed", deployment.ErrDeliveryConflict)
	}
	if b.ServingArtifactID == "" || b.ServingArtifactID != strings.TrimSpace(b.ServingArtifactID) || platformdigest.ValidateSHA256Identity(b.ServingArtifactDigest) != nil {
		return fmt.Errorf("%w: native serving artifact identity is incomplete", deployment.ErrDeliveryInvalid)
	}
	if b.Revision <= 0 || b.CandidateRevision <= 0 || b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() || b.TerminalAt.IsZero() || !b.CreatedAt.Equal(b.CreatedAt.UTC()) || !b.UpdatedAt.Equal(b.UpdatedAt.UTC()) || !b.TerminalAt.Equal(b.TerminalAt.UTC()) || b.UpdatedAt.Before(b.CreatedAt) || b.TerminalAt.Before(b.UpdatedAt) {
		return fmt.Errorf("%w: native build lifecycle evidence is incomplete", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func nativePlanPreviewResponse(plan NativeDeliveryPlan) deploymentgen.DeliveryPlanPreviewResponse {
	baseGeneration := optionalNativeUUID(plan.BaseGenerationID)
	return deploymentgen.DeliveryPlanPreviewResponse{
		Id: plan.ID.String(), ProjectId: plan.ProjectID.String(), TargetId: plan.TargetID,
		Environment: plan.Environment, Operation: deploymentgen.DeliveryOperationKind(plan.Operation),
		SourceDigest: plan.SourceDigest, SourceAttestationDigest: plan.SourceAttestationDigest,
		BaseGenerationId: baseGeneration, BaseTargetRevision: plan.BaseTargetRevision,
		ExecutionDigest: plan.ExecutionDigest, ProvenanceDigest: plan.ProvenanceDigest,
		GovernanceDigest: plan.GovernanceDigest, EvidenceDigest: plan.EvidenceDigest,
		PlanDigest: plan.PlanDigest, Status: deploymentgen.DeliveryPlanStatus(plan.Status),
		ExpiresAt: isoTime(plan.ExpiresAt), CreatedAt: isoTime(plan.CreatedAt), Evidence: plan.Evidence,
	}
}

func nativeBuildStatusResponse(build NativeDeliveryBuild) deploymentgen.DeliveryBuildStatusResponse {
	return deploymentgen.DeliveryBuildStatusResponse{
		Id: build.ID.String(), PlanId: build.PlanID.String(), PlanDigest: build.PlanDigest,
		SourceDigest: build.SourceDigest, ExecutionDigest: build.ExecutionDigest,
		BaseGenerationId:   optionalNativeUUID(build.BaseGenerationID),
		BasePhysicalPoolId: optionalText(build.BasePhysicalPoolID), PhysicalPoolId: build.PhysicalPoolID,
		WriterLeaseId: build.WriterLeaseID.String(), Status: deploymentgen.DeliveryBuildStatus(build.Status),
		SnapshotSealId: optionalNativeUUID(build.SealID), CandidateId: optionalNativeUUID(build.CandidateID),
		FailureCode: optionalText(build.FailureCode), Revision: build.Revision, CandidateRevision: optionalNativeInt64(build.CandidateRevision),
		CreatedAt: isoTime(build.CreatedAt), UpdatedAt: isoTime(build.UpdatedAt), TerminalAt: optionalText(isoTime(build.TerminalAt)),
	}
}

func optionalNativeInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func optionalNativeUUID(value uuid.UUID) *string {
	if value == uuid.Nil {
		return nil
	}
	encoded := value.String()
	return &encoded
}
