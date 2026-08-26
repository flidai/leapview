package sqlite

import (
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
	retried, ok, err := repository.ClaimEvidence(t.Context(), "publisher-two", now.Add(3*time.Minute), time.Minute)
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

func TestRecoveryScheduleCatchupStalenessStatusAndBoundedMetrics(t *testing.T) {
	repository, closeStore := openRecoveryRepository(t)
	defer closeStore()
	base := time.Date(2026, 8, 25, 1, 30, 0, 0, time.UTC)
	definition := recovery.Definition{
		ScheduleID: "hourly-restore", Scenario: "managed-instance", Operation: recovery.OperationRestore,
		PolicyVersion: "ubdr-v1", TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
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
	if snapshot.Failed != 3 || snapshot.EvidencePending != 3 {
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
		PolicyVersion: "ubdr-v1", TargetScope: "instance:prod", ArtifactIdentity: recoveryArtifact,
		PlannedAt: plannedAt, StaleAfter: 24 * time.Hour,
	}
}

func successfulRecoveryResult(completedAt time.Time, kind string) recovery.Result {
	digestByte := "c"
	if kind == "upgrade" {
		digestByte = "d"
	}
	return recovery.Result{
		RecoveryPointAt: completedAt.Add(-15 * time.Minute), RestoreDuration: 45 * time.Second,
		ReadinessDuration: 20 * time.Second,
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
