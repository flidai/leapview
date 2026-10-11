package postgres

import (
	"errors"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"testing"
	"time"
)

func TestRecoveryLedgerExactClaimNeverConsumesUnrelatedWork(t *testing.T) {
	ledger := NewRecoveryLedger(recoveryLedgerRuntimePool(t))
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	enqueue := func(name string, planned time.Time) recovery.Occurrence {
		t.Helper()
		definition := recovery.Definition{ScheduleID: name, Scenario: "managed-recovery", Operation: recovery.OperationRestore, PolicyVersion: "managed-v1", PolicySHA256: repeatHex("a"), TargetScope: "target", ArtifactIdentity: "ghcr.io/flidai/leapview@sha256:" + repeatHex("b"), Cron: "@daily", Timezone: "UTC", StaleAfter: time.Hour, Enabled: true}
		if err := ledger.ReconcileSchedule(t.Context(), definition, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		revision, err := recovery.ScheduleRevisionID(definition)
		if err != nil {
			t.Fatal(err)
		}
		occurrence, _, err := ledger.Enqueue(t.Context(), recovery.EnqueueInput{ScheduleID: name, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: planned, StaleAfter: definition.StaleAfter}, now)
		if err != nil {
			t.Fatal(err)
		}
		return occurrence
	}
	unrelated := enqueue("first-unrelated", now.Add(-time.Minute))
	selected := enqueue("exact-managed", now)
	claim := recovery.ClaimInput{WorkerID: "exact-worker", Actor: "managed-operator", Now: now, Lease: time.Minute}
	if _, ok, err := ledger.ClaimExact(t.Context(), "missing", claim); err != nil || ok {
		t.Fatalf("missing claim: %v %v", ok, err)
	}
	claimed, ok, err := ledger.ClaimExact(t.Context(), selected.ID, claim)
	if err != nil || !ok || claimed.ID != selected.ID {
		t.Fatalf("exact claim: %v %v", ok, err)
	}
	if _, ok, err := ledger.ClaimExact(t.Context(), selected.ID, claim); err != nil || ok {
		t.Fatalf("active lease stolen: %v %v", ok, err)
	}
	read, err := ledger.Occurrence(t.Context(), unrelated.ID)
	if err != nil || read.Status != recovery.StatusPending || read.AttemptCount != 0 {
		t.Fatal("exact claim consumed unrelated work")
	}
	if err := ledger.Start(t.Context(), claimed.ID, claimed.Fence, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	later := claim
	later.Now = now.Add(2 * time.Minute)
	later.WorkerID = "successor"
	successor, ok, err := ledger.ClaimExact(t.Context(), selected.ID, later)
	if err != nil || !ok || successor.Fence.Generation != claimed.Fence.Generation+1 {
		t.Fatalf("expired exact reclaim: %v %v", ok, err)
	}
	attempts, err := ledger.Attempts(t.Context(), selected.ID)
	if err != nil || len(attempts) != 2 || attempts[0].Status != "abandoned" {
		t.Fatal("exact predecessor attempt was not abandoned")
	}
	if err := ledger.Cancel(t.Context(), successor.ID, successor.Fence, later.Now.Add(time.Second), errors.New("operator canceled")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ledger.ClaimExact(t.Context(), selected.ID, later); err != nil || ok {
		t.Fatalf("terminal occurrence revived: %v %v", ok, err)
	}
	read, err = ledger.Occurrence(t.Context(), unrelated.ID)
	if err != nil || read.Status != recovery.StatusPending || read.AttemptCount != 0 {
		t.Fatal("reclaim touched unrelated work")
	}
	if first, ok, err := ledger.ClaimNext(t.Context(), later); err != nil || !ok || first.ID != unrelated.ID {
		t.Fatal("ordinary queue behavior changed")
	}
}
