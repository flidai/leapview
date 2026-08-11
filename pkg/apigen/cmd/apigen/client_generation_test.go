package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestRunCLI_AllGeneratesCompilingTypedClientForSinglePackageProject(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	widgetRef := ir.SchemaRef{Ref: "Widget"}
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "Small API", Version: "1.0.0", Namespace: "SmallAPI"},
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
		Endpoints: []ir.Endpoint{{
			Method:      "post",
			Path:        "/widgets/{widget}/archive",
			OperationID: "archiveWidget",
			Kind:        "command",
			Parameters: []ir.Parameter{
				{Name: "widget", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
				{Name: "Idempotency-Key", In: "header", Required: true, Schema: ir.SchemaRef{Type: "string"}},
			},
			Responses: []ir.Response{
				{
					StatusCode:  200,
					Description: "ok",
					Contents: []ir.BodyContent{{
						ContentType: "application/json",
						BodyKind:    "json",
						Schema:      &widgetRef,
					}},
				},
				{StatusCode: 409, Description: "conflict"},
			},
			Command: &ir.Command{
				Owner:       "Widgets",
				Audit:       ir.AuditPolicy{Required: false},
				Idempotency: "required",
				Failures: []ir.CommandFailure{{
					Kind: "conflict", StatusCode: 409, Code: "WIDGET_CONFLICT", PublicDetail: "The widget conflicts with its current state.",
				}},
			},
		}},
	}

	irPath := filepath.Join(root, "api", "gen", "json-ir.json")
	openAPIPath := filepath.Join(root, "api", "gen", "openapi.yaml")
	require.NoError(t, writeJSONDocument(irPath, doc))
	require.NoError(t, generateOpenAPI(doc, openAPIPath))

	manifestPath := filepath.Join(root, "apigen.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`targets:
  - name: small
    typespec_dir: api/typespec
    ir_out: api/gen/json-ir.json
    openapi_out: api/gen/openapi.yaml
    go_out:
      dir: internal/api
      package: api
      client_file: client.apigen.gen.go
    failure_ts_out: web/generated/api/failures.ts
`), 0o644))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"all", "-manifest", manifestPath, "-target", "small"}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())

	clientPath := filepath.Join(root, "internal", "api", "client.apigen.gen.go")
	require.FileExists(t, clientPath)
	clientContent := mustReadString(t, clientPath)
	require.Contains(t, clientContent, `GenOperationArchiveWidget = "archiveWidget"`)
	require.Contains(t, clientContent, "func (client *GenClient) ArchiveWidget(")
	require.Contains(t, clientContent, "type GenArchiveWidgetFailure interface")
	require.NotContains(t, clientContent, "internal/app")
	require.NotContains(t, clientContent, "capability")
	failureTypesPath := filepath.Join(root, "web", "generated", "api", "failures.ts")
	require.FileExists(t, failureTypesPath)
	failureTypes := mustReadString(t, failureTypesPath)
	require.Contains(t, failureTypes, `export type ArchiveWidgetFailure =`)
	require.Contains(t, failureTypes, `{ kind: "conflict"; code: "WIDGET_CONFLICT"; status: 409; problem: APIGenProblemDetails }`)
	require.Contains(t, failureTypes, `export function matchArchiveWidgetFailure<T>`)

	writePartitionedGenerationGoModule(t, root)
	writeGeneratedServerErrorStub(t, filepath.Join(root, "internal", "api"), "api")
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "api", "client_usage_test.go"), []byte(`package api

import (
	"context"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
)

type fakeTransport struct{}

func (fakeTransport) DoAPIGen(_ context.Context, request apigenclient.Request, response any) (apigenclient.Response, error) {
	if request.OperationID != GenOperationArchiveWidget {
		panic(request.OperationID)
	}
	*(response.(*GenSchemaWidget)) = GenSchemaWidget{Id: "widget-1"}
	return apigenclient.Response{StatusCode: 200}, nil
}

func TestTypedClient(t *testing.T) {
	client := NewGenClient(fakeTransport{})
	response, err := client.ArchiveWidget(context.Background(), GenArchiveWidgetClientRequest{
		Widget: "widget-1",
		Headers: GenArchiveWidgetClientHeaders{IdempotencyKey: "request-1"},
	})
	if err != nil || response.Body.Id != "widget-1" || response.StatusCode != 200 {
		t.Fatalf("response = %#v, err = %v", response, err)
	}
}
`), 0o644))
	runGeneratedGoTest(t, root)
}
