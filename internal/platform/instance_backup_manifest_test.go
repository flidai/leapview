package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestBackupManifestV2InventoriesMembersDeterministically(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "home")
	dbPath := filepath.Join(home, instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "managed-data", "objects", "blob"), "managed")
	writeTestFile(t, filepath.Join(home, "ducklake", "data", "part.parquet"), "ducklake")

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	createdAt := time.Date(2026, time.August, 25, 5, 0, 0, 0, time.UTC)
	identity := testBackupReleaseIdentity("1.2.3", "a")
	policySHA256 := strings.Repeat("b", 64)
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: archivePath,
		BackupID: "backup_test", Now: func() time.Time { return createdAt },
		ReleaseIdentity: identity, TransitionPolicySHA256: policySHA256,
	}); err != nil {
		t.Fatal(err)
	}

	entries := readTarGzEntries(t, archivePath)
	var manifest InstanceBackupManifestV2
	if err := json.Unmarshal(entries[instanceBackupManifestName], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != InstanceBackupManifestVersion || manifest.BackupID != "backup_test" ||
		manifest.CreatedAt != createdAt || manifest.CompletedAt != createdAt || manifest.Environment != "prod" ||
		manifest.ReleaseIdentity != identity || manifest.RequiredTransitionPolicyVersion != "ubdr/v1" ||
		manifest.RequiredTransitionPolicySHA256 != policySHA256 {
		t.Fatalf("manifest = %#v", manifest)
	}
	paths := make([]string, 0, len(manifest.Members))
	for _, member := range manifest.Members {
		paths = append(paths, member.Path)
		body, ok := entries[member.Path]
		if !ok {
			t.Fatalf("manifest member %q is absent from archive", member.Path)
		}
		digest := sha256.Sum256(body)
		if member.Size != int64(len(body)) || member.SHA256 != hex.EncodeToString(digest[:]) || member.Mode == 0 || member.Role == "" {
			t.Fatalf("member %q = %#v", member.Path, member)
		}
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("member paths are not sorted: %v", paths)
	}
	if manifest.InventorySHA256 == "" {
		t.Fatal("manifest inventory checksum is empty")
	}
	if _, err := ValidateInstanceBackupManifestDocument(entries[instanceBackupManifestName], InstanceBackupEvidenceExpectation{
		ArtifactIdentity: identity.Image, PolicyVersion: "ubdr/v1", PolicySHA256: strings.Repeat("c", 64),
	}); err == nil {
		t.Fatal("backup evidence from a different same-version policy was accepted")
	}
	secondArchive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: secondArchive,
		BackupID: "backup_test", Now: func() time.Time { return createdAt }, ReleaseIdentity: identity,
		TransitionPolicySHA256: policySHA256,
	}); err != nil {
		t.Fatal(err)
	}
	secondEntries := readTarGzEntries(t, secondArchive)
	if string(secondEntries[instanceBackupManifestName]) != string(entries[instanceBackupManifestName]) {
		t.Fatalf("equivalent inputs produced different manifests:\nfirst=%s\nsecond=%s", entries[instanceBackupManifestName], secondEntries[instanceBackupManifestName])
	}
}

