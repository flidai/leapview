package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

func TestOssieCommandRoutesExportAndImportThroughProjectCompiler(t *testing.T) {
	projectPath := "../../../dashboards/leapview.yaml"
	var exported bytes.Buffer
	export := OssieCommand(context.Background())
	export.SetOut(&exported)
	export.SetArgs([]string{"ossie", "export", "--project", projectPath, "--semantic-model", "sales"})
	if err := export.Execute(); err != nil {
		t.Fatalf("export command: %v", err)
	}
	if !strings.Contains(exported.String(), `"version": "0.2.0.dev0"`) {
		t.Fatalf("export command output is not pinned Ossie: %s", exported.String()[:min(exported.Len(), 200)])
	}

	var imported bytes.Buffer
	importCommand := OssieCommand(context.Background())
	importCommand.SetIn(strings.NewReader(exported.String()))
	importCommand.SetOut(&imported)
	importCommand.SetArgs([]string{"ossie", "import", "--project", projectPath, "--in", "-"})
	if err := importCommand.Execute(); err != nil {
		t.Fatalf("import command: %v", err)
	}
	if !strings.Contains(imported.String(), "kind: SemanticModel") || !strings.Contains(imported.String(), "metrics:") {
		t.Fatalf("import command did not emit native resource: %s", imported.String())
	}
	if err := configschema.ValidateBytes(configschema.KindSemanticModel, "sales.yaml", imported.Bytes()); err != nil {
		t.Fatalf("import command emitted invalid native schema: %v\n%s", err, imported.String())
	}
	projectCopy := t.TempDir()
	copyTree(t, "../../../dashboards", projectCopy)
	if err := os.WriteFile(filepath.Join(projectCopy, "semantic-models", "sales.yaml"), imported.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := projectcompiler.LoadProject(filepath.Join(projectCopy, "leapview.yaml")); err != nil {
		t.Fatalf("import command output did not compile as native project resource: %v", err)
	}
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
