package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform"
	recovery "github.com/flidai/leapview/internal/refresh/module"
)

const evidenceSchemaVersion = 1

var immutableArtifact = "oci://ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)

type concurrentClaim struct {
	Worker          string `json:"worker"`
	Claimed         bool   `json:"claimed"`
	OccurrenceID    string `json:"occurrenceId,omitempty"`
	FenceGeneration int64  `json:"fenceGeneration,omitempty"`
	Error           string `json:"error,omitempty"`
}

type claimMatrix struct {
	SchemaVersion         int               `json:"schemaVersion"`
	IdempotentDeliveries  int               `json:"idempotentDeliveries"`
	LogicalOccurrences    int               `json:"logicalOccurrences"`
	ConcurrentClaims      []concurrentClaim `json:"concurrentClaims"`
	ValidConcurrentClaims int               `json:"validConcurrentClaims"`
	RestartReclaimed      bool              `json:"restartReclaimed"`
	StaleHeartbeatFenced  bool              `json:"staleHeartbeatFenced"`
	StaleCompletionFenced bool              `json:"staleCompletionFenced"`
	TerminalAttemptCount  int               `json:"terminalAttemptCount"`
	EvidenceRetried       bool              `json:"evidenceRetriedWithoutRerun"`
}

type attemptHistory struct {
	SchemaVersion    int                                   `json:"schemaVersion"`
	Execution        map[string][]recovery.Attempt         `json:"execution"`
	EvidenceDelivery map[string][]recovery.EvidenceAttempt `json:"evidenceDelivery"`
}

type occurrenceEvidence struct {
	SchemaVersion int                   `json:"schemaVersion"`
	GeneratedAt   time.Time             `json:"generatedAt"`
	Occurrences   []recovery.Occurrence `json:"occurrences"`
}

type retentionEvidence struct {
	SchemaVersion int                      `json:"schemaVersion"`
	BeforeIDs     []string                 `json:"beforeIds"`
	Result        recovery.RetentionResult `json:"result"`
	AfterIDs      []string                 `json:"afterIds"`
}

type metricsEvidence struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Status        recovery.StatusSnapshot `json:"status"`
	Metrics       []recovery.Metric       `json:"metrics"`
}

