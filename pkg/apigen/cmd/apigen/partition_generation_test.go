package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestRunCLI_AllGeneratesCompilingPackagePlan(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	doc := partitionedGenerationDocument()
	irPath := filepath.Join(root, "api", "gen", "json-ir.json")
	openAPIPath := filepath.Join(root, "api", "gen", "openapi.yaml")
	require.NoError(t, writeJSONDocument(irPath, doc))
	require.NoError(t, generateOpenAPI(doc, openAPIPath))

	manifestPath := writePartitionedGenerationManifest(t, root, true)
	staleSharedServer := filepath.Join(root, "internal", "shared", "api", "gen", "server.apigen.gen.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(staleSharedServer), 0o755))
	require.NoError(t, os.WriteFile(staleSharedServer, []byte("stale generated server\n"), 0o600))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"all", "-manifest", manifestPath, "-target", "partitioned"}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.Empty(t, stderr.String())

	accessServer := filepath.Join(root, "internal", "access", "api", "gen", "server.apigen.gen.go")
	accessModels := filepath.Join(root, "internal", "access", "api", "gen", "request_models.gen.go")
	accessClient := filepath.Join(root, "internal", "access", "api", "gen", "client.apigen.gen.go")
	dashboardServer := filepath.Join(root, "internal", "dashboard", "api", "gen", "server.apigen.gen.go")
	dashboardModels := filepath.Join(root, "internal", "dashboard", "api", "gen", "request_models.gen.go")
	dashboardClient := filepath.Join(root, "internal", "dashboard", "api", "gen", "client.apigen.gen.go")
	sharedModels := filepath.Join(root, "internal", "shared", "api", "gen", "request_models.gen.go")
	aggregateServer := filepath.Join(root, "internal", "app", "api", "gen", "server.apigen.gen.go")
	cliPath := filepath.Join(root, "cmd", "cli", "gen", "apigen_registry.gen.go")
	require.FileExists(t, accessServer)
	require.FileExists(t, accessModels)
	require.FileExists(t, accessClient)
	require.FileExists(t, dashboardServer)
	require.FileExists(t, dashboardModels)
	require.FileExists(t, dashboardClient)
	require.FileExists(t, sharedModels)
	require.FileExists(t, aggregateServer)
	require.FileExists(t, cliPath)
	require.NoFileExists(t, staleSharedServer)
	require.NoFileExists(t, filepath.Join(root, "internal", "dashboard", "visualization", "ir", "request_models.gen.go"))

	accessServerContent := mustReadString(t, accessServer)
	accessClientContent := mustReadString(t, accessClient)
	dashboardServerContent := mustReadString(t, dashboardServer)
	dashboardClientContent := mustReadString(t, dashboardClient)
	dashboardModelsContent := mustReadString(t, dashboardModels)
	sharedModelsContent := mustReadString(t, sharedModels)
	aggregateServerContent := mustReadString(t, aggregateServer)
	cliContent := mustReadString(t, cliPath)
	require.Contains(t, accessServerContent, `OperationID: "getCurrentPrincipal"`)
	require.Contains(t, accessClientContent, `GenOperationGetCurrentPrincipal = "getCurrentPrincipal"`)
	require.Contains(t, accessClientContent, "func (client *GenClient) GetCurrentPrincipal(")
	require.NotContains(t, accessServerContent, `OperationID: "getDashboard"`)
	require.Contains(t, dashboardServerContent, `OperationID: "getDashboard"`)
	require.Contains(t, dashboardClientContent, `GenOperationGetDashboard = "getDashboard"`)
	require.NotContains(t, dashboardServerContent, `OperationID: "getCurrentPrincipal"`)
	require.Contains(t, dashboardModelsContent, `accessapi "generatedintegration/internal/access/api/gen"`)
	require.Contains(t, dashboardModelsContent, `visualizationir "generatedintegration/internal/dashboard/visualization/ir"`)
	require.Contains(t, sharedModelsContent, "type Shared struct")
	require.Contains(t, aggregateServerContent, `accessapi "generatedintegration/internal/access/api/gen"`)
	require.Contains(t, aggregateServerContent, `dashboardapi "generatedintegration/internal/dashboard/api/gen"`)
	require.NotContains(t, aggregateServerContent, `"generatedintegration/internal/shared/api/gen"`)
	require.Contains(t, aggregateServerContent, "accessapi.RegisterAPIGenRoutes(router, servers.Access)")
	require.Contains(t, aggregateServerContent, "dashboardapi.RegisterAPIGenStrictRoutes(router, servers.Dashboard, responders.Dashboard)")
	require.Contains(t, aggregateServerContent, "func GetEmbeddedOpenAPISpec() (map[string]any, error)")
	require.Contains(t, aggregateServerContent, "for operationID, contract := range accessapi.GetAPIGenOperationContracts()")
	require.Contains(t, aggregateServerContent, "for operationID, contract := range dashboardapi.GetAPIGenOperationContracts()")
	require.Contains(t, aggregateServerContent, "for name, contract := range accessapi.GetAPIGenToolContracts()")
	require.NotContains(t, aggregateServerContent, "dashboardapi.GetAPIGenToolContracts()")
	require.Contains(t, aggregateServerContent, `\"/v1/principal\"`)
	require.Contains(t, aggregateServerContent, `\"/v1/dashboard\"`)
	require.Contains(t, cliContent, `"getCurrentPrincipal"`)
	require.Contains(t, cliContent, `"getDashboard"`)

	writePartitionedGenerationGoModule(t, root)
	writeGeneratedServerErrorStub(t, filepath.Dir(accessServer), "accessapi")
	writeGeneratedServerErrorStub(t, filepath.Dir(dashboardServer), "dashboardapi")
	writeExternalVisualizationModels(t, root)
	writeGeneratedAggregateTest(t, filepath.Dir(aggregateServer))
	runGeneratedGoTest(t, root)
}

