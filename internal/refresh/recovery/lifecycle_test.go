package recovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	validated, err := readUBDRArtifact(EvidenceRestorePreflight, source, "")
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
