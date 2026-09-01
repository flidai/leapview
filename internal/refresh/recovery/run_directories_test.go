package recovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQualificationRunDirectoryReclaimsOnlySupersededOccurrenceGeneration(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"occurrence-a-generation-1",
		"occurrence-a-generation-3",
		"occurrence-b-generation-1",
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runDirectory, err := prepareQualificationRunDirectory(root, Occurrence{
		ID: "occurrence-a", Fence: Fence{Owner: "worker", Generation: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runDirectory != filepath.Join(root, "occurrence-a-generation-2") {
		t.Fatalf("run directory = %q", runDirectory)
	}
	if _, err := os.Stat(filepath.Join(root, "occurrence-a-generation-1")); !os.IsNotExist(err) {
		t.Fatalf("superseded crash run directory remains: %v", err)
	}
	for _, name := range []string{"occurrence-a-generation-2", "occurrence-a-generation-3", "occurrence-b-generation-1"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("active or unrelated run directory %s was removed: %v", name, err)
		}
	}
}

func TestQualificationRunDirectorySweepReclaimsCrashedAndTerminalButPreservesLiveLease(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{
		"active-generation-1", "crashed-generation-2", "terminal-generation-3", "operator-data",
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	occurrences := []Occurrence{
		{ID: "active", Status: StatusRunning, Fence: Fence{Owner: "worker", Generation: 1}, LeaseExpiresAt: now.Add(time.Minute)},
		{ID: "crashed", Status: StatusRunning, Fence: Fence{Owner: "worker", Generation: 2}, LeaseExpiresAt: now.Add(-time.Second)},
		{ID: "terminal", Status: StatusSucceeded, Fence: Fence{Owner: "worker", Generation: 3}, LeaseExpiresAt: now.Add(time.Hour)},
	}
	if err := ReclaimQualificationRunDirectories(root, occurrences, now); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"active-generation-1", "operator-data"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("live or unowned run directory %s was removed: %v", name, err)
		}
	}
	for _, name := range []string{"crashed-generation-2", "terminal-generation-3"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("abandoned run directory %s remains: %v", name, err)
		}
	}
}
