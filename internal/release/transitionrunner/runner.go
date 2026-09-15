// Package transitionrunner executes the bounded forward-only release
// transition.  It deliberately owns orchestration only: authoritative
// projections, durable operation state, the target fence, and the effects are
// supplied by their owning packages.
package transitionrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/safetext"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const (
	PhasePreflight    Phase = transitionoperation.PhasePreflight
	PhaseMigrations   Phase = transitionoperation.PhaseMigrations
	PhaseStage        Phase = transitionoperation.PhaseCandidateStaged
	PhaseActivate     Phase = transitionoperation.PhaseCandidateActivated
	PhaseRestart      Phase = transitionoperation.PhaseCandidateRestarted
	PhasePostValidate Phase = transitionoperation.PhasePostValidated
	PhaseSuccess      Phase = transitionoperation.PhaseSuccess

	// MaxFailureSummary is the bound retained for arbitrary effect errors.
	MaxFailureSummary = 512
)

var (
	ErrInvalid             = errors.New("invalid release transition request")
	ErrStalePreflight      = errors.New("release transition preflight became stale")
	ErrFenceLost           = errors.New("release transition fence was lost")
	ErrIdentityMismatch    = errors.New("release transition immutable identity mismatch")
	ErrStateMismatch       = errors.New("release transition state identity mismatch")
	ErrCompetingOwner      = errors.New("release transition has a competing owner")
	ErrUnsupportedDecision = errors.New("release transition is not binary-rollback-compatible")
	ErrPhaseFailure        = errors.New("release transition phase failed")
)

// Phase is the durable, fine-grained effect progression shared with the
// transition operation contract. Every value is persisted independently so a
// restart never infers stage, activation, or restart completion from another
// milestone.
type Phase = transitionoperation.Phase

// PhaseNames returns the exact forward-only effect order.
func PhaseNames() []Phase {
	return []Phase{PhasePreflight, PhaseMigrations, PhaseStage, PhaseActivate, PhaseRestart, PhasePostValidate, PhaseSuccess}
}

// Request identifies one immutable transition attempt. OperationID may be
// empty when the store allocates it; IdempotencyKey is always required and is
// the replay identity for retries.
type Request struct {
	OperationID    string
	OwnerID        string
	IdempotencyKey string
	LeaseTTL       time.Duration
	Preflight      transitionpreflight.ResolutionRequest
}

// PreflightAuthority is the read-only authoritative resolver. Run invokes it
// once before taking the target fence and once again after taking that fence.
type PreflightAuthority interface {
	ResolveAndEvaluate(context.Context, transitionpreflight.ResolutionRequest) (transitionpreflight.ResolutionResult, error)
}

// FenceAuthority owns the target-wide lease. Validate must read the durable
// fence and compare both owner and generation; an in-memory timestamp check is
// insufficient for a cutover boundary.
type FenceAuthority interface {
	Acquire(context.Context, string, string, string, time.Time) (transitionoperation.Fence, error)
	Renew(context.Context, transitionoperation.Fence, time.Time) (transitionoperation.Fence, error)
	Validate(context.Context, transitionoperation.Fence) error
	Release(context.Context, transitionoperation.Fence) error
}

// EffectInput is passed to every side-effecting phase. Evidence and the exact
// fence are copied from the admitted operation and must not be re-resolved by
// an effect implementation.
type EffectInput struct {
	Operation transitionoperation.Operation
	Evidence  transitionpreflight.Evidence
	Fence     transitionoperation.Fence
}

// EffectResult is the bounded identity projection returned after an effect.
// Implementations may leave identity fields empty for phases that do not
// publish state; when present, they are checked exactly against the admitted
// candidate and target identities.
type EffectResult struct {
	TargetIdentityDigest    string `json:"targetIdentityDigest,omitempty"`
	CandidateArtifactDigest string `json:"candidateArtifactDigest,omitempty"`
	StateIdentityDigest     string `json:"stateIdentityDigest,omitempty"`
	Payload                 []byte `json:"payload,omitempty"`
}

