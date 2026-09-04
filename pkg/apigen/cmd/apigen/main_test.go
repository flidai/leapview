package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	openapiemit "github.com/Yacobolo/toolbelt/apigen/emit/openapi"
	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func jsonContent(ref ir.SchemaRef) []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ref}}
}

type apigenIntegrationResult struct {
	Root                 string
	IRPath               string
	OpenAPIPath          string
	ServerPath           string
	RequestModelsPath    string
	CLIPath              string
	Document             ir.Document
	OpenAPIContent       string
	ServerContent        string
	RequestModelsContent string
	CLIContent           string
}

func TestRunCLI_TopLevelHelp(t *testing.T) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runCLI([]string{"--help"}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "Usage:")
	require.Contains(t, stdout.String(), "apigen <command> [flags]")
	require.Contains(t, stdout.String(), "typespec-compile")
	require.NotContains(t, stdout.String(), "cue-compile")
	require.NotContains(t, stdout.String(), "cue-bootstrap")
	require.Contains(t, stdout.String(), `Use "apigen <command> -h" for command-specific flags.`)
	require.Empty(t, stderr.String())
}

func TestRunCLI_NoArgsShowsUsage(t *testing.T) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runCLI(nil, &stdout, &stderr)
	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "Usage:")
	require.Contains(t, stderr.String(), "apigen <command> [flags]")
}

func TestRunCLI_RemovedCUECommandsFailUnsupported(t *testing.T) {
	t.Helper()

	for _, command := range []string{"cue-compile", "cue-bootstrap"} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := runCLI([]string{command}, &stdout, &stderr)
		require.Equal(t, 1, code)
		require.Empty(t, stdout.String())
		require.Contains(t, stderr.String(), `unsupported command "`+command+`"`)
	}
}

func TestGenerateArtifacts(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	irPath := filepath.Join(dir, "ir.json")

	require.NoError(t, os.WriteFile(irPath, []byte(`{
  "schema_version": "v4",
  "api": {"base_path": "/v1"},
  "info": {"title": "Duck", "version": "0.1.0", "description": "test"},
  "servers": [{"url": "https://localhost:8080", "description": "local"}],
  "schemas": {
    "HealthResponse": {
      "type": "object",
      "properties": {
        "status": {"description": "Health state", "schema": {"type": "string"}}
      },
      "required": ["status"]
    }
  },
  "endpoints": [
    {
      "method": "get",
      "path": "/healthz",
      "operation_id": "getHealth",
      "summary": "Health check",
      "tags": ["system"],
      "responses": [{"status_code": 200, "description": "ok", "contents": [{"content_type": "application/json", "body_kind": "json", "schema": {"ref": "HealthResponse"}}]}]
    }
  ]
}`), 0o644))

	doc, err := loadDocument(irPath)
	require.NoError(t, err)

	openapiPath := filepath.Join(dir, "openapi.yaml")
	serverPath := filepath.Join(dir, "server.apigen.gen.go")
	requestModelsPath := filepath.Join(dir, "request_models.gen.go")
	cliPath := filepath.Join(dir, "cli.gen.go")
	canonicalOpenAPIPath := filepath.Join(dir, "canonical-openapi.yaml")
	require.NoError(t, os.WriteFile(canonicalOpenAPIPath, []byte("openapi: 3.0.0\ninfo:\n  title: Duck\n  version: 0.1.0\npaths: {}\n"), 0o644))

	require.NoError(t, generateOpenAPI(doc, openapiPath))
	require.NoError(t, generateServer(doc, serverPath, "api", requestModelsPath, "api", canonicalOpenAPIPath))
	require.NoError(t, generateCLI(doc, cliPath, "gen"))

	_, err = os.Stat(openapiPath)
	require.NoError(t, err)
	_, err = os.Stat(serverPath)
	require.NoError(t, err)
	_, err = os.Stat(requestModelsPath)
	require.NoError(t, err)
	_, err = os.Stat(cliPath)
	require.NoError(t, err)
}

func TestDocumentJSONPreservesTransportErrors(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		TransportErrors: &ir.TransportErrors{
			Schema:      ir.SchemaRef{Ref: "Problem"},
			ContentType: "application/problem+json",
			Failures: map[string]ir.TransportFailure{
				"handler": {StatusCode: 500, Code: "internal", PublicDetail: "Internal server error."},
			},
		},
	}

	require.Same(t, doc.TransportErrors, documentJSON(doc)["transport_errors"])
}

func TestResolveCommandConfig_GroupedManifestTarget(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    strict_operation_kinds: true
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
    cli_out:
      dir: cmd/cli/gen
`), 0o644))

	config, err := resolveCommandConfig("all", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "api", "typespec"), config.TypeSpecDir)
	require.True(t, config.StrictOperationKinds)
	require.Equal(t, filepath.Join(dir, "api", "gen", "json-ir.json"), config.IRPath)
	require.Equal(t, filepath.Join(dir, "api", "gen", "openapi.yaml"), config.CanonicalOpenAPIPath)
	require.Equal(t, filepath.Join(dir, "internal", "api", "gen", "server.apigen.gen.go"), config.ServerOut)
	require.Equal(t, "gen", config.ServerPackage)
	require.Equal(t, filepath.Join(dir, "internal", "api", "gen", "request_models.gen.go"), config.RequestModelsOut)
	require.Equal(t, "gen", config.RequestModelsPackage)
	require.Equal(t, filepath.Join(dir, "cmd", "cli", "gen", "apigen_registry.gen.go"), config.CLIOut)
	require.Equal(t, "gen", config.CLIPackage)
	require.True(t, config.GenerateCLI)
}

func TestResolveCommandConfig_ContractsManifestTarget(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: signal-contracts
    kind: contracts
    typespec_dir: contracts/typespec
    typespec_entrypoint: signals/main.tsp
    ir_out: contracts/gen/json-ir.json
    go_models_out: contracts/gen/models.gen.go
    go_models_package: contracts
    ts_out: web/generated/contracts.ts
    json_schema_out: schemas/contracts.schema.json
    contract_imports:
      LeapViewVisualization:
        go_package: example.com/project/visualization
        go_alias: visualizationir
        typescript_module: ../visualization
`), 0o644))

	config, err := resolveCommandConfig("all", manifestPath, "signal-contracts", commandConfig{})
	require.NoError(t, err)
	require.Equal(t, "contracts", config.Kind)
	require.Equal(t, filepath.Join(dir, "contracts", "typespec"), config.TypeSpecDir)
	require.Equal(t, "signals/main.tsp", config.TypeSpecEntrypoint)
	require.Equal(t, filepath.Join(dir, "contracts", "gen", "json-ir.json"), config.IRPath)
	require.Equal(t, filepath.Join(dir, "contracts", "gen", "models.gen.go"), config.GoModelsOut)
	require.Equal(t, "contracts", config.GoModelsPackage)
	require.Equal(t, filepath.Join(dir, "web", "generated", "contracts.ts"), config.TSOut)
	require.Equal(t, filepath.Join(dir, "schemas", "contracts.schema.json"), config.JSONSchemaOut)
	require.Equal(t, contractImportSpec{
		GoPackage: "example.com/project/visualization", GoAlias: "visualizationir", TypeScriptModule: "../visualization",
	}, config.ContractImports["LeapViewVisualization"])

	_, err = resolveCommandConfig("openapi", manifestPath, "signal-contracts", commandConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "openapi command requires an http target")
}

