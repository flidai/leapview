package deployment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCandidateStateMachineSupportsIdempotentPreparationLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	firstDigest := "sha256:" + strings.Repeat("a", 64)
	secondDigest := "sha256:" + strings.Repeat("b", 64)
	firstProvenance := "sha256:" + strings.Repeat("c", 64)
	secondProvenance := "sha256:" + strings.Repeat("d", 64)
	candidate, err := NewCandidate(CandidateStartInput{
		ID: "cand_opaque", TargetID: "lvinst_prod", OwnerID: "principal_1",
		Scope:          CandidateScope{ProjectID: "finance", Environment: "prod", BaseGenerationID: "deployment_7"},
		ArtifactDigest: firstDigest, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	require.NoError(t, err)
	if candidate.Status != CandidatePreparing || candidate.Revision != 1 {
		t.Fatalf("candidate = %#v", candidate)
	}

	ready, err := candidate.MarkReady(firstDigest, firstProvenance, now.Add(time.Minute))
	require.NoError(t, err)
	if ready.ProvenanceDigest != firstProvenance {
		t.Fatalf("ready provenance = %q, want %q", ready.ProvenanceDigest, firstProvenance)
	}
	replayed, err := ready.MarkReady(firstDigest, firstProvenance, now.Add(2*time.Minute))
	require.NoError(t, err)
	if replayed != ready {
		t.Fatalf("idempotent ready changed candidate: %#v", replayed)
	}

	replaced, err := ready.ReplaceArtifact(firstDigest, secondDigest, now.Add(3*time.Minute), now.Add(2*time.Hour))
	require.NoError(t, err)
	if replaced.Status != CandidatePreparing || replaced.ArtifactDigest != secondDigest ||
		replaced.Revision != ready.Revision+1 || replaced.FailureReason != "" ||
		replaced.ProvenanceDigest != "" {
		t.Fatalf("replacement = %#v", replaced)
	}
	if _, err := ready.MarkReady(firstDigest, secondProvenance, now.Add(2*time.Minute)); !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("changed provenance replay error = %v, want ErrCandidateConflict", err)
	}
	if _, err := replaced.ReplaceArtifact(firstDigest, firstDigest, now, now.Add(time.Hour)); !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("stale replacement error = %v, want ErrCandidateConflict", err)
	}
}

func TestCandidateStateMachineRetryCancelAndExpireAreDeterministic(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	candidate, err := NewCandidate(CandidateStartInput{
		ID: "cand_opaque", TargetID: "lvinst_prod", OwnerID: "principal_1",
		Scope:          CandidateScope{ProjectID: "finance", Environment: "prod", BaseGenerationID: "deployment_7"},
		ArtifactDigest: digest, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	require.NoError(t, err)
	failed, err := candidate.MarkFailed(digest, "RUNTIME_PREPARATION_FAILED", now.Add(time.Minute))
	require.NoError(t, err)
	if failed.FailureReason != "RUNTIME_PREPARATION_FAILED" {
		t.Fatalf("failure reason = %q", failed.FailureReason)
	}
	retried, err := failed.Retry(now.Add(2*time.Minute), now.Add(2*time.Hour))
	require.NoError(t, err)
	if retried.Status != CandidatePreparing || retried.FailureReason != "" {
		t.Fatalf("retry = %#v", retried)
	}
	replayed, err := retried.Retry(now.Add(3*time.Minute), now.Add(3*time.Hour))
	require.NoError(t, err)
	if replayed != retried {
		t.Fatalf("idempotent retry changed candidate: %#v", replayed)
	}

	cancelled, err := retried.Cancel(now.Add(4 * time.Minute))
	require.NoError(t, err)
	replayedCancel, err := cancelled.Cancel(now.Add(5 * time.Minute))
	require.NoError(t, err)
	if replayedCancel != cancelled {
		t.Fatalf("idempotent cancel changed candidate: %#v", replayedCancel)
	}
	if _, err := cancelled.Retry(now, now.Add(time.Hour)); !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("retry cancelled error = %v, want ErrCandidateConflict", err)
	}

	expired, changed, err := retried.Expire(now.Add(3 * time.Hour))
	require.NoError(t, err)
	if !changed || expired.Status != CandidateExpired {
		t.Fatalf("expiry = %#v changed=%v", expired, changed)
	}
	replayedExpiry, changed, err := expired.Expire(now.Add(4 * time.Hour))
	if err != nil || changed || replayedExpiry != expired {
		t.Fatalf("idempotent expiry = %#v changed=%v err=%v", replayedExpiry, changed, err)
	}
}

func TestCandidateRejectsUnsafeFailureReasons(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	candidate, err := NewCandidate(CandidateStartInput{
		ID: "cand_opaque", TargetID: "lvinst_prod", OwnerID: "principal_1",
		Scope:          CandidateScope{ProjectID: "finance", Environment: "prod", BaseGenerationID: "deployment_7"},
		ArtifactDigest: digest, ExpiresAt: now.Add(time.Hour), Now: now,
	})
	require.NoError(t, err)
	for _, reason := range []string{
		"",
		"runtime preparation failed",
		"PASSWORD=source-secret",
		strings.Repeat("A", 65),
	} {
		if _, err := candidate.MarkFailed(digest, reason, now.Add(time.Minute)); !errors.Is(err, ErrCandidateInvalid) {
			t.Fatalf("MarkFailed(%q) error = %v, want ErrCandidateInvalid", reason, err)
		}
	}
}

func TestCandidateRejectsInvalidIdentityDigestAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	valid := CandidateStartInput{
		ID: "cand_opaque", TargetID: "lvinst_prod", OwnerID: "principal_1",
		Scope:          CandidateScope{ProjectID: "finance", Environment: "prod", BaseGenerationID: "deployment_7"},
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Hour), Now: now,
	}
	cases := map[string]CandidateStartInput{
		"id":          withCandidateStart(valid, func(input *CandidateStartInput) { input.ID = "" }),
		"project":     withCandidateStart(valid, func(input *CandidateStartInput) { input.Scope.ProjectID = "" }),
		"target":      withCandidateStart(valid, func(input *CandidateStartInput) { input.TargetID = "" }),
		"environment": withCandidateStart(valid, func(input *CandidateStartInput) { input.Scope.Environment = "" }),
		"owner":       withCandidateStart(valid, func(input *CandidateStartInput) { input.OwnerID = "" }),
		"base":        withCandidateStart(valid, func(input *CandidateStartInput) { input.Scope.BaseGenerationID = "" }),
		"digest":      withCandidateStart(valid, func(input *CandidateStartInput) { input.ArtifactDigest = "secret" }),
		"expiry":      withCandidateStart(valid, func(input *CandidateStartInput) { input.ExpiresAt = now }),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCandidate(input); err == nil {
				t.Fatal("NewCandidate() succeeded")
			}
		})
	}
}