type Effects interface {
	Migrations(context.Context, EffectInput) (EffectResult, error)
	Stage(context.Context, EffectInput) (EffectResult, error)
	Activate(context.Context, EffectInput) (EffectResult, error)
	Restart(context.Context, EffectInput) (EffectResult, error)
	PostValidate(context.Context, EffectInput) (EffectResult, error)
}

// EffectFuncs is a convenient adapter for narrow tests and composition roots.
type EffectFuncs struct {
	MigrationsFunc   func(context.Context, EffectInput) (EffectResult, error)
	StageFunc        func(context.Context, EffectInput) (EffectResult, error)
	ActivateFunc     func(context.Context, EffectInput) (EffectResult, error)
	RestartFunc      func(context.Context, EffectInput) (EffectResult, error)
	PostValidateFunc func(context.Context, EffectInput) (EffectResult, error)
}

func (f EffectFuncs) Migrations(ctx context.Context, in EffectInput) (EffectResult, error) {
	if f.MigrationsFunc == nil {
		return EffectResult{}, ErrInvalid
	}
	return f.MigrationsFunc(ctx, in)
}
func (f EffectFuncs) Stage(ctx context.Context, in EffectInput) (EffectResult, error) {
	if f.StageFunc == nil {
		return EffectResult{}, ErrInvalid
	}
	return f.StageFunc(ctx, in)
}
func (f EffectFuncs) Activate(ctx context.Context, in EffectInput) (EffectResult, error) {
	if f.ActivateFunc == nil {
		return EffectResult{}, ErrInvalid
	}
	return f.ActivateFunc(ctx, in)
}
func (f EffectFuncs) Restart(ctx context.Context, in EffectInput) (EffectResult, error) {
	if f.RestartFunc == nil {
		return EffectResult{}, ErrInvalid
	}
	return f.RestartFunc(ctx, in)
}
func (f EffectFuncs) PostValidate(ctx context.Context, in EffectInput) (EffectResult, error) {
	if f.PostValidateFunc == nil {
		return EffectResult{}, ErrInvalid
	}
	return f.PostValidateFunc(ctx, in)
}

// OperationStore is the durable operation boundary. Implementations must make
// Create idempotent by request digest and make phase results append-only. The
// target fence authority owns the atomic claim. The runner never treats a local operation copy as
// authoritative after an effect.
type OperationStore interface {
	Create(context.Context, transitionoperation.CreateInput) (transitionoperation.Operation, error)
	Get(context.Context, string) (transitionoperation.Operation, error)
	// PhaseCompleted is exact-subphase state. It must not infer stage,
	// activate, or restart completion from a coarse candidate-startup marker.
	PhaseCompleted(context.Context, string, Phase) (bool, error)
	RecordPhase(context.Context, PhaseRecordInput) (transitionoperation.Operation, error)
	Complete(context.Context, CompleteInput) (transitionoperation.Operation, error)
	Fail(context.Context, FailureInput) (transitionoperation.Operation, error)
}

// PhaseRecordInput is the append-only durable result for one runner subphase.
type PhaseRecordInput struct {
	OperationID string
	OwnerID     string
	Fence       transitionoperation.Fence
	Phase       Phase
	Status      transitionoperation.PhaseResultStatus
	Result      []byte
}

type CompleteInput struct {
	OperationID string
	OwnerID     string
	Fence       transitionoperation.Fence
	Result      []byte
}

type FailureInput struct {
	OperationID string
	OwnerID     string
	Fence       transitionoperation.Fence
	Phase       Phase
	Status      transitionoperation.PhaseResultStatus
	Code        string
	Summary     string
}

// Result is the stable runner result. Evidence is the second authoritative
// preflight result, and Operation is the durable terminal projection.
type Result struct {
	Operation      transitionoperation.Operation
	Evidence       transitionpreflight.Evidence
	EvidenceDigest string
}

type Options struct {
	Operations OperationStore
	Preflight  PreflightAuthority
	Fences     FenceAuthority
	Effects    Effects
	LeaseTTL   time.Duration
	Clock      func() time.Time
}

type Runner struct {
	operations OperationStore
	preflight  PreflightAuthority
	fences     FenceAuthority
	effects    Effects
	leaseTTL   time.Duration
	clock      func() time.Time
}