func TestResolveCommandConfig_TypeSpecCompileRequiresTypeSpecDir(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
`), 0o644))

	config, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "api", "typespec"), config.TypeSpecDir)

	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
`), 0o644))
	_, err = resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "typespec_dir")
}

func TestCompileTypeSpec_GeneratesIRAndOpenAPI(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	setupManagedTypeSpecCache(t)
	irPath := filepath.Join(dir, "json-ir.json")
	openAPIPath := filepath.Join(dir, "openapi.yaml")
	fixtureDir := filepath.Join("..", "..", "typespec", "test", "fixtures", "todo")

	require.NoError(t, compileTypeSpec(fixtureDir, irPath, openAPIPath))

	doc, err := loadDocument(irPath)
	require.NoError(t, err)
	require.Equal(t, "APIGen Todo Example", doc.Info.Title)
	require.Len(t, doc.Endpoints, 5)
	content, ok := ir.PrimaryRequestBodyContent(doc.Endpoints[1])
	require.True(t, ok)
	require.NotNil(t, content.Schema)
	require.Equal(t, "CreateTodoRequest", content.Schema.Ref)
	require.Equal(t, []string{"todos", "create"}, doc.Endpoints[1].CLI.Command)
	require.FileExists(t, openAPIPath)
}

func TestCompileTypeSpec_PreservesEndpointNamespaces(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	setupManagedTypeSpecCache(t)
	typeSpecDir := filepath.Join(dir, "typespec")
	require.NoError(t, os.MkdirAll(typeSpecDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "main.tsp"), []byte(`import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Partitioned API" })
@info(#{ version: "1.0.0" })
namespace PartitionedAPI;

namespace Access {
  @route("/principal")
  @get
  op getCurrentPrincipal(): string;
}

namespace Analytics {
  namespace Reports {
    @route("/reports")
    @get
    op listReports(): string[];
  }
}
`), 0o644))
	irPath := filepath.Join(dir, "json-ir.json")
	openAPIPath := filepath.Join(dir, "openapi.yaml")

	require.NoError(t, compileTypeSpec(typeSpecDir, irPath, openAPIPath))

	doc, err := loadDocument(irPath)
	require.NoError(t, err)
	namespaces := make(map[string]string, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		namespaces[endpoint.OperationID] = endpoint.Namespace
	}
	require.Equal(t, map[string]string{
		"getCurrentPrincipal": "PartitionedAPI.Access",
		"listReports":         "PartitionedAPI.Analytics.Reports",
	}, namespaces)
	require.FileExists(t, openAPIPath)
}

func TestCompileTypeSpec_SupportsConventionalPackageImports(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	setupManagedTypeSpecCache(t)
	typeSpecDir := filepath.Join(dir, "typespec")
	require.NoError(t, os.MkdirAll(typeSpecDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "main.tsp"), []byte(`import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Conventional Import API" })
@info(#{ version: "1.0.0" })
namespace ConventionalImportAPI;

model Widget {
  id: string;
}

@route("/widgets")
@get
@apigen.cli(#{ command: #["widgets", "list"] })
op listWidgets(): Widget;
`), 0o644))
	irPath := filepath.Join(dir, "json-ir.json")
	openAPIPath := filepath.Join(dir, "openapi.yaml")

	require.NoError(t, compileTypeSpec(typeSpecDir, irPath, openAPIPath))

	doc, err := loadDocument(irPath)
	require.NoError(t, err)
	require.Equal(t, "Conventional Import API", doc.Info.Title)
	require.Len(t, doc.Endpoints, 1)
	require.Equal(t, []string{"widgets", "list"}, doc.Endpoints[0].CLI.Command)
	require.FileExists(t, openAPIPath)
}

func TestCompileTypeSpec_SupportsEntrypointWithinSharedSourceRoot(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	setupManagedTypeSpecCache(t)
	typeSpecDir := filepath.Join(dir, "typespec")
	require.NoError(t, os.MkdirAll(filepath.Join(typeSpecDir, "signals"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(typeSpecDir, "visualization"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "visualization", "main.tsp"), []byte(`namespace Visualization { model Envelope { revision: int64; } }`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "signals", "main.tsp"), []byte("import \"@yacobolo/apigen\";\nimport \"../visualization/main.tsp\";\n@apigen.`package`(#{ title: \"Signals\", version: \"1.0.0\" })\nnamespace Signals {\n  @apigen.contract model Dashboard { visual: Visualization.Envelope; }\n}\n"), 0o644))
	irPath := filepath.Join(dir, "json-ir.json")

	require.NoError(t, compileTypeSpec(typeSpecDir, irPath, "", filepath.Join("signals", "main.tsp")))
	doc, err := loadDocument(irPath)
	require.NoError(t, err)
	require.Equal(t, "Signals", doc.Info.Title)
	require.Equal(t, "Visualization", doc.Schemas["Envelope"].Namespace)
}

func TestCompileTypeSpecAndGenerateServer_SupportsInlineBinaryRequestBody(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	setupManagedTypeSpecCache(t)
	typeSpecDir := filepath.Join(dir, "typespec")
	require.NoError(t, os.MkdirAll(typeSpecDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "main.tsp"), []byte(`import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Artifact API" })
@info(#{ version: "1.0.0" })
namespace ArtifactAPI;

model DeploymentArtifactResponse {
  deploymentId: string;
  sizeBytes: int64;
}

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

@route("/workspaces/{workspace}/deployments/{deployment}/artifact")
@put
op uploadDeploymentArtifact(
  @path workspace: string,
  @path deployment: string,
  @header contentType: "application/octet-stream",
  @body body: bytes,
): OkJson<DeploymentArtifactResponse>;
`), 0o644))
	irPath := filepath.Join(dir, "json-ir.json")
	openAPIPath := filepath.Join(dir, "openapi.yaml")
	serverPath := filepath.Join(dir, "server.apigen.gen.go")
	requestModelsPath := filepath.Join(dir, "request_models.gen.go")

	require.NoError(t, compileTypeSpec(typeSpecDir, irPath, openAPIPath))
	doc, err := loadDocument(irPath)
	require.NoError(t, err)
	require.NoError(t, generateServer(doc, serverPath, "api", requestModelsPath, "api", openAPIPath))

	content, ok := ir.PrimaryRequestBodyContent(doc.Endpoints[0])
	require.True(t, ok)
	require.Equal(t, "application/octet-stream", content.ContentType)
	require.Equal(t, "binary", content.BodyKind)
	require.Contains(t, mustReadString(t, serverPath), "type GenUploadDeploymentArtifactBody = []byte")
	require.NotContains(t, mustReadString(t, requestModelsPath), "DeploymentArtifactUploadRequest")
}

