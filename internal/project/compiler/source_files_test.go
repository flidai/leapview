package compiler

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceFilesFromProjectReturnsOnlyResolvedReachableFiles(t *testing.T) {
	root := t.TempDir()
	path := func(value string) string { return filepath.Join(root, filepath.FromSlash(value)) }
	project := Project{
		BaseDir: root,
		ConnectionPaths: map[string]string{
			"warehouse": path("connections/warehouse.yaml"),
		},
		SourcePaths: map[string]string{"orders": path("sources/orders.yaml")},
	}
	projectPath := path("leapview.yaml")
	got, err := sourceFilesFromProject(projectPath, project)
	require.NoError(t, err)
	want := []string{
		projectPath,
		path("connections/warehouse.yaml"),
		path("sources/orders.yaml"),
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("source files = %#v, want %#v", got, want)
	}
}

func TestSourceFilesFromProjectRejectsResolvedPathOutsideProject(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "leapview.yaml")
	outside := filepath.Join(filepath.Dir(root), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	_, err := sourceFilesFromProject(projectPath, Project{
		BaseDir: root,
		ConnectionPaths: map[string]string{
			"escaped": outside,
		},
	})
	if err == nil {
		t.Fatal("resolved source outside project boundary was accepted")
	}
}

func TestSourceFilesFromProjectRejectsSymlinkEscapingProject(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "secret.yaml")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "source.yaml")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Fatal(err)
	}

	_, err := sourceFilesFromProject(filepath.Join(root, "leapview.yaml"), Project{
		BaseDir:     root,
		SourcePaths: map[string]string{"escaped": linkPath},
	})
	if err == nil {
		t.Fatal("symlinked source outside project boundary was accepted")
	}
}