func TestBackupManifestV2HashesTheBytesWrittenToTheArchive(t *testing.T) {
	ctx := context.Background()
	archivePath, _ := createManifestV2TestArchive(t, ctx, "prod")
	manifest := readBackupManifestFromArchive(t, archivePath)
	entries := readTarGzEntries(t, archivePath)
	memberDir := t.TempDir()
	staged := make([]instanceBackupStagedMember, 0, len(manifest.Members))
	for index, member := range manifest.Members {
		source := filepath.Join(memberDir, fmt.Sprintf("member-%d", index))
		if err := os.WriteFile(source, entries[member.Path], os.FileMode(member.Mode)); err != nil {
			t.Fatal(err)
		}
		staged = append(staged, instanceBackupStagedMember{manifest: member, source: source})
	}
	for _, member := range staged {
		if member.manifest.Path == "artifacts/release.tar.gz" {
			if err := os.WriteFile(member.source, []byte("tampered"), os.FileMode(member.manifest.Mode)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var output bytes.Buffer
	err := writeManifestV2Archive(&output, manifest, staged)
	if err == nil || !strings.Contains(err.Error(), "changed while writing archive") {
		t.Fatalf("archive write error = %v", err)
	}
}

func TestBackupMemberRoleClassifiesDuckLakeCatalogFiles(t *testing.T) {
	for _, name := range []string{"ducklake/catalog", "ducklake/catalog/catalog.duckdb", "ducklake/catalog.duckdb"} {
		if got := backupMemberRole(name); got != "ducklake-catalog" {
			t.Fatalf("backupMemberRole(%q) = %q", name, got)
		}
	}
}

func TestRestorePreflightRejectsBeforeTargetMutation(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	createCurrentInstanceState(t, ctx, target, "staging")
	writeTestFile(t, filepath.Join(target, "preserve.txt"), "unchanged")
	before := hashTestTree(t, target)

	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "staging",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
		CurrentBackupOut: filepath.Join(t.TempDir(), "before.tar.gz"),
	})
	if err == nil || plan.ReasonCode != RestorePreflightWrongEnvironment {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if after := hashTestTree(t, target); after != before {
		t.Fatalf("preflight mutated target: before=%s after=%s", before, after)
	}
}

func TestRestorePreflightDetectsMemberChecksumAndStaleTarget(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	createCurrentInstanceState(t, ctx, target, "prod")
	writeTestFile(t, filepath.Join(target, "state.txt"), "before")
	options := InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
		CurrentBackupOut: filepath.Join(t.TempDir(), "before.tar.gz"),
	}
	plan, err := PreflightInstanceRestore(ctx, options)
	if err != nil || !plan.Allowed || plan.ArchiveSHA256 == "" || plan.TargetTreeSHA256 == "" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	wantArchiveSHA, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ArchiveSHA256 != wantArchiveSHA || plan.ManifestSHA256 == "" {
		t.Fatalf("archive sha=%q manifest sha=%q want archive=%q", plan.ArchiveSHA256, plan.ManifestSHA256, wantArchiveSHA)
	}
	writeTestFile(t, filepath.Join(target, "state.txt"), "changed")
	err = RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: archivePath, CurrentBackupOut: filepath.Join(t.TempDir(), "before.tar.gz"),
		ExpectedEnvironment: "prod", TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
		ValidatedPlan: &plan,
	})
	var preflightErr *InstanceRestorePreflightError
	if err == nil || !strings.Contains(err.Error(), RestorePreflightStaleTarget) || !errors.As(err, &preflightErr) {
		t.Fatalf("restore error = %v", err)
	}
}

func TestRestorePreflightAndRestoreShareDestinationValidation(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	writeTestFile(t, filepath.Join(target, "state.txt"), "unchanged")
	unsafeCurrentBackup := filepath.Join(target, "before.tar.gz")
	options := InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, CurrentBackupOut: unsafeCurrentBackup,
	}
	plan, preflightErr := PreflightInstanceRestore(ctx, options)
	if preflightErr == nil || plan.ReasonCode != RestorePreflightArchiveInvalid || !strings.Contains(preflightErr.Error(), "must not be inside") {
		t.Fatalf("preflight plan=%#v err=%v", plan, preflightErr)
	}
	restoreErr := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: archivePath, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, CurrentBackupOut: unsafeCurrentBackup,
	})
	if restoreErr == nil || !strings.Contains(restoreErr.Error(), "must not be inside") {
		t.Fatalf("restore error = %v", restoreErr)
	}
	if got := readTestFile(t, filepath.Join(target, "state.txt")); got != "unchanged" {
		t.Fatalf("restore mutated rejected target: %q", got)
	}
}

