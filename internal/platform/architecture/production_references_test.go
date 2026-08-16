package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoWorkspaceProductionReferences(t *testing.T) {
	if err := CheckNoWorkspaceProductionReferences(repoRoot(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProductionReferenceObservationDeterministicAndPreservesMultiplicity(t *testing.T) {
	root := t.TempDir()
	writeProductionReferenceFixture(t, root, "internal/a.go", "package a\nvar workspaceRef = \"workspace\"\nvar workspaceRef = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "web/a.ts", "export const workspace = true\n")
	writeProductionReferenceFixture(t, root, "internal/a_test.go", "var ignored = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "docs/a.go", "var ignored = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "internal/platform/migrations/001_workspace.sql", "SELECT 'workspace';\n")
	writeProductionReferenceFixture(t, root, "internal/generated/output.go", "var ignored = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "nested/go.mod", "module nested\n")
	writeProductionReferenceFixture(t, root, "nested/runtime.go", "var ignored = \"workspace\"\n")

	first, err := ObserveProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ObserveProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("observations = %d and %d, want three each", len(first), len(second))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("observation %d changed between scans: %#v != %#v", index, first, second)
		}
	}
	if first[0].Category != "browser" || first[1].Category != "go" || first[2].Category != "go" {
		t.Fatalf("observations are not category sorted: %#v", first)
	}
	if first[1].Hash != first[2].Hash {
		t.Fatalf("duplicate normalized lines did not retain the same hash: %#v", first)
	}
	if err := CheckNoWorkspaceProductionReferences(root); err == nil || !strings.Contains(err.Error(), "internal/a.go") {
		t.Fatalf("zero-reference invariant did not report fixture: %v", err)
	}
}

func TestProductionReferenceSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeProductionReferenceFixture(t, outside, "linked.go", "package linked\nvar workspace = true\n")
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "internal", "linked.go")
	if err := os.Symlink(filepath.Join(outside, "linked.go"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	observed, err := ObserveProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 0 {
		t.Fatalf("symlink target was scanned: %#v", observed)
	}
}

func writeProductionReferenceFixture(t *testing.T, root, path, body string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
