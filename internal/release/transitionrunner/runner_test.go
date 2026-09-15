package transitionrunner

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

func TestRunnerSuccessPersistsEveryForwardPhase(t *testing.T) {
	preflight := newRunnerPreflight(t)
	store := newRunnerStore()
	fence := &runnerFence{}
	effects := &runnerEffects{}
	runner, err := New(Options{Operations: store, Preflight: preflight, Fences: fence, Effects: effects, Clock: func() time.Time { return time.Unix(10, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "request"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation.Status != transitionoperation.StatusCompleted {
		t.Fatalf("status=%q", result.Operation.Status)
	}
	for _, phase := range []Phase{PhasePreflight, PhaseMigrations, PhaseStage, PhaseActivate, PhaseRestart, PhasePostValidate, PhaseSuccess} {
		if !store.completed[phase] {
			t.Errorf("phase %q was not durably completed", phase)
		}
	}
	if got, want := strings.Join(effects.calls, ","), "migrations,stage,activate,restart,post-validate"; got != want {
		t.Fatalf("effects=%q want %q", got, want)
	}
}

func TestRunnerRejectsStaleSecondPreflight(t *testing.T) {
	first := newRunnerPreflight(t)
	second := newRunnerPreflight(t)
	second.results[1].Evidence.TargetIdentityDigest = strings.Repeat("x", 0) // force a changed canonical document below
	second.results[1] = newRunnerPreflightWithTarget(t, "sha256:"+strings.Repeat("9", 64))
	store := newRunnerStore()
	runner, err := New(Options{Operations: store, Preflight: &runnerPreflightSequence{results: []transitionpreflight.ResolutionResult{first.results[0], second.results[1]}}, Fences: &runnerFence{}, Effects: &runnerEffects{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "stale"})
	if !errors.Is(err, ErrStalePreflight) {
		t.Fatalf("err=%v", err)
	}
	if store.failure == nil || store.failure.Code != "stale_preflight" {
		t.Fatalf("failure=%#v", store.failure)
	}
}

func TestRunnerStopsWhenFenceLostBeforeEffect(t *testing.T) {
	store := newRunnerStore()
	fence := &runnerFence{validateErr: errors.New("lost")}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: fence, Effects: &runnerEffects{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "fence"})
	if !errors.Is(err, ErrFenceLost) {
		t.Fatalf("err=%v", err)
	}
	if store.failure == nil || store.failure.Code != "fence_lost" {
		t.Fatalf("failure=%#v", store.failure)
	}
}

func TestRunnerMarksFenceLossAfterEffectIndeterminate(t *testing.T) {
	store := newRunnerStore()
	fence := &runnerFence{failValidationAt: 3, validateErr: errors.New("lost after migration")}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: fence, Effects: &runnerEffects{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "post-effect-fence"})
	if !errors.Is(err, ErrFenceLost) {
		t.Fatalf("err=%v", err)
	}
	if result.Operation.Status != transitionoperation.StatusIndeterminate {
		t.Fatalf("status=%q, want indeterminate", result.Operation.Status)
	}
	if store.failure == nil || store.failure.Status != transitionoperation.PhaseResultIndeterminate || store.failure.Phase != PhaseMigrations {
		t.Fatalf("failure=%#v", store.failure)
	}
}

func TestRunnerMarksFenceLossWhileRecordingEffectIndeterminate(t *testing.T) {
	store := newRunnerStore()
	store.recordErrPhase = PhaseMigrations
	store.recordErr = transitionoperation.ErrLeaseExpired
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: &runnerFence{}, Effects: &runnerEffects{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "record-fence-loss"})
	if !errors.Is(err, ErrFenceLost) {
		t.Fatalf("err=%v", err)
	}
	if result.Operation.Status != transitionoperation.StatusIndeterminate {
		t.Fatalf("status=%q, want indeterminate", result.Operation.Status)
	}
	if store.failure == nil || store.failure.Status != transitionoperation.PhaseResultIndeterminate || store.failure.Phase != PhaseMigrations {
		t.Fatalf("failure=%#v", store.failure)
	}
}

func TestRunnerMarksGenericPhaseRecordFailureAfterEffectIndeterminate(t *testing.T) {
	store := newRunnerStore()
	store.recordErrPhase = PhaseMigrations
	store.recordErr = errors.New("database response unavailable")
	effects := &runnerEffects{}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: &runnerFence{}, Effects: effects})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "record-store-failure"})
	if err == nil || !strings.Contains(err.Error(), "database response unavailable") {
		t.Fatalf("err=%v", err)
	}
	if got := strings.Join(effects.calls, ","); got != "migrations" {
		t.Fatalf("effects=%q, want only migrations", got)
	}
	if result.Operation.Status != transitionoperation.StatusIndeterminate {
		t.Fatalf("status=%q, want indeterminate", result.Operation.Status)
	}
	if store.failure == nil || store.failure.Status != transitionoperation.PhaseResultIndeterminate || store.failure.Code != "phase_record_failed" {
		t.Fatalf("failure=%#v", store.failure)
	}
}