func TestRunCLI_AllIntegratesInlineBinaryRequestBody(t *testing.T) {
	t.Helper()

	result := runAPIGenManifestIntegration(t, `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Artifact API" })
@info(#{ version: "1.0.0" })
namespace ArtifactAPI;

model DeploymentArtifactResponse {
  deploymentId: string;
  sizeBytes: int64;
}

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

@route("/workspaces/{workspace}/deployments/{deployment}/artifact")
@put
op uploadDeploymentArtifact(
  @path workspace: string,
  @path deployment: string,
  @header contentType: "application/octet-stream",
  @body body: bytes,
): OkJson<DeploymentArtifactResponse>;
`)

	content, ok := ir.PrimaryRequestBodyContent(result.Document.Endpoints[0])
	require.True(t, ok)
	require.Equal(t, "application/octet-stream", content.ContentType)
	require.Equal(t, "binary", content.BodyKind)
	require.Contains(t, result.OpenAPIContent, "application/octet-stream")
	require.Contains(t, result.ServerContent, "type GenUploadDeploymentArtifactBody = []byte")
	require.NotContains(t, result.RequestModelsContent, "DeploymentArtifactUploadRequest")
}

func TestRunCLI_AllIntegratesJSONCRUDPipeline(t *testing.T) {
	t.Helper()

	result := runAPIGenManifestIntegration(t, `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Widget API" })
@info(#{ version: "1.0.0" })
namespace WidgetAPI;

model Error {
  code: int32;
  message: string;
}

model Widget {
  id: string;
  name: string;
}

model WidgetList {
  data: Widget[];
}

model CreateWidgetRequest {
  name: string;
}

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

model CreatedJson<T> {
  ...CreatedResponse;
  ...Body<T>;
}

model BadRequest {
  ...BadRequestResponse;
  ...Body<Error>;
}

@route("/widgets")
@get
@apigen.cli(#{ command: #["widgets", "list"], output: #{ mode: "collection", tableColumns: #["id", "name"] } })
op listWidgets(@query limit?: int32): OkJson<WidgetList> | BadRequest;

@route("/widgets")
@post
@apigen.cli(#{ command: #["widgets", "create"], bodyInput: "flags_or_json", args: #[#{ source: "body", name: "name" }], output: #{ mode: "detail" } })
op createWidget(@body body: CreateWidgetRequest): CreatedJson<Widget> | BadRequest;

@route("/widgets/{id}")
@get
@apigen.cli(#{ command: #["widgets", "get"], args: #[#{ source: "path", name: "id" }], output: #{ mode: "detail" } })
op getWidget(@path id: string): OkJson<Widget> | BadRequest;
`)

	require.Len(t, result.Document.Endpoints, 3)
	require.Contains(t, result.OpenAPIContent, "/widgets:")
	require.Contains(t, result.ServerContent, "type GenCreateWidgetBody = GenSchemaCreateWidgetRequest")
	require.Contains(t, result.ServerContent, "type GenListWidgetsParams struct")
	require.Contains(t, result.CLIContent, `Command: []string{"widgets", "list"}`)
	require.Contains(t, result.CLIContent, `Command: []string{"widgets", "create"}`)
	require.Contains(t, result.CLIContent, `Args: []apigencobra.ArgBinding{{Source: "body", Name: "name"`)
}

func TestRunCLI_AllIntegratesTransportNativeRequestBodies(t *testing.T) {
	t.Helper()

	result := runAPIGenManifestIntegration(t, `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Transport API" })
@info(#{ version: "1.0.0" })
namespace TransportAPI;

model Artifact {
  id: string;
}

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

@route("/artifacts/{id}/text")
@put
@apigen.cli(#{ command: #["artifacts", "replace-text"], args: #[#{ source: "path", name: "id" }] })
op replaceText(@path id: string, @header contentType: "text/plain", @body body: string): OkJson<Artifact>;

@route("/artifacts/{id}/blob")
@put
@apigen.cli(#{ command: #["artifacts", "replace-blob"], args: #[#{ source: "path", name: "id" }] })
op replaceBlob(@path id: string, @header contentType: "application/octet-stream", @body body: bytes): OkJson<Artifact>;

@route("/artifacts/{id}/file")
@put
@apigen.cli(#{ command: #["artifacts", "replace-file"], args: #[#{ source: "path", name: "id" }] })
op replaceFile(@path id: string, @bodyRoot body: File<"application/octet-stream", bytes>): OkJson<Artifact>;
`)

	require.Contains(t, result.OpenAPIContent, "text/plain")
	require.Contains(t, result.OpenAPIContent, "application/octet-stream")
	require.Contains(t, result.ServerContent, "type GenReplaceTextBody = string")
	require.Contains(t, result.ServerContent, "type GenReplaceBlobBody = []byte")
	require.Contains(t, result.ServerContent, "type GenReplaceFileBody = GenFile")
	require.Contains(t, result.ServerContent, "Reader      io.ReadCloser")
	require.NotContains(t, result.RequestModelsContent, "ReplaceTextRequest")
	require.NotContains(t, result.RequestModelsContent, "ReplaceBlobRequest")
	require.NotContains(t, result.RequestModelsContent, "ReplaceFileRequest")
}

func TestRunCLI_AllIntegratesSharedRouteContentNegotiation(t *testing.T) {
	t.Helper()

	result := runAPIGenManifestIntegration(t, `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Negotiation API" })
@info(#{ version: "1.0.0" })
namespace NegotiationAPI;

model Artifact {
  id: string;
}

model JsonArtifact {
  ...OkResponse;
  @header contentType: "application/json";
  ...Body<Artifact>;
}

model BinaryArtifact {
  ...OkResponse;
  @header contentType: "application/octet-stream";
  ...Body<bytes>;
}

@sharedRoute
@route("/artifacts/{id}")
@get
@apigen.cli(#{ command: #["artifacts", "get"], args: #[#{ source: "path", name: "id" }] })
op getArtifactJson(@path id: string, @header accept: "application/json"): JsonArtifact;

@sharedRoute
@route("/artifacts/{id}")
@get
@apigen.cli(#{ command: #["artifacts", "get"], args: #[#{ source: "path", name: "id" }] })
op getArtifactBinary(@path id: string, @header accept: "application/octet-stream"): BinaryArtifact;
`)

	require.Len(t, result.Document.Endpoints, 1)
	endpoint := result.Document.Endpoints[0]
	require.Equal(t, "getArtifactJson", endpoint.OperationID)
	require.Len(t, endpoint.Parameters, 2)
	require.Equal(t, "accept", endpoint.Parameters[1].Name)
	require.Equal(t, []string{"application/json", "application/octet-stream"}, endpoint.Parameters[1].Schema.Enum)
	require.Len(t, endpoint.Responses, 1)
	require.Len(t, endpoint.Responses[0].Contents, 2)
	require.Contains(t, result.ServerContent, "type GenGetArtifactJsonHeaders struct")
	require.Contains(t, result.ServerContent, "Accept string")
	require.Contains(t, result.ServerContent, "GenGetArtifactJson200ApplicationJSONResponse")
	require.Contains(t, result.ServerContent, "GenGetArtifactJson200ApplicationOctetStreamResponse")
	require.Contains(t, result.CLIContent, `Name: "accept", In: "header"`)
	require.Contains(t, result.CLIContent, `Enum: []string{"application/json", "application/octet-stream"}`)
}

