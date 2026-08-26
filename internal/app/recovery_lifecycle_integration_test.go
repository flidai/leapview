package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/compatibility"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

func TestProductionCompositionConfiguresRecoveryOwnersWithoutLifecycleInjection(t *testing.T) {
	policy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	release, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		t.Fatal("embedded candidate release is missing")
	}
	identity := release.IdentityForPlatform(runtime.GOOS + "/" + runtime.GOARCH)
	root := t.TempDir()
	cfg := config.Config{
		Production: true, HomeDir: filepath.Join(root, "instance"), Environment: "qualification",
		Image: identity.Image, ManagedDataBackend: "local", RecoveryQualificationEnabled: true,
		RecoveryQualificationController: filepath.Join(root, "leapviewctl"),
		RecoveryQualificationBundle:     filepath.Join(root, "bundle"),
		RecoveryQualificationWorkDir:    filepath.Join(root, "work"), RecoveryQualificationCron: "@hourly",
	}
	lifecycle, err := productionRecoveryLifecycle(cfg, buildinfo.Identity{
		Version: identity.Version, Revision: identity.SourceRevision, BuildTime: "2026-08-26T00:00:00Z",
	}, "qualification", "lvinst_production_composition")
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle == nil || len(lifecycle.Adapters) != 4 || lifecycle.Definitions == nil {
		t.Fatalf("normal production composition did not register owner lifecycle: %#v", lifecycle)
	}
}