func TestRestorePreflightAndRestoreShareArchiveAndSymlinkValidation(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	tests := []struct {
		name  string
		setup func(*testing.T) (string, string, string)
		want  string
	}{
		{name: "archive inside target", want: "must not be inside", setup: func(t *testing.T) (string, string, string) {
			target := filepath.Join(t.TempDir(), "target")
			inside := filepath.Join(target, "backup.tar.gz")
			writeTestBytes(t, inside, readTestBytes(t, archivePath))
			return target, inside, filepath.Join(t.TempDir(), "current.tar.gz")
		}},
		{name: "archive symlink", want: "archive must not contain a symlink", setup: func(t *testing.T) (string, string, string) {
			link := filepath.Join(t.TempDir(), "backup.tar.gz")
			if err := os.Symlink(archivePath, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return filepath.Join(t.TempDir(), "target"), link, ""
		}},
		{name: "checkpoint symlink", want: "current instance backup path must not contain a symlink", setup: func(t *testing.T) (string, string, string) {
			target := filepath.Join(t.TempDir(), "target")
			createCurrentInstanceState(t, ctx, target, "prod")
			checkpointTarget := filepath.Join(t.TempDir(), "existing.tar.gz")
			writeTestFile(t, checkpointTarget, "existing")
			link := filepath.Join(t.TempDir(), "current.tar.gz")
			if err := os.Symlink(checkpointTarget, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return target, archivePath, link
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, archive, checkpoint := test.setup(t)
			plan, preflightErr := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
				ArchivePath: archive, TargetHomeDir: target, CurrentBackupOut: checkpoint,
				ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
			})
			if preflightErr == nil || plan.ReasonCode != RestorePreflightArchiveInvalid || !strings.Contains(preflightErr.Error(), test.want) {
				t.Fatalf("preflight plan=%#v err=%v", plan, preflightErr)
			}
			restoreErr := RestoreInstance(ctx, InstanceRestoreOptions{
				TargetHomeDir: target, BackupPath: archive, CurrentBackupOut: checkpoint,
				ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
			})
			if restoreErr == nil || !strings.Contains(restoreErr.Error(), test.want) {
				t.Fatalf("restore error = %v", restoreErr)
			}
		})
	}
}

func TestRestorePreflightAndRestoreRejectSameCheckpointDestinations(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	tests := []struct {
		name       string
		checkpoint func(*testing.T) string
		want       string
	}{
		{name: "existing file", want: "already exists", checkpoint: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "current.tar.gz")
			writeTestFile(t, path, "occupied")
			return path
		}},
		{name: "existing directory", want: "already exists", checkpoint: func(t *testing.T) string {
			return t.TempDir()
		}},
		{name: "source archive", want: "must not equal", checkpoint: func(*testing.T) string {
			return archivePath
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			createCurrentInstanceState(t, ctx, target, "prod")
			writeTestFile(t, filepath.Join(target, "state.txt"), "unchanged")
			before := hashTestTree(t, target)
			checkpoint := test.checkpoint(t)
			preflightOptions := InstanceRestorePreflightOptions{
				ArchivePath: archivePath, TargetHomeDir: target, CurrentBackupOut: checkpoint,
				ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
			}
			plan, preflightErr := PreflightInstanceRestore(ctx, preflightOptions)
			if preflightErr == nil || plan.ReasonCode != RestorePreflightArchiveInvalid || !strings.Contains(preflightErr.Error(), test.want) {
				t.Fatalf("preflight plan=%#v err=%v, want %q", plan, preflightErr, test.want)
			}
			restoreErr := RestoreInstance(ctx, InstanceRestoreOptions{
				TargetHomeDir: target, BackupPath: archivePath, CurrentBackupOut: checkpoint,
				ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
			})
			if restoreErr == nil || restoreErr.Error() != preflightErr.Error() {
				t.Fatalf("restore error = %v, want exact preflight denial %v", restoreErr, preflightErr)
			}
			if after := hashTestTree(t, target); after != before {
				t.Fatalf("checkpoint destination denial mutated target: before=%s after=%s", before, after)
			}
		})
	}
}