func TestRunCLI_AllIntegratesMultipartFormAndMixed(t *testing.T) {
	t.Helper()

	result := runAPIGenManifestIntegration(t, `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Multipart API" })
@info(#{ version: "1.0.0" })
namespace MultipartAPI;

model Metadata {
  name: string;
}

model Artifact {
  id: string;
}

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

@route("/multipart")
@post
@apigen.cli(#{ command: #["artifacts", "upload"] })
op uploadMultipart(@multipartBody body: {
  metadata: HttpPart<Metadata>;
  note?: HttpPart<string>;
  tags: HttpPart<string>[];
  blob: HttpPart<bytes>;
  artifact: HttpPart<File<"application/octet-stream", bytes>>;
}): OkJson<Artifact>;

@route("/mixed")
@post
@apigen.cli(#{ command: #["artifacts", "upload-mixed"] })
op uploadMixed(@header contentType: "multipart/mixed", @multipartBody body: [
  HttpPart<string>,
  HttpPart<File<"application/octet-stream", bytes>, #{ name: "payload" }>,
]): OkJson<Artifact>;
`)

	formEndpoint := integrationEndpoint(t, result.Document, "uploadMultipart")
	formContent, ok := ir.PrimaryRequestBodyContent(formEndpoint)
	require.True(t, ok)
	require.Equal(t, "multipart/form-data", formContent.ContentType)
	require.Len(t, formContent.Parts, 5)
	require.True(t, formContent.Parts[2].Repeated)
	require.Equal(t, "binary", formContent.Parts[3].BodyKind)
	require.Equal(t, "file", formContent.Parts[4].BodyKind)
	require.Contains(t, result.ServerContent, "type GenUploadMultipartMultipartBody struct")
	require.Contains(t, result.ServerContent, "Tags     []string")
	require.Contains(t, result.ServerContent, "Artifact GenFile")
	require.Contains(t, result.CLIContent, `InputMode: "multipart"`)
	require.Contains(t, result.CLIContent, `Name: "tags", WireName: "tags", PartKind: "model", Repeated: true`)
	require.Contains(t, result.OpenAPIContent, "multipart/form-data")
	require.Contains(t, result.OpenAPIContent, "encoding:")

	mixedEndpoint := integrationEndpoint(t, result.Document, "uploadMixed")
	mixedContent, ok := ir.PrimaryRequestBodyContent(mixedEndpoint)
	require.True(t, ok)
	require.Equal(t, "multipart/mixed", mixedContent.ContentType)
	require.Len(t, mixedContent.Parts, 2)
	require.Equal(t, "tuple", mixedContent.Parts[0].PartKind)
	require.Contains(t, result.OpenAPIContent, "x-apigen-multipart-kind: mixed")
	require.Contains(t, result.OpenAPIContent, "x-apigen-multipart-parts:")
	require.Contains(t, result.ServerContent, "part1Parts := apigenMultipartPartsByIndex(parts, 0)")
	require.Contains(t, result.ServerContent, "part2Parts := apigenMultipartPartsByIndex(parts, 1)")
}

func TestCompileTypeSpec_PreservesOutputsWhenToolchainUnavailable(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv(typeSpecPackageDirEnv, filepath.Join(dir, "missing-typespec-package"))
	irPath := filepath.Join(dir, "json-ir.json")
	openAPIPath := filepath.Join(dir, "openapi.yaml")
	require.NoError(t, os.WriteFile(irPath, []byte(`{"existing":true}`), 0o644))
	require.NoError(t, os.WriteFile(openAPIPath, []byte("existing: true\n"), 0o644))

	err := compileTypeSpec(filepath.Join("..", "..", "typespec", "test", "fixtures", "todo"), irPath, openAPIPath)
	require.Error(t, err)
	require.ErrorContains(t, err, "typespec compiler not found")
	require.Equal(t, `{"existing":true}`, strings.TrimSpace(mustReadString(t, irPath)))
	require.Equal(t, "existing: true", strings.TrimSpace(mustReadString(t, openAPIPath)))
}

func TestRunCLI_TypeSpecCompileFailurePreservesOutputsForUnsupportedConstructs(t *testing.T) {
	t.Helper()

	tests := []struct {
		name       string
		source     string
		diagnostic string
	}{
		{
			name: "cookie parameter",
			source: `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Cookie API" })
@info(#{ version: "1.0.0" })
namespace CookieAPI;

@route("/widgets")
@get
op listWidgets(@cookie session: string): string;
`,
			diagnostic: "cookie parameters are not supported",
		},
		{
			name: "basic auth",
			source: `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Basic API" })
@info(#{ version: "1.0.0" })
@useAuth(BasicAuth)
namespace BasicAPI;

@route("/widgets")
@get
op listWidgets(): string;
`,
			diagnostic: "http Basic authentication is not supported",
		},
		{
			name: "oauth auth",
			source: `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

alias MyOAuth2<Scopes extends string[]> = OAuth2Auth<[
  {
    type: OAuth2FlowType.implicit;
    authorizationUrl: "https://auth.example/authorize";
    scopes: Scopes;
  }
]>;

@service(#{ title: "OAuth API" })
@info(#{ version: "1.0.0" })
@useAuth(MyOAuth2<["read"]>)
namespace OAuthAPI {

@route("/widgets")
@get
op listWidgets(): string;
}
`,
			diagnostic: "oauth2 authentication is not supported",
		},
		{
			name: "non X-API-Key header auth",
			source: `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

alias CustomKey = ApiKeyAuth<ApiKeyLocation.header, "X-Custom-Key">;

@service(#{ title: "Custom Key API" })
@info(#{ version: "1.0.0" })
@useAuth(CustomKey)
namespace CustomKeyAPI {

@route("/widgets")
@get
op listWidgets(): string;
}
`,
			diagnostic: "header API key name X-Custom-Key is not supported",
		},
		{
			name: "incompatible shared route bodies",
			source: `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Shared Route API" })
@info(#{ version: "1.0.0" })
namespace SharedRouteAPI;

model Widget {
  id: string;
}

@sharedRoute
@route("/widgets")
@post
op createJson(@header contentType: "application/json", @body body: Widget): string;

@sharedRoute
@route("/widgets")
@post
op createText(@header contentType: "text/plain", @body body: string): string;
`,
			diagnostic: "incompatible request bodies",
		},
		{
			name: "incompatible duplicate response content",
			source: `import "@typespec/http";
import "@typespec/openapi";
import "@yacobolo/apigen";

using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "Duplicate Content API" })
@info(#{ version: "1.0.0" })
namespace DuplicateContentAPI;

model Widget {
  id: string;
}

model OtherWidget {
  name: string;
}

model WidgetResponse {
  ...OkResponse;
  @header contentType: "application/json";
  ...Body<Widget>;
}

model OtherWidgetResponse {
  ...OkResponse;
  @header contentType: "application/json";
  ...Body<OtherWidget>;
}

@sharedRoute
@route("/widgets/{id}")
@get
op getWidgetA(@path id: string, @header accept: "application/json"): WidgetResponse;

@sharedRoute
@route("/widgets/{id}")
@get
op getWidgetB(@path id: string, @header accept: "application/vnd.widget+json"): OtherWidgetResponse;
`,
			diagnostic: "incompatible response content for status 200 and content type application/json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runTypeSpecCompileFailurePreservesOutputs(t, tc.source, tc.diagnostic)
		})
	}
}