func New(options Options) (*Runner, error) {
	if options.Operations == nil || options.Preflight == nil || options.Fences == nil || options.Effects == nil {
		return nil, fmt.Errorf("%w: all authorities are required", ErrInvalid)
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = 15 * time.Minute
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &Runner{operations: options.Operations, preflight: options.Preflight, fences: options.Fences, effects: options.Effects, leaseTTL: options.LeaseTTL, clock: options.Clock}, nil
}

// Run performs or resumes one forward-only transition. It never invokes a
// rollback effect and never accepts provider-recovery-required evidence.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	if r == nil || r.operations == nil || r.preflight == nil || r.fences == nil || r.effects == nil {
		return Result{}, fmt.Errorf("%w: runner is not configured", ErrInvalid)
	}
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}

	first, err := r.preflight.ResolveAndEvaluate(ctx, request.Preflight)
	if err != nil {
		return Result{}, fmt.Errorf("%w: initial preflight: %v", ErrStalePreflight, err)
	}
	evidence, evidenceDigest, createInput, err := admittedInput(first, request)
	if err != nil {
		return Result{}, err
	}
	op, err := r.operations.Create(ctx, createInput)
	if err != nil {
		return Result{}, classifyStoreError(err)
	}
	if err := checkOperationIdentity(op, createInput); err != nil {
		return Result{}, err
	}
	if op.Status.Terminal() {
		if op.Status == transitionoperation.StatusCompleted {
			return Result{Operation: op, Evidence: evidence, EvidenceDigest: evidenceDigest}, nil
		}
		return Result{Operation: op, Evidence: evidence, EvidenceDigest: evidenceDigest}, fmt.Errorf("%w: operation is %s", transitionoperation.ErrAlreadyTerminal, op.Status)
	}

	leaseTTL := request.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = r.leaseTTL
	}
	leaseUntil := r.clock().UTC().Add(leaseTTL)
	fence, err := r.fences.Acquire(ctx, evidence.TargetIdentityDigest, op.OperationID, request.OwnerID, leaseUntil)
	if err != nil {
		if errors.Is(err, transitionoperation.ErrBusy) || errors.Is(err, transitionoperation.ErrConflict) {
			return Result{}, fmt.Errorf("%w: %v", ErrCompetingOwner, err)
		}
		return Result{}, classifyStoreError(err)
	}
	if fence.OperationID == "" {
		fence.OperationID = op.OperationID
	}
	if fence.OwnerID == "" {
		fence.OwnerID = request.OwnerID
	}
	op.Fence = fence
	defer func() { _ = r.fences.Release(context.Background(), fence) }()
	effectContext, stopHeartbeat := context.WithCancel(ctx)
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go r.heartbeatFence(effectContext, stopHeartbeat, fence, leaseTTL, heartbeatErrors, heartbeatDone)
	defer stopHeartbeat()
	if err := r.fences.Validate(ctx, fence); err != nil {
		return r.fail(ctx, op, fence, PhasePreflight, "fence_lost", err, evidence, evidenceDigest, ErrFenceLost)
	}

	second, err := r.preflight.ResolveAndEvaluate(ctx, request.Preflight)
	if err != nil {
		return r.fail(ctx, op, fence, PhasePreflight, "preflight_failed", err, evidence, evidenceDigest, ErrStalePreflight)
	}
	secondEvidence, secondDigest, _, err := admittedInput(second, request)
	if err != nil {
		return r.fail(ctx, op, fence, PhasePreflight, "preflight_invalid", err, evidence, evidenceDigest, err)
	}
	if secondDigest != evidenceDigest || !sameImmutableEvidence(evidence, secondEvidence) {
		return r.fail(ctx, op, fence, PhasePreflight, "stale_preflight", ErrStalePreflight, evidence, evidenceDigest, ErrStalePreflight)
	}
	evidence = secondEvidence

	preflightDone, err := r.operations.PhaseCompleted(ctx, op.OperationID, PhasePreflight)
	if err != nil {
		return Result{}, classifyStoreError(err)
	}
	if !preflightDone {
		canonical, canonicalErr := evidence.CanonicalJSON()
		if canonicalErr != nil {
			return Result{}, canonicalErr
		}
		op, err = r.record(ctx, op, fence, PhasePreflight, transitionoperation.PhaseResultSucceeded, canonical)
		if err != nil {
			return Result{}, err
		}
	}
	for _, step := range []struct {
		phase Phase
		run   func(context.Context, EffectInput) (EffectResult, error)
	}{
		{PhaseMigrations, r.effects.Migrations},
		{PhaseStage, r.effects.Stage},
		{PhaseActivate, r.effects.Activate},
		{PhaseRestart, r.effects.Restart},
		{PhasePostValidate, r.effects.PostValidate},
	} {
		done, doneErr := r.operations.PhaseCompleted(ctx, op.OperationID, step.phase)
		if doneErr != nil {
			return Result{}, classifyStoreError(doneErr)
		}
		if done {
			continue
		}
		if err := r.fences.Validate(ctx, fence); err != nil {
			return r.fail(ctx, op, fence, step.phase, "fence_lost", err, evidence, evidenceDigest, ErrFenceLost)
		}
		out, effectErr := step.run(effectContext, EffectInput{Operation: op, Evidence: evidence, Fence: fence})
		if heartbeatErr := readHeartbeatError(heartbeatErrors); heartbeatErr != nil {
			return r.failIndeterminate(ctx, op, fence, step.phase, "fence_lost", heartbeatErr, evidence, evidenceDigest, ErrFenceLost)
		}
		if effectErr != nil {
			return r.fail(ctx, op, fence, step.phase, "phase_failed", effectErr, evidence, evidenceDigest, fmt.Errorf("%w: %s: %v", ErrPhaseFailure, step.phase, effectErr))
		}
		if err := validateEffectResult(out, evidence, step.phase); err != nil {
			return r.fail(ctx, op, fence, step.phase, "state_mismatch", err, evidence, evidenceDigest, err)
		}
		if err := r.fences.Validate(ctx, fence); err != nil {
			return r.failIndeterminate(ctx, op, fence, step.phase, "fence_lost", err, evidence, evidenceDigest, ErrFenceLost)
		}
		op, err = r.record(ctx, op, fence, step.phase, transitionoperation.PhaseResultSucceeded, resultJSON(out, step.phase))
		if err != nil {
			// The external effect has completed, but a fence loss can prevent its
			// durable success marker from being appended. This applies to every
			// store error, not only an explicit stale-fence response: a transport
			// error can make the commit outcome unknowable too.
			return r.failIndeterminate(ctx, op, fence, step.phase, "phase_record_failed", err, evidence, evidenceDigest, err)
		}
	}
	if err := r.fences.Validate(ctx, fence); err != nil {
		return r.fail(ctx, op, fence, PhaseSuccess, "fence_lost", err, evidence, evidenceDigest, ErrFenceLost)
	}
	successDone, err := r.operations.PhaseCompleted(ctx, op.OperationID, PhaseSuccess)
	if err != nil {
		return Result{}, classifyStoreError(err)
	}
	if !successDone {
		op, err = r.record(ctx, op, fence, PhaseSuccess, transitionoperation.PhaseResultSucceeded, []byte(`{"success":true}`))
		if err != nil {
			return Result{}, err
		}
	}
	stopHeartbeat()
	<-heartbeatDone
	if heartbeatErr := readHeartbeatError(heartbeatErrors); heartbeatErr != nil {
		return r.fail(ctx, op, fence, PhaseSuccess, "fence_lost", heartbeatErr, evidence, evidenceDigest, ErrFenceLost)
	}
	if err := r.fences.Validate(ctx, fence); err != nil {
		return r.fail(ctx, op, fence, PhaseSuccess, "fence_lost", err, evidence, evidenceDigest, ErrFenceLost)
	}
	op, err = r.operations.Complete(ctx, CompleteInput{OperationID: op.OperationID, OwnerID: request.OwnerID, Fence: fence, Result: []byte(`{"success":true}`)})
	if err != nil {
		return Result{}, classifyStoreError(err)
	}
	return Result{Operation: op, Evidence: evidence, EvidenceDigest: evidenceDigest}, nil
}

