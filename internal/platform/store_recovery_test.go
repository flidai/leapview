package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverInterruptedInstanceOperationsRemovesDisposableWork(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	writeTestFile(t, filepath.Join(target, "current"), "current")
	writeTestFile(t, filepath.Join(parent, ".leapview-instance-backup-stale", "copy"), "backup")
	writeTestFile(t, filepath.Join(parent, ".leapview-instance-backup-stale.tar.gz"), "backup archive")
	writeTestFile(t, filepath.Join(parent, ".leapview-restore-stale", "copy"), "restore")
	writeTestFile(t, filepath.Join(parent, ".leapview-restore-old-stale", "copy"), "old")
	writeTestFile(t, filepath.Join(parent, ".leapview-current-backup-stale.tar.gz"), "checkpoint")
	writeTestFile(t, filepath.Join(parent, "leapview-current-backup-stale.tar.gz"), "legacy checkpoint")

	if err := recoverInterruptedInstanceOperations(target); err == nil || !strings.Contains(err.Error(), "ambiguous legacy") {
		t.Fatalf("recoverInterruptedInstanceOperations() error = %v, want ambiguous legacy error", err)
	}
	for _, stale := range []string{".leapview-instance-backup-stale", ".leapview-instance-backup-stale.tar.gz", ".leapview-restore-stale", ".leapview-restore-old-stale", ".leapview-current-backup-stale.tar.gz", "leapview-current-backup-stale.tar.gz"} {
		if _, err := os.Stat(filepath.Join(parent, stale)); err != nil {
			t.Fatalf("legacy artifact %q was mutated: %v", stale, err)
		}
	}
	if got := readTestFile(t, filepath.Join(target, "current")); got != "current" {
		t.Fatalf("current state = %q, want current", got)
	}
}

func TestRecoverInterruptedInstanceOperationsRollsBackMissingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	canonicalTarget, targetID, err := instanceTargetIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(parent, ".leapview-restore-old-"+targetID+"-20260727200000")
	writeTestFile(t, filepath.Join(old, "current"), "recover me")
	if err := writeInstanceOperationMarker(old, canonicalTarget, targetID); err != nil {
		t.Fatal(err)
	}
	staleRestore := filepath.Join(parent, ".leapview-restore-"+targetID+"-stale")
	writeTestFile(t, filepath.Join(staleRestore, "candidate"), "discard me")

	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatalf("recoverInterruptedInstanceOperations() error = %v", err)
	}
	if got := readTestFile(t, filepath.Join(target, "current")); got != "recover me" {
		t.Fatalf("recovered state = %q, want recover me", got)
	}
	if _, err := os.Stat(staleRestore); !os.IsNotExist(err) {
		t.Fatalf("stale restore candidate survived: %v", err)
	}
}

func TestRecoverInterruptedInstanceOperationsScopesSiblingHomes(t *testing.T) {
	parent := t.TempDir()
	targetA := filepath.Join(parent, "home-a")
	targetB := filepath.Join(parent, "home-b")
	canonicalB, idB, _ := instanceTargetIdentity(targetB)
	writeTestFile(t, filepath.Join(targetA, "current"), "a")
	backupB := filepath.Join(parent, ".leapview-instance-backup-"+idB+"-stale")
	writeTestFile(t, filepath.Join(backupB, "payload"), "b-backup")
	restoreB := filepath.Join(parent, ".leapview-restore-"+idB+"-stale")
	writeTestFile(t, filepath.Join(restoreB, "payload"), "b-restore")
	oldB := filepath.Join(parent, ".leapview-restore-old-"+idB+"-20200101000000")
	writeTestFile(t, filepath.Join(oldB, "payload"), "b-old")
	if err := writeInstanceOperationMarker(oldB, canonicalB, idB); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(targetA); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{backupB, restoreB, oldB} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("sibling artifact %s changed: %v", p, err)
		}
	}
}

func TestRecoverInterruptedInstanceOperationsSelectsNewestMatchingGeneration(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	canonical, id, _ := instanceTargetIdentity(target)
	old1 := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200101000000")
	old2 := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200201000000")
	writeTestFile(t, filepath.Join(old1, "value"), "old")
	writeTestFile(t, filepath.Join(old2, "value"), "new")
	if err := writeInstanceOperationMarker(old1, canonical, id); err != nil {
		t.Fatal(err)
	}
	if err := writeInstanceOperationMarker(old2, canonical, id); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(target, "value")); got != "new" {
		t.Fatalf("recovered generation = %q", got)
	}
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Fatalf("older generation survived: %v", err)
	}
}

func TestRecoverInterruptedInstanceOperationsRejectsMismatchedMarker(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	_, id, _ := instanceTargetIdentity(target)
	old := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200101000000")
	writeTestFile(t, filepath.Join(old, "value"), "unsafe")
	if err := writeInstanceOperationMarker(old, filepath.Join(parent, "other"), id); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(target); err == nil {
		t.Fatal("expected marker mismatch error")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("mismatched rollback mutated: %v", err)
	}
}
