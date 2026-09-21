package providerrestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileEvidenceStorePersistsAtomicallyWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	store := FileEvidenceStore{Root: root}
	report := Report{SchemaVersion: ReportSchemaVersion, Kind: ReportKind, Status: StatusRunning, OccurrenceID: "occurrence-a", RecoverySetID: "set-a", FrontierDigest: "sha256:" + strings64("a"), TargetID: "target-a"}
	reference, err := store.Save(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.Load(context.Background(), report.OccurrenceID)
	if err != nil || !found || loaded.OccurrenceID != report.OccurrenceID {
		t.Fatalf("load=%#v found=%v err=%v", loaded, found, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.HasSuffix(entries[0].Name(), ".tmp") {
		t.Fatalf("evidence entries = %v", entries)
	}
	raw, err := os.ReadFile(filepath.Join(root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "postgres://") || strings.Contains(strings.ToLower(string(raw)), "password") {
		t.Fatal("credential-bearing data reached provider evidence")
	}
	if !strings.HasPrefix(reference.URI, "file://") || len(reference.SHA256) != 64 {
		t.Fatalf("reference=%#v", reference)
	}
}
