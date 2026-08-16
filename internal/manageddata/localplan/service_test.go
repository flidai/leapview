package localplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
)

func TestServicePlanDiscoversExactAndRecursiveSourcesDeterministically(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "upload")
	writeFile(t, filepath.Join(from, "exact.csv"), "exact")
	writeFile(t, filepath.Join(from, "north", "a.csv"), "north")
	writeFile(t, filepath.Join(from, "north", "deep", "b.csv"), "deep")
	writeFile(t, filepath.Join(from, "north", "ignore.txt"), "ignore")

	project := testProject(filepath.Join(root, "authored-project"), Connection{Kind: "managed"}, map[string]Source{
		"warehouse.recursive": {Connection: "warehouse", Path: "**/*.csv", Format: "csv"},
		"warehouse.exact":     {Connection: "warehouse", Path: "exact.csv", Format: "csv"},
		"warehouse.overlap":   {Connection: "warehouse", Path: "north/**/*.csv", Format: "csv"},
		"other.ignored":       {Connection: "other", Path: "ignored.csv", Format: "csv"},
	})
	service := testService(project)
	previous := manageddata.Manifest{Files: []manageddata.File{
		{Path: "exact.csv", Size: 5, SHA256: digest("exact")},
		{Path: "removed.csv", Size: 7, SHA256: digest("removed")},
	}}

	got, err := service.Plan(context.Background(), Request{
		ProjectPath: "project.yaml",
		Connection:  "warehouse",
		From:        from,
		Previous:    &previous,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if got.Root != from {
		t.Fatalf("Root = %q, want %q", got.Root, from)
	}
	if want := []string{"warehouse.exact", "warehouse.overlap", "warehouse.recursive"}; !equalStrings(got.Sources, want) {
		t.Fatalf("Sources = %#v, want %#v", got.Sources, want)
	}
	wantFiles := []manageddata.File{
		{Path: "exact.csv", Size: 5, SHA256: digest("exact")},
		{Path: "north/a.csv", Size: 5, SHA256: digest("north")},
		{Path: "north/deep/b.csv", Size: 4, SHA256: digest("deep")},
	}
	if !equalFiles(got.Manifest.Files, wantFiles) {
		t.Fatalf("Manifest.Files = %#v, want %#v", got.Manifest.Files, wantFiles)
	}
	if len(got.Diff.Unchanged) != 1 || got.Diff.Unchanged[0].Path != "exact.csv" {
		t.Fatalf("Diff.Unchanged = %#v", got.Diff.Unchanged)
	}
	if len(got.Diff.Added) != 2 || got.Diff.Added[0].Path != "north/a.csv" || got.Diff.Added[1].Path != "north/deep/b.csv" {
		t.Fatalf("Diff.Added = %#v", got.Diff.Added)
	}
	if len(got.Diff.Removed) != 1 || got.Diff.Removed[0].Path != "removed.csv" {
		t.Fatalf("Diff.Removed = %#v", got.Diff.Removed)
	}

	second, err := service.Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: from})
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest.RevisionID() != second.Manifest.RevisionID() {
		t.Fatalf("revision IDs differ: %q != %q", got.Manifest.RevisionID(), second.Manifest.RevisionID())
	}
	if len(second.Diff.Added) != len(second.Manifest.Files) {
		t.Fatalf("diff without previous manifest = %#v", second.Diff)
	}
}