func (r *Runner) heartbeatFence(ctx context.Context, cancel context.CancelFunc, fence transitionoperation.Fence, leaseTTL time.Duration, failures chan<- error, done chan<- struct{}) {
	defer close(done)
	interval := leaseTTL / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.fences.Renew(ctx, fence, r.clock().UTC().Add(leaseTTL)); err != nil {
				select {
				case failures <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func readHeartbeatError(failures <-chan error) error {
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}

func validateRequest(in Request) error {
	if strings.TrimSpace(in.OwnerID) == "" || in.OwnerID != strings.TrimSpace(in.OwnerID) || strings.TrimSpace(in.IdempotencyKey) == "" || in.IdempotencyKey != strings.TrimSpace(in.IdempotencyKey) {
		return fmt.Errorf("%w: owner and idempotency key", ErrInvalid)
	}
	if in.LeaseTTL < 0 {
		return fmt.Errorf("%w: lease ttl", ErrInvalid)
	}
	return nil
}

func admittedInput(result transitionpreflight.ResolutionResult, request Request) (transitionpreflight.Evidence, string, transitionoperation.CreateInput, error) {
	evidence, err := result.Evidence.Normalize()
	if err != nil {
		return transitionpreflight.Evidence{}, "", transitionoperation.CreateInput{}, fmt.Errorf("%w: evidence: %v", ErrInvalid, err)
	}
	if evidence.Decision != transitionpreflight.DecisionBinaryRollbackCompatible {
		return evidence, result.EvidenceDigest, transitionoperation.CreateInput{}, fmt.Errorf("%w: %s", ErrUnsupportedDecision, evidence.Decision)
	}
	digest, err := evidence.Digest()
	if err != nil || result.EvidenceDigest != "" && result.EvidenceDigest != digest {
		return evidence, "", transitionoperation.CreateInput{}, fmt.Errorf("%w: evidence digest", ErrIdentityMismatch)
	}
	predecessor, err := evidence.Predecessor.Digest()
	if err != nil {
		return evidence, "", transitionoperation.CreateInput{}, fmt.Errorf("%w: predecessor", ErrIdentityMismatch)
	}
	candidate, err := evidence.Candidate.Digest()
	if err != nil {
		return evidence, "", transitionoperation.CreateInput{}, fmt.Errorf("%w: candidate", ErrIdentityMismatch)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		return evidence, "", transitionoperation.CreateInput{}, err
	}
	frontier := evidence.RecoveryFrontier
	if frontier == nil {
		return evidence, "", transitionoperation.CreateInput{}, fmt.Errorf("%w: recovery frontier", ErrIdentityMismatch)
	}
	input := transitionoperation.CreateInput{OperationID: request.OperationID, TargetIdentityDigest: evidence.TargetIdentityDigest, PredecessorArtifactDigest: predecessor, CandidateArtifactDigest: candidate, RecoveryFrontierID: frontier.SetID, RecoveryFrontierDigest: frontier.Digest, PreflightEvidence: canonical, PreflightEvidenceDigest: digest, IdempotencyKey: request.IdempotencyKey}
	requestDigest, err := input.Digest()
	if err != nil {
		return evidence, "", transitionoperation.CreateInput{}, err
	}
	input.RequestDigest = requestDigest
	return evidence, digest, input, nil
}

func checkOperationIdentity(op transitionoperation.Operation, input transitionoperation.CreateInput) error {
	if op.OperationID == "" || op.TargetIdentityDigest != input.TargetIdentityDigest || op.PredecessorArtifactDigest != input.PredecessorArtifactDigest || op.CandidateArtifactDigest != input.CandidateArtifactDigest || op.RecoveryFrontierID != input.RecoveryFrontierID || op.RecoveryFrontierDigest != input.RecoveryFrontierDigest || op.PreflightEvidenceDigest != input.PreflightEvidenceDigest || op.IdempotencyKey != input.IdempotencyKey {
		return ErrIdentityMismatch
	}
	return nil
}

func sameImmutableEvidence(a, b transitionpreflight.Evidence) bool {
	left, leftErr := a.CanonicalJSON()
	right, rightErr := b.CanonicalJSON()
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

func (r *Runner) record(ctx context.Context, op transitionoperation.Operation, fence transitionoperation.Fence, phase Phase, status transitionoperation.PhaseResultStatus, result []byte) (transitionoperation.Operation, error) {
	if len(result) == 0 {
		result = []byte(`{"phase":"` + string(phase) + `"}`)
	}
	updated, err := r.operations.RecordPhase(ctx, PhaseRecordInput{OperationID: op.OperationID, OwnerID: fence.OwnerID, Fence: fence, Phase: phase, Status: status, Result: result})
	if err != nil {
		return op, classifyStoreError(err)
	}
	return updated, nil
}

func resultJSON(result EffectResult, phase Phase) []byte {
	if len(result.Payload) > 0 && json.Valid(result.Payload) {
		return append([]byte(nil), result.Payload...)
	}
	b, _ := json.Marshal(struct {
		Phase     Phase  `json:"phase"`
		Target    string `json:"targetIdentityDigest,omitempty"`
		Candidate string `json:"candidateArtifactDigest,omitempty"`
		State     string `json:"stateIdentityDigest,omitempty"`
	}{Phase: phase, Target: result.TargetIdentityDigest, Candidate: result.CandidateArtifactDigest, State: result.StateIdentityDigest})
	return b
}

func validateEffectResult(result EffectResult, evidence transitionpreflight.Evidence, phase Phase) error {
	target := evidence.TargetIdentityDigest
	candidate, err := evidence.Candidate.Digest()
	if err != nil {
		return fmt.Errorf("%w: candidate identity unavailable", ErrStateMismatch)
	}
	if phase == PhaseRestart || phase == PhasePostValidate {
		if result.TargetIdentityDigest != target {
			return fmt.Errorf("%w: target identity", ErrStateMismatch)
		}
		if result.CandidateArtifactDigest != candidate {
			return fmt.Errorf("%w: candidate identity", ErrStateMismatch)
		}
		return nil
	}
	if result.TargetIdentityDigest != "" && result.TargetIdentityDigest != target {
		return fmt.Errorf("%w: target identity", ErrStateMismatch)
	}
	if result.CandidateArtifactDigest != "" && result.CandidateArtifactDigest != candidate {
		return fmt.Errorf("%w: candidate identity", ErrStateMismatch)
	}
	return nil
}

func (r *Runner) fail(ctx context.Context, op transitionoperation.Operation, fence transitionoperation.Fence, phase Phase, code string, cause error, evidence transitionpreflight.Evidence, evidenceDigest string, returned error) (Result, error) {
	return r.recordFailure(ctx, op, fence, phase, transitionoperation.PhaseResultFailed, code, cause, evidence, evidenceDigest, returned)
}

func (r *Runner) failIndeterminate(ctx context.Context, op transitionoperation.Operation, fence transitionoperation.Fence, phase Phase, code string, cause error, evidence transitionpreflight.Evidence, evidenceDigest string, returned error) (Result, error) {
	return r.recordFailure(ctx, op, fence, phase, transitionoperation.PhaseResultIndeterminate, code, cause, evidence, evidenceDigest, returned)
}

func (r *Runner) recordFailure(ctx context.Context, op transitionoperation.Operation, fence transitionoperation.Fence, phase Phase, status transitionoperation.PhaseResultStatus, code string, cause error, evidence transitionpreflight.Evidence, evidenceDigest string, returned error) (Result, error) {
	summary := "transition failed"
	if cause != nil {
		summary = safetext.BoundedSummary(cause.Error(), MaxFailureSummary)
	}
	failed, failErr := r.operations.Fail(ctx, FailureInput{OperationID: op.OperationID, OwnerID: fence.OwnerID, Fence: fence, Phase: phase, Status: status, Code: code, Summary: summary})
	if failErr != nil {
		return Result{Operation: op, Evidence: evidence, EvidenceDigest: evidenceDigest}, errors.Join(returned, classifyStoreError(failErr))
	}
	return Result{Operation: failed, Evidence: evidence, EvidenceDigest: evidenceDigest}, returned
}

func classifyStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, transitionoperation.ErrBusy) {
		return fmt.Errorf("%w: %v", ErrCompetingOwner, err)
	}
	if errors.Is(err, transitionoperation.ErrStaleFence) || errors.Is(err, transitionoperation.ErrLeaseExpired) {
		return fmt.Errorf("%w: %v", ErrFenceLost, err)
	}
	return err
}
