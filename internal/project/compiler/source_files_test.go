package compiler

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceFilesFromAssemblyReturnsOnlyResolvedReachableFiles(t *testing.T) {
	root := t.TempDir()
	path := func(value string) string { return filepath.Join(root, filepath.FromSlash(value)) }
	project := sourceAssembly{
		BaseDir: root,
		ConnectionPaths: map[string]string{
			"warehouse": path("connections/warehouse.yaml"),
		},
		SourcePaths: map[string]string{"orders": path("sources/orders.yaml")},
	}
	got, err := sourceFilesFromAssembly(root, project)
	require.NoError(t, err)
	want := []string{
		path("connections/warehouse.yaml"),
		path("sources/orders.yaml"),
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("source files = %#v, want %#v", got, want)
	}
}

func TestSourceFilesFromAssemblyIncludesDashboardFragments(t *testing.T) {
	root := t.TempDir()
	dashboardPath := filepath.Join(root, "dashboards", "sales.yaml")
	fragmentPath := filepath.Join(root, "dashboards", "fragments", "visuals.yaml")
	if err := os.MkdirAll(filepath.Dir(fragmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dashboardPath, []byte(`apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales}
spec:
  semanticModel: sales
  filters: []
  includes: {visuals: [fragments/visuals.yaml]}
  visuals: {}
  pages: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fragmentPath, []byte(`visuals:
  revenue:
    type: bar
    query: {type: aggregate, dimensions: [], metrics: [revenue]}
    presentation: {type: cartesian}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sourceFilesFromAssembly(root, sourceAssembly{
		BaseDir:        root,
		DashboardPaths: map[string]string{"sales": dashboardPath},
	})
	require.NoError(t, err)
	want := []string{dashboardPath, fragmentPath}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("source files = %#v, want %#v", got, want)
	}
}

func TestSourceFilesFromAssemblyRejectsResolvedPathOutsideProject(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	_, err := sourceFilesFromAssembly(root, sourceAssembly{
		BaseDir: root,
		ConnectionPaths: map[string]string{
			"escaped": outside,
		},
	})
	if err == nil {
		t.Fatal("resolved source outside project boundary was accepted")
	}
}

func TestSourceFilesFromAssemblyRejectsSymlinkEscapingProject(t *testing.T) {
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

	_, err := sourceFilesFromAssembly(root, sourceAssembly{
		BaseDir:     root,
		SourcePaths: map[string]string{"escaped": linkPath},
	})
	if err == nil {
		t.Fatal("symlinked source outside project boundary was accepted")
	}
}