func TestServicePlanResolvesNameAndCanonicalConnectionID(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "upload")
	writeFile(t, filepath.Join(from, "orders.csv"), "orders")
	project := testProject(root, Connection{ID: "connection:orders", Kind: "managed"}, map[string]Source{
		"orders.file": {Connection: "warehouse", Path: "orders.csv", Format: "csv"},
	})

	byName, err := testService(project).Plan(context.Background(), Request{
		ProjectPath: "project.yaml", Connection: "warehouse", From: from,
	})
	if err != nil {
		t.Fatalf("Plan(name) error = %v", err)
	}
	if byName.Connection != "connection:orders" || byName.ConnectionName != "warehouse" {
		t.Fatalf("Plan(name) identity = %#v", byName)
	}

	byID, err := testService(project).Plan(context.Background(), Request{
		ProjectPath: "project.yaml", Connection: "connection:orders", From: from,
	})
	if err != nil {
		t.Fatalf("Plan(ID) error = %v", err)
	}
	if byID.Connection != byName.Connection || byID.ConnectionName != byName.ConnectionName || byID.Manifest.RevisionID() != byName.Manifest.RevisionID() {
		t.Fatalf("Plan(ID) identity = %#v, by name = %#v", byID, byName)
	}
}

func TestServicePlanRejectsMissingOrInvalidConnectionID(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "upload")
	writeFile(t, filepath.Join(from, "orders.csv"), "orders")
	for _, test := range []struct {
		name string
		id   string
		want string
	}{
		{name: "missing", want: "no canonical stable ID"},
		{name: "invalid", id: "not a valid resource id", want: "invalid stable ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
				"orders.file": {Connection: "warehouse", Path: "orders.csv", Format: "csv"},
			})
			project.Connections["warehouse"] = Connection{ID: test.id, Kind: "managed"}
			_, err := testService(project).Plan(context.Background(), Request{
				ProjectPath: "project.yaml", Connection: "warehouse", From: from,
			})
			assertErrorContains(t, err, test.want)
		})
	}
}

func TestServicePlanRejectsPathsOutsideConnectionRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside.csv"), "outside")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
		"warehouse.escape": {Connection: "warehouse", Path: "../outside.csv", Format: "csv"},
	})

	_, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: filepath.Join(root, "data")})
	assertErrorContains(t, err, "escapes connection root")
}

func TestServicePlanRejectsAbsoluteManagedSourcePath(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "data")
	file := filepath.Join(from, "file.csv")
	writeFile(t, file, "data")
	project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
		"warehouse.absolute": {Connection: "warehouse", Path: file, Format: "csv"},
	})

	_, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: from})
	assertErrorContains(t, err, "managed path must be relative")
}

func TestServicePlanDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside.csv"), "outside")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.csv"), filepath.Join(root, "data", "linked.csv")); err != nil {
		t.Fatal(err)
	}
	project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
		"warehouse.link": {Connection: "warehouse", Path: "linked.csv", Format: "csv"},
	})

	_, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: filepath.Join(root, "data")})
	assertErrorContains(t, err, "symbolic link")
}

func TestServicePlanRecursiveGlobDoesNotTraverseDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside", "secret.csv"), "secret")
	writeFile(t, filepath.Join(root, "data", "visible.csv"), "visible")
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "data", "linked")); err != nil {
		t.Fatal(err)
	}
	project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
		"warehouse.files": {Connection: "warehouse", Path: "**/*.csv", Format: "csv"},
	})

	result, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: filepath.Join(root, "data")})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(result.Manifest.Files) != 1 || result.Manifest.Files[0].Path != "visible.csv" {
		t.Fatalf("Manifest.Files = %#v", result.Manifest.Files)
	}
}

func TestServicePlanRejectsNonRegularAndUnmatchedSources(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "data", "folder.csv"), 0o755); err != nil {
			t.Fatal(err)
		}
		project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
			"warehouse.directory": {Connection: "warehouse", Path: "folder.csv", Format: "csv"},
		})

		_, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: filepath.Join(root, "data")})
		assertErrorContains(t, err, "regular file")
	})

	t.Run("glob", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
			"warehouse.empty": {Connection: "warehouse", Path: "**/*.csv", Format: "csv"},
		})

		_, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: filepath.Join(root, "data")})
		assertErrorContains(t, err, "matched no files")
	})
}