func TestProductionCompositionRetainsValidS3BackupRestoreEvidence(t *testing.T) {
	policy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	release, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		t.Fatal("embedded candidate release is missing")
	}
	identity := release.IdentityForPlatform(runtime.GOOS + "/" + runtime.GOARCH)
	root := t.TempDir()
	home := filepath.Join(root, "instance")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.BindInstanceEnvironment(t.Context(), "qualification"); err != nil {
		t.Fatal(err)
	}
	instanceID, err := store.InstanceID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pointsPath := filepath.Join(root, "external-recovery-points.json")
	evidencePath := filepath.Join(root, "external-evidence.json")
	if err := os.WriteFile(pointsPath, []byte(`[{"role":"managed-data","recoveryPoint":"version-42","evidenceKey":"managed-data-version"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, []byte(`{"managed-data-version":"version-42"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Production: true, HomeDir: home, Environment: "qualification", Image: identity.Image,
		ManagedDataBackend: "s3", ManagedDataS3Endpoint: "https://s3.example.com", ManagedDataS3Region: "eu-west-1",
		ManagedDataS3Bucket: "production-data", ManagedDataS3Prefix: "tenant/managed-data",
		RecoveryQualificationEnabled: true, RecoveryQualificationController: filepath.Join(root, "leapviewctl"),
		RecoveryQualificationBundle: filepath.Join(root, "bundle"), RecoveryQualificationWorkDir: filepath.Join(root, "work"),
		RecoveryQualificationCron: "@hourly", RecoveryQualificationExternalRecoveryPoints: pointsPath,
		RecoveryQualificationExternalEvidence: evidencePath,
	}
	lifecycle, err := productionRecoveryLifecycle(cfg, buildinfo.Identity{
		Version: identity.Version, Revision: identity.SourceRevision, BuildTime: "2026-08-26T00:00:00Z",
	}, "qualification", instanceID)
	if err != nil {
		t.Fatal(err)
	}
	allDefinitions := lifecycle.Definitions
	lifecycle.Definitions = func(ctx context.Context) ([]refreshmodule.RecoveryDefinition, error) {
		definitions, err := allDefinitions(ctx)
		if err != nil {
			return nil, err
		}
		return definitions[:2], nil
	}
	now := time.Now().UTC().Add(time.Minute)
	lifecycle.Clock = recoveryLifecycleClock{now: now}
	lifecycle = refreshmodule.NewRecoveryLifecycle(store.SQLDB(), *lifecycle)
	definitions, err := lifecycle.Definitions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if err := lifecycle.Repository.ReconcileSchedule(t.Context(), definition, now.Add(-65*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := lifecycle.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	occurrences, err := lifecycle.Repository.Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 2 {
		t.Fatalf("S3 qualification occurrences = %d, want backup and restore", len(occurrences))
	}
	for _, occurrence := range occurrences {
		if occurrence.Status != refreshmodule.StatusSucceeded || occurrence.EvidenceStatus != "published" {
			t.Fatalf("S3 qualification did not publish retained evidence: %#v", occurrence)
		}
		for _, reference := range occurrence.Evidence {
			document, err := os.ReadFile(filepath.Join(lifecycle.EvidenceRoot, reference.SHA256+".json"))
			if err != nil {
				t.Fatal(err)
			}
			switch reference.Kind {
			case refreshmodule.EvidenceBackupManifestV2:
				manifest, err := platform.ValidateInstanceBackupManifestDocument(document, platform.InstanceBackupEvidenceExpectation{
					ArtifactIdentity: identity.Image, PolicyVersion: policy.PolicyVersion,
					PolicySHA256: occurrence.PolicySHA256, TargetScope: "instance:" + instanceID,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(manifest.StorageTopology.ExternalStores) != 1 {
					t.Fatalf("S3 manifest topology = %#v", manifest.StorageTopology)
				}
				external := manifest.StorageTopology.ExternalStores[0]
				if external.Provider != "s3-compatible" || external.Endpoint != "https://s3.example.com" || external.Region != "eu-west-1" ||
					external.Bucket != "production-data" || external.Prefix != "tenant/managed-data" ||
					external.RecoveryPoint != "version-42" || external.EvidenceKey != "managed-data-version" {
					t.Fatalf("S3 manifest external identity = %#v", external)
				}
			case refreshmodule.EvidenceRestorePreflight:
				var plan platform.InstanceRestorePreflightPlan
				if err := json.Unmarshal(document, &plan); err != nil {
					t.Fatal(err)
				}
				if !plan.Allowed || len(plan.ExternalPrerequisites) != 1 || plan.ExternalPrerequisites[0].RecoveryPoint != "version-42" {
					t.Fatalf("S3 restore preflight evidence = %#v", plan)
				}
			}
		}
	}
}

func TestProductionCompositionRunsDurableRecoveryQualificationLifecycle(t *testing.T) {
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
	repository := refreshmodule.NewRecoveryRepository(store.SQLDB())
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
	repository = refreshmodule.NewRecoveryRepository(store.SQLDB())

	adapters := map[string]refreshmodule.RecoveryScenarioAdapter{}
	for operation, artifactOutputs := range map[string][]refreshmodule.RecoveryEvidenceArtifact{
		refreshmodule.OperationBackup: {
			{Kind: refreshmodule.EvidenceBackupManifestV2, Path: evidence.backup},
		},
		refreshmodule.OperationRestore: {
			{Kind: refreshmodule.EvidenceBackupManifestV2, Path: evidence.backup},
			{Kind: refreshmodule.EvidenceRestorePreflight, Path: evidence.restore},
		},
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
			if operation == refreshmodule.OperationRestore {
				phase = refreshmodule.RecoveryPhaseRestore
			} else if operation == refreshmodule.OperationUpgrade || operation == refreshmodule.OperationRollback {
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
		if len(occurrences) == 4 && allRecoveryEvidencePublished(occurrences) {
			for _, occurrence := range occurrences {
				expectedEvidence := 1
				if occurrence.Operation == refreshmodule.OperationRestore {
					expectedEvidence = 2
				}
				if occurrence.ArtifactIdentity != artifact || len(occurrence.Evidence) != expectedEvidence {
					t.Fatalf("production occurrence evidence = %#v", occurrence)
				}
				for _, reference := range occurrence.Evidence {
					if reference.SHA256 == "" || !strings.HasPrefix(reference.URI, "artifact://qualification/") || strings.Contains(reference.URI, filepath.Dir(evidence.backup)) {
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

type recoveryLifecycleEvidence struct{ transition, backup, restore string }

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
	members := []platform.InstanceBackupMember{{Path: "leapview.db", Role: "control-plane-database", Size: 1, Mode: 0o600, SHA256: strings.Repeat("e", 64)}}
	inventoryDocument, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	inventoryDigest := sha256.Sum256(inventoryDocument)
	manifest := platform.InstanceBackupManifestV2{
		SchemaVersion: platform.InstanceBackupManifestVersion, Kind: "leapview-instance", BackupID: "lvbackup_recovery_lifecycle",
		ReleaseIdentity: candidate, InstanceID: "lvinst_recovery_lifecycle", Environment: "qualification",
		CreatedAt: now.Add(-20 * time.Minute), CompletedAt: now.Add(-19 * time.Minute), ArchiveMode: "full-instance",
		StorageTopology:                 platform.InstanceBackupStorageTopology{ControlPlane: "local", ManagedData: "local", DuckLake: "local", ExternalStores: []platform.InstanceBackupExternalStoreReference{}},
		RequiredTransitionPolicyVersion: "ubdr-v1", Members: members,
		RequiredTransitionPolicySHA256: strings.Repeat("c", 64),
		InventorySHA256:                fmt.Sprintf("%x", inventoryDigest[:]),
	}
	backup := write("leapview-backup.json", manifest)
	manifestDocument, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestDocument)
	restore := write("preflight-report.json", platform.InstanceRestorePreflightPlan{
		SchemaVersion: 1, Allowed: true, ReasonCode: platform.RestorePreflightAllowed,
		BackupID: manifest.BackupID, ManifestVersion: platform.InstanceBackupManifestVersion,
		ManifestSHA256: fmt.Sprintf("%x", manifestDigest[:]), PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("c", 64), ArchiveSHA256: strings.Repeat("2", 64),
		TargetTreeSHA256: strings.Repeat("3", 64), Environment: "qualification", ArchiveRelease: candidate, TargetRelease: candidate,
		TargetStorageTopology: manifest.StorageTopology, CheckpointTopology: platform.InstanceBackupStorageTopology{ControlPlane: "local", ManagedData: "local", DuckLake: "local", ExternalStores: []platform.InstanceBackupExternalStoreReference{}},
		ExternalPrerequisites: []platform.InstanceBackupExternalStoreReference{}, ExclusiveLockVerified: true,
		Replace: []string{"leapview.db"}, Preserve: []string{}, Reset: []string{},
	})
	return recoveryLifecycleEvidence{transition: transition, backup: backup, restore: restore}
}

func recoveryLifecycleDefinitions(artifact string) []refreshmodule.RecoveryDefinition {
	definitions := make([]refreshmodule.RecoveryDefinition, 0, 4)
	for _, operation := range []string{refreshmodule.OperationBackup, refreshmodule.OperationRestore, refreshmodule.OperationUpgrade, refreshmodule.OperationRollback} {
		targetScope := "instance:lvinst_recovery_lifecycle"
		if operation == refreshmodule.OperationUpgrade || operation == refreshmodule.OperationRollback {
			targetScope = "release:v0.3.0"
		}
		definitions = append(definitions, refreshmodule.RecoveryDefinition{
			ScheduleID: "production-" + operation, Scenario: "ubdr-foundation", Operation: operation,
			PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("c", 64), TargetScope: targetScope, ArtifactIdentity: artifact,
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
