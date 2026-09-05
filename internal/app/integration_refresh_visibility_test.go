package app

import (
	"os"
	"path/filepath"
	"testing"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
)

// TestCanonicalProjectProvidesRefreshPipelineForEverySemanticModel keeps the
// authored graph contract in app tests. Runtime refresh execution belongs to
// the native PostgreSQL integration suites; the retired SQLite fixture had no
// canonical delivery authority after the clean cutover.
func TestCanonicalProjectProvidesRefreshPipelineForEverySemanticModel(t *testing.T) {
	project, err := projectcompiler.Compile(canonicalProjectPath(t))
	if err != nil {
		t.Fatalf("compile canonical project: %v", err)
	}
	covered := make(map[string]bool, len(project.RefreshPipelines()))
	for _, pipeline := range project.RefreshPipelines() {
		covered[pipeline.SemanticModelID.String()] = true
	}
	for semanticModelID := range project.Models() {
		if !covered[semanticModelID] {
			t.Errorf("semantic model %s has no refresh pipeline", semanticModelID)
		}
	}
}

func canonicalProjectPath(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve test working directory: %v", err)
	}
	for dir := workingDir; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "dashboards", "leapview.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	t.Fatalf("canonical project dashboards/leapview.yaml not found from %s", workingDir)
	return ""
}