func TestRunCLI_ServerPackagePlanPreflightPreservesExistingOutputs(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	doc := partitionedGenerationDocument()
	for index := range doc.Endpoints {
		if doc.Endpoints[index].OperationID != "getDashboard" {
			continue
		}
		doc.Endpoints[index].RequestBody = &ir.RequestBody{
			Contents: jsonContent(ir.SchemaRef{Type: "object"}),
		}
	}
	irPath := filepath.Join(root, "api", "gen", "json-ir.json")
	openAPIPath := filepath.Join(root, "api", "gen", "openapi.yaml")
	require.NoError(t, writeJSONDocument(irPath, doc))
	require.NoError(t, generateOpenAPI(doc, openAPIPath))
	manifestPath := writePartitionedGenerationManifest(t, root, false)

	outputs := []string{
		filepath.Join(root, "internal", "access", "api", "gen", "server.apigen.gen.go"),
		filepath.Join(root, "internal", "access", "api", "gen", "request_models.gen.go"),
		filepath.Join(root, "internal", "dashboard", "api", "gen", "server.apigen.gen.go"),
		filepath.Join(root, "internal", "dashboard", "api", "gen", "request_models.gen.go"),
		filepath.Join(root, "internal", "shared", "api", "gen", "request_models.gen.go"),
		filepath.Join(root, "internal", "app", "api", "gen", "server.apigen.gen.go"),
	}
	for _, output := range outputs {
		require.NoError(t, os.MkdirAll(filepath.Dir(output), 0o755))
		require.NoError(t, os.WriteFile(output, []byte("existing output\n"), 0o600))
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"server", "-manifest", manifestPath, "-target", "partitioned"}, &stdout, &stderr)
	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "requires a named IR schema")
	for _, output := range outputs {
		require.Equal(t, "existing output", mustReadString(t, output))
	}
}

func TestRunCLI_ServerRemovesStaleAggregateWithoutEndpointPartitions(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	doc := partitionedGenerationDocument()
	doc.Endpoints = nil
	doc.Contracts = []ir.Contract{{
		Name:   "Shared",
		Schema: ir.SchemaRef{Ref: "Shared"},
	}}
	irPath := filepath.Join(root, "api", "gen", "json-ir.json")
	openAPIPath := filepath.Join(root, "api", "gen", "openapi.yaml")
	require.NoError(t, writeJSONDocument(irPath, doc))
	require.NoError(t, generateOpenAPI(doc, openAPIPath))
	manifestPath := writePartitionedGenerationManifest(t, root, false)

	staleAggregate := filepath.Join(root, "internal", "app", "api", "gen", "server.apigen.gen.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(staleAggregate), 0o755))
	require.NoError(t, os.WriteFile(staleAggregate, []byte("stale aggregate\n"), 0o600))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"server", "-manifest", manifestPath, "-target", "partitioned"}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.NoFileExists(t, staleAggregate)
	require.FileExists(t, filepath.Join(root, "internal", "access", "api", "gen", "request_models.gen.go"))
	require.FileExists(t, filepath.Join(root, "internal", "dashboard", "api", "gen", "request_models.gen.go"))
	require.FileExists(t, filepath.Join(root, "internal", "shared", "api", "gen", "request_models.gen.go"))
}

