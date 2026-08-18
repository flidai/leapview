package deployment

// This file contains the composition-level plan/build lifecycle.  It is
// intentionally expressed in ports: control state remains owned by the
// deployment repository while physical catalog construction and sealing are
// supplied by target-owned adapters.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

// DeliveryCandidateBuildInput is the transport-neutral hand-off used by the
// existing candidate synchronization endpoint. The module commits the exact
// source snapshot first, then delegates candidate construction to this port;
// implementations are expected to call Plan and Build on the same lifecycle.
type DeliveryCandidateBuildInput struct {
	ProjectID      projectgraph.ResourceID
	OwnerID        string
	ArtifactDigest string
	Operation      DeliveryOperationKind
	CandidateKey   string
	Candidate      Candidate
	Source         project.CandidateSourceSnapshot
	Plan           *DeliveryPlan
}

// DeliveryTarget is the read-only target fence used by planning and build
// stale checks.  TargetRevision is the sole publication/build authority.
type DeliveryTarget struct {
	TargetID           string
	ProjectID          string
	Environment        string
	ActiveGenerationID string
	TargetRevision     int64
}

// DeliveryTargetResolver never acquires writer credentials or touches object
// storage.  Implementations read the authoritative target revision and active
// generation from the control plane.
type DeliveryTargetResolver interface {
	ResolveDeliveryTarget(context.Context, string) (DeliveryTarget, error)
}

// DeliveryPlanRequest is the explicit input to plan. Persist must be true for
// the mutating command; Preview is a read-only convenience which sets it false.
type DeliveryPlanRequest struct {
	ID           string
	ActorID      string
	TargetID     string
	ProjectID    string
	Environment  string
	Operation    DeliveryOperationKind
	SourceDigest string
	// SourceAttestationDigest is an opaque target-issued identity for the
	// exact retained source revision. It participates in provenance/plan
	// identity, never execution identity.
	SourceAttestationDigest string
	Execution               DeliveryExecutionInputs
	Provenance              DeliveryProvenance
	Governance              DeliveryGovernance
	Evidence                DeliveryPlanEvidence
	CreatedAt               time.Time
	Persist                 bool
}

// DeliveryPlanRepository is the narrow persistence port needed for planning.
type DeliveryPlanRepository interface {
	CreatePlan(context.Context, DeliveryPlan) (DeliveryPlan, error)
	PlanByID(context.Context, string) (DeliveryPlan, error)
}

// DeliveryBuildRepository owns durable attempt/lease transitions. A
// repository implementation must make CreateWriterLeaseAndBuildAttempt
// idempotent for an identical identity and reject drift as a conflict.
type DeliveryBuildRepository interface {
	DeliveryPlanRepository
	CreateWriterLeaseAndBuildAttempt(context.Context, DeliveryWriterLease, DeliveryBuildAttempt) (DeliveryWriterLease, DeliveryBuildAttempt, error)
	DeliveryBuildAttemptByID(context.Context, string) (DeliveryBuildAttempt, error)
	TransitionBuildAttempt(context.Context, string, int64, DeliveryBuildAttemptStatus, time.Time) (DeliveryBuildAttempt, error)
	MarkBuildFailed(context.Context, string, int64, string, time.Time) (DeliveryBuildAttempt, error)
}

// DeliveryBuildFailureReleaser atomically marks a deterministic pre-seal
// failure and releases the exact writer lease. Implementations should use one
// control-plane transaction so failed attempts cannot keep a writer lease
// alive until TTL expiry.
type DeliveryBuildFailureReleaser interface {
	MarkBuildFailedAndReleaseLease(context.Context, string, int64, DeliveryWriterLease, string, time.Time) (DeliveryBuildAttempt, error)
}

// DeliveryWriterLeaseReleaser is the compatibility fallback for repositories
// which predate the atomic failure operation. It is still exact/fenced, but a
// terminal-release adapter is preferred whenever available.
type DeliveryWriterLeaseReleaser interface {
	TransitionWriterLease(context.Context, string, DeliveryLeaseStatus, DeliveryLeaseStatus, time.Time) (DeliveryWriterLease, error)
}