func TestRestorePreflightRejectsUncheckpointableCurrentStateBeforeMutation(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	tests := []struct {
		name        string
		environment string
		mutate      func(*testing.T, string)
	}{
		{name: "internal symlink", environment: "prod", mutate: func(t *testing.T, target string) {
			if err := os.Symlink("state.txt", filepath.Join(target, "linked-state")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}},
		{name: "corrupt database", environment: "prod", mutate: func(t *testing.T, target string) {
			writeTestFile(t, filepath.Join(target, instanceBackupDBName), "not a sqlite database")
		}},
		{name: "wrong database environment", environment: "staging"},
		{name: "empty database identity", environment: "prod", mutate: func(t *testing.T, target string) {
			store, err := Open(ctx, filepath.Join(target, instanceBackupDBName))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `INSERT INTO platform_settings (key, value) VALUES (?, '') ON CONFLICT(key) DO UPDATE SET value = ''`, instanceIDSetting); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			createCurrentInstanceState(t, ctx, target, test.environment)
			writeTestFile(t, filepath.Join(target, "state.txt"), "unchanged")
			if test.mutate != nil {
				test.mutate(t, target)
			}
			databasePath := filepath.Join(target, instanceBackupDBName)
			beforeDatabase, err := fileSHA256(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := filepath.Join(t.TempDir(), "current.tar.gz")
			options := InstanceRestorePreflightOptions{
				ArchivePath: archivePath, TargetHomeDir: target, CurrentBackupOut: checkpoint,
				ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
			}
			plan, preflightErr := PreflightInstanceRestore(ctx, options)
			if preflightErr == nil || plan.ReasonCode != RestorePreflightCheckpointInvalid {
				t.Fatalf("preflight plan=%#v err=%v, want checkpoint denial", plan, preflightErr)
			}
			directCheckpoint := filepath.Join(t.TempDir(), "direct-current.tar.gz")
			directErr := BackupInstance(ctx, InstanceBackupOptions{
				HomeDir: target, DBPath: databasePath, OutPath: directCheckpoint,
				Environment: "prod", ReleaseIdentity: identity,
			})
			if directErr == nil || !strings.Contains(preflightErr.Error(), directErr.Error()) {
				t.Fatalf("checkpoint backup error = %v, want preflight to use the same validation: %v", directErr, preflightErr)
			}
			restoreErr := RestoreInstance(ctx, InstanceRestoreOptions{
				TargetHomeDir: target, BackupPath: archivePath, CurrentBackupOut: checkpoint,
				ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
			})
			if restoreErr == nil || restoreErr.Error() != preflightErr.Error() {
				t.Fatalf("restore error = %v, want exact preflight denial %v", restoreErr, preflightErr)
			}
			afterDatabase, err := fileSHA256(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if afterDatabase != beforeDatabase || readTestFile(t, filepath.Join(target, "state.txt")) != "unchanged" {
				t.Fatal("checkpoint-readiness denial mutated current state")
			}
			if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
				t.Fatalf("checkpoint-readiness denial created checkpoint: %v", err)
			}
			if _, err := os.Stat(directCheckpoint); !os.IsNotExist(err) {
				t.Fatalf("checkpoint-readiness denial created direct checkpoint: %v", err)
			}
		})
	}
}

func TestRestoreConsumesExactExternalRecoveryEvidence(t *testing.T) {
	ctx := context.Background()
	baseArchive, identity := createManifestV2TestArchive(t, ctx, "prod")
	manifest := readBackupManifestFromArchive(t, baseArchive)
	manifest.StorageTopology.ManagedData = "external"
	manifest.StorageTopology.ExternalStores = []InstanceBackupExternalStoreReference{{
		Role: "managed-data", Provider: "aws", Endpoint: "https://s3.us-east-1.amazonaws.com",
		Region: "us-east-1", Bucket: "bucket", Prefix: "prefix",
		RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
	}}
	entries := manifestV2TestEntries(t, baseArchive, manifest)
	entries[0].body = marshalManifestV2ForTest(t, manifest)
	archivePath := filepath.Join(t.TempDir(), "external.tar.gz")
	writeInstanceBackupArchive(t, archivePath, entries)

	missingTarget := filepath.Join(t.TempDir(), "missing-evidence-target")
	err := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: missingTarget, BackupPath: archivePath, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, TargetStorageTopology: backupStorageIdentity(manifest.StorageTopology),
	})
	if err == nil || !strings.Contains(err.Error(), RestorePreflightExternalEvidence) {
		t.Fatalf("restore without external evidence error = %v", err)
	}
	if _, statErr := os.Stat(missingTarget); !os.IsNotExist(statErr) {
		t.Fatalf("rejected restore created target: %v", statErr)
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: archivePath, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, TargetStorageTopology: backupStorageIdentity(manifest.StorageTopology),
		ExternalEvidence: map[string]string{"managed-data-version": "version-42"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, instanceBackupDBName)); err != nil {
		t.Fatalf("restored database missing: %v", err)
	}

	for name, mutate := range map[string]func(*InstanceBackupExternalStoreReference){
		"provider": func(reference *InstanceBackupExternalStoreReference) { reference.Provider = "s3-compatible" },
		"endpoint": func(reference *InstanceBackupExternalStoreReference) {
			reference.Endpoint = "https://objects.example.test"
		},
		"region": func(reference *InstanceBackupExternalStoreReference) { reference.Region = "eu-west-1" },
		"bucket": func(reference *InstanceBackupExternalStoreReference) { reference.Bucket = "other-bucket" },
		"prefix": func(reference *InstanceBackupExternalStoreReference) { reference.Prefix = "other-prefix" },
	} {
		t.Run("rejects mismatched "+name, func(t *testing.T) {
			targetTopology := backupStorageIdentity(manifest.StorageTopology)
			mutate(&targetTopology.ExternalStores[0])
			mismatchedTarget := filepath.Join(t.TempDir(), "target")
			plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
				ArchivePath: archivePath, TargetHomeDir: mismatchedTarget, ExpectedEnvironment: "prod",
				TargetReleaseIdentity: identity, TargetStorageTopology: targetTopology,
				ExternalEvidence: map[string]string{"managed-data-version": "version-42"},
			})
			if err == nil || plan.ReasonCode != RestorePreflightStorageTopology {
				t.Fatalf("plan=%#v err=%v", plan, err)
			}
			if _, statErr := os.Stat(mismatchedTarget); !os.IsNotExist(statErr) {
				t.Fatalf("topology rejection mutated target: %v", statErr)
			}
		})
	}
}

