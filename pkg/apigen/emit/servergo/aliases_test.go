package servergo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmit_UsesGeneratedAliasesForReferencedPathQueryAndHeaderSchemas(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"ResourceKind": {Type: "string", Enum: []string{"project", "dashboard"}},
		},
		Endpoints: []ir.Endpoint{{
			Method: "get", Path: "/projects/{project}/resources", OperationID: "listResources",
			Parameters: []ir.Parameter{
				{Name: "project", In: "path", Required: true, Schema: ir.SchemaRef{Ref: "ResourceKind"}},
				{Name: "kind", In: "query", Schema: ir.SchemaRef{Ref: "ResourceKind"}},
				{Name: "X-Resource-Kind", In: "header", Required: true, Schema: ir.SchemaRef{Ref: "ResourceKind"}},
			},
			Responses: []ir.Response{{StatusCode: 200, Description: "ok"}},
		}},
	}

	generated, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(generated)
	require.Contains(t, content, "ListResources(w http.ResponseWriter, r *http.Request, project GenSchemaResourceKind, params GenListResourcesParams, headers GenListResourcesHeaders)")
	require.Contains(t, content, "\tKind *GenSchemaResourceKind")
	require.Contains(t, content, "\tXResourceKind GenSchemaResourceKind")
	require.NotContains(t, content, "project ResourceKind")
	require.NotContains(t, content, "Kind *ResourceKind")

	// Request-model generation owns this alias. Defining it as an alias to a
	// separately declared enum here mirrors the cross-package model boundary
	// and proves the server emitter does not require a local concrete enum.
	assertGeneratedServerCompiles(t, generated, `package gen

type ResourceKind string

type GenSchemaResourceKind = ResourceKind

var _ GenSchemaResourceKind = "project"
`)
}

func apigenModuleRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	require.NoError(t, err)
	return root
}

func assertGeneratedServerCompiles(t *testing.T, generated []byte, testSource string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module generatedtest

go 1.25.8

require github.com/Yacobolo/toolbelt/apigen v0.0.0

replace github.com/Yacobolo/toolbelt/apigen => `+apigenModuleRoot(t)+`
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.apigen.gen.go"), generated, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server_test.go"), []byte(testSource), 0o644))

	cmd := exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
