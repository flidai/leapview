package refreshpostgres

// Native canonical refresh execution deliberately stops at the native
// delivery build boundary. Plan and build are the only mutation authorities
// used here; publication and activation belong to the refresh PostgreSQL
// persistence/finalizer, which joins their transaction to the refresh run
// lease and data-version evidence. Keeping that split prevents a pending
// delivery publication from being created outside the finalizer's fence.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/google/uuid"
)

// NativeRefreshDeliveryReader is the immutable native delivery read surface
// needed to resolve the active base and recover exact sealed build evidence.
// deploymentmodule.NativeDeliveryReader satisfies this interface; the
// narrower local contract keeps tests and alternate read-only adapters small.
type NativeRefreshDeliveryReader interface {
	OperatorSnapshot(context.Context, string) (deploymentnative.DeliveryOperatorSnapshot, error)
	LoadPlan(context.Context, string) (deploymentnative.DeliveryPlan, error)
	LoadBuildAttempt(context.Context, string) (deploymentnative.DeliveryBuildAttempt, error)
	LoadSnapshotSeal(context.Context, string) (deploymentnative.SnapshotSeal, error)
	LoadCandidate(context.Context, string) (deploymentnative.DeliveryCandidate, error)
	LoadGeneration(context.Context, string) (deploymentnative.DeliveryGeneration, error)
}

// NativeRefreshDeliveryMutations is the complete command boundary required by
// refresh execution. The completion methods re-read operation/event/audit and
// full persisted plan/build evidence; invoking only the mutation methods would
// bypass that generated-command contract.
type NativeRefreshDeliveryMutations interface {
	deploymentmodule.NativeDeliveryMutationPort
	deploymentmodule.NativeDeliveryCommandCompleter
}

// PostgresNativeRefreshExecutor adapts the typed native plan/build mutation
// port to refresh/run's canonical executor callback. It is target-bound: the
// target is process-owned and is never taken from a refresh job payload.
type PostgresNativeRefreshExecutor struct {
	Mutations NativeRefreshDeliveryMutations
	Reader    NativeRefreshDeliveryReader
	TargetID  string
}

var _ NativeRefreshDeliveryReader = (deploymentmodule.NativeDeliveryReader)(nil)

// NewPostgresNativeRefreshExecutor validates a target-bound native executor
// without opening a database or performing any I/O.
func NewPostgresNativeRefreshExecutor(mutations NativeRefreshDeliveryMutations, reader NativeRefreshDeliveryReader, targetID string) (*PostgresNativeRefreshExecutor, error) {
	if mutations == nil {
		return nil, errors.New("native refresh delivery mutations are required")
	}
	if reader == nil {
		return nil, errors.New("native refresh delivery reader is required")
	}
	if targetID == "" || targetID != strings.TrimSpace(targetID) || len(targetID) > 255 {
		return nil, errors.New("native refresh delivery target id must be canonical")
	}
	return &PostgresNativeRefreshExecutor{Mutations: mutations, Reader: reader, TargetID: targetID}, nil
}