func TestRestoreSafetyCheckpointPreservesExactExternalTopology(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	manifest := readBackupManifestFromArchive(t, archivePath)
	manifest.StorageTopology = externalTestTopology("source-version-42", "source-managed-data-version")
	entries := manifestV2TestEntries(t, archivePath, manifest)
	entries[0].body = marshalManifestV2ForTest(t, manifest)
	externalArchive := filepath.Join(t.TempDir(), "external.tar.gz")
	writeInstanceBackupArchive(t, externalArchive, entries)

	target := filepath.Join(t.TempDir(), "target")
	createCurrentInstanceState(t, ctx, target, "prod")
	writeTestFile(t, filepath.Join(target, "current-state.txt"), "checkpoint-me")
	checkpointTopology := externalTestTopology("current-version-7", "current-managed-data-version")
	checkpoint := filepath.Join(t.TempDir(), "current.tar.gz")
	options := InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: externalArchive, CurrentBackupOut: checkpoint,
		ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
		TargetStorageTopology: backupStorageIdentity(manifest.StorageTopology), CurrentStorageTopology: checkpointTopology,
		ExternalEvidence: map[string]string{"source-managed-data-version": "source-version-42"},
	}
	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: options.BackupPath, TargetHomeDir: options.TargetHomeDir, CurrentBackupOut: options.CurrentBackupOut,
		ExpectedEnvironment: options.ExpectedEnvironment, TargetReleaseIdentity: options.TargetReleaseIdentity,
		TargetStorageTopology: options.TargetStorageTopology, CurrentStorageTopology: options.CurrentStorageTopology,
		ExternalEvidence: options.ExternalEvidence,
	})
	if err != nil || plan.CheckpointRequiredBytes == 0 || plan.CheckpointAvailableBytes == 0 || plan.RequiredBytes == 0 {
		t.Fatalf("checkpoint preflight plan=%#v err=%v", plan, err)
	}
	options.ValidatedPlan = &plan
	if err := RestoreInstance(ctx, options); err != nil {
		t.Fatal(err)
	}
	checkpointManifest := readBackupManifestFromArchive(t, checkpoint)
	if !reflect.DeepEqual(checkpointManifest.StorageTopology, normalizeBackupStorageTopology(checkpointTopology)) {
		t.Fatalf("checkpoint topology = %#v, want %#v", checkpointManifest.StorageTopology, checkpointTopology)
	}
}

func externalTestTopology(recoveryPoint, evidenceKey string) InstanceBackupStorageTopology {
	return InstanceBackupStorageTopology{
		ControlPlane: "local", ManagedData: "external", DuckLake: "local",
		ExternalStores: []InstanceBackupExternalStoreReference{{
			Role: "managed-data", Provider: "aws", Endpoint: "https://s3.us-east-1.amazonaws.com",
			Region: "us-east-1", Bucket: "bucket", Prefix: "prefix",
			RecoveryPoint: recoveryPoint, EvidenceKey: evidenceKey,
		}},
	}
}

func TestRestoreRefusesSubstitutedArchivePathAfterPreflight(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	substitute := filepath.Join(t.TempDir(), "same-bytes.tar.gz")
	if err := os.WriteFile(substitute, readTestBytes(t, archivePath), 0o600); err != nil {
		t.Fatal(err)
	}
	err = RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: substitute, ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true, ValidatedPlan: &plan,
	})
	if err == nil || !strings.Contains(err.Error(), RestorePreflightStaleArchive) {
		t.Fatalf("substituted archive error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("substituted archive created target: %v", err)
	}
}