func TestResolveTypeSpecPackage_UsesDevelopmentOverride(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv(typeSpecPackageDirEnv, dir)

	pkg, err := resolveTypeSpecPackage()
	require.NoError(t, err)
	require.Equal(t, mustAbs(t, dir), pkg.Dir)
	require.False(t, pkg.Managed)
}

func TestInstallBundledTypeSpecPackage_UsesWritableCache(t *testing.T) {
	t.Helper()

	cacheRoot := t.TempDir()
	pkg, err := installBundledTypeSpecPackage(cacheRoot)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(pkg.Dir, filepath.Join(cacheRoot, "apigen", "typespec")+string(os.PathSeparator)))
	require.True(t, pkg.Managed)
	require.FileExists(t, filepath.Join(pkg.Dir, "package.json"))
	require.FileExists(t, filepath.Join(pkg.Dir, "package-lock.json"))
	require.FileExists(t, filepath.Join(pkg.Dir, "lib", "main.tsp"))
	require.FileExists(t, filepath.Join(pkg.Dir, "dist", "src", "index.js"))
}

func TestCompileTypeSpec_FailurePreservesExistingOutputs(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	setupManagedTypeSpecCache(t)
	irPath := filepath.Join(dir, "json-ir.json")
	openAPIPath := filepath.Join(dir, "openapi.yaml")
	require.NoError(t, os.WriteFile(irPath, []byte(`{"stale":true}`), 0o644))
	require.NoError(t, os.WriteFile(openAPIPath, []byte("stale: true\n"), 0o644))

	fixtureDir := filepath.Join("..", "..", "typespec", "test", "fixtures", "invalid")
	err := compileTypeSpec(fixtureDir, irPath, openAPIPath)
	require.Error(t, err)
	require.ErrorContains(t, err, "requires request body to resolve to a named model schema")
	require.Equal(t, `{"stale":true}`, strings.TrimSpace(mustReadString(t, irPath)))
	require.Equal(t, "stale: true", strings.TrimSpace(mustReadString(t, openAPIPath)))
}

func TestResolveCommandConfig_GroupedManifestOverrides(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/generated/api
      package: transport
      server_file: service_server.gen.go
      request_models_file: models.gen.go
    cli_out:
      dir: internal/generated/commands
      package: cli
      file: registry.gen.go
`), 0o644))

	config, err := resolveCommandConfig("all", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "internal", "generated", "api", "service_server.gen.go"), config.ServerOut)
	require.Equal(t, "transport", config.ServerPackage)
	require.Equal(t, filepath.Join(dir, "internal", "generated", "api", "models.gen.go"), config.RequestModelsOut)
	require.Equal(t, "transport", config.RequestModelsPackage)
	require.Equal(t, filepath.Join(dir, "internal", "generated", "commands", "registry.gen.go"), config.CLIOut)
	require.Equal(t, "cli", config.CLIPackage)
	require.Nil(t, config.GoPackagePlan)
}

func TestResolveCommandConfig_NormalizesNamespacePackagePlan(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      aggregate:
        dir: internal/app/api/gen
        package: aggregate
      packages:
        AcmeAPI.Dashboard:
          dir: internal/dashboard/api/gen
          import_path: github.com/acme/example/internal/dashboard/api/gen
        AcmeAPI.Access:
          dir: internal/access/api/gen
          package: accessapi
          import_path: github.com/acme/example/internal/access/api/gen
`), 0o644))

	config, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.NotNil(t, config.GoPackagePlan)
	require.Nil(t, config.GoPackagePlan.Default)
	require.Equal(t, unmatchedNamespaceError, config.GoPackagePlan.Unmatched)
	require.Equal(t, resolvedGoPackageOutput{
		Dir:               filepath.Join(dir, "internal", "app", "api", "gen"),
		Package:           "aggregate",
		ServerFile:        "server.apigen.gen.go",
		RequestModelsFile: "request_models.gen.go",
	}, *config.GoPackagePlan.Aggregate)
	require.Equal(t, []namespaceGoPackageOutput{
		{
			Namespace: "AcmeAPI.Access",
			Output: resolvedGoPackageOutput{
				Dir:               filepath.Join(dir, "internal", "access", "api", "gen"),
				Package:           "accessapi",
				ImportPath:        "github.com/acme/example/internal/access/api/gen",
				ServerFile:        "server.apigen.gen.go",
				RequestModelsFile: "request_models.gen.go",
			},
		},
		{
			Namespace: "AcmeAPI.Dashboard",
			Output: resolvedGoPackageOutput{
				Dir:               filepath.Join(dir, "internal", "dashboard", "api", "gen"),
				Package:           "gen",
				ImportPath:        "github.com/acme/example/internal/dashboard/api/gen",
				ServerFile:        "server.apigen.gen.go",
				RequestModelsFile: "request_models.gen.go",
			},
		},
	}, config.GoPackagePlan.Packages)
	require.Empty(t, config.ServerOut)
	require.Empty(t, config.RequestModelsOut)
}

