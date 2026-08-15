package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/schema"
)

func TestValidateCommandRejectsAmbiguousProjectArgs(t *testing.T) {
	project := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	opts := &rootOptions{}
	cmd := validateCommand(context.Background(), opts)
	cmd.SetArgs([]string{"--project", project, project})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("validate command error = nil, want ambiguity error")
	}
	if !strings.Contains(err.Error(), "either --project or positional project") {
		t.Fatalf("error = %v, want ambiguity message", err)
	}
}

func TestValidateCommandAcceptsShowcaseProject(t *testing.T) {
	project := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	opts := &rootOptions{}
	cmd := validateCommand(context.Background(), opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{project})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate command error = %v", err)
	}
	if !strings.Contains(out.String(), "ok "+project) {
		t.Fatalf("output = %q, want positional project path", out.String())
	}
}

func TestRunSchemaExportWritesJSONSchemas(t *testing.T) {
	outDir := t.TempDir()
	err := runSchemaExport(&rootOptions{schemaFormat: "json-schema", schemaOut: outDir})
	if err != nil {
		t.Fatalf("runSchemaExport() error = %v", err)
	}
	for _, name := range []string{
		configschema.JSONSchemaFilename(configschema.KindProject),
		configschema.JSONSchemaFilename(configschema.KindModel),
		configschema.JSONSchemaFilename(configschema.KindDashboard),
	} {
		content, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read exported schema %s: %v", name, err)
		}
		if !bytes.Contains(content, []byte(`"$schema"`)) {
			t.Fatalf("%s does not look like a JSON Schema: %s", name, content)
		}
	}
}
