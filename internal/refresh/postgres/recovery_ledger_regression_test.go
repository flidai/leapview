package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/refresh/recovery"
)

func recoveryTestDefinition(id string, enabled bool) recovery.Definition {
	return recovery.Definition{
		ScheduleID: id, Scenario: "ledger-regression", Operation: recovery.OperationUpgrade,
		PolicyVersion: "ubdr-v1", PolicySHA256: repeatHex("a"), TargetScope: "target-a",
		ArtifactIdentity: "ghcr.io/flidai/leapview@sha256:" + repeatHex("b"), Cron: "* * * * *", Timezone: "UTC",
		StaleAfter: 48 * time.Hour, Enabled: enabled,
	}
}

func TestRecoveryLedgerDisabledSchedulesDoNotMaterialize(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	definition := recoveryTestDefinition("disabled", true)
	if err := ledger.ReconcileSchedule(t.Context(), definition, base); err != nil {
		t.Fatal(err)
	}
	definition.Enabled = false
	if err := ledger.ReconcileSchedule(t.Context(), definition, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, err := ledger.EnqueueDue(t.Context(), base.Add(3*time.Minute), 10); err != nil || len(due) != 0 {
		t.Fatalf("disabled same revision due=%d err=%v", len(due), err)
	}
	definition.PolicyVersion = "ubdr-v2"
	definition.PolicySHA256 = repeatHex("c")
	if err := ledger.ReconcileSchedule(t.Context(), definition, base.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, err := ledger.EnqueueDue(t.Context(), base.Add(5*time.Minute), 10); err != nil || len(due) != 0 {
		t.Fatalf("disabled replacement due=%d err=%v", len(due), err)
	}
	if occurrences, err := ledger.Occurrences(t.Context()); err != nil || len(occurrences) != 0 {
		t.Fatalf("disabled occurrences=%d err=%v", len(occurrences), err)
	}
}

func TestRecoveryLedgerRolloverPreservesLargeBacklog(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	old := recoveryTestDefinition("backlog", true)
	if err := ledger.ReconcileSchedule(t.Context(), old, base.Add(-1002*time.Minute)); err != nil {
		t.Fatal(err)
	}
	next := old
	next.PolicyVersion = "ubdr-v2"
	next.PolicySHA256 = repeatHex("c")
	for range 3 {
		if err := ledger.ReconcileSchedule(t.Context(), next, base); err != nil {
			t.Fatal(err)
		}
	}
	occurrences, err := ledger.Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 1002 {
		t.Fatalf("materialized backlog=%d, want 1002", len(occurrences))
	}
}

func TestRecoveryLedgerEnqueueDueIsFairAndReturnsOnlyNewRows(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"schedule-a", "schedule-b"} {
		if err := ledger.ReconcileSchedule(t.Context(), recoveryTestDefinition(id, true), base.Add(-2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ledger.EnqueueDue(t.Context(), base, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first enqueue=%d err=%v", len(first), err)
	}
	second, err := ledger.EnqueueDue(t.Context(), base, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("second enqueue=%d err=%v", len(second), err)
	}
	if first[0].ScheduleID == second[0].ScheduleID {
		t.Fatalf("batch-size-one fairness repeatedly serviced %q", first[0].ScheduleID)
	}
	third, err := ledger.EnqueueDue(t.Context(), base, 1)
	if err != nil || len(third) != 1 || third[0].ID == first[0].ID || third[0].ID == second[0].ID {
		t.Fatalf("same-clock enqueue result=%+v err=%v", third, err)
	}
}

func TestRecoveryLedgerEnqueueDueDoesNotReturnEarlierReplayAtSameClock(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	definition := recoveryTestDefinition("same-clock-replay", true)
	if err := ledger.ReconcileSchedule(t.Context(), definition, base.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	revision, _ := recovery.ScheduleRevisionID(definition)
	input := recovery.EnqueueInput{ScheduleID: definition.ScheduleID, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: base.Add(-time.Minute), StaleAfter: definition.StaleAfter}
	if _, created, err := ledger.Enqueue(t.Context(), input, base); err != nil || !created {
		t.Fatalf("preexisting enqueue created=%v err=%v", created, err)
	}
	if due, err := ledger.EnqueueDue(t.Context(), base, 1); err != nil || len(due) != 0 {
		t.Fatalf("replayed enqueue due=%d err=%v, want no rows created by call", len(due), err)
	}
	if due, err := ledger.EnqueueDue(t.Context(), base, 1); err != nil || len(due) != 1 || !due[0].PlannedAt.Equal(base) {
		t.Fatalf("next same-clock enqueue=%+v err=%v", due, err)
	}
}

func TestRecoveryLedgerRejectsInvalidChronology(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	definition := recoveryTestDefinition("chronology", true)
	revision, err := recovery.ScheduleRevisionID(definition)
	if err != nil {
		t.Fatal(err)
	}
	input := recovery.EnqueueInput{ScheduleID: definition.ScheduleID, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: base, StaleAfter: definition.StaleAfter}
	occurrence, _, err := ledger.Enqueue(t.Context(), input, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "worker", Actor: "operator", Now: base.Add(-time.Second), Lease: time.Minute}); err != nil || ok {
		t.Fatalf("claim before plan ok=%v err=%v", ok, err)
	}
	claimed, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "worker", Actor: "operator", Now: base, Lease: time.Minute})
	if err != nil || !ok {
		t.Fatalf("valid claim ok=%v err=%v", ok, err)
	}
	if err := ledger.Start(t.Context(), occurrence.ID, claimed.Fence, base.Add(-time.Nanosecond)); !errors.Is(err, recovery.ErrFenced) {
		t.Fatalf("start before claim error=%v, want fenced", err)
	}
	if err := ledger.Start(t.Context(), occurrence.ID, claimed.Fence, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordPhase(t.Context(), occurrence.ID, claimed.Fence, "readiness", "started", base); !errors.Is(err, recovery.ErrFenced) {
		t.Fatalf("phase before execution error=%v, want fenced", err)
	}
	if err := ledger.RecordPhase(t.Context(), occurrence.ID, claimed.Fence, "readiness", "started", base.Add(2*time.Second)); err != nil {
		t.Fatalf("valid phase start: %v", err)
	}
	if err := ledger.RecordPhase(t.Context(), occurrence.ID, claimed.Fence, "readiness", "completed", base.Add(3*time.Second)); err != nil {
		t.Fatalf("valid phase completion: %v", err)
	}
}

func TestRecoveryLedgerRuntimeUpdatesCannotBypassAuthority(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	ledger := NewRecoveryLedger(pool)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	definition := recoveryTestDefinition("guards", true)
	revision, _ := recovery.ScheduleRevisionID(definition)
	occurrence, _, err := ledger.Enqueue(t.Context(), recovery.EnqueueInput{ScheduleID: definition.ScheduleID, ScheduleRevision: revision, Scenario: definition.Scenario, Operation: definition.Operation, PolicyVersion: definition.PolicyVersion, PolicySHA256: definition.PolicySHA256, TargetScope: definition.TargetScope, ArtifactIdentity: definition.ArtifactIdentity, PlannedAt: base, StaleAfter: definition.StaleAfter}, base)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := ledger.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "worker", Actor: "operator", Now: base, Lease: time.Minute})
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := ledger.Start(t.Context(), occurrence.ID, claim.Fence, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	mutations := []string{
		`UPDATE refresh.recovery_qualification_occurrence SET target_scope='target-b' WHERE occurrence_id=$1`,
		`UPDATE refresh.recovery_qualification_occurrence SET artifact_identity='ghcr.io/flidai/leapview@sha256:` + repeatHex("d") + `' WHERE occurrence_id=$1`,
		`UPDATE refresh.recovery_qualification_occurrence SET fence_generation=fence_generation+2 WHERE occurrence_id=$1`,
		`UPDATE refresh.recovery_qualification_attempt SET worker_id='foreign' WHERE occurrence_id=$1`,
		`UPDATE refresh.recovery_qualification_occurrence SET status='succeeded',result='success',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE occurrence_id=$1`,
	}
	for index, query := range mutations {
		if _, err := pool.Exec(t.Context(), query, occurrence.ID); err == nil {
			t.Fatalf("direct authority mutation %d unexpectedly succeeded", index)
		}
	}
	readback, err := ledger.Occurrence(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readback.TargetScope != definition.TargetScope || readback.ArtifactIdentity != definition.ArtifactIdentity || readback.Fence != claim.Fence || readback.Status != recovery.StatusRunning {
		t.Fatalf("guarded occurrence changed: %+v", readback)
	}
}

func TestRecoveryLedgerRetentionIsBoundedAndProgresses(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	_, err := pool.Exec(t.Context(), `
INSERT INTO refresh.recovery_qualification_occurrence(
 occurrence_id,request_digest,schedule_id,schedule_revision_id,scenario,operation,policy_version,policy_sha256,target_scope,artifact_identity,planned_at,expires_at,status,result,created_at,finished_at,evidence_status)
SELECT 'retention-'||n,'sha256:`+repeatHex("a")+`','retention','revision','retention','upgrade','v1','`+repeatHex("b")+`','target','ghcr.io/flidai/leapview@sha256:`+repeatHex("c")+`',$1::timestamptz-n*interval '1 second',$1::timestamptz+interval '1 day','canceled','canceled',$1::timestamptz-n*interval '1 second',$1::timestamptz-n*interval '1 second','none'
FROM generate_series(1,1002) AS n`, base.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ledger := NewRecoveryLedger(pool)
	first, err := ledger.Retain(t.Context(), recovery.RetentionPolicy{Now: base, ComplianceWindow: time.Hour})
	if err != nil || len(first.DeletedIDs) != 1000 {
		t.Fatalf("first retention deleted=%d err=%v", len(first.DeletedIDs), err)
	}
	second, err := ledger.Retain(t.Context(), recovery.RetentionPolicy{Now: base, ComplianceWindow: time.Hour})
	if err != nil || len(second.DeletedIDs) != 2 {
		t.Fatalf("second retention deleted=%d err=%v", len(second.DeletedIDs), err)
	}
	remaining, err := ledger.Occurrences(t.Context())
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining retention rows=%d err=%v", len(remaining), err)
	}
}
