package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/prometheus/client_golang/prometheus"
)

var recoveryArtifact = "oci://ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
var recoveryPolicySHA256 = strings.Repeat("b", 64)

func TestRecoveryOccurrenceEnqueueIsIdempotentAndRejectsConflictingArtifact(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 25, 5, 23, 0, 0, time.UTC)
	input := recoveryInput("idempotency", recovery.OperationRestore, now)
	created := 0
	for range 100 {
		occurrence, wasCreated, err := repository.Enqueue(t.Context(), input, now)
		if err != nil {
			t.Fatal(err)
		}
		if occurrence.ID == "" {
			t.Fatal("durable occurrence identity is empty")
		}
		if wasCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created occurrences = %d, want 1", created)
	}
	input.ArtifactIdentity = "oci://ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	if _, _, err := repository.Enqueue(t.Context(), input, now); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("conflicting immutable occurrence error = %v, want ErrConflict", err)
	}
	occurrences, err := repository.Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 1 {
		t.Fatalf("durable occurrence count = %d, want 1", len(occurrences))
	}
}

func TestRecoveryLedgerFailsClosedForMissingAndCorruptOccurrences(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	ctx := t.Context()
	if _, err := repository.Occurrence(ctx, "missing-occurrence"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing occurrence error = %v, want sql.ErrNoRows", err)
	}
	now := time.Date(2026, 8, 25, 5, 30, 0, 0, time.UTC)
	occurrence, _, err := repository.Enqueue(ctx, recoveryInput("corrupt-ledger", recovery.OperationBackup, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx,
		"UPDATE recovery_qualification_occurrences SET planned_at = ? WHERE occurrence_id = ?",
		"not-a-timestamp", occurrence.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Occurrence(ctx, occurrence.ID); err == nil || !strings.Contains(err.Error(), "parse recovery qualification time") {
		t.Fatalf("corrupt occurrence error = %v, want timestamp parse failure", err)
	}
	if _, err := repository.Status(ctx, now); err == nil || !strings.Contains(err.Error(), "parse recovery qualification time") {
		t.Fatalf("corrupt status error = %v, want timestamp parse failure", err)
	}
}

func TestRecoveryConcurrentClaimFencesStaleWorkerAndPreservesAttemptHistory(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 25, 6, 0, 0, 0, time.UTC)
	occurrence, _, err := repository.Enqueue(t.Context(), recoveryInput("concurrent", recovery.OperationUpgrade, now), now)
	if err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		occurrence recovery.Occurrence
		claimed    bool
		err        error
	}
	results := make(chan claimResult, 16)
	var wait sync.WaitGroup
	for worker := range 16 {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			claimed, ok, claimErr := repository.ClaimNext(t.Context(), recovery.ClaimInput{
				WorkerID: fmt.Sprintf("worker-%02d", worker), Actor: "scheduler",
				Now: now, Lease: time.Minute,
			})
			results <- claimResult{occurrence: claimed, claimed: ok, err: claimErr}
		}(worker)
	}
	wait.Wait()
	close(results)
	claims := []recovery.Occurrence{}
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.claimed {
			claims = append(claims, result.occurrence)
		}
	}
	if len(claims) != 1 {
		t.Fatalf("valid concurrent claims = %d, want 1", len(claims))
	}
	stale := claims[0]
	if err := repository.Start(t.Context(), occurrence.ID, stale.Fence, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	reclaimed, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{
		WorkerID: "worker-recovery", Actor: "scheduler", Now: now.Add(2 * time.Minute), Lease: time.Minute,
	})
	if err != nil || !ok {
		t.Fatalf("reclaim expired running occurrence = (%t, %v)", ok, err)
	}
	if reclaimed.ID != occurrence.ID || reclaimed.Fence.Generation <= stale.Fence.Generation {
		t.Fatalf("reclaimed occurrence/fence = %#v, stale %#v", reclaimed, stale.Fence)
	}
	if err := repository.Heartbeat(t.Context(), occurrence.ID, stale.Fence, now.Add(2*time.Minute), time.Minute); !errors.Is(err, recovery.ErrFenced) {
		t.Fatalf("stale heartbeat error = %v, want ErrFenced", err)
	}
	if err := repository.Start(t.Context(), occurrence.ID, reclaimed.Fence, now.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	result := successfulRecoveryResult(now.Add(2*time.Minute+30*time.Second), "upgrade")
	if err := repository.Complete(t.Context(), occurrence.ID, stale.Fence, now.Add(2*time.Minute+30*time.Second), result); !errors.Is(err, recovery.ErrFenced) {
		t.Fatalf("stale completion error = %v, want ErrFenced", err)
	}
	recordRecoveryTestPhase(t, repository, occurrence, reclaimed.Fence, now.Add(2*time.Minute+2*time.Second), now.Add(2*time.Minute+20*time.Second))
	if err := repository.Complete(t.Context(), occurrence.ID, reclaimed.Fence, now.Add(2*time.Minute+30*time.Second), result); err != nil {
		t.Fatal(err)
	}
	attempts, err := repository.Attempts(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Status != "abandoned" || attempts[1].Status != "succeeded" {
		t.Fatalf("attempt history = %#v", attempts)
	}
}

func TestRecoveryLedgerSurvivesRestartAndEvidencePublicationRetriesWithoutRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := platform.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(store.SQLDB())
	now := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)
	occurrence, _, err := repository.Enqueue(t.Context(), recoveryInput("restart", recovery.OperationRestore, now), now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "worker-one", Actor: "scheduler", Now: now, Lease: 5 * time.Minute})
	if err != nil || !ok {
		t.Fatalf("claim = (%t, %v)", ok, err)
	}
	if err := repository.Start(t.Context(), occurrence.ID, claimed.Fence, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recordRecoveryTestPhase(t, repository, occurrence, claimed.Fence, now.Add(2*time.Second), now.Add(50*time.Second))
	if err := repository.Complete(t.Context(), occurrence.ID, claimed.Fence, now.Add(time.Minute), successfulRecoveryResult(now.Add(time.Minute), "restore")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := platform.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	repository = NewRepository(restarted.SQLDB())
	published, ok, err := repository.ClaimEvidence(t.Context(), "publisher-one", now.Add(2*time.Minute), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim evidence = (%t, %v)", ok, err)
	}
	firstFence := published.EvidenceFence
	if err := repository.FailEvidence(t.Context(), occurrence.ID, firstFence, now.Add(2*time.Minute+10*time.Second), errors.New("upload token=super-secret failed")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repository.ClaimEvidence(t.Context(), "publisher-too-early", now.Add(3*time.Minute), time.Minute); err != nil || ok {
		t.Fatalf("evidence retry ignored persisted backoff: claimed=%t error=%v", ok, err)
	}
	retried, ok, err := repository.ClaimEvidence(t.Context(), "publisher-two", now.Add(3*time.Minute+11*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("retry evidence = (%t, %v)", ok, err)
	}
	if err := repository.PublishEvidence(t.Context(), occurrence.ID, firstFence, now.Add(3*time.Minute+time.Second)); !errors.Is(err, recovery.ErrFenced) {
		t.Fatalf("stale publisher error = %v, want ErrFenced", err)
	}
	if err := repository.PublishEvidence(t.Context(), occurrence.ID, retried.EvidenceFence, now.Add(3*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Occurrence(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EvidenceStatus != recovery.EvidencePublished || stored.AttemptCount != 1 {
		t.Fatalf("published occurrence = %#v", stored)
	}
	evidenceAttempts, err := repository.EvidenceAttempts(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidenceAttempts) != 2 || evidenceAttempts[0].Status != "failed" || evidenceAttempts[1].Status != "published" {
		t.Fatalf("evidence attempts = %#v", evidenceAttempts)
	}
	if strings.Contains(evidenceAttempts[0].FailureReasonRedacted, "super-secret") {
		t.Fatalf("evidence failure leaked secret: %s", evidenceAttempts[0].FailureReasonRedacted)
	}
}

func TestRecoveryPublicationSkipsTerminalNoEvidenceAndPublishesNewerSuccess(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	failed, _, err := repository.Enqueue(t.Context(), recoveryInput("failed-empty", recovery.OperationBackup, now), now)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "failure-worker", Actor: "scheduler", Now: now, Lease: time.Hour})
	if err != nil || !ok || claim.ID != failed.ID {
		t.Fatalf("claim failed occurrence = (%#v, %t, %v)", claim, ok, err)
	}
	if err := repository.Start(t.Context(), failed.ID, claim.Fence, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Fail(t.Context(), failed.ID, claim.Fence, now.Add(time.Minute), recovery.Result{}, errors.New("qualification unavailable")); err != nil {
		t.Fatal(err)
	}
	succeeded := finishRecovery(t, repository, "newer-success", recovery.OperationBackup, now.Add(2*time.Minute), true)
	publisher := &recordingRecoveryPublisher{}
	lifecycle := recovery.Lifecycle{
		Repository:  repository,
		Definitions: func(context.Context) ([]recovery.Definition, error) { return nil, nil },
		Adapters:    map[string]recovery.ScenarioAdapter{}, Publisher: publisher,
		Clock: fixedRecoveryClock{now: now.Add(4 * time.Minute)}, WorkerID: "lifecycle-worker", Actor: "scheduler",
		Lease: time.Minute, BatchSize: 10, ComplianceWindow: 30 * 24 * time.Hour, EvidenceRoot: t.TempDir(),
	}
	if err := lifecycle.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	storedFailed, err := repository.Occurrence(t.Context(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedSuccess, err := repository.Occurrence(t.Context(), succeeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedFailed.EvidenceStatus != recovery.EvidenceNone || storedSuccess.EvidenceStatus != recovery.EvidencePublished {
		t.Fatalf("publication states = failed:%s success:%s", storedFailed.EvidenceStatus, storedSuccess.EvidenceStatus)
	}
	if len(publisher.ids) != 1 || publisher.ids[0] != succeeded.ID {
		t.Fatalf("published occurrence IDs = %v", publisher.ids)
	}
}

type recordingRecoveryPublisher struct{ ids []string }

func (publisher *recordingRecoveryPublisher) Publish(_ context.Context, occurrence recovery.Occurrence) error {
	publisher.ids = append(publisher.ids, occurrence.ID)
	return nil
}

func TestRecoveryPhaseDurationsExcludeTimeOutsideOwnedPhases(t *testing.T) {
	for _, test := range []struct {
		operation string
		phase     string
	}{
		{recovery.OperationRestore, recovery.PhaseRestore},
		{recovery.OperationUpgrade, recovery.PhaseReadiness},
	} {
		t.Run(test.operation, func(t *testing.T) {
			repository, closeStore := openRecoveryRepository(t)
			defer closeStore()
			now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
			occurrence, _, err := repository.Enqueue(t.Context(), recoveryInput("phase-"+test.operation, test.operation, now), now)
			if err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "phase-worker", Actor: "scheduler", Now: now, Lease: time.Hour})
			if err != nil || !ok {
				t.Fatalf("claim = (%t, %v)", ok, err)
			}
			if err := repository.Start(t.Context(), occurrence.ID, claimed.Fence, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := repository.RecordPhase(t.Context(), occurrence.ID, claimed.Fence, test.phase, recovery.PhaseStarted, now.Add(10*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := repository.RecordPhase(t.Context(), occurrence.ID, claimed.Fence, test.phase, recovery.PhaseCompleted, now.Add(12*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := repository.Complete(t.Context(), occurrence.ID, claimed.Fence, now.Add(30*time.Second), successfulRecoveryResult(now.Add(30*time.Second), test.operation)); err != nil {
				t.Fatal(err)
			}
			stored, err := repository.Occurrence(t.Context(), occurrence.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.QualificationDurationMillis != 29_000 {
				t.Fatalf("total duration = %dms, want 29000ms", stored.QualificationDurationMillis)
			}
			if test.phase == recovery.PhaseRestore && (stored.RestoreDurationMillis != 2_000 || stored.ReadinessDurationMillis != 0) {
				t.Fatalf("restore/readiness durations = %d/%d", stored.RestoreDurationMillis, stored.ReadinessDurationMillis)
			}
			if test.phase == recovery.PhaseReadiness && (stored.ReadinessDurationMillis != 2_000 || stored.RestoreDurationMillis != 0) {
				t.Fatalf("restore/readiness durations = %d/%d", stored.RestoreDurationMillis, stored.ReadinessDurationMillis)
			}
		})
	}
}

func TestRecoveryScheduleCatchupStalenessStatusAndBoundedMetrics(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	base := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	definition := recovery.Definition{
		ScheduleID: "hourly-restore", Scenario: "managed-instance", Operation: recovery.OperationRestore,
		PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256, TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
		Cron: "0 * * * *", Timezone: "UTC", StaleAfter: time.Minute, Enabled: true,
	}
	if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
		t.Fatal(err)
	}
	due, err := (recovery.Scheduler{
		Repository: repository, Clock: fixedRecoveryClock{now: time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC)}, BatchSize: 10,
	}).EnqueueDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 3 {
		t.Fatalf("catch-up occurrences = %d, want 3", len(due))
	}
	if again, err := repository.EnqueueDue(t.Context(), time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC), 10); err != nil || len(again) != 0 {
		t.Fatalf("duplicate schedule delivery = (%d, %v)", len(again), err)
	}
	_, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{
		WorkerID: "worker-stale", Actor: "scheduler", Now: time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC), Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stale scheduled occurrence was claimed")
	}
	snapshot, err := repository.Status(t.Context(), time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Failed != 3 || snapshot.EvidencePending != 0 {
		t.Fatalf("stale status = %#v", snapshot)
	}
	for _, metric := range snapshot.Metrics() {
		for key := range metric.Labels {
			if key != "operation" && key != "state" {
				t.Fatalf("metric %s has unbounded label %s", metric.Name, key)
			}
		}
	}
}

func TestRecoveryMetricsCollectorExposesOnlyBoundedOperationAndStateLabels(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	finishRecovery(t, repository, "metrics-restore", recovery.OperationRestore, now.Add(-time.Hour), true)
	registry := prometheus.NewRegistry()
	registry.MustRegister(recovery.NewMetricsCollector(repository, fixedRecoveryClock{now: now}))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundSuccessAge := false
	for _, family := range families {
		if family.GetName() == "leapview_recovery_qualification_last_success_age_seconds" {
			foundSuccessAge = true
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() != "operation" && label.GetName() != "state" {
					t.Fatalf("metric %s has unbounded label %s", family.GetName(), label.GetName())
				}
			}
		}
	}
	if !foundSuccessAge {
		t.Fatal("last-success age metric is missing")
	}
}

func TestRecoveryRetentionPreservesComplianceWindowLatestResultsAndActiveLeases(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	oldestSuccess := finishRecovery(t, repository, "success-oldest", recovery.OperationBackup, now.Add(-90*24*time.Hour), true)
	outsideWindowSuccess := finishRecovery(t, repository, "success-outside-window", recovery.OperationBackup, now.Add(-80*24*time.Hour), true)
	oldestFailure := finishRecovery(t, repository, "failure-oldest", recovery.OperationBackup, now.Add(-70*24*time.Hour), false)
	latestFailure := finishRecovery(t, repository, "failure-latest", recovery.OperationBackup, now.Add(-60*24*time.Hour), false)
	insideWindow := finishRecovery(t, repository, "inside-window", recovery.OperationBackup, now.Add(-10*24*time.Hour), true)
	latestRestoreSuccess := finishRecovery(t, repository, "restore-latest", recovery.OperationRestore, now.Add(-50*24*time.Hour), true)
	activeInput := recoveryInput("active-lease", recovery.OperationRestore, now.Add(-time.Hour))
	activeInput.StaleAfter = 24 * time.Hour
	active, _, err := repository.Enqueue(t.Context(), activeInput, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "worker-active", Actor: "scheduler", Now: now, Lease: time.Hour}); err != nil || !ok {
		t.Fatalf("claim active occurrence = (%t, %v)", ok, err)
	}
	result, err := repository.Retain(t.Context(), recovery.RetentionPolicy{Now: now, ComplianceWindow: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DeletedIDs) != 3 || !containsString(result.DeletedIDs, oldestSuccess.ID) ||
		!containsString(result.DeletedIDs, outsideWindowSuccess.ID) || !containsString(result.DeletedIDs, oldestFailure.ID) {
		t.Fatalf("retention deleted IDs = %#v", result.DeletedIDs)
	}
	for _, preserved := range []string{latestFailure.ID, insideWindow.ID, latestRestoreSuccess.ID, active.ID} {
		if !containsString(result.PreservedIDs, preserved) {
			t.Fatalf("retention did not preserve %s: %#v", preserved, result)
		}
	}
}

func TestRecoveryStatusPassivelyDistinguishesMissingAndStaleWork(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		repository, closeStore := openRecoveryRepository(t)
		defer closeStore()
		snapshot, err := repository.Status(t.Context(), time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.Unconfigured || snapshot.ConfiguredSchedules != 0 || snapshot.MissingRuns != 0 {
			t.Fatalf("unconfigured status = %#v", snapshot)
		}
	})

	t.Run("overdue schedule without occurrence", func(t *testing.T) {
		repository, closeStore := openRecoveryRepository(t)
		defer closeStore()
		base := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
		definition := recovery.Definition{
			ScheduleID: "missing-hourly", Scenario: "managed-instance", Operation: recovery.OperationRestore,
			PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256, TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
			Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 30 * time.Minute, Enabled: true,
		}
		if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
			t.Fatal(err)
		}
		snapshot, err := repository.Status(t.Context(), time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Unconfigured || snapshot.ConfiguredSchedules != 1 || snapshot.MissingRuns != 3 || snapshot.Overdue != 2 {
			t.Fatalf("missing schedule status = %#v", snapshot)
		}
	})

	t.Run("expired execution lease", func(t *testing.T) {
		repository, closeStore := openRecoveryRepository(t)
		defer closeStore()
		now := time.Date(2026, 8, 25, 5, 0, 0, 0, time.UTC)
		_, _, err := repository.Enqueue(t.Context(), recoveryInput("crashed-worker", recovery.OperationBackup, now), now)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "crashed", Actor: "scheduler", Now: now, Lease: time.Minute}); err != nil || !ok {
			t.Fatalf("claim crashed worker = (%t, %v)", ok, err)
		}
		snapshot, err := repository.Status(t.Context(), now.Add(2*time.Minute))
		if err != nil || snapshot.StaleExecutionLeases != 1 || snapshot.Failed != 1 || snapshot.Running != 0 {
			t.Fatalf("stale execution status = %#v, %v", snapshot, err)
		}
	})

	t.Run("expired publisher lease", func(t *testing.T) {
		repository, closeStore := openRecoveryRepository(t)
		defer closeStore()
		now := time.Date(2026, 8, 25, 6, 0, 0, 0, time.UTC)
		completed := finishRecovery(t, repository, "crashed-publisher", recovery.OperationRestore, now, true)
		claimed, ok, err := repository.ClaimEvidence(t.Context(), "publisher", completed.FinishedAt.Add(time.Second), time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim publisher = (%t, %v)", ok, err)
		}
		snapshot, err := repository.Status(t.Context(), claimed.EvidenceLeaseExpiresAt.Add(time.Second))
		if err != nil || snapshot.StaleEvidenceLeases != 1 || snapshot.EvidenceFailed != 1 {
			t.Fatalf("stale evidence status = %#v, %v", snapshot, err)
		}
	})
}

func TestRecoveryLifecycleChronologyRejectsImpossibleEvidence(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)
	occurrence, _, err := repository.Enqueue(t.Context(), recoveryInput("chronology", recovery.OperationRestore, now), now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{WorkerID: "chronology-worker", Actor: "scheduler", Now: now, Lease: time.Hour})
	if err != nil || !ok {
		t.Fatalf("claim chronology = (%t, %v)", ok, err)
	}
	if err := repository.Start(t.Context(), occurrence.ID, claimed.Fence, now.Add(-time.Second)); err == nil {
		t.Fatal("start before claim was accepted")
	}
	if err := repository.Complete(t.Context(), occurrence.ID, claimed.Fence, now.Add(time.Minute), successfulRecoveryResult(now.Add(time.Minute), "restore")); err == nil {
		t.Fatal("completion before start was accepted")
	}
	started := now.Add(time.Second)
	if err := repository.Start(t.Context(), occurrence.ID, claimed.Fence, started); err != nil {
		t.Fatal(err)
	}
	futurePoint := successfulRecoveryResult(now.Add(2*time.Second), "restore")
	futurePoint.RecoveryPointAt = now.Add(3 * time.Second)
	if err := repository.Complete(t.Context(), occurrence.ID, claimed.Fence, now.Add(2*time.Second), futurePoint); err == nil {
		t.Fatal("future recovery point was accepted")
	}
	impossible := successfulRecoveryResult(now.Add(10*time.Second), "restore")
	impossible.RestoreDuration = 20 * time.Second
	if err := repository.Complete(t.Context(), occurrence.ID, claimed.Fence, now.Add(10*time.Second), impossible); err == nil {
		t.Fatal("impossible restore duration was accepted")
	}
}

func TestRecoveryScheduleCatchupDistributesLimitedBatchAcrossOwners(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	base := time.Date(2026, 8, 25, 0, 30, 0, 0, time.UTC)
	now := time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC)
	operations := []string{
		recovery.OperationBackup,
		recovery.OperationRestore,
		recovery.OperationUpgrade,
		recovery.OperationRollback,
	}
	for _, operation := range operations {
		definition := recovery.Definition{
			ScheduleID: "hourly-" + operation, Scenario: "managed-instance", Operation: operation,
			PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256,
			TargetScope: "release:candidate", ArtifactIdentity: recoveryArtifact,
			Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true,
		}
		if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
			t.Fatal(err)
		}
	}

	due, err := repository.EnqueueDue(t.Context(), now, len(operations))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != len(operations) {
		t.Fatalf("catch-up occurrences = %d, want %d", len(due), len(operations))
	}
	counts := map[string]int{}
	for _, occurrence := range due {
		counts[occurrence.Operation]++
	}
	for _, operation := range operations {
		if counts[operation] != 1 {
			t.Fatalf("catch-up occurrence count for %s = %d, want 1: %v", operation, counts[operation], counts)
		}
	}
}

func TestRecoveryScheduleCatchupDoesNotStarveRecentlyDueOwners(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	now := time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC)
	operations := []string{
		recovery.OperationBackup,
		recovery.OperationRestore,
		recovery.OperationUpgrade,
		recovery.OperationRollback,
	}
	for _, operation := range operations {
		base := now.Add(-35 * time.Minute)
		if operation == recovery.OperationBackup {
			base = now.Add(-5 * 24 * time.Hour)
		}
		definition := recovery.Definition{
			ScheduleID: "owner-" + operation, Scenario: "managed-instance", Operation: operation,
			PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256,
			TargetScope: "release:candidate", ArtifactIdentity: recoveryArtifact,
			Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 30 * 24 * time.Hour, Enabled: true,
		}
		if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
			t.Fatal(err)
		}
	}

	wantOrder := []string{
		recovery.OperationBackup,
		recovery.OperationRestore,
		recovery.OperationRollback,
		recovery.OperationUpgrade,
	}
	for pass := range 3 {
		due, err := repository.EnqueueDue(t.Context(), now.Add(time.Duration(pass)*time.Hour), len(operations))
		if err != nil {
			t.Fatal(err)
		}
		if len(due) != len(wantOrder) {
			t.Fatalf("catch-up pass %d occurrences = %d, want %d", pass, len(due), len(wantOrder))
		}
		for index, occurrence := range due {
			if occurrence.Operation != wantOrder[index] {
				t.Fatalf("catch-up pass %d operation %d = %s, want %s", pass, index, occurrence.Operation, wantOrder[index])
			}
		}
	}
}