// DeliveryCompletionReader is implemented by durable adapters which can
// reconstruct the complete sealed identity after a process restart. Build
// uses it for sealed-attempt retries instead of returning a candidate ID
// without seal/object/artifact evidence.
type DeliveryCompletionReader interface {
	CompletedDelivery(ctx context.Context, attemptID, candidateID string) (catalogseal.Completion, error)
}

// DeliveryPlanResult records whether a durable row was written. Keeping this
// bit explicit prevents preview callers from mistaking an in-memory plan for
// target deployment state.
type DeliveryPlanResult struct {
	Plan      DeliveryPlan
	Persisted bool
}

// DeliveryBuildInput is passed to the target-owned physical runner after the
// exact plan, lease, and attempt have been durably established.
type DeliveryBuildInput struct {
	Plan    DeliveryPlan
	Attempt DeliveryBuildAttempt
	Lease   DeliveryWriterLease
}

// DeliveryBuildOutput is the only physical result accepted by Build. The
// detached catalog is still private until catalogseal.Seal completes.
type DeliveryBuildOutput struct {
	Catalog             catalogseal.DetachedCatalog
	QualificationDigest string
	ClosureDigest       string
	CompatibilityDigest string
	// ResolvedInputs is immutable build-time evidence for each planned data
	// input. It is persisted with the ready candidate and is never inferred
	// from the planning declaration after physical work begins.
	ResolvedInputs DeliveryResolvedBuildInputs
	ObjectStore    catalogseal.ObjectStore
	SealRepository catalogseal.SealRepository
	RemoteVerifier catalogseal.RemoteVerifier
	Cleanup        func() error
}

// DeliveryArtifactIdentity is the immutable serving artifact identity
// produced by candidate preparation. Preparation is deliberately deferred
// until after the durable build attempt and writer lease have been created.
// This keeps serving-state/artifact rows inside the attempt's crash ledger.
type DeliveryArtifactIdentity = release.CandidateArtifactIdentity

// DeliveryArtifactPreparer runs once the build attempt exists, immediately
// before the phased runner constructs the private catalog. It may create
// durable serving-state/artifact rows, but must return their exact identities
// so the later seal binds those rows rather than inferred values.
type DeliveryArtifactPreparer func(context.Context, DeliveryBuildInput) (DeliveryArtifactIdentity, error)

// DeliveryBuildArtifactBinder durably binds the prepared serving identity to
// the exact attempt. Implementations must be idempotent and reject a foreign
// identity for an existing attempt.
type DeliveryBuildArtifactBinder interface {
	BindDeliveryBuildArtifacts(context.Context, string, int64, DeliveryArtifactIdentity, time.Time) (DeliveryBuildAttempt, error)
}

// DeliveryBuildRunner constructs, normalizes, and fully qualifies one private
// candidate catalog. It must use candidatecatalog's lease-checked APIs and
// must not mark a candidate ready itself.
type DeliveryBuildRunner func(context.Context, DeliveryBuildInput) (DeliveryBuildOutput, error)

// DeliveryBuildPhasedRunner lets durable attempt status describe the actual
// physical boundaries. Construct runs while status is building; Normalize and
// Qualify run under their corresponding durable states.
type DeliveryBuildPhasedRunner interface {
	Construct(context.Context, DeliveryBuildInput) (any, error)
	Normalize(context.Context, DeliveryBuildInput, any) error
	Qualify(context.Context, DeliveryBuildInput, any) (DeliveryBuildOutput, error)
}

// DeliveryBuildRequest contains immutable identities supplied by the command.
// Base catalog bytes and physical-pool contracts are owned by the runner; this
// port deliberately keeps credentials out of deployment.
type DeliveryBuildRequest struct {
	PlanID                string
	IdempotencyKey        string
	AttemptID             string
	WriterLeaseID         string
	CandidateID           string
	SealID                string
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
	PhysicalPoolID        string
	BaseCatalogDigest     string
	BasePhysicalPoolID    string
	OwnerID               string
	Epoch                 int64
	LeaseLifetime         time.Duration
	CreatedAt             time.Time
	Runner                DeliveryBuildRunner
	PhasedRunner          DeliveryBuildPhasedRunner
	PrepareArtifacts      DeliveryArtifactPreparer
}

