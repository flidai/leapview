package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

func TestRecoveryLifecycleRunsDurableQualificationLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "platform.db")
	store, err := platform.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 4, 5, 0, 0, time.UTC)
	artifact := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	evidence := writeRecoveryLifecycleEvidence(t, t.TempDir(), artifact, now)
	retainedEvidence := filepath.Join(t.TempDir(), "retained")
	definitions := recoveryLifecycleDefinitions(artifact)
	repository := refreshmodule.NewSQLiteRecoveryRepository(store.SQLDB())
	for _, definition := range definitions {
		if err := repository.ReconcileSchedule(ctx, definition, now.Add(-35*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.EnqueueDue(ctx, now.Add(-3*time.Minute), 10); err != nil {
		t.Fatal(err)
	}
	interrupted, ok, err := repository.ClaimNext(ctx, refreshmodule.ClaimInput{
		WorkerID: "interrupted-worker", Actor: "scheduled-qualification", Now: now.Add(-3 * time.Minute), Lease: time.Minute,
	})
	if err != nil || !ok {
		t.Fatalf("claim interrupted occurrence = (%t, %v)", ok, err)
	}
	if err := repository.Start(ctx, interrupted.ID, interrupted.Fence, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = platform.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository = refreshmodule.NewSQLiteRecoveryRepository(store.SQLDB())

	adapters := map[string]refreshmodule.RecoveryScenarioAdapter{}
	for operation, artifactOutputs := range map[string][]refreshmodule.RecoveryEvidenceArtifact{
		refreshmodule.OperationUpgrade: {
			{Kind: refreshmodule.EvidenceTransitionQualification, Path: evidence.transition},
		},
		refreshmodule.OperationRollback: {
			{Kind: refreshmodule.EvidenceTransitionQualification, Path: evidence.transition},
		},
	} {
		operation := operation
		outputs := artifactOutputs
		adapters[operation] = refreshmodule.RecoveryScenarioAdapterFunc(func(ctx context.Context, _ refreshmodule.Occurrence) (refreshmodule.RecoveryScenarioOutcome, error) {
			phase := ""
			if operation == refreshmodule.OperationUpgrade || operation == refreshmodule.OperationRollback {
				phase = refreshmodule.RecoveryPhaseReadiness
			}
			if phase != "" {
				if err := refreshmodule.RecordRecoveryQualificationPhase(ctx, phase, refreshmodule.RecoveryPhaseStarted); err != nil {
					return refreshmodule.RecoveryScenarioOutcome{}, err
				}
				if err := refreshmodule.RecordRecoveryQualificationPhase(ctx, phase, refreshmodule.RecoveryPhaseCompleted); err != nil {
					return refreshmodule.RecoveryScenarioOutcome{}, err
				}
			}
			return refreshmodule.RecoveryScenarioOutcome{Artifacts: outputs}, nil
		})
	}
	lifecycle := &refreshmodule.RecoveryLifecycle{
		Definitions: func(context.Context) ([]refreshmodule.RecoveryDefinition, error) { return definitions, nil },
		Adapters:    adapters, Publisher: refreshmodule.RecoveryFileEvidencePublisher{Root: retainedEvidence},
		Clock: recoveryLifecycleClock{now: now}, WorkerID: "production-recovery-worker", Actor: "scheduled-qualification",
		Lease: 5 * time.Minute, BatchSize: 10, ComplianceWindow: 90 * 24 * time.Hour, EvidenceRoot: retainedEvidence,
	}
	server, err := assembleRuntimeChecked(ctx, nil, testStoreOptions(store, assemblyConfig{
		RecoveryLifecycle: lifecycle, RecoveryInterval: time.Hour, JobLeaseTimeout: time.Minute,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.StartBackgroundJobs(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.StopBackgroundJobs(context.Background())

	deadline := time.Now().Add(10 * time.Second)
	for {
		occurrences, readErr := repository.Occurrences(ctx)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(occurrences) == 2 && allRecoveryEvidencePublished(occurrences) {
			for _, occurrence := range occurrences {
				if occurrence.ArtifactIdentity != artifact || len(occurrence.Evidence) != 1 {
					t.Fatalf("production occurrence evidence = %#v", occurrence)
				}
				for _, reference := range occurrence.Evidence {
					if reference.SHA256 == "" || !strings.HasPrefix(reference.URI, "artifact://qualification/") || strings.Contains(reference.URI, filepath.Dir(evidence.transition)) {
						t.Fatalf("production occurrence evidence = %#v", occurrence)
					}
				}
			}
			attempts, attemptErr := repository.Attempts(ctx, interrupted.ID)
			if attemptErr != nil {
				t.Fatal(attemptErr)
			}
			if len(attempts) != 2 || attempts[0].Status != "abandoned" || attempts[1].Status != "succeeded" {
				t.Fatalf("restart attempt history = %#v", attempts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("production recovery lifecycle did not finish: %#v", occurrences)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type recoveryLifecycleEvidence struct{ transition string }

func writeRecoveryLifecycleEvidence(t *testing.T, root, artifact string, now time.Time) recoveryLifecycleEvidence {
	t.Helper()
	write := func(name string, value any) string {
		t.Helper()
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	candidate := compatibility.ReleaseIdentity{
		ReleaseID: "v0.3.0", Version: "0.3.0", SourceRevision: strings.Repeat("b", 40),
		Image: artifact, Distribution: "public", Platform: "linux/amd64",
	}
	predecessor := compatibility.ReleaseIdentity{
		ReleaseID: "v0.2.0", Version: "0.2.0", SourceRevision: strings.Repeat("8", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("9", 64), Distribution: "public", Platform: "linux/amd64",
	}
	state := map[string]string{
		"instanceId": "lvinst_recovery_lifecycle", "environment": "qualification", "canonicalOrigin": "https://qualification.example",
		"principalId": "principal_qualification", "principalKind": "user", "principalEmail": "qualification@example.com", "principalDisplayName": "Qualification",
	}
	transition := write("transition-qualification.json", map[string]any{
		"schemaVersion": 1, "policyVersion": "ubdr-v1", "recoveryPointAt": now.Add(-20 * time.Minute), "predecessor": predecessor, "candidate": candidate,
		"policySha256": strings.Repeat("c", 64), "stateBeforeUpgradeSha256": strings.Repeat("d", 64),
		"stateAfterUpgradeSha256": strings.Repeat("d", 64), "stateAfterRollbackSha256": strings.Repeat("d", 64),
		"inventoryBeforeUpgrade": state, "inventoryAfterUpgrade": state, "inventoryAfterRollback": state,
		"upgradeResult": "success", "rollbackResult": "success", "preservationVerified": true,
	})
	return recoveryLifecycleEvidence{transition: transition}
}

func recoveryLifecycleDefinitions(artifact string) []refreshmodule.RecoveryDefinition {
	definitions := make([]refreshmodule.RecoveryDefinition, 0, 2)
	for _, operation := range []string{refreshmodule.OperationUpgrade, refreshmodule.OperationRollback} {
		definitions = append(definitions, refreshmodule.RecoveryDefinition{
			ScheduleID: "production-" + operation, Scenario: "ubdr-foundation", Operation: operation,
			PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("c", 64), TargetScope: "release:v0.3.0", ArtifactIdentity: artifact,
			Cron: "0 * * * *", Timezone: "UTC", StaleAfter: 24 * time.Hour, Enabled: true,
		})
	}
	return definitions
}

func allRecoveryEvidencePublished(values []refreshmodule.Occurrence) bool {
	for _, value := range values {
		if value.Status != refreshmodule.StatusSucceeded || value.EvidenceStatus != "published" {
			return false
		}
	}
	return true
}

type recoveryLifecycleClock struct{ now time.Time }

func (clock recoveryLifecycleClock) Now() time.Time { return clock.now }
