package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recoveryLedgerEvidence struct {
	Occurrence               recovery.Occurrence        `json:"occurrence"`
	FailureOccurrence        recovery.Occurrence        `json:"failureOccurrence"`
	Attempts                 []recovery.Attempt         `json:"attempts"`
	EvidenceAttempts         []recovery.EvidenceAttempt `json:"evidenceAttempts"`
	Retention                recovery.RetentionResult   `json:"retention"`
	RestartReadback          bool                       `json:"restartReadback"`
	ForeignFenceRejected     bool                       `json:"foreignFenceRejected"`
	StaleFenceRejected       bool                       `json:"staleFenceRejected"`
	PublicationRetry         bool                       `json:"publicationRetry"`
	PhaseTransitionsStored   bool                       `json:"phaseTransitionsStored"`
	ExactReplayIdempotent    bool                       `json:"exactReplayIdempotent"`
	IdentityConflictRejected bool                       `json:"identityConflictRejected"`
	ExpiredLeaseRecovered    bool                       `json:"expiredLeaseRecovered"`
	TerminalFailurePersisted bool                       `json:"terminalFailurePersisted"`
}

func TestRecoveryLedgerPostgresDurabilityFencingPublicationAndRetention(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	evidence := exerciseRecoveryLedger(t, pool)
	if evidence.Occurrence.Status != recovery.StatusSucceeded || evidence.Occurrence.EvidenceStatus != "published" {
		t.Fatalf("final occurrence = status %q evidence %q", evidence.Occurrence.Status, evidence.Occurrence.EvidenceStatus)
	}
	if !evidence.RestartReadback || !evidence.ForeignFenceRejected || !evidence.StaleFenceRejected || !evidence.PublicationRetry || !evidence.PhaseTransitionsStored || !evidence.ExactReplayIdempotent || !evidence.IdentityConflictRejected || !evidence.ExpiredLeaseRecovered || !evidence.TerminalFailurePersisted {
		t.Fatalf("qualification evidence incomplete: %+v", evidence)
	}
	if len(evidence.Retention.DeletedIDs) != 1 || len(evidence.Retention.PreservedIDs) != 2 {
		t.Fatalf("retention result = %+v", evidence.Retention)
	}
}

func recoveryLedgerRuntimePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	database, _ := refreshTestDB(t)
	pool, err := pgxpool.New(t.Context(), database.URL(postgrestest.Role{Name: "leapview_control_runtime", Password: "refresh_runtime_password", Login: true}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func exerciseRecoveryLedger(t *testing.T, pool DBTX) recoveryLedgerEvidence {
	t.Helper()
	ctx := t.Context()
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	definition := recovery.Definition{
		ScheduleID: "fai-1001-upgrade", Scenario: "postgres-ledger-qualification", Operation: recovery.OperationUpgrade,
		PolicyVersion: "ubdr-v1", PolicySHA256: repeatHex("a"), TargetScope: "target-production-a",
		ArtifactIdentity: "ghcr.io/flidai/leapview@sha256:" + repeatHex("b"), Cron: "0 * * * *", Timezone: "UTC",
		StaleAfter: 24 * time.Hour, Enabled: true,
	}
	if err := ledger.ReconcileSchedule(ctx, definition, base.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	revision, err := recovery.ScheduleRevisionID(definition)
	if err != nil {
		t.Fatal(err)
	}
	due, err := ledger.EnqueueDue(ctx, base.Add(-2*time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("due schedule occurrences = %d, want 2", len(due))
	}
	old, latest := due[0], due[1]
	replayInput := recovery.EnqueueInput{ScheduleID: definition.ScheduleID, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: latest.PlannedAt, StaleAfter: definition.StaleAfter}
	replayed, created, err := ledger.Enqueue(ctx, replayInput, base.Add(-time.Hour))
	if err != nil || created || replayed.ID != latest.ID {
		t.Fatalf("exact replay: occurrence=%q created=%v err=%v", replayed.ID, created, err)
	}
	conflictInput := replayInput
	conflictInput.ArtifactIdentity = "ghcr.io/flidai/leapview@sha256:" + repeatHex("d")
	_, _, identityConflictErr := ledger.Enqueue(ctx, conflictInput, base.Add(-time.Hour))
	complete := func(id string, claimAt time.Time, worker string, recoverExpired bool) (recovery.Occurrence, bool, bool, bool) {
		lease := 10 * time.Minute
		if recoverExpired {
			lease = time.Minute
		}
		claimed, ok, err := ledger.ClaimNext(ctx, recovery.ClaimInput{WorkerID: worker, Actor: "qualification-operator", Now: claimAt, Lease: lease})
		if err != nil {
			t.Fatal(err)
		}
		if !ok || claimed.ID != id {
			t.Fatalf("claimed %q, want %q", claimed.ID, id)
		}
		restarted := NewRecoveryLedger(pool)
		readback, err := restarted.Occurrence(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		restartOK := readback.Fence == claimed.Fence && readback.TargetScope == definition.TargetScope
		foreign := recovery.Fence{Owner: "foreign-worker", Generation: claimed.Fence.Generation}
		foreignRejected := errors.Is(restarted.Start(ctx, id, foreign, claimAt.Add(time.Second)), recovery.ErrFenced)
		recovered := !recoverExpired
		startAt := claimAt.Add(2 * time.Second)
		if recoverExpired {
			oldFence := claimed.Fence
			claimed, ok, err = restarted.ClaimNext(ctx, recovery.ClaimInput{WorkerID: worker + "-recovered", Actor: "qualification-operator", Now: claimAt.Add(2 * time.Minute), Lease: 10 * time.Minute})
			if err != nil || !ok || claimed.ID != id {
				t.Fatalf("expired lease recovery: occurrence=%q ok=%v err=%v", claimed.ID, ok, err)
			}
			recovered = claimed.Fence.Generation == oldFence.Generation+1 && errors.Is(restarted.Start(ctx, id, oldFence, claimAt.Add(2*time.Minute+time.Second)), recovery.ErrFenced)
			startAt = claimAt.Add(2*time.Minute + 2*time.Second)
		}
		if err := restarted.Start(ctx, id, claimed.Fence, startAt); err != nil {
			t.Fatal(err)
		}
		if err := restarted.RecordPhase(ctx, id, claimed.Fence, "readiness", "started", startAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := restarted.RecordPhase(ctx, id, claimed.Fence, "readiness", "completed", startAt.Add(6*time.Second)); err != nil {
			t.Fatal(err)
		}
		result := recovery.Result{RecoveryPointAt: claimAt.Add(-time.Minute), Evidence: []recovery.EvidenceReference{{Kind: "qualification-report", URI: "file:///var/lib/leapview/evidence/" + id + ".json", SHA256: repeatHex("c")}}}
		if err := restarted.Complete(ctx, id, claimed.Fence, startAt.Add(8*time.Second), result); err != nil {
			t.Fatal(err)
		}
		return claimed, restartOK, foreignRejected, recovered
	}
	oldClaim, restartOK, foreignRejected, _ := complete(old.ID, base.Add(-3*time.Hour), "worker-old", false)
	_, restartLatest, foreignLatest, expiredRecovered := complete(latest.ID, base.Add(-time.Hour), "worker-latest", true)
	if !restartLatest || !foreignLatest {
		t.Fatal("latest restart/fence control failed")
	}
	staleRejected := errors.Is(ledger.Heartbeat(ctx, old.ID, oldClaim.Fence, base, time.Minute), recovery.ErrFenced)
	publishedClaim, ok, err := ledger.ClaimEvidence(ctx, "publisher-a", base, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || publishedClaim.ID != old.ID {
		t.Fatalf("evidence claim = %q", publishedClaim.ID)
	}
	if err := ledger.PublishEvidence(ctx, old.ID, publishedClaim.EvidenceFence, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failedClaim, ok, err := ledger.ClaimEvidence(ctx, "publisher-a", base.Add(2*time.Second), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || failedClaim.ID != latest.ID {
		t.Fatalf("evidence claim = %q", failedClaim.ID)
	}
	if err := ledger.FailEvidence(ctx, latest.ID, failedClaim.EvidenceFence, base.Add(3*time.Second), recovery.NewFailure("publication_interrupted", "publication transport interrupted")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ledger.ClaimEvidence(ctx, "publisher-b", base.Add(30*time.Second), 5*time.Minute); err != nil || ok {
		t.Fatalf("premature retry ok=%v err=%v", ok, err)
	}
	retry, ok, err := ledger.ClaimEvidence(ctx, "publisher-b", base.Add(2*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || retry.ID != latest.ID {
		t.Fatalf("retry claim = %q", retry.ID)
	}
	if err := ledger.PublishEvidence(ctx, latest.ID, retry.EvidenceFence, base.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	failureInput := replayInput
	failureInput.PlannedAt = base.Add(time.Hour)
	failed, created, err := ledger.Enqueue(ctx, failureInput, base.Add(time.Hour))
	if err != nil || !created {
		t.Fatalf("failure occurrence enqueue: created=%v err=%v", created, err)
	}
	failureClaim, ok, err := ledger.ClaimNext(ctx, recovery.ClaimInput{WorkerID: "worker-failure", Actor: "qualification-operator", Now: base.Add(time.Hour), Lease: 10 * time.Minute})
	if err != nil || !ok || failureClaim.ID != failed.ID {
		t.Fatalf("failure occurrence claim=%q ok=%v err=%v", failureClaim.ID, ok, err)
	}
	if err := ledger.Start(ctx, failed.ID, failureClaim.Fence, base.Add(time.Hour+time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Fail(ctx, failed.ID, failureClaim.Fence, base.Add(time.Hour+2*time.Second), recovery.Result{}, recovery.NewFailure("qualification_failed", "bounded qualification failure")); err != nil {
		t.Fatal(err)
	}
	failureFinal, err := ledger.Occurrence(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	final, err := ledger.Occurrence(ctx, latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ledger.Attempts(ctx, latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidenceAttempts, err := ledger.EvidenceAttempts(ctx, latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	retention, err := ledger.Retain(ctx, recovery.RetentionPolicy{Now: base.Add(48 * time.Hour), ComplianceWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return recoveryLedgerEvidence{Occurrence: final, FailureOccurrence: failureFinal, Attempts: attempts, EvidenceAttempts: evidenceAttempts, Retention: retention, RestartReadback: restartOK, ForeignFenceRejected: foreignRejected, StaleFenceRejected: staleRejected, PublicationRetry: len(evidenceAttempts) == 2 && evidenceAttempts[0].Status == "failed" && evidenceAttempts[1].Status == "published", PhaseTransitionsStored: !final.ReadinessStartedAt.IsZero() && !final.ReadinessCompletedAt.IsZero() && final.ReadinessDurationMillis == 5000, ExactReplayIdempotent: true, IdentityConflictRejected: errors.Is(identityConflictErr, recovery.ErrConflict), ExpiredLeaseRecovered: expiredRecovered && len(attempts) == 2 && attempts[0].Status == "abandoned" && attempts[1].Status == "succeeded", TerminalFailurePersisted: failureFinal.Status == recovery.StatusFailed && failureFinal.FailureCode == "qualification_failed"}
}

func repeatHex(value string) string {
	out := ""
	for len(out) < 64 {
		out += value
	}
	return out[:64]
}