type DeliveryBuildResult struct {
	Attempt    DeliveryBuildAttempt
	Completion catalogseal.Completion
}

// DeliveryLifecycle is the canonical target lifecycle used by module, API,
// CLI, and automation callers.
type DeliveryLifecycle struct {
	Targets DeliveryTargetResolver
	Store   DeliveryBuildRepository
	Now     func() time.Time
}

func NewDeliveryLifecycle(targets DeliveryTargetResolver, store DeliveryBuildRepository) (*DeliveryLifecycle, error) {
	if targets == nil || store == nil {
		return nil, fmt.Errorf("delivery target resolver and repository are required")
	}
	return &DeliveryLifecycle{Targets: targets, Store: store, Now: time.Now}, nil
}

func (l *DeliveryLifecycle) now() time.Time {
	if l != nil && l.Now != nil {
		return l.Now().UTC()
	}
	return time.Now().UTC()
}

func closeBuildPhase(value any) error {
	if value == nil {
		return nil
	}
	switch handle := value.(type) {
	case interface{ Close() error }:
		return handle.Close()
	case interface{ Cleanup() error }:
		return handle.Cleanup()
	case func() error:
		return handle()
	default:
		return nil
	}
}

func (l *DeliveryLifecycle) markBuildFailed(ctx context.Context, attempt DeliveryBuildAttempt, lease DeliveryWriterLease, code string) error {
	now := l.now()
	if terminal, ok := l.Store.(DeliveryBuildFailureReleaser); ok {
		_, err := terminal.MarkBuildFailedAndReleaseLease(ctx, attempt.ID, attempt.Revision, lease, code, now)
		return err
	}
	_, err := l.Store.MarkBuildFailed(ctx, attempt.ID, attempt.Revision, code, now)
	if err != nil {
		return err
	}
	if lease.Status != DeliveryLeaseActive {
		return nil
	}
	if releaser, ok := l.Store.(DeliveryWriterLeaseReleaser); ok {
		_, releaseErr := releaser.TransitionWriterLease(ctx, lease.ID, DeliveryLeaseActive, DeliveryLeaseReleased, now)
		if releaseErr != nil {
			return releaseErr
		}
	}
	return nil
}

func failBuildPreparation(l *DeliveryLifecycle, ctx context.Context, attempt DeliveryBuildAttempt, lease DeliveryWriterLease, cause error) error {
	if l == nil {
		return cause
	}
	if err := l.markBuildFailed(ctx, attempt, lease, "ARTIFACT_PREPARATION_FAILED"); err != nil {
		return errors.Join(cause, fmt.Errorf("record failed build: %w", err))
	}
	return cause
}