func TestRestorePreflightRejectsUnsupportedV1Explicitly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "source", instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "v1.tar.gz")
	writeInstanceBackupArchive(t, archivePath, []testTarEntry{
		{name: instanceBackupManifestName, mode: 0o600, body: []byte(`{"version":1,"kind":"leapview-instance","dbPath":"leapview.db"}` + "\n")},
		{name: instanceBackupDBName, mode: 0o600, body: readTestBytes(t, dbPath)},
	})
	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: filepath.Join(dir, "target"),
		TargetReleaseIdentity: legacyBackupTarget(t),
	})
	if err == nil || plan.ReasonCode != RestorePreflightUnsupportedManifest {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	plan, err = PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: filepath.Join(dir, "target"),
		TargetReleaseIdentity: legacyBackupTarget(t), TransitionPolicy: legacyBackupPolicy(t),
	})
	if err != nil || !plan.Allowed || plan.ManifestVersion != 1 || !strings.Contains(plan.Remediation, "policy") {
		t.Fatalf("explicit v1 plan=%#v err=%v", plan, err)
	}
}

func TestRestorePreflightNegativeFixtureMatrixIsNonMutating(t *testing.T) {
	ctx := context.Background()
	baseArchive, identity := createManifestV2TestArchive(t, ctx, "prod")
	baseManifest := readBackupManifestFromArchive(t, baseArchive)
	baseEntries := manifestV2TestEntries(t, baseArchive, baseManifest)
	policy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := policy.ReleaseByID("v0.1.0")
	if !ok {
		t.Fatal("embedded policy has no v0.1.0 release")
	}

	tests := []struct {
		name     string
		reason   string
		mutate   func(*InstanceBackupManifestV2, *[]testTarEntry)
		minimum  uint64
		truncate bool
	}{
		{name: "checksum mismatch", reason: RestorePreflightChecksumMismatch, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			(*entries)[len(*entries)-1].body = append((*entries)[len(*entries)-1].body, 'x')
		}},
		{name: "missing member", reason: RestorePreflightMemberMissing, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = (*entries)[:len(*entries)-1]
		}},
		{name: "unexpected member", reason: RestorePreflightMemberUnexpected, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "extra.txt", mode: 0o600, body: []byte("extra")})
		}},
		{name: "duplicate member", reason: RestorePreflightDuplicatePath, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, (*entries)[1])
		}},
		{name: "unsafe path", reason: RestorePreflightUnsafePath, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "../escape", mode: 0o600, body: []byte("escape")})
		}},
		{name: "link", reason: RestorePreflightUnsupportedEntry, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "link", mode: 0o777, symlink: true, linkname: "leapview.db"})
		}},
		{name: "device", reason: RestorePreflightUnsupportedEntry, mutate: func(_ *InstanceBackupManifestV2, entries *[]testTarEntry) {
			*entries = append(*entries, testTarEntry{name: "device", mode: 0o600, typeflag: tar.TypeChar})
		}},
		{name: "truncated archive", reason: RestorePreflightArchiveInvalid, truncate: true},
		{name: "wrong environment", reason: RestorePreflightWrongEnvironment, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.Environment = "staging"
		}},
		{name: "database instance mismatch", reason: RestorePreflightChecksumMismatch, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.InstanceID = "different-instance"
		}},
		{name: "external topology without reference", reason: RestorePreflightUnsupportedManifest, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.StorageTopology.ManagedData = "external"
		}},
		{name: "local topology with external reference", reason: RestorePreflightUnsupportedManifest, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.StorageTopology.ExternalStores = []InstanceBackupExternalStoreReference{{
				Role: "managed-data", Provider: "aws", Endpoint: "https://s3.us-east-1.amazonaws.com",
				Region: "us-east-1", Bucket: "bucket", Prefix: "prefix",
				RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
			}}
		}},
		{name: "external evidence", reason: RestorePreflightExternalEvidence, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.StorageTopology.ManagedData = "external"
			manifest.StorageTopology.ExternalStores = []InstanceBackupExternalStoreReference{{
				Role: "managed-data", Provider: "aws", Endpoint: "https://s3.us-east-1.amazonaws.com",
				Region: "us-east-1", Bucket: "bucket", Prefix: "prefix",
				RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
			}}
		}},
		{name: "external reference secret", reason: RestorePreflightUnsupportedManifest, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.StorageTopology.ManagedData = "external"
			manifest.StorageTopology.ExternalStores = []InstanceBackupExternalStoreReference{{
				Role: "managed-data", Provider: "s3-compatible", Endpoint: "https://access:secret@storage.example",
				Region: "us-east-1", Bucket: "bucket", Prefix: "prefix",
				RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
			}}
		}},
		{name: "incompatible release", reason: RestorePreflightIncompatibleRelease, mutate: func(manifest *InstanceBackupManifestV2, _ *[]testTarEntry) {
			manifest.ReleaseIdentity = legacy.IdentityForPlatform("linux/amd64")
		}},
		{name: "insufficient disk", reason: RestorePreflightInsufficientDisk, minimum: ^uint64(0)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := baseManifest
			manifest.Members = append([]InstanceBackupMember{}, baseManifest.Members...)
			manifest.StorageTopology.ExternalStores = append([]InstanceBackupExternalStoreReference{}, baseManifest.StorageTopology.ExternalStores...)
			entries := append([]testTarEntry{}, baseEntries...)
			if test.mutate != nil {
				test.mutate(&manifest, &entries)
			}
			entries[0].body = marshalManifestV2ForTest(t, manifest)
			archivePath := filepath.Join(t.TempDir(), "negative.tar.gz")
			writeInstanceBackupArchive(t, archivePath, entries)
			if test.truncate {
				contents := readTestBytes(t, archivePath)
				if err := os.WriteFile(archivePath, contents[:len(contents)/2], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(t.TempDir(), "target")
			createCurrentInstanceState(t, ctx, target, "prod")
			writeTestFile(t, filepath.Join(target, "state.txt"), "unchanged")
			before := hashTestTree(t, target)
			targetTopology := InstanceBackupStorageTopology{}
			currentTopology := InstanceBackupStorageTopology{}
			if test.reason == RestorePreflightExternalEvidence {
				targetTopology = backupStorageIdentity(manifest.StorageTopology)
				currentTopology = manifest.StorageTopology
			}
			plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
				ArchivePath: archivePath, TargetHomeDir: target, ExpectedEnvironment: "prod",
				TargetReleaseIdentity: identity, ExclusiveLockHeld: true, MinimumFreeBytes: test.minimum,
				CurrentBackupOut:      filepath.Join(t.TempDir(), "before.tar.gz"),
				TargetStorageTopology: targetTopology, CurrentStorageTopology: currentTopology,
			})
			if err == nil || plan.ReasonCode != test.reason {
				t.Fatalf("plan=%#v err=%v, want %s", plan, err, test.reason)
			}
			if after := hashTestTree(t, target); after != before {
				t.Fatalf("negative preflight mutated target: before=%s after=%s", before, after)
			}
		})
	}
}