func TestResolveCommandConfig_NormalizesDefaultNamespacePackageOutput(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: default
      default:
        dir: internal/api/gen
        import_path: github.com/acme/example/internal/api/gen
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/access/api/gen
`), 0o644))

	config, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.NotNil(t, config.GoPackagePlan)
	require.Equal(t, unmatchedNamespaceDefault, config.GoPackagePlan.Unmatched)
	require.Equal(t, resolvedGoPackageOutput{
		Dir:               filepath.Join(dir, "internal", "api", "gen"),
		Package:           "gen",
		ImportPath:        "github.com/acme/example/internal/api/gen",
		ServerFile:        "server.apigen.gen.go",
		RequestModelsFile: "request_models.gen.go",
	}, *config.GoPackagePlan.Default)
}

func TestResolveCommandConfig_ValidatesPartitionImportPaths(t *testing.T) {
	t.Helper()

	tests := []struct {
		name       string
		importPath string
		wantErr    string
	}{
		{name: "missing", wantErr: `go_out.packages["AcmeAPI.Access"].import_path is required`},
		{name: "absolute filesystem path", importPath: "/workspace/internal/access", wantErr: "must be a canonical Go import path"},
		{name: "backslash", importPath: `github.com\acme\access`, wantErr: "must be a canonical Go import path"},
		{name: "trailing slash", importPath: "github.com/acme/access/", wantErr: "must be a canonical Go import path"},
		{name: "dot segment", importPath: "github.com/acme/./access", wantErr: "must be a canonical Go import path"},
		{name: "parent segment", importPath: "github.com/acme/../access", wantErr: "must be a canonical Go import path"},
		{name: "duplicate slash", importPath: "github.com/acme//access", wantErr: "must be a canonical Go import path"},
		{name: "surrounding whitespace", importPath: " github.com/acme/access ", wantErr: "must be a canonical Go import path"},
		{name: "current directory", importPath: ".", wantErr: "must be a canonical Go import path"},
		{name: "parent directory", importPath: "..", wantErr: "must be a canonical Go import path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := filepath.Join(dir, "apigen.targets.yaml")
			manifest := `targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: '` + tt.importPath + "'\n"
			require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o644))

			_, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestResolveCommandConfig_RejectsConflictingCoalescedPackageOutputs(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/identity/api/gen
          package: identityapi
          import_path: github.com/acme/example/internal/access/api/gen
        AcmeAPI.Sessions:
          dir: internal/identity/api/gen
          package: identityapi
          import_path: github.com/acme/example/internal/sessions/api/gen
`), 0o644))

	_, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.ErrorContains(t, err, "go_out packages resolve to the same directory with inconsistent output settings")
}

func TestResolveCommandConfig_RejectsDuplicateImportPathsForDifferentOutputs(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/shared/api/gen
        AcmeAPI.Dashboard:
          dir: internal/dashboard/api/gen
          import_path: github.com/acme/example/internal/shared/api/gen
`), 0o644))

	_, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.ErrorContains(t, err, `go_out import_path "github.com/acme/example/internal/shared/api/gen" resolves to multiple directories`)
}

func TestResolveCommandConfig_RejectsInvalidNamespacePackagePlans(t *testing.T) {
	t.Helper()

	tests := []struct {
		name    string
		goOut   string
		wantErr string
	}{
		{
			name: "flat and package plan forms cannot be mixed",
			goOut: `      dir: internal/api/gen
      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen`,
			wantErr: "go_out cannot mix dir/package/file fields with default/aggregate/packages/unmatched",
		},
		{
			name: "unmatched policy is required",
			goOut: `      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen`,
			wantErr: "go_out.unmatched must be one of default or error",
		},
		{
			name:    "package mapping is required",
			goOut:   `      unmatched: error`,
			wantErr: "go_out.packages must declare at least one namespace",
		},
		{
			name: "unmatched policy is closed",
			goOut: `      unmatched: duplicate
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen`,
			wantErr: "go_out.unmatched must be one of default or error",
		},
		{
			name: "default policy requires output",
			goOut: `      unmatched: default
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen`,
			wantErr: "go_out.unmatched=default requires go_out.default",
		},
		{
			name: "default output requires import path",
			goOut: `      unmatched: default
      default:
        dir: internal/api/gen
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/access/api/gen`,
			wantErr: "go_out.default.import_path is required",
		},
		{
			name: "namespace is required",
			goOut: `      unmatched: error
      packages:
        "":
          dir: internal/access/api/gen`,
			wantErr: "go_out.packages namespace is required",
		},
		{
			name: "nested package name is validated",
			goOut: `      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          package: 123access`,
			wantErr: `go_out.packages["AcmeAPI.Access"]: invalid inferred go package "123access"`,
		},
		{
			name: "Go keyword package name is rejected",
			goOut: `      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          package: type
          import_path: github.com/acme/example/internal/access/api/gen`,
			wantErr: `go_out.packages["AcmeAPI.Access"]: invalid inferred go package "type"`,
		},
		{
			name: "same directory cannot declare different packages",
			goOut: `      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/shared/api/gen
          package: accessapi
          import_path: github.com/acme/example/internal/shared/api/gen
        AcmeAPI.Dashboard:
          dir: internal/shared/api/gen
          package: dashboardapi
          import_path: github.com/acme/example/internal/shared/api/gen`,
			wantErr: "go_out packages resolve to the same directory with different package names",
		},
		{
			name: "aggregate cannot share a partition directory",
			goOut: `      unmatched: error
      aggregate:
        dir: internal/shared/api/gen
      packages:
        AcmeAPI.Access:
          dir: internal/shared/api/gen
          import_path: github.com/acme/example/internal/shared/api/gen`,
			wantErr: "go_out.aggregate must use a directory separate from package outputs",
		},
		{
			name: "authored aggregate import path must be canonical",
			goOut: `      unmatched: error
      aggregate:
        dir: internal/app/api/gen
        import_path: /workspace/internal/app/api/gen
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/access/api/gen`,
			wantErr: "go_out.aggregate.import_path must be a canonical Go import path",
		},
		{
			name: "generated filenames cannot escape their output directory",
			goOut: `      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/access/api/gen
          server_file: ../server.apigen.gen.go`,
			wantErr: `go_out.packages["AcmeAPI.Access"].server_file must be a filename within its output directory`,
		},
		{
			name: "generated filenames must be distinct",
			goOut: `      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/access/api/gen
          server_file: generated.go
          request_models_file: generated.go`,
			wantErr: `go_out.packages["AcmeAPI.Access"] server_file and request_models_file must be different`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := filepath.Join(dir, "apigen.targets.yaml")
			manifest := `targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
` + tt.goOut + "\n"
			require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o644))

			_, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestResolveCommandConfig_AllowsNamespacesToShareOnePackage(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/identity/api/gen
          package: identityapi
          import_path: github.com/acme/example/internal/identity/api/gen
        AcmeAPI.Sessions:
          dir: internal/identity/api/gen
          package: identityapi
          import_path: github.com/acme/example/internal/identity/api/gen
`), 0o644))

	config, err := resolveCommandConfig("typespec-compile", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.Len(t, config.GoPackagePlan.Packages, 2)
	require.Equal(t, config.GoPackagePlan.Packages[0].Output, config.GoPackagePlan.Packages[1].Output)
}

func TestResolveCommandConfig_AllowsPackagePlanServerEmission(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      unmatched: error
      packages:
        AcmeAPI.Access:
          dir: internal/access/api/gen
          import_path: github.com/acme/example/internal/access/api/gen
`), 0o644))

	for _, command := range []string{"server", "all"} {
		t.Run(command, func(t *testing.T) {
			config, err := resolveCommandConfig(command, manifestPath, "example", commandConfig{})
			require.NoError(t, err)
			require.NotNil(t, config.GoPackagePlan)
			require.Empty(t, config.ServerOut)
			require.Empty(t, config.RequestModelsOut)
		})
	}
}

