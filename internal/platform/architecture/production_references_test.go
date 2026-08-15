package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceProductionReferenceBaseline(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, workspaceProductionReferenceBaseline))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := ParseWorkspaceProductionReferenceBaseline(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkspaceProductionReferenceBaseline(root, baseline); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceProductionReferenceBaselineIsShrinkOnly(t *testing.T) {
	root := t.TempDir()
	path := "internal/example.go"
	writeProductionReferenceFixture(t, root, path, "package example\nvar workspace = true\n")
	baseline, err := ObserveWorkspaceProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkspaceProductionReferenceBaseline(root, baseline); err != nil {
		t.Fatalf("baseline rejected unchanged observation: %v", err)
	}

	writeProductionReferenceFixture(t, root, path, "package example\nvar workspace = true\nvar anotherWorkspace = true\n")
	if err := CheckWorkspaceProductionReferenceBaseline(root, baseline); err == nil || !strings.Contains(err.Error(), "internal/example.go") {
		t.Fatalf("addition was not rejected with a readable diagnostic: %v", err)
	}

	writeProductionReferenceFixture(t, root, path, "package example\n")
	if err := CheckWorkspaceProductionReferenceBaseline(root, baseline); err != nil {
		t.Fatalf("removal was rejected: %v", err)
	}
}

func TestWorkspaceProductionReferenceObservationDeterministicAndPreservesMultiplicity(t *testing.T) {
	root := t.TempDir()
	writeProductionReferenceFixture(t, root, "internal/a.go", "package a\nvar workspaceRef = \"workspace\"\nvar workspaceRef = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "web/a.ts", "export const workspace = true\n")
	writeProductionReferenceFixture(t, root, "internal/a_test.go", "var ignored = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "docs/a.go", "var ignored = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "internal/platform/migrations/001_workspace.sql", "SELECT 'workspace';\n")
	writeProductionReferenceFixture(t, root, "internal/generated/output.go", "var ignored = \"workspace\"\n")
	writeProductionReferenceFixture(t, root, "nested/go.mod", "module nested\n")
	writeProductionReferenceFixture(t, root, "nested/runtime.go", "var ignored = \"workspace\"\n")

	first, err := ObserveWorkspaceProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ObserveWorkspaceProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("observations = %d and %d, want three each", len(first), len(second))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("observation %d changed between scans: %#v != %#v", index, first[index], second[index])
		}
	}
	if first[0].Category != "browser" || first[1].Category != "go" || first[2].Category != "go" {
		t.Fatalf("observations are not category sorted: %#v", first)
	}
	if first[1].Hash != first[2].Hash {
		t.Fatalf("duplicate normalized lines did not retain the same hash: %#v", first)
	}
}

func TestWorkspaceProductionReferenceSkipsSymlinks(t *testing.T) {
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
	observed, err := ObserveWorkspaceProductionReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 0 {
		t.Fatalf("symlink target was scanned: %#v", observed)
	}
}

func TestParseWorkspaceProductionReferenceBaseline(t *testing.T) {
	input := "# comment\n\ngo\tinternal/a.go\tsha256:" + strings.Repeat("a", 64) + "\ngo\tinternal/a.go\tsha256:" + strings.Repeat("a", 64) + "\n"
	rows, err := ParseWorkspaceProductionReferenceBaseline(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != rows[1] {
		t.Fatalf("parsed rows = %#v, want two duplicate rows", rows)
	}
	if _, err := ParseWorkspaceProductionReferenceBaseline(strings.NewReader("go internal/a.go bad\n")); err == nil {
		t.Fatal("malformed baseline row was accepted")
	}
	unsorted := "go\tz.go\tsha256:" + strings.Repeat("a", 64) + "\ngo\ta.go\tsha256:" + strings.Repeat("b", 64) + "\n"
	if _, err := ParseWorkspaceProductionReferenceBaseline(strings.NewReader(unsorted)); err == nil {
		t.Fatal("unsorted baseline rows were accepted")
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