// Execute performs one idempotent native refresh restatement. The native
// mutation coordinators own replay and transactional consequences; this
// adapter only carries stable identity and verifies the resulting immutable
// generation/seal tuple before refresh completion is attempted.
func (e *PostgresNativeRefreshExecutor) Execute(ctx context.Context, job refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
	if e == nil || e.Mutations == nil || e.Reader == nil || e.TargetID == "" {
		return refreshrun.CanonicalRefreshResult{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	if err := validateNativeRefreshJob(job); err != nil {
		return refreshrun.CanonicalRefreshResult{}, err
	}

	snapshot, err := e.Reader.OperatorSnapshot(ctx, e.TargetID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh target: %w", err)
	}
	if snapshot.TargetID != e.TargetID || snapshot.ProjectID != job.Identity.ProjectID.String() || snapshot.Environment != job.Identity.Environment {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh target scope differs from job", deployment.ErrDeliveryConflict)
	}
	if snapshot.TargetRevision <= 0 || strings.TrimSpace(snapshot.ActiveGenerationID) == "" {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh active target evidence is unavailable", deployment.ErrDeliveryConflict)
	}
	if snapshot.TargetRevision != job.TargetRevision {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("native refresh target fence changed before execution: %w", refreshrun.ErrRunStale)
	}
	if snapshot.ActiveGenerationID != job.Identity.GenerationID {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("native refresh base changed before execution: %w", refreshrun.ErrRunStale)
	}
	if _, err := canonicalNativeUUID(snapshot.ActiveGenerationID, "active generation"); err != nil {
		return refreshrun.CanonicalRefreshResult{}, err
	}

	baseGeneration, err := e.Reader.LoadGeneration(ctx, snapshot.ActiveGenerationID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh base generation: %w", err)
	}
	if baseGeneration.GenerationID != snapshot.ActiveGenerationID || baseGeneration.TargetID != e.TargetID || baseGeneration.PlanID == "" || baseGeneration.CandidateID == "" || baseGeneration.SnapshotSealID == "" {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh base generation evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	basePlanRow, err := e.Reader.LoadPlan(ctx, baseGeneration.PlanID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh base plan: %w", err)
	}
	basePlan, err := basePlanRow.RichPlan()
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh rich plan: %w", err)
	}
	if basePlan.ID != baseGeneration.PlanID || basePlan.TargetID != e.TargetID || basePlan.ProjectID != job.Identity.ProjectID || basePlan.Environment != job.Identity.Environment || basePlan.Digest != baseGeneration.PlanDigest {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh base plan identity differs", deployment.ErrDeliveryConflict)
	}
	if err := platformdigest.ValidateSHA256Identity(basePlan.SourceDigest); err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh source digest: %v", deployment.ErrDeliveryConflict, err)
	}
	attestationDigest := strings.TrimSpace(basePlan.Provenance.AttestationDigest)
	if err := platformdigest.ValidateSHA256Identity(attestationDigest); err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh source attestation digest: %v", deployment.ErrDeliveryConflict, err)
	}
	sourceOwnerID := strings.TrimSpace(basePlan.SourceOwnerID)
	if sourceOwnerID == "" {
		sourceOwnerID = strings.TrimSpace(basePlan.ActorID)
	}
	if sourceOwnerID == "" {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh source owner is unavailable", deployment.ErrDeliveryConflict)
	}

	var pipelinePlan *projectpipelineplan.Plan
	if job.PipelinePlan != nil {
		canonical := job.PipelinePlan.Canonical()
		pipelinePlan = &canonical
	}
	plan, err := e.Mutations.CreatePlan(ctx, deploymentmodule.NativeDeliveryPlanRequest{
		ProjectID: job.Identity.ProjectID, TargetID: e.TargetID, Environment: job.Identity.Environment,
		PrincipalID: job.PrincipalID, SourceOwnerID: sourceOwnerID,
		Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: basePlan.SourceDigest,
		SourceAttestationDigest: attestationDigest, IdempotencyKey: "refresh-plan-" + job.RunID,
		PipelinePlan: pipelinePlan,
	})
	if err != nil {
		if errors.Is(err, deploymentnative.ErrStaleFence) || errors.Is(err, deploymentnative.ErrCASConflict) {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("plan canonical refresh: %w: %v", refreshrun.ErrRunStale, err)
		}
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("plan canonical refresh: %w", err)
	}
	if err := validateNativeRefreshPlan(plan, job, e.TargetID, snapshot, basePlan.SourceDigest, attestationDigest); err != nil {
		return refreshrun.CanonicalRefreshResult{}, err
	}
	if err := e.Mutations.CompleteNativePlanCommand(ctx, plan); err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("complete native refresh plan command: %w", err)
	}

	build, err := e.Mutations.BuildPlan(ctx, deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: job.Identity.ProjectID, TargetID: e.TargetID, Environment: job.Identity.Environment,
		PlanID: plan.ID, PrincipalID: job.PrincipalID, IdempotencyKey: "refresh-build-" + job.RunID,
	})
	if err != nil {
		if errors.Is(err, deploymentnative.ErrStaleFence) || errors.Is(err, deploymentnative.ErrCASConflict) {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("build canonical refresh: %w: %v", refreshrun.ErrRunStale, err)
		}
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("build canonical refresh: %w", err)
	}
	if err := validateNativeRefreshBuild(build, plan, snapshot.ActiveGenerationID); err != nil {
		return refreshrun.CanonicalRefreshResult{}, err
	}
	if err := e.Mutations.CompleteNativeBuildCommand(ctx, build); err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("complete native refresh build command: %w", err)
	}

	attempt, err := e.Reader.LoadBuildAttempt(ctx, build.ID.String())
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh build attempt: %w", err)
	}
	if attempt.AttemptID != build.ID.String() || attempt.PlanID != plan.ID.String() || attempt.CandidateID != build.CandidateID.String() || attempt.State != deploymentnative.AttemptCommitted || attempt.SnapshotID <= 0 {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh build attempt evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	seal, err := e.Reader.LoadSnapshotSeal(ctx, build.SealID.String())
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh snapshot seal: %w", err)
	}
	if seal.SealID != build.SealID.String() || seal.AttemptID != build.ID.String() || seal.CandidateID != build.CandidateID.String() || seal.PlanDigest != plan.PlanDigest || seal.DuckLakeSnapshotID != attempt.SnapshotID || seal.DuckLakeSnapshotID <= 0 {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh snapshot seal evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	candidate, err := e.Reader.LoadCandidate(ctx, build.CandidateID.String())
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh candidate: %w", err)
	}
	if candidate.CandidateID != build.CandidateID.String() || candidate.TargetID != e.TargetID || candidate.PlanID != plan.ID.String() || candidate.AttemptID != build.ID.String() || candidate.SnapshotSealID != build.SealID.String() || candidate.Status != "qualified" {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh candidate evidence is incomplete", deployment.ErrDeliveryConflict)
	}
	generation, err := e.Reader.LoadGeneration(ctx, build.ServingStateID.String())
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve native refresh generation: %w", err)
	}
	if generation.GenerationID != build.ServingStateID.String() || generation.TargetID != e.TargetID || generation.CandidateID != build.CandidateID.String() || generation.SnapshotSealID != build.SealID.String() || generation.PlanID != plan.ID.String() || generation.ServingArtifactDigest != build.ServingArtifactDigest {
		return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("%w: native refresh generation evidence is incomplete", deployment.ErrDeliveryConflict)
	}

	// Native generation admission binds serving-state and delivery-generation
	// UUIDs. Populate both result fields explicitly so the finalizer can require
	// that identity equality before opening its publication transaction.
	return refreshrun.CanonicalRefreshResult{
		PlanID: plan.ID.String(), ServingStateID: build.ServingStateID.String(),
		NativeGenerationID: build.ServingStateID.String(), SnapshotID: seal.DuckLakeSnapshotID,
	}, nil
}

