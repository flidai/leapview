package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/jackc/pgx/v5/pgxpool"
)

func transitionInput(t *testing.T, key, target string) transitionoperation.CreateInput {
	t.Helper()
	predecessor := policyArtifact("1", "1", "1", "1")
	candidate := policyArtifact("2", "2", "2", "2")
	policy := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionBinaryRollbackCompatible)
	input := policyEvaluationInput(predecessor, candidate, policy)
	input.TargetIdentityDigest = target
	input.Control.TargetIdentityDigest = target
	input.River.TargetIdentityDigest = target
	input.DuckLake.TargetIdentityDigest = target
	input.RecoveryFrontier.TargetIdentityDigest = target
	evidence, err := transitionpreflight.Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest, err := evidence.Digest()
	if err != nil {
		t.Fatal(err)
	}
	predecessorDigest, _ := predecessor.Digest()
	candidateDigest, _ := candidate.Digest()
	return transitionoperation.CreateInput{
		TargetIdentityDigest:      target,
		PredecessorArtifactDigest: predecessorDigest,
		CandidateArtifactDigest:   candidateDigest,
		RecoveryFrontierID:        input.RecoveryFrontier.SetID,
		RecoveryFrontierDigest:    input.RecoveryFrontier.Digest,
		PreflightEvidenceDigest:   evidenceDigest,
		PreflightEvidence:         canonical,
		IdempotencyKey:            key,
	}
}

func TestTransitionOperationIdentityReplayAndConflict(t *testing.T) {
	p := testDB(t)
	repo := NewTransitionRepository(p)
	in := transitionInput(t, "transition-replay", digest("a"))
	first, err := repo.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID == "" || first.RequestDigest == "" {
		t.Fatalf("first operation missing identity: %#v", first)
	}
	replay := in
	second, err := repo.Create(t.Context(), replay)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if second.OperationID != first.OperationID || string(second.PreflightEvidence) != string(first.PreflightEvidence) || second.RequestDigest != first.RequestDigest {
		t.Fatalf("replay changed durable identity: first=%#v second=%#v", first, second)
	}
	replay.OperationID = "0198f2c0-7c7a-7f00-8a11-000000000111"
	if _, err := repo.Create(t.Context(), replay); !errors.Is(err, transitionoperation.ErrConflict) {
		t.Fatalf("changed operation id = %v, want conflict", err)
	}
	replay.OperationID = ""
	conflict := replay
	conflict.CandidateArtifactDigest = digest("5")
	if _, err := repo.Create(t.Context(), conflict); !errors.Is(err, transitionoperation.ErrConflict) {
		t.Fatalf("changed candidate = %v, want conflict", err)
	}
	conflict = replay
	conflict.PreflightEvidence = []byte(`{"decision":"unsupported","phase":"preflight"}`)
	conflict.PreflightEvidenceDigest = digest("6")
	if _, err := repo.Create(t.Context(), conflict); !errors.Is(err, transitionoperation.ErrInvalid) {
		t.Fatalf("malformed evidence = %v, want invalid", err)
	}
	noncanonical := in
	noncanonical.PreflightEvidence = append([]byte("\n"), noncanonical.PreflightEvidence...)
	if _, err := repo.Create(t.Context(), noncanonical); !errors.Is(err, transitionoperation.ErrConflict) {
		t.Fatalf("noncanonical evidence = %v, want conflict", err)
	}
}