func TestRestorePreflightChecksCapacityBeforeMemberDecompression(t *testing.T) {
	ctx := context.Background()
	baseArchive, identity := createManifestV2TestArchive(t, ctx, "prod")
	manifest := readBackupManifestFromArchive(t, baseArchive)
	database := manifest.Members[0]
	for _, member := range manifest.Members {
		if member.Path == instanceBackupDBName {
			database = member
			break
		}
	}
	database.Size = math.MaxInt64
	manifest.Members = []InstanceBackupMember{database}
	digest, err := inventorySHA256(manifest.Members)
	if err != nil {
		t.Fatal(err)
	}
	manifest.InventorySHA256 = digest
	archivePath := filepath.Join(t.TempDir(), "capacity.tar.gz")
	writeInstanceBackupArchive(t, archivePath, []testTarEntry{{
		name: instanceBackupManifestName, mode: 0o600, body: marshalManifestV2ForTest(t, manifest),
	}})
	plan, err := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: filepath.Join(t.TempDir(), "target"),
		TargetReleaseIdentity: identity,
	})
	if err == nil || plan.ReasonCode != RestorePreflightInsufficientDisk || !strings.Contains(plan.Remediation, "before archive decompression") {
		t.Fatalf("capacity plan=%#v err=%v", plan, err)
	}
}

func TestRestoreTransitionDirectionUsesArchiveAndTargetVersions(t *testing.T) {
	older := testBackupReleaseIdentity("1.2.3", "a")
	newer := testBackupReleaseIdentity("1.3.0", "b")
	for _, test := range []struct {
		name      string
		archive   compatibility.ReleaseIdentity
		target    compatibility.ReleaseIdentity
		operation compatibility.Operation
	}{
		{name: "upgrade", archive: older, target: newer, operation: compatibility.OperationUpgrade},
		{name: "rollback", archive: newer, target: older, operation: compatibility.OperationRollback},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := restoreTransitionRequest(test.archive, test.target)
			if err != nil {
				t.Fatal(err)
			}
			if request.Operation != test.operation || request.Current != test.archive || request.Next != test.target {
				t.Fatalf("transition request = %#v", request)
			}
		})
	}
}