func TestRenderPartitionedServerDocument_RejectsExternalToLocalContractCycle(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Cycle", Version: "1.0.0", Namespace: "ExampleAPI"},
		Schemas: map[string]ir.Schema{
			"Dashboard": {
				Type:      "object",
				Namespace: "ExampleAPI.Dashboard",
				Properties: map[string]ir.SchemaProperty{
					"visual": {Schema: ir.SchemaRef{Ref: "ExternalVisual"}},
				},
			},
			"ExternalVisual": {
				Type:      "object",
				Namespace: "External.Visualization",
				Properties: map[string]ir.SchemaProperty{
					"dashboard": {Schema: ir.SchemaRef{Ref: "Dashboard"}},
				},
			},
		},
		Endpoints: []ir.Endpoint{
			endpointWithResponse("getDashboard", "ExampleAPI.Dashboard", "Dashboard"),
		},
	}
	plan := goPackagePlan{
		Unmatched: unmatchedNamespaceError,
		Packages: []namespaceGoPackageOutput{{
			Namespace: "ExampleAPI.Dashboard",
			Output: resolvedGoPackageOutput{
				Dir:               t.TempDir(),
				Package:           "dashboardapi",
				ImportPath:        "example.com/dashboard",
				ServerFile:        "server.apigen.gen.go",
				RequestModelsFile: "request_models.gen.go",
			},
		}},
	}
	imports := map[string]contractImportSpec{
		"External.Visualization": {
			GoPackage: "example.com/visualization",
			GoAlias:   "visualization",
		},
	}

	_, err := renderPartitionedServerDocument(doc, plan, "", imports)
	require.ErrorContains(t, err, `contract import cycle: external schema "ExternalVisual" references local schema "Dashboard"`)
}

func TestApplyGeneratedOutputChanges_RejectsDuplicatesBeforeReplacement(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "generated.go")
	require.NoError(t, os.WriteFile(path, []byte("existing\n"), 0o600))

	err := applyGeneratedOutputChanges([]generatedOutputChange{
		{Path: path, Content: []byte("first")},
		{Path: path, Content: []byte("second")},
	})
	require.ErrorContains(t, err, "is declared more than once")
	require.Equal(t, "existing", mustReadString(t, path))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "generated.go", entries[0].Name())
}

func partitionedGenerationDocument() ir.Document {
	return ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/v1"},
		Info: ir.Info{
			Title: "Partitioned API", Version: "1.0.0", Namespace: "ExampleAPI",
		},
		OpenAPI: ir.OpenAPI{Version: "3.0.0"},
		Schemas: map[string]ir.Schema{
			"Dashboard": {
				Type:      "object",
				Namespace: "ExampleAPI.Dashboard",
				Properties: map[string]ir.SchemaProperty{
					"owner":  {Schema: ir.SchemaRef{Ref: "Principal"}},
					"visual": {Schema: ir.SchemaRef{Ref: "ExternalVisual"}},
				},
				Required: []string{"owner", "visual"},
			},
			"Principal": {
				Type:      "object",
				Namespace: "ExampleAPI.Access",
				Properties: map[string]ir.SchemaProperty{
					"id":     {Schema: ir.SchemaRef{Type: "string"}},
					"shared": {Schema: ir.SchemaRef{Ref: "Shared"}},
				},
				Required: []string{"id", "shared"},
			},
			"Shared": {
				Type:      "object",
				Namespace: "ExampleAPI.Shared",
				Properties: map[string]ir.SchemaProperty{
					"revision": {Schema: ir.SchemaRef{Type: "integer", Format: "int64"}},
				},
				Required: []string{"revision"},
			},
			"ExternalVisual": {
				Type:      "object",
				Namespace: "External.Visualization",
				Properties: map[string]ir.SchemaProperty{
					"kind": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"kind"},
			},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/principal",
				OperationID: "getCurrentPrincipal",
				Namespace:   "ExampleAPI.Access",
				CLI:         &ir.CLI{Command: []string{"access", "principal"}},
				Tool:        &ir.Tool{Name: "get_current_principal", Effect: "read", Output: ir.ToolOutput{Mode: "raw"}},
				Responses: []ir.Response{{
					StatusCode: 200, Description: "ok", Contents: jsonContent(ir.SchemaRef{Ref: "Principal"}),
				}},
			},
			{
				Method:      "get",
				Path:        "/dashboard",
				OperationID: "getDashboard",
				Namespace:   "ExampleAPI.Dashboard",
				CLI:         &ir.CLI{Command: []string{"dashboard", "get"}},
				Responses: []ir.Response{{
					StatusCode: 200, Description: "ok", Contents: jsonContent(ir.SchemaRef{Ref: "Dashboard"}),
				}},
			},
		},
	}
}

