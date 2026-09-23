package providerrestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedResourceManifestPersistsImmediatelyAndSurvivesMissingSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	store := RetainedResourceManifestStore{Path: path}
	if err := store.Start("run-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Record("run-a", RetainedResource{Kind: "container", ID: "postgres-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("resource identity was not persisted immediately: %v", err)
	}
	manifest, found, err := (RetainedResourceManifestStore{Path: path}).Load()
	if err != nil || !found || len(manifest.Resources) != 1 || manifest.Resources[0].ID != "postgres-a" {
		t.Fatalf("restart load=%#v found=%v err=%v", manifest, found, err)
	}
}

func TestRetainedResourceManifestSupportsPriorCleanupBeforeNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	store := RetainedResourceManifestStore{Path: path}
	if err := store.Start("run-a"); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []RetainedResource{{Kind: "network", ID: "network-a"}, {Kind: "container", ID: "postgres-a"}, {Kind: "container", ID: "objects-a"}} {
		if err := store.Record("run-a", resource); err != nil {
			t.Fatal(err)
		}
	}
	prior, found, err := store.Load()
	if err != nil || !found || len(prior.Resources) != 3 {
		t.Fatalf("prior manifest=%#v found=%v err=%v", prior, found, err)
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := store.Start("run-b"); err != nil {
		t.Fatal(err)
	}
	next, found, err := store.Load()
	if err != nil || !found || next.RunID != "run-b" || len(next.Resources) != 0 {
		t.Fatalf("second run manifest=%#v found=%v err=%v", next, found, err)
	}
}
