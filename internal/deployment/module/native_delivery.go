package module

// Native delivery contracts intentionally stop at the module boundary.  They
// carry PostgreSQL delivery identities as UUID values and do not expose the
// legacy DeliveryLifecycle, file-catalog, or SQLite candidate models.  The
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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// NativeDeliveryMutationPort is the clean-slate PostgreSQL plan/build seam
// used by the HTTP handlers. Publication and rollback remain owned by the
// existing publication coordinator (DeliveryMutationPort). Implementations
// must perform any compiler evidence, candidate admission, physical build,
// seal, and consequence recording before returning a result.
type NativeDeliveryMutationPort interface {
	CreatePlan(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error)
	BuildPlan(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error)
}

// NativeDeliveryCommandCompleter is optional for ports used outside the
// generated HTTP transport. Production ports should implement it so the
// APIGen command guard can verify native event/audit evidence without forcing
// the module to depend on a legacy delivery reader.
type NativeDeliveryCommandCompleter interface {
	CompleteNativePlanCommand(context.Context, NativeDeliveryPlan) error
	CompleteNativeBuildCommand(context.Context, NativeDeliveryBuild) error
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
}

// NativeDeliveryPlan is the value-only response projection returned by a
// native planner. IDs are UUID-native; target and project identities retain
// their domain-native forms.
type NativeDeliveryPlan struct {
	ID uuid.UUID
	// ActorID, RequestDigest, EventID, and AuditID are internal completion
	// evidence. The HTTP response intentionally does not expose them; the
	// production command completer uses them to re-read the exact durable
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
	// omits them; the production command completer uses them to re-read the
	// exact durable build consequences before acknowledging the mutation.
	ActorID               string
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
	BaseCatalogDigest     string
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
}

// NativeDeliveryMutationFuncs is a small adapter for composition and tests.
// The callbacks are expected to call the native PostgreSQL repository and any
// injected compiler/physical builder; nil callbacks fail closed.
type NativeDeliveryMutationFuncs struct {
	Plan  func(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error)
	Build func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error)
}

func completeNativePlanCommand(ctx context.Context, port NativeDeliveryMutationPort, plan NativeDeliveryPlan) error {
	operationID, generated := apigencommand.OperationID(ctx)
	if !generated {
		return nil
	}
	if operationID != deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID() {
		return fmt.Errorf("%w: active %q is not native plan creation", apigencommand.ErrOperationMismatch, operationID)
	}
	completer, ok := port.(NativeDeliveryCommandCompleter)
	if !ok {
		return ErrDeliveryInputUnavailable
	}
	return completer.CompleteNativePlanCommand(ctx, plan)
}

func completeNativeBuildCommand(ctx context.Context, port NativeDeliveryMutationPort, build NativeDeliveryBuild) error {
	operationID, generated := apigencommand.OperationID(ctx)
	if !generated {
		return nil
	}
	if operationID != deploymentgen.GenCommandOperationBuildDeliveryPlan().APIGenOperationID() {
		return fmt.Errorf("%w: active %q is not native plan build", apigencommand.ErrOperationMismatch, operationID)
	}
	completer, ok := port.(NativeDeliveryCommandCompleter)
	if !ok {
		return ErrDeliveryInputUnavailable
	}
	return completer.CompleteNativeBuildCommand(ctx, build)
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
	}
	if environment != "" && r.Environment != environment {
		return fmt.Errorf("%w: environment does not match instance", deployment.ErrDeliveryInvalid)
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
	if b.Revision <= 0 || b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() || b.TerminalAt.IsZero() || !b.CreatedAt.Equal(b.CreatedAt.UTC()) || !b.UpdatedAt.Equal(b.UpdatedAt.UTC()) || !b.TerminalAt.Equal(b.TerminalAt.UTC()) || b.UpdatedAt.Before(b.CreatedAt) || b.TerminalAt.Before(b.UpdatedAt) {
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
		BaseGenerationId: optionalNativeUUID(build.BaseGenerationID), BaseCatalogDigest: optionalText(build.BaseCatalogDigest),
		BasePhysicalPoolId: optionalText(build.BasePhysicalPoolID), PhysicalPoolId: build.PhysicalPoolID,
		WriterLeaseId: build.WriterLeaseID.String(), Status: deploymentgen.DeliveryBuildStatus(build.Status),
		SealId: optionalNativeUUID(build.SealID), CandidateId: optionalNativeUUID(build.CandidateID),
		FailureCode: optionalText(build.FailureCode), Revision: build.Revision,
		CreatedAt: isoTime(build.CreatedAt), UpdatedAt: isoTime(build.UpdatedAt), TerminalAt: optionalText(isoTime(build.TerminalAt)),
	}
}

func optionalNativeUUID(value uuid.UUID) *string {
	if value == uuid.Nil {
		return nil
	}
	encoded := value.String()
	return &encoded
}