func validateBuildRequestIdentities(request DeliveryBuildRequest) error {
	for name, value := range map[string]string{
		"plan":          request.PlanID,
		"attempt":       request.AttemptID,
		"writer lease":  request.WriterLeaseID,
		"candidate":     request.CandidateID,
		"seal":          request.SealID,
		"physical pool": request.PhysicalPoolID,
	} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if request.PrepareArtifacts == nil {
		if err := ValidateDeliveryID(request.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryID(request.ServingStateID); err != nil {
			return fmt.Errorf("serving state id: %w", err)
		}
		if err := ValidateDeliveryDigest(request.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
	}
	if request.BaseCatalogDigest != "" {
		if err := ValidateDeliveryDigest(request.BaseCatalogDigest); err != nil {
			return fmt.Errorf("base catalog digest: %w", err)
		}
	}
	if request.BasePhysicalPoolID != "" {
		if err := ValidateDeliveryID(request.BasePhysicalPoolID); err != nil {
			return fmt.Errorf("base physical pool id: %w", err)
		}
	}
	if request.Epoch < 1 {
		return fmt.Errorf("%w: writer lease epoch must be positive", ErrDeliveryInvalid)
	}
	return nil
}

// Plan computes target-specific evidence. No writer lease, credentials,
// candidate catalog, or object-store operation is reachable from this method.
func (l *DeliveryLifecycle) Plan(ctx context.Context, request DeliveryPlanRequest) (DeliveryPlanResult, error) {
	if l == nil || l.Targets == nil {
		return DeliveryPlanResult{}, fmt.Errorf("delivery target resolver is required")
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = l.now()
	}
	target, err := l.Targets.ResolveDeliveryTarget(ctx, request.TargetID)
	if err != nil {
		return DeliveryPlanResult{}, err
	}
	if target.TargetID != request.TargetID || target.ProjectID != request.ProjectID || target.Environment != request.Environment || target.TargetRevision < 0 {
		return DeliveryPlanResult{}, fmt.Errorf("%w: target scope does not match plan request", ErrDeliveryConflict)
	}
	plan, err := NewDeliveryPlan(DeliveryPlan{
		ID: request.ID, ActorID: request.ActorID, TargetID: target.TargetID, ProjectID: projectgraph.ResourceID(request.ProjectID), Environment: request.Environment,
		Operation: request.Operation, SourceDigest: request.SourceDigest, BaseGenerationID: target.ActiveGenerationID,
		BaseTargetRevision: target.TargetRevision, Execution: request.Execution, Provenance: request.Provenance,
		Governance: request.Governance, Evidence: request.Evidence, CreatedAt: request.CreatedAt,
	})
	if err != nil {
		return DeliveryPlanResult{}, err
	}
	if !request.Persist {
		return DeliveryPlanResult{Plan: plan}, nil
	}
	if l.Store == nil {
		return DeliveryPlanResult{}, fmt.Errorf("delivery plan repository is required for persistence")
	}
	persisted, err := l.Store.CreatePlan(ctx, plan)
	if err != nil {
		return DeliveryPlanResult{}, err
	}
	return DeliveryPlanResult{Plan: persisted, Persisted: true}, nil
}

// Preview is an explicitly read-only plan operation.
func (l *DeliveryLifecycle) Preview(ctx context.Context, request DeliveryPlanRequest) (DeliveryPlan, error) {
	request.Persist = false
	result, err := l.Plan(ctx, request)
	return result.Plan, err
}

// Build loads an exact persisted plan, fences its base before physical work,
// creates the durable writer lease/attempt, and seals only after qualification.
func (l *DeliveryLifecycle) Build(ctx context.Context, request DeliveryBuildRequest) (DeliveryBuildResult, error) {
	if l == nil || l.Store == nil || l.Targets == nil {
		return DeliveryBuildResult{}, fmt.Errorf("delivery lifecycle repositories and target resolver are required")
	}
	if request.Runner == nil && request.PhasedRunner == nil {
		return DeliveryBuildResult{}, fmt.Errorf("%w: complete build identities and runner are required", ErrDeliveryInvalid)
	}
	if err := validateBuildRequestIdentities(request); err != nil {
		return DeliveryBuildResult{}, err
	}
	now := l.now()
	plan, err := l.Store.PlanByID(ctx, request.PlanID)
	if err != nil {
		return DeliveryBuildResult{}, err
	}
	target, err := l.Targets.ResolveDeliveryTarget(ctx, plan.TargetID)
	if err != nil {
		return DeliveryBuildResult{}, err
	}
	if target.ProjectID != plan.ProjectID.String() || target.Environment != plan.Environment {
		return DeliveryBuildResult{}, fmt.Errorf("%w: target scope changed", ErrDeliveryStale)
	}
	if err := plan.PublicationEligible(target.ActiveGenerationID, target.TargetRevision, now); err != nil {
		return DeliveryBuildResult{}, err
	}
	if request.LeaseLifetime <= 0 {
		request.LeaseLifetime = time.Hour
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = now
	}
	lease, err := NewDeliveryWriterLease(DeliveryWriterLease{ID: request.WriterLeaseID, AttemptID: request.AttemptID, PhysicalPoolID: request.PhysicalPoolID, OwnerID: request.OwnerID, Epoch: request.Epoch, CreatedAt: request.CreatedAt, ExpiresAt: request.CreatedAt.Add(request.LeaseLifetime)})
	if err != nil {
		return DeliveryBuildResult{}, err
	}
	attempt, err := NewDeliveryBuildAttempt(DeliveryBuildAttempt{ID: request.AttemptID, PlanID: plan.ID, IdempotencyKey: request.IdempotencyKey, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseGenerationID: plan.BaseGenerationID, BaseCatalogDigest: request.BaseCatalogDigest, BasePhysicalPoolID: request.BasePhysicalPoolID, PhysicalPoolID: request.PhysicalPoolID, WriterLeaseID: lease.ID, CreatedAt: request.CreatedAt})
	if err != nil {
		return DeliveryBuildResult{}, err
	}
	_, attempt, err = l.Store.CreateWriterLeaseAndBuildAttempt(ctx, lease, attempt)
	if err != nil {
		return DeliveryBuildResult{}, err
	}
	// Refresh the persisted lease before any target recheck or physical work.
	// The repository may allocate a stronger pool epoch than the caller
	// supplied, and failure/release must use that exact durable identity.
	if reader, ok := l.Store.(interface {
		DeliveryWriterLeaseByID(context.Context, string) (DeliveryWriterLease, error)
	}); ok {
		if persistedLease, leaseErr := reader.DeliveryWriterLeaseByID(ctx, lease.ID); leaseErr == nil {
			lease = persistedLease
		}
	}
	// A sealed attempt is already converged; callers may safely retry without
	// reacquiring writer credentials or rerunning physical work. It is
	// intentionally resolved before the stale recheck because publication
	// convergence must not mutate a terminal build attempt.
	if attempt.Status == DeliveryBuildSealed {
		reader, ok := l.Store.(DeliveryCompletionReader)
		if !ok {
			return DeliveryBuildResult{}, fmt.Errorf("%w: sealed attempt completion evidence is unavailable", ErrDeliveryConflict)
		}
		completion, readErr := reader.CompletedDelivery(ctx, attempt.ID, request.CandidateID)
		if readErr != nil {
			return DeliveryBuildResult{}, readErr
		}
		return DeliveryBuildResult{Attempt: attempt, Completion: completion}, nil
	}
	// The initial plan read and lease transaction are intentionally separate.
	// A target revision can advance between them, so reject-stale policy gets a
	// final read immediately before artifact preparation/catalog construction.
	// This is the last point at which the build can fail without physical work.
	latestTarget, targetErr := l.Targets.ResolveDeliveryTarget(ctx, plan.TargetID)
	if targetErr != nil {
		return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, targetErr)
	}
	if latestTarget.TargetID != plan.TargetID || latestTarget.ProjectID != plan.ProjectID.String() || latestTarget.Environment != plan.Environment {
		return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, fmt.Errorf("%w: target scope changed before physical work", ErrDeliveryStale))
	}
	if plan.Stale(latestTarget.ActiveGenerationID, latestTarget.TargetRevision) {
		switch plan.Evidence.StalePolicy.Mode {
		case "reject":
			return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, fmt.Errorf("%w: target revision changed before physical work", ErrDeliveryStale))
		case "allow_retained_base":
			if err := plan.ValidateRetainedBaseRequest(request.BaseCatalogDigest, request.BasePhysicalPoolID); err != nil {
				return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, err)
			}
		default:
			return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, fmt.Errorf("%w: unsupported stale policy %q", ErrDeliveryInvalid, plan.Evidence.StalePolicy.Mode))
		}
	}
	if attempt.Status != DeliveryBuildBuilding && attempt.Status != DeliveryBuildNormalizing && attempt.Status != DeliveryBuildValidating && attempt.Status != DeliveryBuildSealing {
		return DeliveryBuildResult{}, fmt.Errorf("%w: build attempt is %s", ErrDeliveryTransition, attempt.Status)
	}
	// Serving artifacts are prepared only after the attempt/lease transaction
	// has committed. A crash before this point leaves a durable building
	// attempt that can be reconciled without any pre-attempt catalog rows.
	if request.PrepareArtifacts != nil {
		boundIdentity := DeliveryArtifactIdentity{ServingArtifactID: attempt.ServingArtifactID, ServingArtifactDigest: attempt.ServingArtifactDigest, ServingStateID: attempt.ServingStateID}
		identity, prepareErr := request.PrepareArtifacts(ctx, DeliveryBuildInput{Plan: plan, Attempt: attempt, Lease: lease})
		if prepareErr != nil {
			return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, prepareErr)
		}
		if boundIdentity.ServingArtifactID != "" && identity != boundIdentity {
			return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, fmt.Errorf("%w: prepared serving identity changed on retry", ErrDeliveryConflict))
		}
		if attempt.ServingArtifactID == "" {
			binder, ok := l.Store.(DeliveryBuildArtifactBinder)
			if !ok {
				return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, fmt.Errorf("durable serving artifact binding is unavailable"))
			}
			attempt, prepareErr = binder.BindDeliveryBuildArtifacts(ctx, attempt.ID, attempt.Revision, identity, l.now())
			if prepareErr != nil {
				return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, prepareErr)
			}
		}
		request.ServingArtifactID = identity.ServingArtifactID
		request.ServingArtifactDigest = identity.ServingArtifactDigest
		request.ServingStateID = identity.ServingStateID
		request.PrepareArtifacts = nil
		if err := validateBuildRequestIdentities(request); err != nil {
			return DeliveryBuildResult{}, failBuildPreparation(l, ctx, attempt, lease, err)
		}
	}
	input := DeliveryBuildInput{Plan: plan, Attempt: attempt, Lease: lease}
	var normalizing DeliveryBuildAttempt
	var validating DeliveryBuildAttempt
	var output DeliveryBuildOutput
	var phase any
	closePhase := func() error {
		if phase == nil {
			return nil
		}
		err := closeBuildPhase(phase)
		phase = nil
		return err
	}
	fail := func(failureAttempt DeliveryBuildAttempt, code string, cause error) error {
		cleanupErr := closePhase()
		failureErr := l.markBuildFailed(ctx, failureAttempt, lease, code)
		if cleanupErr != nil {
			cause = errors.Join(cause, fmt.Errorf("cleanup private build catalog: %w", cleanupErr))
		}
		if failureErr != nil {
			cause = errors.Join(cause, fmt.Errorf("record failed build: %w", failureErr))
		}
		return cause
	}
	if request.PhasedRunner != nil {
		// A process may restart after any durable phase transition. Rebuild the
		// private phase object, then replay only the idempotent work needed to
		// reach the recorded phase; never attempt an illegal backwards CAS.
		phase, err = request.PhasedRunner.Construct(ctx, input)
		if err != nil {
			return DeliveryBuildResult{}, fail(attempt, "CONSTRUCT_FAILED", err)
		}
		current := attempt
		if current.Status == DeliveryBuildBuilding {
			current, err = l.Store.TransitionBuildAttempt(ctx, current.ID, current.Revision, DeliveryBuildNormalizing, now)
			if err != nil {
				return DeliveryBuildResult{}, fail(attempt, "NORMALIZING_TRANSITION_FAILED", err)
			}
		}
		normalizing = current
		if err := request.PhasedRunner.Normalize(ctx, input, phase); err != nil {
			return DeliveryBuildResult{}, fail(current, "NORMALIZE_FAILED", err)
		}
		if current.Status == DeliveryBuildNormalizing {
			current, err = l.Store.TransitionBuildAttempt(ctx, current.ID, current.Revision, DeliveryBuildValidating, l.now())
			if err != nil {
				return DeliveryBuildResult{}, fail(normalizing, "VALIDATING_TRANSITION_FAILED", err)
			}
		}
		validating = current
		output, err = request.PhasedRunner.Qualify(ctx, input, phase)
		if err != nil {
			return DeliveryBuildResult{}, fail(current, "QUALIFY_FAILED", err)
		}
	} else {
		normalizing, err = l.Store.TransitionBuildAttempt(ctx, attempt.ID, attempt.Revision, DeliveryBuildNormalizing, now)
		if err == nil {
			output, err = request.Runner(ctx, input)
		}
		if err != nil {
			failureAttempt := attempt
			if normalizing.ID != "" {
				failureAttempt = normalizing
			}
			return DeliveryBuildResult{}, fail(failureAttempt, "BUILD_FAILED", err)
		}
	}
	if output.Cleanup != nil {
		defer output.Cleanup()
	}
	resolvedInputs, resolvedErr := ValidateDeliveryResolvedBuildInputs(plan, output.ResolvedInputs)
	if resolvedErr != nil {
		failureAttempt := validating
		if failureAttempt.ID == "" {
			failureAttempt = normalizing
		}
		return DeliveryBuildResult{}, fail(failureAttempt, "RESOLVED_INPUTS_INVALID", resolvedErr)
	}
	resolvedJSON, resolvedJSONErr := json.Marshal(resolvedInputs)
	if resolvedJSONErr != nil {
		failureAttempt := validating
		if failureAttempt.ID == "" {
			failureAttempt = normalizing
		}
		return DeliveryBuildResult{}, fail(failureAttempt, "RESOLVED_INPUTS_INVALID", resolvedJSONErr)
	}
	output.ResolvedInputs = resolvedInputs
	if output.Catalog == nil || output.ObjectStore == nil || output.SealRepository == nil || output.RemoteVerifier == nil {
		failureAttempt := validating
		if failureAttempt.ID == "" {
			failureAttempt = normalizing
		}
		return DeliveryBuildResult{}, fail(failureAttempt, "QUALIFICATION_INCOMPLETE", fmt.Errorf("%w: qualified catalog and seal adapters are required", ErrDeliveryInvalid))
	}
	if request.PhasedRunner == nil {
		validating, err = l.Store.TransitionBuildAttempt(ctx, normalizing.ID, normalizing.Revision, DeliveryBuildValidating, l.now())
		if err != nil {
			return DeliveryBuildResult{}, fail(normalizing, "VALIDATING_TRANSITION_FAILED", err)
		}
	}
	sealing := validating
	if validating.Status != DeliveryBuildSealing {
		sealing, err = l.Store.TransitionBuildAttempt(ctx, attempt.ID, validating.Revision, DeliveryBuildSealing, l.now())
		if err != nil {
			return DeliveryBuildResult{}, fail(validating, "SEALING_TRANSITION_FAILED", err)
		}
	}
	identity := catalogseal.SealIdentity{SealID: request.SealID, Attempt: catalogseal.AttemptIdentity{ID: sealing.ID, WriterLeaseID: sealing.WriterLeaseID}, Plan: catalogseal.PlanIdentity{ID: plan.ID, Digest: plan.Digest, ExecutionDigest: plan.ExecutionDigest}, Pool: catalogseal.PoolIdentity{ID: request.PhysicalPoolID, CompatibilityDigest: output.CompatibilityDigest}, Qualification: catalogseal.QualificationIdentity{Digest: output.QualificationDigest}, Closure: catalogseal.ClosureIdentity{Digest: output.ClosureDigest}, Candidate: catalogseal.CandidateIdentity{ID: request.CandidateID, ServingArtifactID: request.ServingArtifactID, ServingArtifactDigest: request.ServingArtifactDigest, ServingStateID: request.ServingStateID}}
	completion, err := catalogseal.Seal(ctx, catalogseal.Request{SealID: request.SealID, Attempt: identity.Attempt, Plan: identity.Plan, Pool: identity.Pool, Qualification: identity.Qualification, Closure: identity.Closure, Candidate: identity.Candidate, Catalog: output.Catalog, Store: output.ObjectStore, Repository: output.SealRepository, Verifier: output.RemoteVerifier, ResolvedInputsJSON: string(resolvedJSON), ResolvedInputsDigest: resolvedInputs.EvidenceDigest})
	if err != nil {
		// Keep any preparing/uploaded remote root for reconciliation; only the
		// private local staging is removed by output.Cleanup above.
		failureErr := l.markBuildFailed(ctx, sealing, lease, "SEAL_FAILED")
		if failureErr != nil {
			return DeliveryBuildResult{}, errors.Join(err, failureErr)
		}
		return DeliveryBuildResult{}, err
	}
	finalAttempt, err := l.Store.DeliveryBuildAttemptByID(ctx, sealing.ID)
	if err != nil {
		return DeliveryBuildResult{}, err
	}
	return DeliveryBuildResult{Attempt: finalAttempt, Completion: completion}, nil
}
