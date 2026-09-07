package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthoredSourceFixturesUseDiscoveredGraph keeps checked-in evaluation and
// visual-documentation projects aligned with the project-wide authoring
// contract. A legacy workspace directory or metadata field must not silently
// become an accepted example again.
func TestAuthoredSourceFixturesUseDiscoveredGraph(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	fixtures := []string{
		filepath.Join(root, "evaluation", "project"),
		filepath.Join(root, "internal", "app", "tools", "visualdocgen", "testdata", "project"),
	}
	for _, dir := range fixtures {
		dir := dir
		t.Run(filepath.ToSlash(dir), func(t *testing.T) {
			if _, err := LoadSourceRoot(dir); err != nil {
				t.Fatalf("LoadSourceRoot(%q): %v", dir, err)
			}
			err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if strings.EqualFold(entry.Name(), "workspace") || strings.EqualFold(entry.Name(), "workspaces") {
						return &legacyWorkspaceFixtureError{path: path}
					}
					return nil
				}
				contents, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(contents), "workspace:") || strings.Contains(string(contents), "workspaces:") {
					return &legacyWorkspaceFixtureError{path: path}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type legacyWorkspaceFixtureError struct{ path string }

func (e *legacyWorkspaceFixtureError) Error() string {
	return "legacy workspace field in authored fixture: " + e.path
}