func TestResolveCommandConfig_GroupedManifestWithoutCLI(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
`), 0o644))

	config, err := resolveCommandConfig("all", manifestPath, "example", commandConfig{})
	require.NoError(t, err)
	require.False(t, config.GenerateCLI)
	require.Empty(t, config.CLIOut)

	_, err = resolveCommandConfig("cli", manifestPath, "example", commandConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "cli_out")
}

func TestResolveCommandConfig_GroupedManifestRejectsLegacyFields(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    server_out: internal/api/server.apigen.gen.go
    go_out:
      dir: internal/api/gen
`), 0o644))

	_, err := resolveCommandConfig("all", manifestPath, "example", commandConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "legacy flat manifest fields")
	require.NotContains(t, err.Error(), "0.2.0")
}

func TestResolveCommandConfig_GroupedManifestRejectsStringCLIOut(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/gen
    cli_out: cmd/cli/gen/apigen_registry.gen.go
`), 0o644))

	_, err := resolveCommandConfig("all", manifestPath, "example", commandConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "cli_out must be a mapping")
}

func TestResolveCommandConfig_GroupedManifestRejectsInvalidInferredPackage(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: example
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api/123-generated
`), 0o644))

	_, err := resolveCommandConfig("all", manifestPath, "example", commandConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid inferred go package")
}

func TestMultiTargetManifest_GeneratesVersionedArtifacts(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	setupManagedTypeSpecCache(t)
	writeMinimalTypeSpecContract(t, filepath.Join(root, "api", "v1", "typespec"), "/v1", "Widget API", "1.0.0")
	writeMinimalTypeSpecContract(t, filepath.Join(root, "api", "v2", "typespec"), "/v2", "Widget API v2", "2.0.0")

	manifestPath := filepath.Join(root, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: v1
    typespec_dir: api/v1/typespec
    ir_out: internal/api/v1/gen/json-ir.json
    openapi_out: internal/api/v1/gen/openapi.yaml
    go_out:
      dir: internal/api/v1
      package: apiv1
      server_file: server.apigen.gen.go
      request_models_file: request_models.gen.go
    cli_out:
      dir: pkg/cli/gen
      package: genv1
      file: apigen_v1_registry.gen.go
  - name: v2
    typespec_dir: api/v2/typespec
    ir_out: internal/api/v2/gen/json-ir.json
    openapi_out: internal/api/v2/gen/openapi.yaml
    go_out:
      dir: internal/api/v2
      package: apiv2
      server_file: server.apigen.gen.go
      request_models_file: request_models.gen.go
`), 0o644))

	v1Config, err := resolveCommandConfig("all", manifestPath, "v1", commandConfig{})
	require.NoError(t, err)
	require.NoError(t, compileTypeSpec(v1Config.TypeSpecDir, v1Config.IROut, v1Config.OpenAPIOut))

	v1Doc, err := loadDocument(v1Config.IRPath)
	require.NoError(t, err)
	require.Equal(t, "Widget API", v1Doc.Info.Title)
	require.NoError(t, generateServer(v1Doc, v1Config.ServerOut, v1Config.ServerPackage, v1Config.RequestModelsOut, v1Config.RequestModelsPackage, v1Config.CanonicalOpenAPIPath))
	require.NoError(t, generateCLI(v1Doc, v1Config.CLIOut, v1Config.CLIPackage))

	v1OpenAPI := mustReadString(t, v1Config.OpenAPIOut)
	require.Contains(t, v1OpenAPI, "/v1/widgets:")
	v1Server := mustReadString(t, v1Config.ServerOut)
	require.Contains(t, v1Server, `Path: "/v1/widgets"`)
	v1CLI := mustReadString(t, v1Config.CLIOut)
	require.Contains(t, v1CLI, `Path: "/v1/widgets"`)

	v2Config, err := resolveCommandConfig("all", manifestPath, "v2", commandConfig{})
	require.NoError(t, err)
	require.False(t, v2Config.GenerateCLI)
	require.NoError(t, compileTypeSpec(v2Config.TypeSpecDir, v2Config.IROut, v2Config.OpenAPIOut))

	v2Doc, err := loadDocument(v2Config.IRPath)
	require.NoError(t, err)
	require.Equal(t, "Widget API v2", v2Doc.Info.Title)
	require.NoError(t, generateServer(v2Doc, v2Config.ServerOut, v2Config.ServerPackage, v2Config.RequestModelsOut, v2Config.RequestModelsPackage, v2Config.CanonicalOpenAPIPath))

	v2OpenAPI := mustReadString(t, v2Config.OpenAPIOut)
	require.Contains(t, v2OpenAPI, "/v2/widgets:")
	v2Server := mustReadString(t, v2Config.ServerOut)
	require.Contains(t, v2Server, `Path: "/v2/widgets"`)
	_, err = os.Stat(v2Config.CLIOut)
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestGenerateServer_FailsForUnnamedRequestBodySchema(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "Widget API", Version: "1.0.0"},
		OpenAPI:       ir.OpenAPI{Version: "3.0.0"},
		Schemas: map[string]ir.Schema{
			"Widget": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"id": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"id"},
			},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/widgets",
				OperationID: "createWidget",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Type: "object"})},
				Responses:   []ir.Response{{StatusCode: 201, Description: "created", Contents: jsonContent(ir.SchemaRef{Ref: "Widget"})}},
			},
		},
	}

	canonicalOpenAPIPath := writeCanonicalOpenAPI(t, dir, doc)
	serverPath := filepath.Join(dir, "server.apigen.gen.go")
	requestModelsPath := filepath.Join(dir, "request_models.gen.go")

	err := generateServer(doc, serverPath, "api", requestModelsPath, "api", canonicalOpenAPIPath)
	require.Error(t, err)
	require.ErrorContains(t, err, "requires a named IR schema")
	require.ErrorContains(t, err, "createWidget")
}

func TestGenerateServer_AllowsInlineBinaryRequestBody(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "Artifact API", Version: "1.0.0"},
		OpenAPI:       ir.OpenAPI{Version: "3.0.0"},
		Schemas: map[string]ir.Schema{
			"DeploymentArtifactResponse": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"deploymentId": {Schema: ir.SchemaRef{Type: "string"}},
					"sizeBytes":    {Schema: ir.SchemaRef{Type: "integer", Format: "int64"}},
				},
				Required: []string{"deploymentId", "sizeBytes"},
			},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "put",
				Path:        "/workspaces/{workspace}/deployments/{deployment}/artifact",
				OperationID: "uploadDeploymentArtifact",
				Parameters: []ir.Parameter{
					{Name: "workspace", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
					{Name: "deployment", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
					{Name: "Content-Type", In: "header", Required: true, Schema: ir.SchemaRef{Type: "string", Enum: []string{"application/octet-stream"}}},
				},
				RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
					ContentType: "application/octet-stream",
					BodyKind:    "binary",
					Schema:      &ir.SchemaRef{Type: "string", Format: "binary"},
				}}},
				Responses: []ir.Response{{StatusCode: 200, Description: "uploaded", Contents: jsonContent(ir.SchemaRef{Ref: "DeploymentArtifactResponse"})}},
			},
		},
	}

	canonicalOpenAPIPath := writeCanonicalOpenAPI(t, dir, doc)
	serverPath := filepath.Join(dir, "server.apigen.gen.go")
	requestModelsPath := filepath.Join(dir, "request_models.gen.go")

	require.NoError(t, generateServer(doc, serverPath, "api", requestModelsPath, "api", canonicalOpenAPIPath))
	serverContent := mustReadString(t, serverPath)
	requestModelsContent := mustReadString(t, requestModelsPath)

	require.Contains(t, serverContent, "type GenUploadDeploymentArtifactBody = []byte")
	require.Contains(t, serverContent, "Body       *GenUploadDeploymentArtifactBody")
	require.Contains(t, requestModelsContent, "type GenSchemaDeploymentArtifactResponse = DeploymentArtifactResponse")
	require.NotContains(t, requestModelsContent, "DeploymentArtifactUploadRequest")
}

func writeMinimalTypeSpecContract(t *testing.T, typeSpecDir string, pathPrefix string, title string, version string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(typeSpecDir, 0o755))
	source := `using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "` + title + `" })