func TestCandidateRejectsWhitespaceAliasesAtDomainBoundary(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	base := CandidateStartInput{
		ID: "cand_opaque", TargetID: "lvinst_prod", OwnerID: "principal_1",
		Scope:          CandidateScope{ProjectID: "finance", Environment: "prod", BaseGenerationID: "generation_7"},
		ArtifactDigest: digest, ExpiresAt: now.Add(time.Hour), Now: now,
	}
	for name, mutate := range map[string]func(*CandidateStartInput){
		"id":          func(v *CandidateStartInput) { v.ID = " cand_opaque" },
		"project":     func(v *CandidateStartInput) { v.Scope.ProjectID = "finance " },
		"target":      func(v *CandidateStartInput) { v.TargetID = " lvinst_prod" },
		"environment": func(v *CandidateStartInput) { v.Scope.Environment = "prod " },
		"owner":       func(v *CandidateStartInput) { v.OwnerID = " principal_1" },
		"base":        func(v *CandidateStartInput) { v.Scope.BaseGenerationID = "generation_7 " },
		"digest":      func(v *CandidateStartInput) { v.ArtifactDigest = " " + digest },
		"key":         func(v *CandidateStartInput) { v.Key = " preview " },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := NewCandidate(input); !errors.Is(err, ErrCandidateInvalid) {
				t.Fatalf("NewCandidate() error = %v, want ErrCandidateInvalid", err)
			}
		})
	}
	candidate, err := NewCandidate(base)
	require.NoError(t, err)
	if _, err := candidate.ReplaceArtifact(" "+digest, digest, now.Add(time.Minute), now.Add(2*time.Hour)); !errors.Is(err, ErrCandidateInvalid) {
		t.Fatalf("ReplaceArtifact() error = %v, want ErrCandidateInvalid", err)
	}
	if _, err := candidate.MarkReady(digest, " sha256:"+strings.Repeat("b", 64), now.Add(time.Minute)); !errors.Is(err, ErrCandidateInvalid) {
		t.Fatalf("MarkReady() error = %v, want ErrCandidateInvalid", err)
	}
	if _, err := candidate.MarkFailed(digest, " RUNTIME_FAILED", now.Add(time.Minute)); !errors.Is(err, ErrCandidateInvalid) {
		t.Fatalf("MarkFailed() error = %v, want ErrCandidateInvalid", err)
	}
}

func withCandidateStart(input CandidateStartInput, change func(*CandidateStartInput)) CandidateStartInput {
	change(&input)
	return input
}
