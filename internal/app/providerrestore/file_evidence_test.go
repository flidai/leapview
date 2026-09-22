package providerrestore

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/refresh/recovery"
)

func TestFileEvidenceStorePersistsAtomicallyWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	store := FileEvidenceStore{Root: root}
	report := Report{SchemaVersion: ReportSchemaVersion, Kind: ReportKind, Status: StatusRunning, OccurrenceID: "occurrence-a", Fence: recovery.Fence{Owner: "worker-a", Generation: 1}, RecoverySetID: "set-a", FrontierDigest: "sha256:" + strings64("a"), TargetID: "target-a"}
	reference, err := store.Save(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), reference)
	if err != nil || loaded.OccurrenceID != report.OccurrenceID {
		t.Fatalf("load=%#v err=%v", loaded, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.HasSuffix(entries[0].Name(), ".tmp") {
		t.Fatalf("evidence entries = %v", entries)
	}
	raw, err := os.ReadFile(root + "/" + entries[0].Name())
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