func TestRunnerRejectsWrongCandidateRestartIdentity(t *testing.T) {
	store := newRunnerStore()
	effects := &runnerEffects{wrongRestart: true}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: &runnerFence{}, Effects: effects})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "wrong-restart"})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("err=%v", err)
	}
	if store.failure == nil || store.failure.Phase != PhaseRestart {
		t.Fatalf("failure=%#v", store.failure)
	}
}

func TestRunnerRejectsStateIdentityMismatch(t *testing.T) {
	store := newRunnerStore()
	effects := &runnerEffects{wrongTarget: true}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: &runnerFence{}, Effects: effects})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "wrong-state"})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunnerRejectsCompetingOwner(t *testing.T) {
	store := newRunnerStore()
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: &runnerFence{acquireErr: transitionoperation.ErrBusy}, Effects: &runnerEffects{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "busy"})
	if !errors.Is(err, ErrCompetingOwner) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunnerResumesExactCompletedSubphases(t *testing.T) {
	store := newRunnerStore()
	store.completed[PhasePreflight] = true
	store.completed[PhaseMigrations] = true
	store.completed[PhaseStage] = true
	store.completed[PhaseActivate] = true
	effects := &runnerEffects{}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: &runnerFence{}, Effects: effects})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "resume"}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(effects.calls, ","), "restart,post-validate"; got != want {
		t.Fatalf("effects=%q want %q", got, want)
	}
}