func TestTransitionOperationTargetFenceIsExclusiveAndScoped(t *testing.T) {
	p := testDB(t)
	repo := NewTransitionRepository(p)
	first, err := repo.Create(t.Context(), transitionInput(t, "fence-one", digest("a")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Create(t.Context(), transitionInput(t, "fence-two", digest("a")))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.Claim(t.Context(), first.OperationID, "owner-one", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Fence.FencingGeneration != 1 {
		t.Fatalf("first fence generation = %d, want 1", claimed.Fence.FencingGeneration)
	}
	if _, err := repo.Claim(t.Context(), second.OperationID, "owner-two", time.Now().UTC().Add(time.Minute)); !errors.Is(err, transitionoperation.ErrBusy) {
		t.Fatalf("competing target claim = %v, want busy", err)
	}
	if _, err := repo.Claim(t.Context(), second.OperationID, "owner-one", time.Now().UTC().Add(time.Minute)); !errors.Is(err, transitionoperation.ErrBusy) {
		t.Fatalf("same-name competing target claim = %v, want busy", err)
	}
	other, err := repo.Create(t.Context(), transitionInput(t, "fence-other-target", digest("b")))
	if err != nil {
		t.Fatal(err)
	}
	otherClaim, err := repo.Claim(t.Context(), other.OperationID, "owner-two", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("different target claim: %v", err)
	}
	if otherClaim.Fence.FencingGeneration != 1 || otherClaim.Fence.OperationID != other.OperationID {
		t.Fatalf("different target fence = %#v", otherClaim.Fence)
	}
	if err := repo.Validate(t.Context(), claimed.Fence); err != nil {
		t.Fatalf("first fence should remain valid: %v", err)
	}
	if err := repo.Release(t.Context(), claimed.Fence); err != nil {
		t.Fatalf("release running fence: %v", err)
	}
	if err := repo.Validate(t.Context(), claimed.Fence); err != nil {
		t.Fatalf("running operation fence was released before a durable terminal result: %v", err)
	}
}

func TestTransitionOperationPersistsMonotonicPhasesAndCompletion(t *testing.T) {
	p := testDB(t)
	repo := NewTransitionRepository(p)
	op, err := repo.Create(t.Context(), transitionInput(t, "phase-progression", digest("a")))
	if err != nil {
		t.Fatal(err)
	}
	op, err = repo.Claim(t.Context(), op.OperationID, "phase-owner", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	phases := transitionoperation.PhaseNames()
	for _, phase := range phases {
		result := transitionoperation.PhaseResult{Phase: phase, Status: transitionoperation.PhaseResultSucceeded, Result: []byte(fmt.Sprintf(`{"phase":%q}`, phase)), StartedAt: time.Now().UTC().Add(-time.Millisecond)}
		op, err = repo.RecordTransitionPhase(t.Context(), op.Fence, result)
		if err != nil {
			t.Fatalf("record %s: %v", phase, err)
		}
		if phase != transitionoperation.PhaseSuccess && op.CurrentPhase == phase {
			t.Fatalf("phase %s did not advance: %#v", phase, op)
		}
	}
	if op.Status != transitionoperation.StatusRunning {
		t.Fatalf("success result should retain fence until Complete, status=%s", op.Status)
	}
	completed, err := repo.Complete(t.Context(), transitionrunner.CompleteInput{OperationID: op.OperationID, OwnerID: op.Fence.OwnerID, Fence: op.Fence, Result: []byte(`{"success":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != transitionoperation.StatusCompleted || len(completed.PhaseResults) != len(phases) {
		t.Fatalf("completed operation = %#v", completed)
	}
	if err := repo.Validate(t.Context(), op.Fence); !errors.Is(err, transitionoperation.ErrStaleFence) {
		t.Fatalf("released fence validation = %v, want stale fence", err)
	}
	if _, err := repo.RecordTransitionPhase(t.Context(), op.Fence, transitionoperation.PhaseResult{Phase: transitionoperation.PhaseSuccess, Status: transitionoperation.PhaseResultSucceeded, Result: []byte(`{"success":true}`)}); !errors.Is(err, transitionoperation.ErrAlreadyTerminal) && !errors.Is(err, transitionoperation.ErrStaleFence) {
		t.Fatalf("post-completion phase write = %v", err)
	}
}

func TestTransitionOperationRecordsIndeterminateAfterLeaseExpiry(t *testing.T) {
	p := testDB(t)
	repo := NewTransitionRepository(p)
	op, err := repo.Create(t.Context(), transitionInput(t, "expired-post-effect", digest("a")))
	if err != nil {
		t.Fatal(err)
	}
	op, err = repo.Claim(t.Context(), op.OperationID, "effect-owner", time.Now().UTC().Add(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `SELECT pg_sleep(0.1)`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Fail(t.Context(), transitionrunner.FailureInput{
		OperationID: op.OperationID,
		OwnerID:     op.Fence.OwnerID,
		Fence:       op.Fence,
		Phase:       transitionrunner.PhasePreflight,
		Status:      transitionoperation.PhaseResultIndeterminate,
		Code:        "fence_lost",
		Summary:     "effect completed before lease expiry was observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != transitionoperation.StatusIndeterminate {
		t.Fatalf("status=%q, want indeterminate", got.Status)
	}
	if len(got.PhaseResults) != 1 || got.PhaseResults[0].Status != transitionoperation.PhaseResultIndeterminate {
		t.Fatalf("phase results=%#v", got.PhaseResults)
	}
}

func TestTransitionOperationReadbackSurvivesPoolReopen(t *testing.T) {
	p := testDB(t)
	repo := NewTransitionRepository(p)
	in := transitionInput(t, "restart-readback", digest("a"))
	want, err := repo.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	want, err = repo.Claim(t.Context(), want.OperationID, "restart-owner", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want, err = repo.RecordTransitionPhase(t.Context(), want.Fence, transitionoperation.PhaseResult{Phase: transitionoperation.PhasePreflight, Status: transitionoperation.PhaseResultSucceeded, Result: []byte(`{"phase":"preflight","digest":"one"}`)})
	if err != nil {
		t.Fatal(err)
	}
	dsn := p.Config().ConnString()
	p.Close()
	reopened, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := NewTransitionRepository(reopened).Get(t.Context(), want.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OperationID != want.OperationID || got.CurrentPhase != transitionoperation.PhaseMigrations || got.Status != transitionoperation.StatusRunning || len(got.PhaseResults) != 1 || string(got.PhaseResults[0].Result) != string(want.PhaseResults[0].Result) {
		t.Fatalf("reopened operation = %#v, want %#v", got, want)
	}
}

func TestTransitionOperationReadbackRejectsCorruptPhaseEvidence(t *testing.T) {
	p := testDB(t)
	repo := NewTransitionRepository(p)
	op, err := repo.Create(t.Context(), transitionInput(t, "corrupt-phase-readback", digest("a")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `
		INSERT INTO release.release_transition_phase_result
		(operation_id,phase,result_status,result_digest,result_bytes)
		VALUES ($1::uuid,'preflight','succeeded',$2,$3)`,
		op.OperationID, digest("f"), []byte(`{"phase":"preflight"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(t.Context(), op.OperationID); !errors.Is(err, transitionoperation.ErrConflict) {
		t.Fatalf("corrupt phase readback = %v, want conflict", err)
	}
}
