package developmentinput_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/projectinit"
	"github.com/flidai/leapview/internal/project/developmentinput"
)

func TestNamesReturnsDeclaredInputsDeterministically(t *testing.T) {
	root := initializedProject(t)
	path := filepath.Join(root, filepath.FromSlash(developmentinput.DefaultRelativePath))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "inputs:\n  sample:", "inputs:\n  z_fixture:\n    connection: sample\n    from: data/sample\n    provenance:\n      kind: synthetic\n      generator: leapview-init/v1\n      rows: 12\n      bounded: true\n    files:\n      sales.csv:\n        sha256: b09b718b0967e3d1bed215440e3d46258b0748363ea7ca6120a483cb5464af05\n        sizeBytes: 545\n  sample:", 1))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	names, err := developmentinput.Names(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sample", "z_fixture"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestLoadVerifiesExactSyntheticInput(t *testing.T) {
	root := initializedProject(t)
	selected, err := developmentinput.Load(root, "sample")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Connection != "sample" || selected.Provenance.Generator != "leapview-init/v1" || selected.Provenance.Rows != 12 || len(selected.Files) != 1 {
		t.Fatalf("selected input = %#v", selected)
	}
	if selected.Files[0].Path != "sales.csv" || selected.Files[0].SizeBytes != 545 {
		t.Fatalf("selected file = %#v", selected.Files[0])
	}
}

func TestLoadRejectsFixtureDriftAndUndeclaredFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{name: "changed", mutate: func(t *testing.T, root string) {
			path := filepath.Join(root, "data", "sample", "sales.csv")
			if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: "size does not match"},
		{name: "missing", mutate: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "data", "sample", "sales.csv")); err != nil {
				t.Fatal(err)
			}
		}, want: "is missing"},
		{name: "extra", mutate: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "data", "sample", "extra.csv"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: "undeclared file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initializedProject(t)
			test.mutate(t, root)
			if _, err := developmentinput.Load(root, "sample"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsSchemaAndYAMLAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name string
		old  string
		new  string
		want string
	}{
		{name: "version", old: "version: 1", new: "version: 2", want: "unsupported"},
		{name: "unknown field", old: "    connection: sample", new: "    connection: sample\n    credential: forbidden", want: "field credential not found"},
		{name: "duplicate", old: "version: 1", new: "version: 1\nversion: 1", want: "duplicate key"},
		{name: "escape", old: "    from: data/sample", new: "    from: ../outside", want: "escapes"},
		{name: "noncanonical root", old: "    from: data/sample", new: "    from: data/./sample", want: "must be canonical"},
		{name: "noncanonical file", old: "      sales.csv:", new: "      nested/../sales.csv:", want: "must be canonical"},
		{name: "custom tag", old: "version: 1", new: "version: !unsafe 1", want: "do not allow YAML tag"},
		{name: "explicit standard tag", old: "version: 1", new: "version: !!int 1", want: "do not allow explicit YAML tags"},
		{name: "unbounded row count", old: "      rows: 12", new: "      rows: 1000001", want: "bounded synthetic provenance"},
		{name: "oversized file declaration", old: "        sizeBytes: 545", new: "        sizeBytes: 67108865", want: "exceeds 67108864 bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initializedProject(t)
			path := filepath.Join(root, filepath.FromSlash(developmentinput.DefaultRelativePath))
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), test.old, test.new, 1))
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := developmentinput.Load(root, "sample"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsFixtureSymlink(t *testing.T) {
	root := initializedProject(t)
	path := filepath.Join(root, "data", "sample", "sales.csv")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "sales.csv")
	if err := os.WriteFile(outside, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := developmentinput.Load(root, "sample"); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsManifestAndFixtureDirectorySymlinks(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		root := initializedProject(t)
		path := filepath.Join(root, filepath.FromSlash(developmentinput.DefaultRelativePath))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "development-inputs.yaml")
		if err := os.WriteFile(outside, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := developmentinput.Load(root, "sample"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("fixture directory", func(t *testing.T) {
		root := initializedProject(t)
		source := filepath.Join(root, "data", "sample")
		outside := filepath.Join(t.TempDir(), "sample")
		if err := os.Rename(source, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, source); err != nil {
			t.Fatal(err)
		}
		if _, err := developmentinput.Load(root, "sample"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLoadRejectsExcessiveYAMLDepth(t *testing.T) {
	root := initializedProject(t)
	path := filepath.Join(root, filepath.FromSlash(developmentinput.DefaultRelativePath))
	content := "version: 1\ninputs:\n"
	for depth := 0; depth < 40; depth++ {
		content += strings.Repeat("  ", depth+1) + "key:\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := developmentinput.Load(root, "sample"); err == nil || !strings.Contains(err.Error(), "structural limits") {
		t.Fatalf("error = %v", err)
	}
}

func initializedProject(t *testing.T) string {
	t.Helper()
	root, err := projectinit.Initialize(filepath.Join(t.TempDir(), "analytics"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
