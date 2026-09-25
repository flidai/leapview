//go:build linux

package demoupgrade

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func snapshotFixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	volumes := map[string]string{"postgres": filepath.Join(root, "pg"), "home": filepath.Join(root, "home")}
	for name, path := range volumes {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "state"), []byte(name+"-before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "backups", "point"), volumes
}
func TestColdSnapshotRestoresCompletePairAndPreservesFailedState(t *testing.T) {
	root, volumes := snapshotFixture(t)
	digest, err := CaptureStoppedDirectories(t.Context(), root, "demo-02", volumes)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range volumes {
		if err = os.WriteFile(filepath.Join(path, "state"), []byte("after-migration"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = RestoreStoppedDirectories(t.Context(), root, "demo-02", digest, volumes); err != nil {
		t.Fatal(err)
	}
	for name, path := range volumes {
		data, _ := os.ReadFile(filepath.Join(path, "state"))
		if string(data) != name+"-before" {
			t.Fatal("mixed recovery point:", name, string(data))
		}
	}
	retained, _ := filepath.Glob(filepath.Join(filepath.Dir(volumes["home"]), ".restore-*", "replaced", "state"))
	if len(retained) != 2 {
		t.Fatal("failed state not retained:", retained)
	}
	for _, path := range retained {
		data, _ := os.ReadFile(path)
		if string(data) != "after-migration" {
			t.Fatal(string(data))
		}
	}
	if err = RestoreStoppedDirectories(t.Context(), root, "demo-02", digest, volumes); err != nil {
		t.Fatal("idempotent recovery failed:", err)
	}
}
func TestColdSnapshotCorruptionNeverTouchesEitherDestination(t *testing.T) {
	root, volumes := snapshotFixture(t)
	digest, err := CaptureStoppedDirectories(t.Context(), root, "demo-02", volumes)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "home", "state"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if RestoreStoppedDirectories(t.Context(), root, "demo-02", digest, volumes) == nil {
		t.Fatal("corrupt point accepted")
	}
	for name, path := range volumes {
		data, _ := os.ReadFile(filepath.Join(path, "state"))
		if string(data) != name+"-before" {
			t.Fatal("live destination changed")
		}
	}
}
func TestColdSnapshotRefusesExternalLinksAndOverlap(t *testing.T) {
	root, volumes := snapshotFixture(t)
	external := filepath.Join(t.TempDir(), "external")
	os.WriteFile(external, []byte("outside"), 0600)
	os.Symlink(external, filepath.Join(volumes["home"], "link"))
	if _, err := CaptureStoppedDirectories(t.Context(), root, "demo-02", volumes); err == nil {
		t.Fatal("external state silently excluded")
	}
	if _, err := CaptureStoppedDirectories(t.Context(), filepath.Join(volumes["postgres"], "backup"), "demo-02", volumes); err == nil {
		t.Fatal("recursive snapshot accepted")
	}
}
func TestColdSnapshotRejectsWrongTargetAndMissingDomain(t *testing.T) {
	root, volumes := snapshotFixture(t)
	digest, err := CaptureStoppedDirectories(t.Context(), root, "demo-02", volumes)
	if err != nil {
		t.Fatal(err)
	}
	if RestoreStoppedDirectories(t.Context(), root, "different", digest, volumes) == nil {
		t.Fatal("wrong target accepted")
	}
	if RestoreStoppedDirectories(t.Context(), root, "demo-02", digest, map[string]string{"home": volumes["home"]}) == nil {
		t.Fatal("partial restore accepted")
	}
}
func TestCancelledCaptureCannotPublishARecoveryPoint(t *testing.T) {
	root, volumes := snapshotFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CaptureStoppedDirectories(ctx, root, "demo-02", volumes); err == nil {
		t.Fatal("cancelled capture succeeded")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("incomplete snapshot published", err)
	}
}