func validateNativeRefreshJob(job refreshrun.JobRecord) error {
	if err := job.Validate(); err != nil {
		return err
	}
	for label, value := range map[string]string{"run id": job.RunID, "principal id": job.PrincipalID} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: native refresh %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
	}
	if job.TargetRevision <= 0 {
		return fmt.Errorf("%w: native refresh target revision is required", deployment.ErrDeliveryInvalid)
	}
	if job.PipelinePlan == nil {
		return fmt.Errorf("%w: native refresh pipeline plan is required", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func validateNativeRefreshPlan(plan deploymentmodule.NativeDeliveryPlan, job refreshrun.JobRecord, targetID string, snapshot deploymentnative.DeliveryOperatorSnapshot, sourceDigest, attestationDigest string) error {
	if _, err := canonicalNativeUUID(plan.ID.String(), "plan"); err != nil {
		return err
	}
	if plan.ProjectID != job.Identity.ProjectID || plan.TargetID != targetID || plan.Environment != job.Identity.Environment || plan.Operation != string(deployment.DeliveryOperationRestatement) || plan.SourceDigest != sourceDigest || plan.SourceAttestationDigest != attestationDigest || plan.BaseGenerationID.String() != snapshot.ActiveGenerationID || plan.BaseTargetRevision != snapshot.TargetRevision || plan.Status != "planned" {
		return fmt.Errorf("%w: native refresh plan identity differs", deployment.ErrDeliveryConflict)
	}
	return nil
}

func validateNativeRefreshBuild(build deploymentmodule.NativeDeliveryBuild, plan deploymentmodule.NativeDeliveryPlan, baseGenerationID string) error {
	for label, value := range map[string]string{"build": build.ID.String(), "candidate": build.CandidateID.String(), "seal": build.SealID.String(), "serving state": build.ServingStateID.String()} {
		if _, err := canonicalNativeUUID(value, label); err != nil {
			return err
		}
	}
	if build.PlanID != plan.ID || build.BaseGenerationID.String() != baseGenerationID || build.Status != "sealed" || build.SourceDigest != plan.SourceDigest || build.PlanDigest != plan.PlanDigest || build.ServingArtifactDigest == "" {
		return fmt.Errorf("%w: native refresh build identity differs", deployment.ErrDeliveryConflict)
	}
	return nil
}

func canonicalNativeUUID(value, label string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		return uuid.Nil, fmt.Errorf("%w: native refresh %s identity is not a canonical UUIDv7", deployment.ErrDeliveryConflict, label)
	}
	return parsed, nil
}