func TestRunnerRenewsFenceDuringLongEffect(t *testing.T) {
	store := newRunnerStore()
	fence := &runnerFence{}
	effects := &runnerEffects{migrationDelay: 80 * time.Millisecond}
	runner, err := New(Options{Operations: store, Preflight: newRunnerPreflight(t), Fences: fence, Effects: effects, LeaseTTL: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{OperationID: runnerOperationID, OwnerID: "owner", IdempotencyKey: "heartbeat"}); err != nil {
		t.Fatal(err)
	}
	if fence.renewCalls.Load() == 0 {
		t.Fatal("long-running effect did not renew the target fence")
	}
}

const runnerOperationID = "0198f2c0-7c7a-7f00-8a11-000000000999"

type runnerPreflightSequence struct {
	results []transitionpreflight.ResolutionResult
	calls   int
}

func (p *runnerPreflightSequence) ResolveAndEvaluate(context.Context, transitionpreflight.ResolutionRequest) (transitionpreflight.ResolutionResult, error) {
	r := p.results[p.calls]
	p.calls++
	return r, nil
}

type runnerFence struct {
	validateErr      error
	acquireErr       error
	failValidationAt int64
	calls            atomic.Int64
	renewCalls       atomic.Int64
}

func (f *runnerFence) Acquire(context.Context, string, string, string, time.Time) (transitionoperation.Fence, error) {
	if f.acquireErr != nil {
		return transitionoperation.Fence{}, f.acquireErr
	}
	return transitionoperation.Fence{OperationID: runnerOperationID, OwnerID: "owner", FencingGeneration: 1}, nil
}
func (f *runnerFence) Renew(_ context.Context, fence transitionoperation.Fence, until time.Time) (transitionoperation.Fence, error) {
	f.renewCalls.Add(1)
	fence.LeaseExpiresAt = until
	return fence, nil
}
func (f *runnerFence) Validate(context.Context, transitionoperation.Fence) error {
	call := f.calls.Add(1)
	if f.failValidationAt > 0 && call < f.failValidationAt {
		return nil
	}
	return f.validateErr
}
func (*runnerFence) Release(context.Context, transitionoperation.Fence) error { return nil }

type runnerEffects struct {
	calls                     []string
	wrongRestart, wrongTarget bool
	migrationDelay            time.Duration
}

func (e *runnerEffects) result(name string, in EffectInput) (EffectResult, error) {
	e.calls = append(e.calls, name)
	result := EffectResult{TargetIdentityDigest: "sha256:" + strings.Repeat("f", 64)}
	result.CandidateArtifactDigest, _ = in.Evidence.Candidate.Digest()
	if name == "restart" && e.wrongRestart {
		result.CandidateArtifactDigest = "sha256:" + strings.Repeat("9", 64)
	}
	if e.wrongTarget {
		result.TargetIdentityDigest = "sha256:" + strings.Repeat("9", 64)
	}
	return result, nil
}
func (e *runnerEffects) Migrations(_ context.Context, in EffectInput) (EffectResult, error) {
	if e.migrationDelay > 0 {
		time.Sleep(e.migrationDelay)
	}
	return e.result("migrations", in)
}
func (e *runnerEffects) Stage(_ context.Context, in EffectInput) (EffectResult, error) {
	return e.result("stage", in)
}
func (e *runnerEffects) Activate(_ context.Context, in EffectInput) (EffectResult, error) {
	return e.result("activate", in)
}
func (e *runnerEffects) Restart(_ context.Context, in EffectInput) (EffectResult, error) {
	return e.result("restart", in)
}
func (e *runnerEffects) PostValidate(_ context.Context, in EffectInput) (EffectResult, error) {
	return e.result("post-validate", in)
}

type runnerStore struct {
	op             transitionoperation.Operation
	completed      map[Phase]bool
	failure        *FailureInput
	recordErrPhase Phase
	recordErr      error
}

func newRunnerStore() *runnerStore { return &runnerStore{completed: map[Phase]bool{}} }
func (s *runnerStore) Create(_ context.Context, in transitionoperation.CreateInput) (transitionoperation.Operation, error) {
	if s.op.OperationID != "" {
		return s.op, nil
	}
	s.op = transitionoperation.Operation{OperationID: in.OperationID, TargetIdentityDigest: in.TargetIdentityDigest, PredecessorArtifactDigest: in.PredecessorArtifactDigest, CandidateArtifactDigest: in.CandidateArtifactDigest, RecoveryFrontierID: in.RecoveryFrontierID, RecoveryFrontierDigest: in.RecoveryFrontierDigest, PreflightEvidence: in.PreflightEvidence, PreflightEvidenceDigest: in.PreflightEvidenceDigest, IdempotencyKey: in.IdempotencyKey, RequestDigest: in.RequestDigest, Status: transitionoperation.StatusPending, CurrentPhase: transitionoperation.PhasePreflight}
	return s.op, nil
}
func (s *runnerStore) Get(context.Context, string) (transitionoperation.Operation, error) {
	return s.op, nil
}
func (s *runnerStore) PhaseCompleted(_ context.Context, _ string, phase Phase) (bool, error) {
	return s.completed[Phase(strings.TrimSpace(string(phase)))], nil
}
func (s *runnerStore) RecordPhase(_ context.Context, in PhaseRecordInput) (transitionoperation.Operation, error) {
	if in.Phase == s.recordErrPhase && s.recordErr != nil {
		err := s.recordErr
		s.recordErr = nil
		return s.op, err
	}
	s.completed[in.Phase] = true
	return s.op, nil
}
func (s *runnerStore) Complete(context.Context, CompleteInput) (transitionoperation.Operation, error) {
	s.op.Status = transitionoperation.StatusCompleted
	return s.op, nil
}
func (s *runnerStore) Fail(_ context.Context, in FailureInput) (transitionoperation.Operation, error) {
	s.failure = &in
	if in.Status == transitionoperation.PhaseResultIndeterminate {
		s.op.Status = transitionoperation.StatusIndeterminate
	} else {
		s.op.Status = transitionoperation.StatusFailed
	}
	return s.op, nil
}

func newRunnerPreflight(t *testing.T) *runnerPreflightSequence {
	t.Helper()
	return &runnerPreflightSequence{results: []transitionpreflight.ResolutionResult{newRunnerPreflightWithTarget(t, "sha256:"+strings.Repeat("f", 64)), newRunnerPreflightWithTarget(t, "sha256:"+strings.Repeat("f", 64))}}
}
func newRunnerPreflightWithTarget(t *testing.T, target string) transitionpreflight.ResolutionResult {
	t.Helper()
	pred := transitionpreflight.ArtifactIdentity{Release: compatibility.ReleaseIdentity{ReleaseID: "pred", Version: "1", SourceRevision: strings.Repeat("1", 40), Image: "ghcr.io/example@sha256:" + strings.Repeat("a", 64), Distribution: "public", Platform: "linux/amd64"}, ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, ArtifactAdmissionDigest: "sha256:" + strings.Repeat("d", 64)}
	candidate := transitionpreflight.ArtifactIdentity{Release: compatibility.ReleaseIdentity{ReleaseID: "cand", Version: "2", SourceRevision: strings.Repeat("2", 40), Image: "ghcr.io/example@sha256:" + strings.Repeat("b", 64), Distribution: "public", Platform: "linux/amd64"}, ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, ArtifactAdmissionDigest: "sha256:" + strings.Repeat("e", 64)}
	pd, _ := pred.Digest()
	cd, _ := candidate.Digest()
	policy := transitionpreflight.ReleasePolicy{Version: transitionpreflight.ReleasePolicyVersion, Rules: []transitionpreflight.ReleasePolicyRule{{PredecessorArtifactDigest: pd, CandidateArtifactDigest: cd, RollbackFromArtifactDigest: cd, RollbackToArtifactDigest: pd, Decision: transitionpreflight.DecisionBinaryRollbackCompatible}}}
	policy.Digest, _ = policy.ContentDigest()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "catalog:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	input := transitionpreflight.Input{SchemaVersion: 1, TargetIdentityDigest: target, Predecessor: pred, Candidate: candidate, MigrationOwnership: transitionpreflight.MigrationOwnership{GooseControlSchemaOwner: transitionpreflight.OwnerLeapView, RiverOperationalSchemaOwner: transitionpreflight.OwnerRiver, RiverJobHistoryOwner: transitionpreflight.OwnerLeapView}, Control: transitionpreflight.PostgreSQLControlProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, PredecessorSchemaVersion: "goose/v1", CandidateSchemaVersion: "goose/v2", TargetIdentityDigest: target}, River: transitionpreflight.RiverJobProjection{SchemaCompatibility: transitionpreflight.CompatibilityBackwardCompatible, JobHistoryCompatibility: transitionpreflight.CompatibilityBackwardCompatible, ExistingSchemaVersion: "river/v1", RequiredSchemaVersion: "river/v2", ExistingJobHistoryVersion: "jobs/v1", RequiredJobHistoryVersion: "jobs/v2", TargetIdentityDigest: target}, DuckLake: transitionpreflight.DuckLakeProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, Predecessor: tuple, Candidate: tuple, TargetIdentityDigest: target}, RecoveryFrontier: &transitionpreflight.RecoveryFrontierRef{SetID: "018f3f83-7b2f-7b37-9f9e-000000000010", Digest: "sha256:" + strings.Repeat("c", 64), TargetIdentityDigest: target}, ReleasePolicy: policy}
	evidence, err := transitionpreflight.Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := evidence.Digest()
	return transitionpreflight.ResolutionResult{Evidence: evidence, EvidenceDigest: digest}
}