func TestRestorePreflightValidationMemoryIsBounded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	home := filepath.Join(dir, "source")
	dbPath := filepath.Join(home, instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(home, "managed-data", "objects", "large.bin")
	if err := os.MkdirAll(filepath.Dir(largePath), 0o700); err != nil {
		t.Fatal(err)
	}
	const memberBytes = 32 << 20
	large, err := os.OpenFile(largePath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(memberBytes); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	identity := testBackupReleaseIdentity("1.2.3", "a")
	archivePath := filepath.Join(dir, "large.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: archivePath, Environment: "prod",
		BackupID: "backup_memory", ReleaseIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	options := InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: filepath.Join(dir, "target"), ExpectedEnvironment: "prod",
		TargetReleaseIdentity: identity, ExclusiveLockHeld: true,
	}
	if _, err := PreflightInstanceRestore(ctx, options); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	plan, err := PreflightInstanceRestore(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	const maximumValidationAllocation = 12 << 20
	if allocated > maximumValidationAllocation {
		t.Fatalf("preflight allocated %d bytes while validating a %d-byte member", allocated, memberBytes)
	}
	if plan.ValidationBufferBytes != InstanceBackupValidationBufferSize || plan.ValidationBufferBytes >= memberBytes {
		t.Fatalf("validation buffer = %d, member bytes = %d", plan.ValidationBufferBytes, memberBytes)
	}
}

func createManifestV2TestArchive(t *testing.T, ctx context.Context, environment string) (string, compatibility.ReleaseIdentity) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "source")
	dbPath := filepath.Join(home, instanceBackupDBName)
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, environment); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "artifacts", "release.tar.gz"), "artifact")
	identity := testBackupReleaseIdentity("1.2.3", "a")
	archivePath := filepath.Join(dir, "backup.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: archivePath, BackupID: "backup_test",
		Now:             func() time.Time { return time.Date(2026, time.August, 25, 5, 0, 0, 0, time.UTC) },
		ReleaseIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	return archivePath, identity
}

func createCurrentInstanceState(t *testing.T, ctx context.Context, home, environment string) {
	t.Helper()
	store, err := Open(ctx, filepath.Join(home, instanceBackupDBName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, environment); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func testBackupReleaseIdentity(version, digestCharacter string) compatibility.ReleaseIdentity {
	return compatibility.ReleaseIdentity{
		ReleaseID: "v" + version, Version: version, SourceRevision: strings.Repeat("a", 40),
		Image:        "ghcr.io/flidai/leapview@sha256:" + strings.Repeat(digestCharacter, 64),
		Distribution: "public", Platform: "linux/amd64",
	}
}

func legacyBackupPolicy(t *testing.T) *compatibility.Policy {
	t.Helper()
	policy, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	for index := range policy.Releases {
		if policy.Releases[index].ID == policy.CandidateRelease {
			policy.Releases[index].LegacyBackupVersions = []int{1}
		}
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	return policy
}

func legacyBackupTarget(t *testing.T) compatibility.ReleaseIdentity {
	t.Helper()
	policy := legacyBackupPolicy(t)
	release, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		t.Fatalf("candidate release %q is absent", policy.CandidateRelease)
	}
	return release.IdentityForPlatform("linux/amd64")
}

func hashTestTree(t *testing.T, root string) string {
	t.Helper()
	digest, err := instanceTreeSHA256(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeTestBytes(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readBackupManifestFromArchive(t *testing.T, archivePath string) InstanceBackupManifestV2 {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	header, err := tr.Next()
	if err != nil || header.Name != instanceBackupManifestName {
		t.Fatalf("manifest header=%#v err=%v", header, err)
	}
	var manifest InstanceBackupManifestV2
	if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func manifestV2TestEntries(t *testing.T, archivePath string, manifest InstanceBackupManifestV2) []testTarEntry {
	t.Helper()
	contents := readTarGzEntries(t, archivePath)
	entries := []testTarEntry{{name: instanceBackupManifestName, mode: 0o600, body: contents[instanceBackupManifestName]}}
	for _, member := range manifest.Members {
		entries = append(entries, testTarEntry{name: member.Path, mode: int64(member.Mode), body: contents[member.Path]})
	}
	return entries
}

func marshalManifestV2ForTest(t *testing.T, manifest InstanceBackupManifestV2) []byte {
	t.Helper()
	document, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(document, '\n')
}
