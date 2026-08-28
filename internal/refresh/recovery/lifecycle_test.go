package recovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestQualificationEvidenceRetentionIsContentBoundAndRejectsSymlinks(t *testing.T) {
	plan := platform.InstanceRestorePreflightPlan{
		SchemaVersion: 1, Allowed: true, ReasonCode: platform.RestorePreflightAllowed,
		BackupID: "lvbackup_lifecycle", ManifestVersion: platform.InstanceBackupManifestVersion,
		ManifestSHA256: strings.Repeat("a", 64), ArchiveSHA256: strings.Repeat("b", 64),
		ExclusiveLockVerified: true,
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "preflight.json")
	if err := os.WriteFile(source, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "evidence")
	validated, err := readUBDRArtifact(EvidenceRestorePreflight, source, Occurrence{})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := retainUBDRArtifact(validated, store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reference.URI, "artifact://qualification/") || strings.Contains(reference.URI, filepath.Dir(source)) {
		t.Fatalf("content-addressed reference = %#v", reference)
	}

	symlink := filepath.Join(t.TempDir(), "preflight-link.json")
	if err := os.Symlink(source, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateUBDRArtifact(EvidenceRestorePreflight, symlink); err == nil {
		t.Fatal("symlink evidence was accepted")
	}

	retained := filepath.Join(store, reference.SHA256+".json")
	if err := os.WriteFile(retained, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (FileEvidencePublisher{Root: store}).Publish(context.Background(), Occurrence{Evidence: []EvidenceReference{reference}}); err == nil {
		t.Fatal("publisher accepted evidence bytes that no longer match the ledger digest")
	}
}

func TestEvidenceCommitIsDirectoryDurableBeforeLedgerCompletion(t *testing.T) {
	path, occurrence := writeTransitionEvidence(t)
	artifact, err := readUBDRArtifact(EvidenceTransitionQualification, path, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEvidenceDurability{}
	if _, err := retainUBDRArtifactWithDurability(artifact, filepath.Join(t.TempDir(), "evidence"), recorder); err != nil {
		t.Fatal(err)
	}
	want := []string{"file-sync", "rename", "directory-sync"}
	if !reflect.DeepEqual(recorder.events, want) {
		t.Fatalf("durability order = %v, want %v", recorder.events, want)
	}
}

type recordingEvidenceDurability struct{ events []string }

func (recorder *recordingEvidenceDurability) SyncFile(file *os.File) error {
	recorder.events = append(recorder.events, "file-sync")
	return file.Sync()
}
func (recorder *recordingEvidenceDurability) Rename(source, destination string) error {
	recorder.events = append(recorder.events, "rename")
	return os.Rename(source, destination)
}
func (recorder *recordingEvidenceDurability) SyncDirectory(path string) error {
	recorder.events = append(recorder.events, "directory-sync")
	return (osEvidenceDurability{}).SyncDirectory(path)
}

func TestEvidenceGarbageCollectionPreservesSharedDigests(t *testing.T) {
	root := t.TempDir()
	shared := strings.Repeat("a", 64)
	orphan := strings.Repeat("b", 64)
	for _, digest := range []string{shared, orphan} {
		if err := os.WriteFile(filepath.Join(root, digest+".json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reference := EvidenceReference{Kind: EvidenceBackupManifestV2, URI: "artifact://qualification/backup/" + shared + ".json", SHA256: shared}
	if err := GarbageCollectEvidence(root, []Occurrence{{Evidence: []EvidenceReference{reference}}, {Evidence: []EvidenceReference{reference}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, shared+".json")); err != nil {
		t.Fatalf("shared evidence was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, orphan+".json")); !os.IsNotExist(err) {
		t.Fatalf("orphan evidence still exists: %v", err)
	}
	if err := GarbageCollectEvidence(root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, shared+".json")); !os.IsNotExist(err) {
		t.Fatalf("unreferenced shared evidence still exists: %v", err)
	}
}

func TestLedgerRejectsForgedAdapterMeasurementsAndOwnerIdentityMismatch(t *testing.T) {
	path, occurrence := writeTransitionEvidence(t)
	for name, mutate := range map[string]func(*Occurrence){
		"artifact": func(value *Occurrence) {
			value.ArtifactIdentity = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("f", 64)
		},
		"policy":        func(value *Occurrence) { value.PolicyVersion = "ubdr-v2" },
		"policy digest": func(value *Occurrence) { value.PolicySHA256 = strings.Repeat("e", 64) },
		"target":        func(value *Occurrence) { value.TargetScope = "release:v9.9.9" },
	} {
		t.Run(name, func(t *testing.T) {
			mismatched := occurrence
			mutate(&mismatched)
			if _, err := validateScenarioArtifacts(mismatched, []EvidenceArtifact{{Kind: EvidenceTransitionQualification, Path: path}}); err == nil {
				t.Fatal("owner evidence identity mismatch was accepted")
			}
		})
	}
	started := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	forged := ScenarioOutcome{
		RecoveryPointAt: started.Add(-time.Hour), RestoreCompletedAt: started.Add(time.Second), ReadyAt: started.Add(2 * time.Second),
		Artifacts: []EvidenceArtifact{{Kind: EvidenceTransitionQualification, Path: path}},
	}
	if _, err := forged.result(occurrence, started, started.Add(time.Minute), t.TempDir()); err == nil {
		t.Fatal("adapter-provided measurement timestamps were accepted")
	}
}

func writeTransitionEvidence(t *testing.T) (string, Occurrence) {
	t.Helper()
	candidate := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	predecessor := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	state := compatibility.TransitionQualificationState{
		InstanceID: "lvinst_transition", Environment: "qualification", CanonicalOrigin: "https://qualification.example",
		PrincipalID: "principal_qualification", PrincipalKind: "user", PrincipalEmail: "qualification@example.com", PrincipalName: "Qualification",
	}
	evidence := compatibility.TransitionQualificationEvidence{
		SchemaVersion: 1, PolicyVersion: "ubdr-v1", RecoveryPointAt: time.Date(2026, 8, 25, 23, 0, 0, 0, time.UTC),
		Predecessor:  compatibility.ReleaseIdentity{ReleaseID: "v0.2.0", Version: "0.2.0", SourceRevision: strings.Repeat("1", 40), Image: predecessor, Distribution: "public", Platform: "linux/amd64"},
		Candidate:    compatibility.ReleaseIdentity{ReleaseID: "v0.3.0", Version: "0.3.0", SourceRevision: strings.Repeat("2", 40), Image: candidate, Distribution: "public", Platform: "linux/amd64"},
		PolicySHA256: strings.Repeat("c", 64), StateBeforeUpgrade: strings.Repeat("d", 64), StateAfterUpgrade: strings.Repeat("d", 64), StateAfterRollback: strings.Repeat("d", 64),
		InventoryBefore: state, InventoryAfterUpgrade: state, InventoryAfterRollback: state,
		UpgradeResult: "success", RollbackResult: "success", PreservationVerified: true,
	}
	document, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "transition-qualification.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, Occurrence{Operation: OperationUpgrade, ArtifactIdentity: candidate, PolicyVersion: "ubdr-v1", PolicySHA256: strings.Repeat("c", 64), TargetScope: "release:v0.3.0"}
}

func TestScenarioEvidenceMustBindScheduledArtifactAndCompleteRestoreSet(t *testing.T) {
	scheduled := "oci://ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	manifest := platform.InstanceBackupManifestV2{
		SchemaVersion: platform.InstanceBackupManifestVersion, Kind: "leapview-instance",
		BackupID: "lvbackup_binding", InstanceID: "lvinst_binding",
		ReleaseIdentity: compatibility.ReleaseIdentity{
			Image: "oci://ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64),
		},
		InventorySHA256: strings.Repeat("c", 64),
		Members:         []platform.InstanceBackupMember{{Path: "leapview.db", SHA256: strings.Repeat("d", 64)}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "leapview-backup.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateScenarioArtifacts(Occurrence{
		Operation: OperationBackup, ArtifactIdentity: scheduled,
	}, []EvidenceArtifact{{Kind: EvidenceBackupManifestV2, Path: path}}); err == nil {
		t.Fatal("backup evidence for a different release artifact was accepted")
	}
	if _, err := validateScenarioArtifacts(Occurrence{
		Operation: OperationRestore, ArtifactIdentity: scheduled,
	}, []EvidenceArtifact{{Kind: EvidenceRestorePreflight, Path: path}}); err == nil {
		t.Fatal("restore qualification without its bound backup manifest was accepted")
	}
}
