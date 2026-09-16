package projectinit

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInitializeCreatesExactProjectAtomically(t *testing.T) {
	target := filepath.Join(t.TempDir(), "analytics")
	root, err := Initialize(target)
	if err != nil {
		t.Fatal(err)
	}
	if root != target {
		t.Fatalf("root = %q, want %q", root, target)
	}
	for _, path := range []string{
		".gitignore",
		".leapview/development-inputs.yaml",
		"data/sample/sales.csv",
		"dashboards/connections/sample.yaml",
		"dashboards/sources/sample.sales.yaml",
		"dashboards/models/sales.yaml",
		"dashboards/semantic-models/sales.yaml",
		"dashboards/dashboards/sales-overview.yaml",
		"dashboards/pipelines/sample-refresh.yaml",
	} {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s is not a regular project file", path)
		}
	}
}

func TestInitializeNeverAdoptsExistingDestination(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "analytics")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "owned.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(target); err == nil {
		t.Fatal("existing project destination was adopted")
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "keep" {
		t.Fatalf("existing file changed: %q, %v", content, err)
	}
}

func TestInitializeRejectsDestinationSymlink(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "analytics")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(link); err == nil {
		t.Fatal("destination symlink was accepted")
	}
}

func TestInitializeRejectsParentSymlink(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(filepath.Join(linkedParent, "analytics")); err == nil {
		t.Fatal("parent symlink was accepted")
	}
	if _, err := os.Lstat(filepath.Join(realParent, "analytics")); !os.IsNotExist(err) {
		t.Fatalf("initializer mutated symlink target: %v", err)
	}
}

func TestConcurrentInitializeHasOneWinnerAndNoPartialMerge(t *testing.T) {
	target := filepath.Join(t.TempDir(), "analytics")
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			_, err := Initialize(target)
			results <- err
		}()
	}
	start.Done()
	var successes int
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful initializers = %d, want 1", successes)
	}
	if _, err := os.Stat(filepath.Join(target, "dashboards", "dashboards", "sales-overview.yaml")); err != nil {
		t.Fatalf("winning project is incomplete: %v", err)
	}
}