@info(#{ version: "` + version + `" })
namespace WidgetAPI;

model Widget {
  id: string;
  name: string;
}

@route("` + pathPrefix + `/widgets")
@get
@summary("List widgets")
@apigen.cli(#{ command: #["widgets", "list"] })
op listWidgets(): Widget;
`
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "main.tsp"), []byte(source), 0o644))
}

func runAPIGenManifestIntegration(t *testing.T, source string) apigenIntegrationResult {
	t.Helper()

	root := t.TempDir()
	realHome := os.Getenv("HOME")
	setupManagedTypeSpecCache(t)

	typeSpecDir := filepath.Join(root, "api", "typespec")
	require.NoError(t, os.MkdirAll(typeSpecDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "main.tsp"), []byte(source), 0o644))

	manifestPath := filepath.Join(root, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: integration
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api
      package: api
      server_file: server.apigen.gen.go
      request_models_file: request_models.gen.go
    cli_out:
      dir: cmd/cli/gen
      package: gen
      file: apigen_registry.gen.go
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"typespec-compile", "-manifest", manifestPath, "-target", "integration"}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.Empty(t, stderr.String())

	stdout.Reset()
	stderr.Reset()
	code = runCLI([]string{"all", "-manifest", manifestPath, "-target", "integration"}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	require.Empty(t, stderr.String())

	result := apigenIntegrationResult{
		Root:              root,
		IRPath:            filepath.Join(root, "api", "gen", "json-ir.json"),
		OpenAPIPath:       filepath.Join(root, "api", "gen", "openapi.yaml"),
		ServerPath:        filepath.Join(root, "internal", "api", "server.apigen.gen.go"),
		RequestModelsPath: filepath.Join(root, "internal", "api", "request_models.gen.go"),
		CLIPath:           filepath.Join(root, "cmd", "cli", "gen", "apigen_registry.gen.go"),
	}
	require.FileExists(t, result.IRPath)
	require.FileExists(t, result.OpenAPIPath)
	require.FileExists(t, result.ServerPath)
	require.FileExists(t, result.RequestModelsPath)
	require.FileExists(t, result.CLIPath)

	doc, err := loadDocument(result.IRPath)
	require.NoError(t, err)
	result.Document = doc
	result.OpenAPIContent = mustReadString(t, result.OpenAPIPath)
	result.ServerContent = mustReadString(t, result.ServerPath)
	result.RequestModelsContent = mustReadString(t, result.RequestModelsPath)
	result.CLIContent = mustReadString(t, result.CLIPath)

	compileGeneratedIntegrationPackages(t, result, realHome)
	return result
}

func compileGeneratedIntegrationPackages(t *testing.T, result apigenIntegrationResult, realHome string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(result.Root, "go.mod"), []byte(`module generatedintegration

go 1.25.8

require github.com/Yacobolo/toolbelt/apigen v0.0.0

replace github.com/Yacobolo/toolbelt/apigen => `+apigenModuleRoot(t)+`
`), 0o644))
	if !strings.Contains(result.RequestModelsContent, "type Error struct") {
		require.NoError(t, os.WriteFile(filepath.Join(result.Root, "internal", "api", "app_types_test.go"), []byte(`package api

type Error struct {
	Code int32
	Message string
}
`), 0o644))
	}

	cmd := exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = result.Root
	if realHome != "" {
		cmd.Env = append(os.Environ(), "HOME="+realHome, "GOMODCACHE="+filepath.Join(realHome, "go", "pkg", "mod"))
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func runTypeSpecCompileFailurePreservesOutputs(t *testing.T, source string, expectedDiagnostic string) {
	t.Helper()

	root := t.TempDir()
	setupManagedTypeSpecCache(t)
	typeSpecDir := filepath.Join(root, "api", "typespec")
	require.NoError(t, os.MkdirAll(typeSpecDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(typeSpecDir, "main.tsp"), []byte(source), 0o644))

	irPath := filepath.Join(root, "api", "gen", "json-ir.json")
	openAPIPath := filepath.Join(root, "api", "gen", "openapi.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(irPath), 0o755))
	require.NoError(t, os.WriteFile(irPath, []byte(`{"stale":true}`), 0o644))
	require.NoError(t, os.WriteFile(openAPIPath, []byte("stale: true\n"), 0o644))

	manifestPath := filepath.Join(root, "apigen.targets.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: invalid
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"typespec-compile", "-manifest", manifestPath, "-target", "invalid"}, &stdout, &stderr)
	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), expectedDiagnostic)
	require.Equal(t, `{"stale":true}`, strings.TrimSpace(mustReadString(t, irPath)))
	require.Equal(t, "stale: true", strings.TrimSpace(mustReadString(t, openAPIPath)))
}

func integrationEndpoint(t *testing.T, doc ir.Document, operationID string) ir.Endpoint {
	t.Helper()

	for _, endpoint := range doc.Endpoints {
		if endpoint.OperationID == operationID {
			return endpoint
		}
	}
	t.Fatalf("operation %q not found", operationID)
	return ir.Endpoint{}
}

func apigenModuleRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	require.NoError(t, err)
	return root
}

func mustReadString(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.TrimSpace(string(content))
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()

	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	return abs
}

func writeCanonicalOpenAPI(t *testing.T, dir string, doc ir.Document) string {
	t.Helper()

	content, err := openapiemit.EmitYAML(doc, openapiemit.Options{})
	require.NoError(t, err)

	path := filepath.Join(dir, "canonical-openapi.yaml")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}