func TestRecoveryScheduleCatchupRotatesAcrossLimitedBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(store.SQLDB())
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	now := time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC)
	scheduleIDs := []string{
		"fairness-a",
		"fairness-b",
		"fairness-c",
		"fairness-d",
		"fairness-e",
	}
	operations := []string{
		recovery.OperationBackup,
		recovery.OperationRestore,
		recovery.OperationUpgrade,
		recovery.OperationRollback,
		recovery.OperationBackup,
	}
	for index, scheduleID := range scheduleIDs {
		definition := recovery.Definition{
			ScheduleID: scheduleID, Scenario: "managed-instance", Operation: operations[index],
			PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256,
			TargetScope: "release:candidate", ArtifactIdentity: recoveryArtifact,
			Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 60 * 24 * time.Hour, Enabled: true,
		}
		if err := repository.ReconcileSchedule(t.Context(), definition, now.Add(-30*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	counts := make(map[string]int, len(scheduleIDs))
	for pass := range 10 {
		due, err := repository.EnqueueDue(t.Context(), now, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(due) != 4 {
			t.Fatalf("catch-up pass %d occurrences = %d, want 4", pass, len(due))
		}
		for _, occurrence := range due {
			counts[occurrence.ScheduleID]++
		}
		if pass == 0 {
			assertRecoveryScheduleOrder(t, due, scheduleIDs[:4])
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = platform.Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			repository = NewRepository(store.SQLDB())
		}
		if pass == 1 {
			assertRecoveryScheduleOrder(t, due, []string{"fairness-e", "fairness-a", "fairness-b", "fairness-c"})
		}
	}
	for _, scheduleID := range scheduleIDs {
		if counts[scheduleID] != 8 {
			t.Fatalf("catch-up count for %s = %d, want 8: %v", scheduleID, counts[scheduleID], counts)
		}
	}
}

func assertRecoveryScheduleOrder(t *testing.T, occurrences []recovery.Occurrence, want []string) {
	t.Helper()
	if len(occurrences) != len(want) {
		t.Fatalf("occurrence count = %d, want %d", len(occurrences), len(want))
	}
	for index, occurrence := range occurrences {
		if occurrence.ScheduleID != want[index] {
			t.Fatalf("occurrence %d schedule = %s, want %s", index, occurrence.ScheduleID, want[index])
		}
	}
}

func TestRecoveryStatusSnapshotIsConsistentDuringConcurrentEnqueue(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	base := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	now := time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC)
	definition := recovery.Definition{
		ScheduleID: "concurrent-status", Scenario: "managed-instance", Operation: recovery.OperationBackup,
		PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256, TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
		Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true,
	}
	if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		<-start
		for range 25 {
			if _, err := repository.EnqueueDue(t.Context(), now, 1); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()
	close(start)
	for range 100 {
		snapshot, err := repository.Status(t.Context(), now)
		if err != nil {
			t.Fatal(err)
		}
		pending := int64(0)
		for _, operation := range snapshot.Operations {
			pending += operation.Pending
		}
		if snapshot.Due != snapshot.MissingRuns+pending {
			t.Fatalf("status mixed SQLite snapshots: due=%d missing=%d pending=%d", snapshot.Due, snapshot.MissingRuns, pending)
		}
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryScheduleRevisionPreservesOverdueArtifactIdentity(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	base := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	definition := recovery.Definition{
		ScheduleID: "release-hourly", Scenario: "release-transition", Operation: recovery.OperationUpgrade,
		PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256, TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
		Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true,
	}
	if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
		t.Fatal(err)
	}
	artifactB := "oci://ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	definition.ArtifactIdentity = artifactB
	if err := repository.ReconcileSchedule(t.Context(), definition, time.Date(2026, 8, 25, 3, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnqueueDue(t.Context(), time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC), 10); err != nil {
		t.Fatal(err)
	}
	occurrences, err := repository.Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 3 {
		t.Fatalf("occurrences = %d, want two artifact-A misses and one artifact-B run", len(occurrences))
	}
	if occurrences[0].ArtifactIdentity != recoveryArtifact || occurrences[1].ArtifactIdentity != recoveryArtifact || occurrences[2].ArtifactIdentity != artifactB {
		t.Fatalf("artifact revision history = %#v", occurrences)
	}
	if occurrences[0].ScheduleRevision == occurrences[2].ScheduleRevision {
		t.Fatal("immutable schedule revision did not change with artifact identity")
	}
}

func TestRecoveryScheduleRevisionBindsExactPolicyDigestAtSameVersion(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	base := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	definition := recovery.Definition{
		ScheduleID: "policy-hourly", Scenario: "release-transition", Operation: recovery.OperationUpgrade,
		PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("b", 64), TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
		Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true,
	}
	if err := repository.ReconcileSchedule(t.Context(), definition, base); err != nil {
		t.Fatal(err)
	}
	definition.PolicySHA256 = strings.Repeat("c", 64)
	if err := repository.ReconcileSchedule(t.Context(), definition, time.Date(2026, 8, 25, 3, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnqueueDue(t.Context(), time.Date(2026, 8, 25, 4, 5, 0, 0, time.UTC), 10); err != nil {
		t.Fatal(err)
	}
	occurrences, err := repository.Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 3 || occurrences[0].PolicySHA256 != strings.Repeat("b", 64) ||
		occurrences[1].PolicySHA256 != strings.Repeat("b", 64) || occurrences[2].PolicySHA256 != strings.Repeat("c", 64) {
		t.Fatalf("policy revision history = %#v", occurrences)
	}
	if occurrences[0].PolicyVersion != occurrences[2].PolicyVersion || occurrences[0].ScheduleRevision == occurrences[2].ScheduleRevision {
		t.Fatal("same-version policy digests were treated as interchangeable")
	}
}

func openRecoveryRepository(t *testing.T) (*Repository, func()) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(store.SQLDB()), func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func recoveryInput(scheduleID, operation string, plannedAt time.Time) recovery.EnqueueInput {
	return recovery.EnqueueInput{
		ScheduleID: scheduleID, Scenario: "managed-instance", Operation: operation,
		PolicyVersion: "ubdr-v1", PolicySHA256: recoveryPolicySHA256, TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
		PlannedAt: plannedAt, StaleAfter: 24 * time.Hour,
	}
}

func successfulRecoveryResult(completedAt time.Time, kind string) recovery.Result {
	digestByte := "c"
	if kind == "upgrade" {
		digestByte = "d"
	}
	return recovery.Result{
		RecoveryPointAt: completedAt.Add(-15 * time.Minute),
		Evidence: []recovery.EvidenceReference{{
			Kind: kind + "-report", URI: "artifact://qualification/" + kind + ".json",
			SHA256: strings.Repeat(digestByte, 64),
		}},
	}
}

func finishRecovery(t *testing.T, repository *Repository, scheduleID, operation string, plannedAt time.Time, success bool) recovery.Occurrence {
	t.Helper()
	input := recoveryInput(scheduleID, operation, plannedAt)
	input.StaleAfter = 200 * 24 * time.Hour
	occurrence, _, err := repository.Enqueue(t.Context(), input, plannedAt)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repository.ClaimNext(t.Context(), recovery.ClaimInput{
		WorkerID: "worker-" + scheduleID, Actor: "scheduler", Now: plannedAt, Lease: time.Hour,
	})
	if err != nil || !ok {
		t.Fatalf("claim %s = (%t, %v)", scheduleID, ok, err)
	}
	if claimed.ID != occurrence.ID {
		t.Fatalf("claimed %s while completing %s", claimed.ID, occurrence.ID)
	}
	if err := repository.Start(t.Context(), occurrence.ID, claimed.Fence, plannedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	completedAt := plannedAt.Add(time.Minute)
	recordRecoveryTestPhase(t, repository, occurrence, claimed.Fence, plannedAt.Add(2*time.Second), completedAt.Add(-time.Second))
	result := successfulRecoveryResult(completedAt, operation)
	if success {
		err = repository.Complete(t.Context(), occurrence.ID, claimed.Fence, completedAt, result)
	} else {
		err = repository.Fail(t.Context(), occurrence.ID, claimed.Fence, completedAt, result, errors.New("qualification failed token=secret"))
	}
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Occurrence(t.Context(), occurrence.ID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func recordRecoveryTestPhase(t *testing.T, repository *Repository, occurrence recovery.Occurrence, fence recovery.Fence, startedAt, completedAt time.Time) {
	t.Helper()
	phase := ""
	switch occurrence.Operation {
	case recovery.OperationRestore:
		phase = recovery.PhaseRestore
	case recovery.OperationUpgrade, recovery.OperationRollback:
		phase = recovery.PhaseReadiness
	default:
		return
	}
	if err := repository.RecordPhase(t.Context(), occurrence.ID, fence, phase, recovery.PhaseStarted, startedAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordPhase(t.Context(), occurrence.ID, fence, phase, recovery.PhaseCompleted, completedAt); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type fixedRecoveryClock struct{ now time.Time }

func (clock fixedRecoveryClock) Now() time.Time { return clock.now }