func main() {
	var evidenceDir string
	flag.StringVar(&evidenceDir, "evidence-dir", ".tmp/qualification/ubdr/recovery-ledger", "bounded evidence output directory")
	flag.Parse()
	if err := run(context.Background(), evidenceDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, evidenceDir string) error {
	evidenceDir, err := filepath.Abs(strings.TrimSpace(evidenceDir))
	if err != nil || strings.TrimSpace(evidenceDir) == "" {
		return fmt.Errorf("resolve evidence directory: %w", err)
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return err
	}
	workDir, err := os.MkdirTemp("", "leapview-recovery-ledger-evidence-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	databasePath := filepath.Join(workDir, "ledger.db")
	store, err := platform.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	repository := recovery.NewRecoveryRepository(store.SQLDB())
	now := time.Date(2026, 8, 25, 5, 23, 0, 0, time.UTC)
	backupInput := ledgerInput("weekly-backup", "managed-instance", recovery.OperationBackup, now)
	created := 0
	var backup recovery.Occurrence
	for range 100 {
		var wasCreated bool
		var enqueueErr error
		backup, wasCreated, enqueueErr = repository.Enqueue(ctx, backupInput, now)
		if enqueueErr != nil {
			_ = store.Close()
			return enqueueErr
		}
		if wasCreated {
			created++
		}
	}
	if created != 1 {
		_ = store.Close()
		return fmt.Errorf("idempotent enqueue created %d logical occurrences", created)
	}

	matrix := claimMatrix{SchemaVersion: evidenceSchemaVersion, IdempotentDeliveries: 100, LogicalOccurrences: created}
	claimResults := make(chan concurrentClaim, 12)
	var wait sync.WaitGroup
	for worker := range 12 {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			workerID := fmt.Sprintf("worker-%02d", worker)
			claimed, ok, claimErr := repository.ClaimNext(ctx, recovery.ClaimInput{
				WorkerID: workerID, Actor: "scheduled-qualification", Now: now, Lease: time.Minute,
			})
			result := concurrentClaim{Worker: workerID, Claimed: ok}
			if claimErr != nil {
				result.Error = recovery.RedactFailure(claimErr)
			}
			if ok {
				result.OccurrenceID = claimed.ID
				result.FenceGeneration = claimed.Fence.Generation
			}
			claimResults <- result
		}(worker)
	}
	wait.Wait()
	close(claimResults)
	var staleFence recovery.Fence
	for result := range claimResults {
		matrix.ConcurrentClaims = append(matrix.ConcurrentClaims, result)
		if result.Error != "" {
			_ = store.Close()
			return fmt.Errorf("concurrent claim failed: %s", result.Error)
		}
		if result.Claimed {
			matrix.ValidConcurrentClaims++
			staleFence = recovery.Fence{Owner: result.Worker, Generation: result.FenceGeneration}
		}
	}
	sort.Slice(matrix.ConcurrentClaims, func(i, j int) bool { return matrix.ConcurrentClaims[i].Worker < matrix.ConcurrentClaims[j].Worker })
	if matrix.ValidConcurrentClaims != 1 {
		_ = store.Close()
		return fmt.Errorf("concurrent workers produced %d valid claims", matrix.ValidConcurrentClaims)
	}
	if err := repository.Start(ctx, backup.ID, staleFence, now.Add(10*time.Second)); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}

	store, err = platform.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	repository = recovery.NewRecoveryRepository(store.SQLDB())
	reclaimed, ok, err := repository.ClaimNext(ctx, recovery.ClaimInput{
		WorkerID: "restart-worker", Actor: "scheduled-qualification", Now: now.Add(2 * time.Minute), Lease: 5 * time.Minute,
	})
	if err != nil || !ok || reclaimed.ID != backup.ID {
		return fmt.Errorf("restart did not reclaim exact occurrence: claimed=%t occurrence=%s error=%v", ok, reclaimed.ID, err)
	}
	matrix.RestartReclaimed = true
	matrix.StaleHeartbeatFenced = errors.Is(repository.Heartbeat(ctx, backup.ID, staleFence, now.Add(2*time.Minute), time.Minute), recovery.ErrFenced)
	if err := repository.Start(ctx, backup.ID, reclaimed.Fence, now.Add(2*time.Minute+time.Second)); err != nil {
		return err
	}
	backupCompleted := now.Add(3 * time.Minute)
	backupResult := ledgerResult(backupCompleted, recovery.OperationBackup)
	matrix.StaleCompletionFenced = errors.Is(repository.Complete(ctx, backup.ID, staleFence, backupCompleted, backupResult), recovery.ErrFenced)
	if err := repository.Complete(ctx, backup.ID, reclaimed.Fence, backupCompleted, backupResult); err != nil {
		return err
	}

	for index, operation := range []string{recovery.OperationRestore, recovery.OperationUpgrade, recovery.OperationRollback} {
		planned := now.Add(time.Duration(index+10) * time.Minute)
		occurrence, _, err := repository.Enqueue(ctx, ledgerInput("weekly-"+operation, "managed-instance", operation, planned), planned)
		if err != nil {
			return err
		}
		claimed, ok, err := repository.ClaimNext(ctx, recovery.ClaimInput{
			WorkerID: "worker-" + operation, Actor: "scheduled-qualification", Now: planned, Lease: 5 * time.Minute,
		})
		if err != nil || !ok || claimed.ID != occurrence.ID {
			return fmt.Errorf("claim %s qualification: claimed=%t error=%v", operation, ok, err)
		}
		if err := repository.Start(ctx, occurrence.ID, claimed.Fence, planned.Add(time.Second)); err != nil {
			return err
		}
		phase := recovery.RecoveryPhaseReadiness
		if operation == recovery.OperationRestore {
			phase = recovery.RecoveryPhaseRestore
		}
		if err := repository.RecordPhase(ctx, occurrence.ID, claimed.Fence, phase, recovery.RecoveryPhaseStarted, planned.Add(10*time.Second)); err != nil {
			return err
		}
		if err := repository.RecordPhase(ctx, occurrence.ID, claimed.Fence, phase, recovery.RecoveryPhaseCompleted, planned.Add(30*time.Second)); err != nil {
			return err
		}
		completed := planned.Add(time.Minute)
		switch operation {
		case recovery.OperationUpgrade:
			err = repository.Fail(ctx, occurrence.ID, claimed.Fence, completed, ledgerResult(completed, operation), errors.New("upgrade qualification failed token=redacted-by-ledger"))
		case recovery.OperationRollback:
			err = repository.Cancel(ctx, occurrence.ID, claimed.Fence, completed, errors.New("operator canceled rollback after controlled drill"))
		default:
			err = repository.Complete(ctx, occurrence.ID, claimed.Fence, completed, ledgerResult(completed, operation))
		}
		if err != nil {
			return err
		}
	}

	publication, ok, err := repository.ClaimEvidence(ctx, "publisher-first", now.Add(20*time.Minute), time.Minute)
	if err != nil || !ok {
		return fmt.Errorf("claim first evidence publication: %v", err)
	}
	if err := repository.FailEvidence(ctx, publication.ID, publication.EvidenceFence, now.Add(20*time.Minute+10*time.Second), errors.New("upload api_key=must-not-appear failed")); err != nil {
		return err
	}
	retry, ok, err := repository.ClaimEvidence(ctx, "publisher-retry", now.Add(21*time.Minute+11*time.Second), time.Minute)
	if err != nil || !ok || retry.ID != publication.ID {
		return fmt.Errorf("retry exact evidence publication: %v", err)
	}
	if err := repository.PublishEvidence(ctx, retry.ID, retry.EvidenceFence, now.Add(21*time.Minute+12*time.Second)); err != nil {
		return err
	}
	matrix.EvidenceRetried = true
	for {
		item, ok, err := repository.ClaimEvidence(ctx, "publisher-drain", now.Add(22*time.Minute), time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := repository.PublishEvidence(ctx, item.ID, item.EvidenceFence, now.Add(22*time.Minute+time.Second)); err != nil {
			return err
		}
	}

	stalePlanned := now.Add(30 * time.Minute)
	stale, _, err := repository.Enqueue(ctx, recovery.EnqueueInput{
		ScheduleID: "overdue-restore", Scenario: "managed-instance", Operation: recovery.OperationRestore,
		PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("c", 64), TargetScope: "instance:prod", ArtifactIdentity: immutableArtifact,
		PlannedAt: stalePlanned, StaleAfter: time.Minute,
	}, stalePlanned)
	if err != nil {
		return err
	}
	if _, ok, err := repository.ClaimNext(ctx, recovery.ClaimInput{WorkerID: "stale-probe", Actor: "scheduler", Now: stalePlanned.Add(2 * time.Minute), Lease: time.Minute}); err != nil || ok {
		return fmt.Errorf("stale occurrence claim: claimed=%t error=%v", ok, err)
	}
	if stored, err := repository.Occurrence(ctx, stale.ID); err != nil || stored.Status != recovery.StatusExpired {
		return fmt.Errorf("stale occurrence was not detectable: status=%s error=%v", stored.Status, err)
	}

	oldest, err := finishLedgerOccurrence(ctx, repository, "retention-oldest", "retention-scenario", now.Add(-100*24*time.Hour))
	if err != nil {
		return err
	}
	newest, err := finishLedgerOccurrence(ctx, repository, "retention-newest", "retention-scenario", now.Add(-90*24*time.Hour))
	if err != nil {
		return err
	}
	before, err := repository.Occurrences(ctx)
	if err != nil {
		return err
	}
	history := attemptHistory{SchemaVersion: evidenceSchemaVersion, Execution: map[string][]recovery.Attempt{}, EvidenceDelivery: map[string][]recovery.EvidenceAttempt{}}
	for _, occurrence := range before {
		history.Execution[occurrence.ID], err = repository.Attempts(ctx, occurrence.ID)
		if err != nil {
			return err
		}
		history.EvidenceDelivery[occurrence.ID], err = repository.EvidenceAttempts(ctx, occurrence.ID)
		if err != nil {
			return err
		}
	}
	matrix.TerminalAttemptCount = len(history.Execution[backup.ID])
	if matrix.TerminalAttemptCount != 2 || !matrix.StaleHeartbeatFenced || !matrix.StaleCompletionFenced {
		return fmt.Errorf("crash/restart fencing evidence is incomplete: %#v", matrix)
	}
	retention, err := repository.Retain(ctx, recovery.RetentionPolicy{Now: now.Add(24 * time.Hour), ComplianceWindow: 30 * 24 * time.Hour})
	if err != nil {
		return err
	}
	if !contains(retention.DeletedIDs, oldest.ID) || !contains(retention.PreservedIDs, newest.ID) {
		return fmt.Errorf("retention did not delete oldest and preserve latest result: %#v", retention)
	}
	after, err := repository.Occurrences(ctx)
	if err != nil {
		return err
	}
	status, err := repository.Status(ctx, now.Add(24*time.Hour))
	if err != nil {
		return err
	}
	if status.Failed == 0 || status.RecoveredExpiredLeases == 0 {
		return fmt.Errorf("stale, failed, or reclaimed recovery evidence is not detectable: %#v", status)
	}

	return writeEvidenceSet(evidenceDir,
		struct {
			name  string
			value any
		}{"occurrences.json", occurrenceEvidence{SchemaVersion: evidenceSchemaVersion, GeneratedAt: now.Add(24 * time.Hour), Occurrences: after}},
		struct {
			name  string
			value any
		}{"attempt-history.json", history},
		struct {
			name  string
			value any
		}{"crash-restart-concurrent-claim-matrix.json", matrix},
		struct {
			name  string
			value any
		}{"retention-before-after.json", retentionEvidence{SchemaVersion: evidenceSchemaVersion, BeforeIDs: occurrenceIDs(before), Result: retention, AfterIDs: occurrenceIDs(after)}},
		struct {
			name  string
			value any
		}{"metrics-snapshot.json", metricsEvidence{SchemaVersion: evidenceSchemaVersion, Status: status, Metrics: status.Metrics()}},
	)
}

func finishLedgerOccurrence(ctx context.Context, repository recovery.RecoveryRepository, scheduleID, scenario string, planned time.Time) (recovery.Occurrence, error) {
	input := ledgerInput(scheduleID, scenario, recovery.OperationBackup, planned)
	input.StaleAfter = 200 * 24 * time.Hour
	occurrence, _, err := repository.Enqueue(ctx, input, planned)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	claimed, ok, err := repository.ClaimNext(ctx, recovery.ClaimInput{WorkerID: "worker-" + scheduleID, Actor: "scheduler", Now: planned, Lease: time.Hour})
	if err != nil || !ok || claimed.ID != occurrence.ID {
		return recovery.Occurrence{}, fmt.Errorf("claim retention occurrence: claimed=%t error=%v", ok, err)
	}
	if err := repository.Start(ctx, occurrence.ID, claimed.Fence, planned.Add(time.Second)); err != nil {
		return recovery.Occurrence{}, err
	}
	completed := planned.Add(time.Minute)
	if err := repository.Complete(ctx, occurrence.ID, claimed.Fence, completed, ledgerResult(completed, recovery.OperationBackup)); err != nil {
		return recovery.Occurrence{}, err
	}
	stored, err := repository.Occurrence(ctx, occurrence.ID)
	if err != nil {
		return recovery.Occurrence{}, err
	}
	return stored, nil
}

func ledgerInput(scheduleID, scenario, operation string, planned time.Time) recovery.EnqueueInput {
	return recovery.EnqueueInput{
		ScheduleID: scheduleID, Scenario: scenario, Operation: operation,
		PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("c", 64), TargetScope: "instance:prod",
		ArtifactIdentity: immutableArtifact, PlannedAt: planned, StaleAfter: 24 * time.Hour,
	}
}

func ledgerResult(completed time.Time, operation string) recovery.Result {
	return recovery.Result{
		RecoveryPointAt: completed.Add(-15 * time.Minute),
		Evidence: []recovery.EvidenceReference{{
			Kind: operation + "-report", URI: "artifact://qualification/" + operation + ".json",
			SHA256: strings.Repeat("b", 64),
		}},
	}
}

func occurrenceIDs(values []recovery.Occurrence) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	sort.Strings(ids)
	return ids
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func writeEvidenceSet(evidenceDir string, values ...struct {
	name  string
	value any
}) error {
	for _, value := range values {
		encoded, err := json.MarshalIndent(value.value, "", "  ")
		if err != nil {
			return err
		}
		encoded = append(encoded, '\n')
		path := filepath.Join(evidenceDir, value.name)
		temporary, err := os.CreateTemp(evidenceDir, ".recovery-ledger-evidence-*")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		if err := temporary.Chmod(0o600); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		if _, err := temporary.Write(encoded); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Sync(); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
	}
	return nil
}
