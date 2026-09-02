package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthoredProjectFixturesUseSourceRoot keeps checked-in evaluation and
// visual-documentation projects aligned with the fixed-directory authoring
// contract. Legacy manifests, workspace directories, and workspace metadata
// must not silently become accepted examples again.
func TestAuthoredProjectFixturesUseSourceRoot(t *testing.T) {
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
				if entry.Name() == "leapview.yaml" {
					return &legacyProjectManifestFixtureError{path: path}
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

type legacyProjectManifestFixtureError struct{ path string }

func (e *legacyProjectManifestFixtureError) Error() string {
	return "legacy Project manifest in authored fixture: " + e.path
}