func TestServicePlanValidatesConnectionAndRequest(t *testing.T) {
	root := t.TempDir()
	service := testService(testProject(root, Connection{Kind: "s3"}, nil))

	_, err := service.Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse"})
	assertErrorContains(t, err, "cannot plan managed data")

	_, err = service.Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "missing"})
	assertErrorContains(t, err, "unknown connection")

	_, err = service.Plan(context.Background(), Request{Connection: "warehouse"})
	assertErrorContains(t, err, "project path is required")

	managed := testService(testProject(root, Connection{Kind: "managed"}, nil))
	_, err = managed.Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse"})
	assertErrorContains(t, err, "from is required")

	for name, connection := range map[string]Connection{
		"root":  {Kind: "managed", Root: "authored"},
		"scope": {Kind: "managed", Scope: "authored"},
	} {
		t.Run("managed authored "+name, func(t *testing.T) {
			service := testService(testProject(root, connection, nil))
			_, err := service.Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: root})
			assertErrorContains(t, err, "cannot define root or scope")
		})
	}
}

func TestServicePlanRejectsRemovedLocalConnection(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "legacy", "file.csv"), "legacy")
	project := testProject(root, Connection{Kind: "local", Root: "legacy"}, map[string]Source{
		"warehouse.file": {Connection: "warehouse", Path: "file.csv", Format: "csv"},
	})

	_, err := testService(project).Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: root})
	assertErrorContains(t, err, `connection kind "local" cannot plan managed data`)
}

func TestServicePlanHonorsManifestLimits(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data", "large.csv"), "large")
	project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
		"warehouse.large": {Connection: "warehouse", Path: "large.csv", Format: "csv"},
	})

	_, err := testService(project).Plan(context.Background(), Request{
		ProjectPath: "project.yaml",
		Connection:  "warehouse",
		From:        filepath.Join(root, "data"),
		Limits:      manageddata.Limits{MaxFileBytes: 4},
	})
	assertErrorContains(t, err, "maximum file size")
}

func TestServicePlanRejectsFileMutationWhileHashing(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "data", "changing.csv")
	writeFile(t, file, "before")
	project := testProject(root, Connection{Kind: "managed"}, map[string]Source{
		"warehouse.changing": {Connection: "warehouse", Path: "changing.csv", Format: "csv"},
	})
	service := testService(project)
	service.files = mutatingFileSystem{path: file}

	_, err := service.Plan(context.Background(), Request{ProjectPath: "project.yaml", Connection: "warehouse", From: filepath.Join(root, "data")})
	assertErrorContains(t, err, "changed while hashing")
}

func TestServicePlanPropagatesProjectLoadErrors(t *testing.T) {
	service := NewService(func(string) (Project, error) {
		return Project{}, errors.New("load failed")
	})
	_, err := service.Plan(context.Background(), Request{
		ProjectPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Connection:  "warehouse",
	})
	if err == nil {
		t.Fatal("Plan() error = nil, want project load error")
	}
}

func testProject(_ string, connection Connection, sources map[string]Source) Project {
	if connection.ID == "" {
		connection.ID = "connection:warehouse"
	}
	return Project{
		Connections: map[string]Connection{
			"warehouse": connection,
			"other":     {ID: "connection:other", Kind: "managed"},
		},
		Sources: sources,
	}
}

func testService(project Project) *Service {
	return NewService(func(string) (Project, error) { return project, nil })
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func equalFiles(got, want []manageddata.File) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}

type mutatingFileSystem struct {
	path string
}

func (m mutatingFileSystem) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(name)
}

func (m mutatingFileSystem) Open(name string) (openedFile, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return &mutatingFile{File: file, path: m.path}, nil
}

func (m mutatingFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

type mutatingFile struct {
	*os.File
	path      string
	statCalls int
}

func (f *mutatingFile) Stat() (os.FileInfo, error) {
	f.statCalls++
	if f.statCalls == 2 {
		if err := os.WriteFile(f.path, []byte("changed while hashing"), 0o644); err != nil {
			return nil, err
		}
	}
	return f.File.Stat()
}