func writePartitionedGenerationManifest(t *testing.T, root string, withCLI bool) string {
	t.Helper()

	cli := ""
	if withCLI {
		cli = `    cli_out:
      dir: cmd/cli/gen
      package: gen
      file: apigen_registry.gen.go
`
	}
	content := `targets:
  - name: partitioned
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      aggregate:
        dir: internal/app/api/gen
        package: aggregate
      packages:
        ExampleAPI.Access:
          dir: internal/access/api/gen
          package: accessapi
          import_path: generatedintegration/internal/access/api/gen
          client_file: client.apigen.gen.go
        ExampleAPI.Dashboard:
          dir: internal/dashboard/api/gen
          package: dashboardapi
          import_path: generatedintegration/internal/dashboard/api/gen
          client_file: client.apigen.gen.go
        ExampleAPI.Shared:
          dir: internal/shared/api/gen
          package: sharedapi
          import_path: generatedintegration/internal/shared/api/gen
    contract_imports:
      External.Visualization:
        go_package: generatedintegration/internal/dashboard/visualization/ir
        go_alias: visualizationir
` + cli
	path := filepath.Join(root, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func writePartitionedGenerationGoModule(t *testing.T, root string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(`module generatedintegration

go 1.25.8

require github.com/Yacobolo/toolbelt/apigen v0.0.0

replace github.com/Yacobolo/toolbelt/apigen => `+apigenModuleRoot(t)+`
`), 0o644))
}

func writeGeneratedServerErrorStub(t *testing.T, dir, packageName string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "app_types.go"), []byte("package "+packageName+`

type Error struct {
	Code int32
	Message string
}
`), 0o644))
}

func writeGeneratedAggregateTest(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "aggregate_test.go"), []byte(`package aggregate

import "testing"

func TestAggregateProtocolMetadata(t *testing.T) {
	contracts := GetAPIGenOperationContracts()
	if len(contracts) != 2 {
		t.Fatalf("operation contracts = %d, want 2", len(contracts))
	}
	contract := contracts["getCurrentPrincipal"]
	contract.Tags = append(contract.Tags, "mutated")
	if fresh, _ := GetAPIGenOperationContract("getCurrentPrincipal"); len(fresh.Tags) == len(contract.Tags) {
		t.Fatal("operation contract was not defensively copied")
	}
	if tools := GetAPIGenToolContracts(); len(tools) != 1 {
		t.Fatalf("tool contracts = %d, want 1", len(tools))
	}
	if _, ok := GetAPIGenToolContract("get_current_principal"); !ok {
		t.Fatal("aggregate tool contract is missing")
	}
	spec, err := GetEmbeddedOpenAPISpec()
	if err != nil {
		t.Fatal(err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok || paths["/v1/principal"] == nil || paths["/v1/dashboard"] == nil {
		t.Fatalf("aggregate OpenAPI paths = %#v", spec["paths"])
	}
}
`), 0o644))
}

func writeExternalVisualizationModels(t *testing.T, root string) {
	t.Helper()

	dir := filepath.Join(root, "internal", "dashboard", "visualization", "ir")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models.go"), []byte(`package ir

type ExternalVisual struct {
	Kind string
}
`), 0o644))
}

func runGeneratedGoTest(t *testing.T, root string) {
	t.Helper()

	cmd := exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = root
	if realHome := os.Getenv("HOME"); strings.TrimSpace(realHome) != "" {
		cmd.Env = append(os.Environ(), "HOME="+realHome, "GOMODCACHE="+filepath.Join(realHome, "go", "pkg", "mod"))
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
