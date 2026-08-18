package module

import (
	"context"
	"testing"
)

func TestBuildSealedOnlyDoesNotOpenProcessCatalog(t *testing.T) {
	m, err := Build(context.Background(), Config{DisableProcessEnvironment: true, RootDir: t.TempDir(), CatalogPath: t.TempDir() + "/legacy.duckdb", DataPath: t.TempDir(), RuntimeCacheEntries: 1, RuntimeCacheBytes: 1, NodeCacheEntries: 1, NodeCacheBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.environment != nil {
		t.Fatal("sealed-only analytics opened a process-wide environment")
	}
}
