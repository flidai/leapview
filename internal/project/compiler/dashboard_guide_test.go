package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compile the guide's actual source, rather than a copied fixture, with the
// bundled semantic definitions an agent is told to use.
func TestDashboardGuideCompilesAgainstBundledSales(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	guide, err := os.ReadFile(filepath.Join(repo, "docs/articles/build/dashboard.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(guide), "```yaml\n")
	if !ok {
		t.Fatal("guide has no YAML resource")
	}
	source, _, ok := strings.Cut(block, "```")
	if !ok {
		t.Fatal("guide YAML block is not closed")
	}
	root := t.TempDir()
	for _, directory := range []string{"connections", "sources", "models", "semantic-models"} {
		if err := os.CopyFS(filepath.Join(root, directory), os.DirFS(filepath.Join(repo, "dashboards", directory))); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "dashboards", "executive-sales.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(root); err != nil {
		t.Fatalf("dashboard guide does not compile against bundled sales: %v", err)
	}
}
