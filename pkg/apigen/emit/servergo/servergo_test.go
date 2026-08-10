package servergo

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func jsonContent(ref ir.SchemaRef) []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &ref}}
}

func binaryContent() []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/octet-stream", BodyKind: "binary", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}}}
}

func fileContent() []ir.BodyContent {
	return []ir.BodyContent{{ContentType: "application/octet-stream", BodyKind: "file", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}}}
}

func TestEmit(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"QueryResult":         {Type: "object"},
			"SubmitQueryResponse": {Type: "object"},
			"CancelQueryResponse": {Type: "object"},
			"PaginatedGroups":     {Type: "object"},
			"Error":               {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			{Method: "get", Path: "/healthz", OperationID: "getHealth", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type GenServerInterface interface")
	require.Contains(t, content, "RegisterAPIGenRoutes")
	require.Contains(t, content, "HandleAPIGen")
	require.Contains(t, content, "type GenOperationDispatcher interface")
	require.Contains(t, content, "DispatchAPIGenOperation")
	require.NotContains(t, content, "*ServerInterfaceWrapper")
	require.Contains(t, content, "apigenchi.RegisterRoutes(router, []apigenchi.Route{")
	require.Contains(t, content, "{Method: \"GET\", Path: \"/healthz\", OperationID: \"getHealth\"}")
	require.Contains(t, content, "func RegisterAPIGenRoutes(router apigenchi.Router, server GenServerInterface)")
	require.Contains(t, content, "func RegisterAPIGenStrictRoutes(router apigenchi.Router, handler GenStrictServerInterface, responder GenTransportErrorResponder)")
	require.Contains(t, content, "func DispatchAPIGenOperation(operationID string, dispatcher GenOperationDispatcher")
	require.NotContains(t, content, "\"github.com/oapi-codegen/runtime\"")
	require.NotContains(t, content, "\"reflect\"")
	require.Contains(t, content, "type genStrictAdapter struct")
	require.Contains(t, content, "func (a genStrictAdapter) HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request)")
	require.Contains(t, content, "type genStrictBridge struct")
	require.Contains(t, content, "type GenStrictServerInterface interface")
	require.Contains(t, content, "func DispatchAPIGenStrictOperation(operationID string, handler GenStrictServerInterface")
	require.Contains(t, content, "type GenOperationContract struct")
	require.Contains(t, content, "var genOperationContracts = map[string]GenOperationContract{")
	require.Contains(t, content, "func GetAPIGenOperationContracts() map[string]GenOperationContract")
	require.Contains(t, content, "func GetAPIGenOperationContract(operationID string) (GenOperationContract, bool)")
	require.Contains(t, content, "func APIGenOperationAllowsStatus(operationID string, statusCode int) bool")
}

func TestEmit_UsesIRPathAsIs(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{Method: "post", Path: "/query", OperationID: "executeQuery", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "{Method: \"POST\", Path: \"/query\", OperationID: \"executeQuery\"}")
	require.NotContains(t, content, "{Method: \"POST\", Path: \"/v1/query\", OperationID: \"executeQuery\"}")
}

func TestEmit_UsesAPIBasePathForRoutes(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{Method: "post", Path: "/query", OperationID: "executeQuery", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "{Method: \"POST\", Path: \"/v1/query\", OperationID: \"executeQuery\"}")
}

func TestValidateOperationIDs(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{Method: "get", Path: "/a", OperationID: "create-user", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
			{Method: "post", Path: "/b", OperationID: "create_user", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	err := ValidateOperationIDs(doc)
	require.Error(t, err)
}

func TestEmit_DispatchParityAndHealthHandling(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{Method: "get", Path: "/healthz", OperationID: "getHealth", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
			{Method: "post", Path: "/query", OperationID: "executeQuery", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
			{Method: "get", Path: "/groups", OperationID: "listGroups", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "{Method: \"GET\", Path: \"/healthz\", OperationID: \"getHealth\"}")
	require.Contains(t, content, "{Method: \"POST\", Path: \"/query\", OperationID: \"executeQuery\"}")
	require.Contains(t, content, "{Method: \"GET\", Path: \"/groups\", OperationID: \"listGroups\"}")
	require.Contains(t, content, "}, server.HandleAPIGen)")

	require.Contains(t, content, "ExecuteQuery(w http.ResponseWriter, r *http.Request)")
	require.Contains(t, content, "ListGroups(w http.ResponseWriter, r *http.Request)")
	require.NotContains(t, content, "GetHealth(w http.ResponseWriter, r *http.Request)")

	require.Contains(t, content, "case \"executeQuery\":")
	require.Contains(t, content, "dispatcher.ExecuteQuery(w, r)")
	require.Contains(t, content, "case \"listGroups\":")
	require.Contains(t, content, "dispatcher.ListGroups(w, r)")
	require.Contains(t, content, "case \"getHealth\":")
	require.Contains(t, content, "w.Header().Set(\"Content-Type\", \"application/json\")")
	require.Contains(t, content, "_ = json.NewEncoder(w).Encode(map[string]string{\"status\": \"ok\"})")
	require.NotContains(t, content, "dispatcher.GetHealth(w, r)")
}

func TestEmit_OperationContractsIncludeManualAndBodyMetadata(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/auth/local-login",
				OperationID: "localLogin",
				Tags:        []string{"Auth"},
				RequestBody: &ir.RequestBody{Required: true, Contents: jsonContent(ir.SchemaRef{Ref: "LoginRequest"})},
				Responses: []ir.Response{
					{StatusCode: 200, Description: "ok"},
					{StatusCode: 401, Description: "unauthorized"},
				},
				Extensions: map[string]any{
					"x-apigen-manual": true,
					"x-authz":         map[string]any{"mode": "none"},
				},
			},
		},
		Schemas: map[string]ir.Schema{
			"LoginRequest": {Type: "object"},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, `"localLogin": {OperationID: "localLogin", Kind: "query", Namespace: "", Method: "POST", Path: "/auth/local-login"`)
	require.Contains(t, content, `DocumentedStatusCodes: []int{200, 401}`)
	require.Contains(t, content, `RequestBodyRequired: true`)
	require.Contains(t, content, `AuthzMode: "none"`)
	require.Contains(t, content, `Protected: true`)
	require.Contains(t, content, `Manual: true`)
}

func TestEmit_OperationContractsIncludeExtensionDefensiveCopies(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/workspaces/{workspace}/widgets",
				OperationID: "listWidgets",
				Namespace:   "WidgetAPI",
				Tags:        []string{"Widgets"},
				Parameters: []ir.Parameter{{
					Name: "workspace", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"},
				}},
				Responses: []ir.Response{{StatusCode: 200, Description: "ok"}},
				Command: &ir.Command{
					Owner:               "WidgetAPI",
					Audit:               ir.AuditPolicy{Required: true, SuccessAction: "widget.listed", Guarantee: "best-effort"},
					AdditionalExposures: []string{"ui"},
					Target:              &ir.OperationTarget{Parameter: "workspace", Type: "workspace"},
				},
				Extensions: map[string]any{
					"x-downstream": map[string]any{
						"enabled": true,
						"name":    "list_workspace_assets",
						"risk":    "read",
						"score":   1.5,
						"limit":   uint64(math.MaxUint64),
						"tags":    []any{"workspace", "lineage"},
						"nested":  map[string]any{"nullable": nil, "count": 3},
					},
				},
			},
		},
	}

	b, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "Extensions map[string]any")
	require.Contains(t, content, `Namespace: "WidgetAPI"`)
	require.Contains(t, content, `Command: &GenCommandContract{Owner: "WidgetAPI", Audit: GenAuditPolicy{Required: true, SuccessAction: "widget.listed", Guarantee: "best-effort"}, AdditionalExposures: []GenOperationSurface{"ui"}, Target: &GenOperationTarget{Parameter: "workspace", Type: "workspace"}`)
	require.Contains(t, content, `func GetAPIGenCommandRuntimeContract(operationID string) (apigencommand.Contract, bool)`)
	require.Contains(t, content, `Guarantee: apigencommand.Guarantee(contract.Command.Audit.Guarantee)`)
	require.Contains(t, content, `Extensions: map[string]any{"x-downstream": map[string]any{`)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module generatedtest

go 1.25.8

require github.com/Yacobolo/toolbelt/apigen v0.0.0

replace github.com/Yacobolo/toolbelt/apigen => `+apigenModuleRoot(t)+`
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.apigen.gen.go"), b, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server_test.go"), []byte(`package gen

import "testing"

type Error struct {
	Code int32
	Message string
}

func TestExtensionDefensiveCopies(t *testing.T) {
	contracts := GetAPIGenOperationContracts()
	contracts["listWidgets"].Command.AdditionalExposures[0] = GenOperationSurfaceAgent
	contracts["listWidgets"].Command.Target.Parameter = "mutated"
	agent := contracts["listWidgets"].Extensions["x-downstream"].(map[string]any)
	agent["enabled"] = false
	agent["tags"].([]any)[0] = "mutated"
	agent["nested"].(map[string]any)["count"] = 99

	first, ok := GetAPIGenOperationContract("listWidgets")
	if !ok {
		t.Fatal("missing operation")
	}
	if first.Command.AdditionalExposures[0] != GenOperationSurfaceUI || first.Command.Target.Parameter != "workspace" {
		t.Fatalf("command mutated: %#v", first.Command)
	}
	firstAgent := first.Extensions["x-downstream"].(map[string]any)
	if firstAgent["enabled"] != true {
		t.Fatalf("enabled mutated: %#v", firstAgent["enabled"])
	}
	if firstAgent["tags"].([]any)[0] != "workspace" {
		t.Fatalf("tags mutated: %#v", firstAgent["tags"])
	}
	if firstAgent["nested"].(map[string]any)["count"] != 3 {
		t.Fatalf("nested mutated: %#v", firstAgent["nested"])
	}
	if firstAgent["limit"].(uint64) != ^uint64(0) {
		t.Fatalf("limit changed type or value: %#v", firstAgent["limit"])
	}

	firstAgent["enabled"] = false
	second, _ := GetAPIGenOperationContract("listWidgets")
	if second.Extensions["x-downstream"].(map[string]any)["enabled"] != true {
		t.Fatal("single-operation accessor returned mutable global state")
	}
}
`), 0o644))

	cmd := exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestEmit_RejectsInvalidExtensionValues(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/widgets",
				OperationID: "listWidgets",
				Responses:   []ir.Response{{StatusCode: 200, Description: "ok"}},
				Extensions:  map[string]any{"x-downstream": map[string]any{"score": math.Inf(1)}},
			},
		},
	}

	_, err := Emit(doc, Options{})
	require.Error(t, err)
	require.ErrorContains(t, err, "extension")
	require.ErrorContains(t, err, "number must be finite")
}

func TestEmit_DoesNotMutateInputDocument(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{Method: "get", Path: "/z", OperationID: "listZ", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
			{Method: "get", Path: "/a", OperationID: "listA", Responses: []ir.Response{{StatusCode: 200, Description: "ok"}}},
		},
	}

	_, err := Emit(doc, Options{})
	require.NoError(t, err)
	require.Equal(t, "/z", doc.Endpoints[0].Path)
	require.Equal(t, "/a", doc.Endpoints[1].Path)
}

func TestEmit_GeneratesPathQueryAndHeaderBinding(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/groups/{groupId}/members",
				OperationID: "listGroupMembers",
				Parameters: []ir.Parameter{
					{Name: "groupId", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
					{Name: "max_results", In: "query", Required: false, Schema: ir.SchemaRef{Type: "integer", Format: "int32"}},
					{Name: "accept", In: "header", Required: true, Schema: ir.SchemaRef{Type: "string", Enum: []string{"application/json", "application/octet-stream"}}},
				},
				Responses: []ir.Response{{StatusCode: 200, Description: "ok"}},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "ListGroupMembers(w http.ResponseWriter, r *http.Request, groupId string, params GenListGroupMembersParams, headers GenListGroupMembersHeaders)")
	require.Contains(t, content, "apigenchi.BindPathParameter(\"groupId\", apigenchi.URLParam(r, \"groupId\"), true, &groupId)")
	require.Contains(t, content, "apigenchi.BindQueryParameter(r.URL.Query(), \"max_results\", false, &params.MaxResults)")
	require.Contains(t, content, "apigenchi.BindHeaderParameter(r.Header, \"accept\", true, &headers.Accept)")
	require.Contains(t, content, `Kind: "path_parameter"`)
	require.Contains(t, content, `Kind: "query_parameter"`)
	require.Contains(t, content, `Kind: "header_parameter"`)
	require.Contains(t, content, "dispatcher.ListGroupMembers(w, r, groupId, params, headers)")
	require.Contains(t, content, "type GenListGroupMembersParams struct {")
	require.Contains(t, content, "\tMaxResults *int32")
	require.Contains(t, content, "type GenListGroupMembersHeaders struct {")
	require.Contains(t, content, "\tAccept string")
	require.Contains(t, content, "type GenTransportErrorResponder interface {")
	require.Contains(t, content, "func writeAPIGenError(responder GenTransportErrorResponder")
	require.Contains(t, content, "var request GenListGroupMembersRequest")
	require.Contains(t, content, "\"fmt\"")
	require.Contains(t, content, "response, err := b.handler.ListGroupMembers(r.Context(), request)")
	require.Contains(t, content, "if err := response.VisitListGroupMembersResponse(w); err != nil")
	require.Contains(t, content, "type GenListGroupMembersRequest struct {")
	require.Contains(t, content, "\tGroupId string")
	require.Contains(t, content, "\tParams GenListGroupMembersParams")
	require.Contains(t, content, "\tHeaders GenListGroupMembersHeaders")
	require.Contains(t, content, "type GenListGroupMembersResponse interface {")
	require.Contains(t, content, "\tVisitListGroupMembersResponse(w http.ResponseWriter) error")
	require.Contains(t, content, "type GenListGroupMembers200ResponseHeaders struct {")
	require.Contains(t, content, "type GenListGroupMembers200Response struct {")
	require.Contains(t, content, "\tHeaders GenListGroupMembers200ResponseHeaders")
	require.Contains(t, content, "func (response GenListGroupMembers200Response) VisitListGroupMembersResponse(w http.ResponseWriter) error {")
	require.Contains(t, content, "w.Header().Set(\"X-RateLimit-Limit\", fmt.Sprint(response.Headers.XRateLimitLimit))")
	require.Contains(t, content, "w.WriteHeader(200)")
	require.Contains(t, content, "return nil")
	require.Contains(t, content, "ListGroupMembers(ctx context.Context, request GenListGroupMembersRequest) (GenListGroupMembersResponse, error)")
	require.NotContains(t, content, "type ListGroupMembers200Response = GenListGroupMembers200Response")
}

func TestEmit_GeneratesStrictJSONBodyDecoding(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"CreatePipelineRequest": {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/pipelines",
				OperationID: "createPipeline",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "CreatePipelineRequest"})},
				Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "\"io\"")
	require.Contains(t, content, "func decodeAPIGenJSONBody(body io.Reader, dest any, requiredBody bool, requiredFields ...string) error {")
	require.Contains(t, content, "decoder.DisallowUnknownFields()")
	require.Contains(t, content, "return fmt.Errorf(\"request body must not be empty\")")
	require.Contains(t, content, "return fmt.Errorf(\"request body must contain a single JSON value\")")
	require.Contains(t, content, "decoder := json.NewDecoder(strings.NewReader(string(raw)))")
	require.Contains(t, content, "if err := decodeAPIGenJSONBody(r.Body, &body, false); err != nil {")
	require.Contains(t, content, `Kind: "malformed_body"`)
	require.Contains(t, content, `Kind: "handler"`)
	require.Contains(t, content, `Kind: "response_serialization"`)
}

func TestEmit_GeneratesInjectedTransportErrorResponder(t *testing.T) {
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Problem API", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Problem": {Type: "object"},
			"Request": {Type: "object"},
		},
		TransportErrors: &ir.TransportErrors{
			Schema:      ir.SchemaRef{Ref: "Problem"},
			ContentType: "application/problem+json",
			Failures: map[string]ir.TransportFailure{
				"malformed_body":         {StatusCode: 400, Code: "malformed_body", PublicDetail: "Malformed body."},
				"unsupported_media_type": {StatusCode: 415, Code: "unsupported_media_type", PublicDetail: "Unsupported media type."},
				"handler":                {StatusCode: 500, Code: "internal", PublicDetail: "Internal error."},
				"response_serialization": {StatusCode: 500, Code: "internal", PublicDetail: "Internal error."},
			},
		},
		Endpoints: []ir.Endpoint{{Method: "post", Path: "/", OperationID: "create", RequestBody: &ir.RequestBody{Required: true, Contents: jsonContent(ir.SchemaRef{Ref: "Request"})}, Responses: []ir.Response{{StatusCode: 204, Description: "ok"}}}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type GenTransportError struct")
	require.Contains(t, content, "Cause error")
	require.Contains(t, content, "type GenTransportErrorResponder interface")
	require.Contains(t, content, "RegisterAPIGenStrictRoutes(router apigenchi.Router, handler GenStrictServerInterface, responder GenTransportErrorResponder)")
	require.Contains(t, content, `Kind: "unsupported_media_type"`)
	require.Contains(t, content, `StatusCode: 415`)
	require.Contains(t, content, `Code: "unsupported_media_type"`)
	require.NotContains(t, content, "Encode(Error{")
}

func TestEmit_GeneratesTransportAwareBodies(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"FormRequest": {Type: "object", Properties: map[string]ir.SchemaProperty{"name": {Schema: ir.SchemaRef{Type: "string"}}}},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "put",
				Path:        "/text",
				OperationID: "replaceText",
				RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{ContentType: "text/plain", BodyKind: "text", Schema: &ir.SchemaRef{Type: "string"}}}},
				Responses:   []ir.Response{{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "text/plain", BodyKind: "text", Schema: &ir.SchemaRef{Type: "string"}}}}},
			},
			{
				Method:      "put",
				Path:        "/blob",
				OperationID: "replaceBlob",
				RequestBody: &ir.RequestBody{Required: true, Contents: binaryContent()},
				Responses:   []ir.Response{{StatusCode: 200, Description: "ok", Contents: binaryContent()}},
			},
			{
				Method:      "put",
				Path:        "/form",
				OperationID: "replaceForm",
				RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{ContentType: "application/x-www-form-urlencoded", BodyKind: "form_urlencoded", Schema: &ir.SchemaRef{Ref: "FormRequest"}}}},
				Responses:   []ir.Response{{StatusCode: 204, Description: "ok"}},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type GenReplaceTextBody = string")
	require.Contains(t, content, "type GenReplaceBlobBody = []byte")
	require.Contains(t, content, "type GenReplaceFormBody = GenSchemaFormRequest")
	require.Contains(t, content, "decodeAPIGenTextBody(r.Body, true)")
	require.Contains(t, content, "decodeAPIGenBytesBody(r.Body, true)")
	require.Contains(t, content, "decodeAPIGenFormBody(r, &body, true")
	require.Contains(t, content, "type GenReplaceText200TextResponse struct")
	require.Contains(t, content, "type GenReplaceBlob200BinaryResponse struct")
	require.Contains(t, content, "w.Header().Set(\"Content-Type\", \"text/plain\")")
	require.Contains(t, content, "w.Header().Set(\"Content-Type\", \"application/octet-stream\")")
	require.NotContains(t, content, "type GenReplaceBlobBody = GenSchema")
	require.NotContains(t, content, "type GenReplaceBlob200JSONResponse")
}

func TestEmit_GeneratesMultiContentResponseVariants(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Artifact": {Type: "object", Properties: map[string]ir.SchemaProperty{"id": {Schema: ir.SchemaRef{Type: "string"}}}},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/artifacts/{id}",
				OperationID: "getArtifact",
				Parameters:  []ir.Parameter{{Name: "id", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}}},
				Responses: []ir.Response{{
					StatusCode:  200,
					Description: "ok",
					Contents: []ir.BodyContent{
						{ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Artifact"}},
						{ContentType: "application/octet-stream", BodyKind: "binary", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
					},
				}},
			},
		},
	}

	b, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type GenGetArtifact200ApplicationJSONResponse struct")
	require.Contains(t, content, "type GenGetArtifact200ApplicationOctetStreamResponse struct")
	require.Contains(t, content, "func (response GenGetArtifact200ApplicationJSONResponse) VisitGetArtifactResponse")
	require.Contains(t, content, "func (response GenGetArtifact200ApplicationOctetStreamResponse) VisitGetArtifactResponse")
	require.Contains(t, content, "w.Header().Set(\"Content-Type\", \"application/json\")")
	require.Contains(t, content, "w.Header().Set(\"Content-Type\", \"application/octet-stream\")")
	require.NotContains(t, content, "type GenGetArtifact200JSONResponse")
	require.NotContains(t, content, "type GenGetArtifact200BinaryResponse")

	assertGeneratedServerCompiles(t, b, `package gen

type Error struct {
	Code int32
	Message string
}

type GenSchemaArtifact struct {
	Id string
}

var _ GenGetArtifactResponse = GenGetArtifact200ApplicationJSONResponse{}
var _ GenGetArtifactResponse = GenGetArtifact200ApplicationOctetStreamResponse{}
`)
}

func TestEmit_GeneratesMultipartBodyDecoding(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"Metadata": {
				Type:       "object",
				Required:   []string{"name"},
				Properties: map[string]ir.SchemaProperty{"name": {Schema: ir.SchemaRef{Type: "string"}}},
			},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/artifacts",
				OperationID: "uploadArtifact",
				RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
					ContentType: "multipart/form-data",
					BodyKind:    "multipart",
					Parts: []ir.MultipartPart{
						{Name: "metadata", WireName: "metadata", PartKind: "model", Required: true, ContentType: "application/json", BodyKind: "json", Schema: &ir.SchemaRef{Ref: "Metadata"}},
						{Name: "note", WireName: "note", PartKind: "model", Required: false, ContentType: "text/plain", BodyKind: "text", Schema: &ir.SchemaRef{Type: "string"}},
						{Name: "artifact", WireName: "artifact", PartKind: "model", Required: true, ContentType: "application/octet-stream", BodyKind: "file", Filename: true, Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
						{Name: "checksums", WireName: "checksum", PartKind: "model", Required: false, Repeated: true, ContentType: "text/plain", BodyKind: "text", Schema: &ir.SchemaRef{Type: "string"}},
						{Name: "part5", PartKind: "tuple", Required: true, ContentType: "application/octet-stream", BodyKind: "binary", Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
					},
				}}},
				Responses: []ir.Response{{StatusCode: 204, Description: "uploaded"}},
			},
		},
	}

	b, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type GenUploadArtifactMultipartBody struct {")
	require.Contains(t, content, "\tMetadata GenSchemaMetadata")
	require.Contains(t, content, "\tNote *string")
	require.Contains(t, content, "\tArtifact GenFile")
	require.Contains(t, content, "\tChecksums []string")
	require.Contains(t, content, "\tPart5 []byte")
	require.Contains(t, content, "type GenUploadArtifactBody = GenUploadArtifactMultipartBody")
	require.Contains(t, content, `parts, err := readAPIGenMultipartParts(r, map[string]bool{"artifact": true}, map[int]bool{})`)
	require.Contains(t, content, "defer cleanupAPIGenMultipartParts(parts)")
	require.Contains(t, content, `if err := validateAPIGenMultipartParts(parts, map[string]apigenMultipartRule{`)
	require.Contains(t, content, `"metadata": {Repeated: false}`)
	require.Contains(t, content, `"checksum": {Repeated: true}`)
	require.Contains(t, content, `}, 5); err != nil {`)
	require.Contains(t, content, "metadataParts := apigenMultipartPartsByName(parts, \"metadata\")")
	require.Contains(t, content, "if !metadataOK {")
	require.Contains(t, content, "json.Unmarshal(metadataPart.Raw, &metadataValue)")
	require.Contains(t, content, "noteValue := string(notePart.Raw)")
	require.Contains(t, content, "artifactValue := genFileFromMultipartPart(artifactPart, \"application/octet-stream\")")
	require.Contains(t, content, "for _, checksumsPart := range checksumsParts {")
	require.Contains(t, content, "part5Parts := apigenMultipartPartsByIndex(parts, 4)")
	require.Contains(t, content, "func readAPIGenMultipartParts(r *http.Request, fileNames map[string]bool, fileIndexes map[int]bool) ([]apigenMultipartPart, error) {")
	require.Contains(t, content, "tempFile, err := os.CreateTemp(\"\", \"apigen-multipart-*\")")
	require.Contains(t, content, "func cleanupAPIGenMultipartParts(parts []apigenMultipartPart) {")

	assertGeneratedServerCompiles(t, b, `package gen

type Error struct {
	Code int32
	Message string
}

type GenSchemaMetadata struct {
	Name string
}

var _ = GenUploadArtifactBody{
	Metadata: GenSchemaMetadata{Name: "duck"},
	Artifact: GenFile{Contents: []byte("payload")},
	Checksums: []string{"abc"},
	Part5: []byte("payload"),
}
`)
}

func TestEmit_GeneratesMixedMultipartBodyDecodingByOrder(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/mixed",
				OperationID: "uploadMixed",
				RequestBody: &ir.RequestBody{Required: true, Contents: []ir.BodyContent{{
					ContentType: "multipart/mixed",
					BodyKind:    "multipart",
					Parts: []ir.MultipartPart{
						{Name: "part1", WireName: "metadata", PartKind: "tuple", Required: true, ContentType: "text/plain", BodyKind: "text", Schema: &ir.SchemaRef{Type: "string"}},
						{Name: "part2", WireName: "artifact", PartKind: "tuple", Required: true, ContentType: "application/octet-stream", BodyKind: "file", Filename: true, Schema: &ir.SchemaRef{Type: "string", Format: "binary"}},
					},
				}}},
				Responses: []ir.Response{{StatusCode: 204, Description: "uploaded"}},
			},
		},
	}

	b, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, `parts, err := readAPIGenMultipartParts(r, map[string]bool{}, map[int]bool{1: true})`)
	require.Contains(t, content, `if err := validateAPIGenMultipartParts(parts, map[string]apigenMultipartRule{}, 2); err != nil {`)
	require.Contains(t, content, "part1Parts := apigenMultipartPartsByIndex(parts, 0)")
	require.Contains(t, content, "part2Parts := apigenMultipartPartsByIndex(parts, 1)")
	require.NotContains(t, content, `apigenMultipartPartsByName(parts, "metadata")`)
	require.NotContains(t, content, `apigenMultipartPartsByName(parts, "artifact")`)

	assertGeneratedServerCompiles(t, b, `package gen

type Error struct {
	Code int32
	Message string
}

var _ = GenUploadMixedBody{
	Part1: "metadata",
	Part2: GenFile{},
}
`)
}

func TestEmit_GeneratesGenFileRequestAndResponse(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "put",
				Path:        "/artifact",
				OperationID: "putArtifact",
				RequestBody: &ir.RequestBody{Required: true, Contents: fileContent()},
				Responses: []ir.Response{{
					StatusCode:  200,
					Description: "ok",
					Contents:    fileContent(),
				}},
			},
		},
	}

	b, err := Emit(doc, Options{PackageName: "gen"})
	require.NoError(t, err)
	content := string(b)
	require.Contains(t, content, "type GenFile struct {")
	require.Contains(t, content, "\tContents []byte")
	require.Contains(t, content, "\tReader io.ReadCloser")
	require.Contains(t, content, "\tContentType string")
	require.Contains(t, content, "\tFilename *string")
	require.Contains(t, content, "\tSize *int64")
	require.Contains(t, content, "type GenPutArtifactBody = GenFile")
	require.Contains(t, content, `body = GenFile{Reader: r.Body, ContentType: r.Header.Get("Content-Type"), Size: apigenContentLengthPointer(r.ContentLength)}`)
	require.Contains(t, content, "type GenPutArtifact200FileResponse struct {")
	require.Contains(t, content, "Body GenFile")
	require.Contains(t, content, "writeAPIGenFileResponse(w, response.Body, \"application/octet-stream\", 200)")
	require.Contains(t, content, "if file.Reader != nil {")
	require.Contains(t, content, "_, err := io.Copy(w, file.Reader)")

	assertGeneratedServerCompiles(t, b, `package gen

type Error struct {
	Code int32
	Message string
}

var _ GenPutArtifactResponse = GenPutArtifact200FileResponse{Body: GenFile{Contents: []byte("payload")}}
`)
}

func TestEmit_UsesNamedRequestBodySchemas(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"CreateAPIKeyRequest":   {Type: "object"},
			"CreatePipelineRequest": {Type: "object"},
			"MetricQueryRequest":    {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/api-keys",
				OperationID: "createAPIKey",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "CreateAPIKeyRequest"})},
				Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			},
			{
				Method:      "post",
				Path:        "/pipelines",
				OperationID: "createPipeline",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "CreatePipelineRequest"})},
				Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			},
			{
				Method:      "post",
				Path:        "/metric-queries:run",
				OperationID: "runMetricQuery",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Ref: "MetricQueryRequest"})},
				Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenCreateAPIKeyBody = GenSchemaCreateAPIKeyRequest")
	require.Contains(t, content, "type GenCreatePipelineBody = GenSchemaCreatePipelineRequest")
	require.Contains(t, content, "type GenRunMetricQueryBody = GenSchemaMetricQueryRequest")
}

func TestEmit_FailsForUnnamedRequestBodySchema(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/widgets",
				OperationID: "createWidget",
				RequestBody: &ir.RequestBody{Contents: jsonContent(ir.SchemaRef{Type: "object"})},
				Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			},
		},
	}

	_, err := Emit(doc, Options{})
	require.Error(t, err)
	require.ErrorContains(t, err, "requires a named IR schema")
	require.ErrorContains(t, err, "createWidget")
}

func TestEmit_ImportsTimeForDateTimeParameters(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"QueryResult":         {Type: "object"},
			"SubmitQueryResponse": {Type: "object"},
			"CancelQueryResponse": {Type: "object"},
			"PaginatedGroups":     {Type: "object"},
			"Error":               {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "get",
				Path:        "/audit-logs",
				OperationID: "listAuditLogs",
				Parameters: []ir.Parameter{
					{Name: "from", In: "query", Schema: ir.SchemaRef{Type: "string", Format: "date-time"}},
				},
				Responses: []ir.Response{{StatusCode: 200, Description: "ok"}},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "\"time\"")
	require.Contains(t, content, "type GenListAuditLogsParams struct {")
	require.Contains(t, content, "\tFrom *time.Time")
}

func TestEmit_EmitsCanonicalResponseTypesOnly(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Schemas: map[string]ir.Schema{
			"QueryResult":         {Type: "object"},
			"SubmitQueryResponse": {Type: "object"},
			"CancelQueryResponse": {Type: "object"},
			"PaginatedGroups":     {Type: "object"},
			"Error":               {Type: "object"},
		},
		Endpoints: []ir.Endpoint{
			{
				Method:      "post",
				Path:        "/query",
				OperationID: "executeQuery",
				Responses:   []ir.Response{{StatusCode: 201, Description: "created", Contents: jsonContent(ir.SchemaRef{Ref: "#/schemas/QueryResult"})}},
			},
			{
				Method:      "post",
				Path:        "/queries",
				OperationID: "submitQuery",
				Responses:   []ir.Response{{StatusCode: 201, Description: "created", Contents: jsonContent(ir.SchemaRef{Ref: "#/schemas/SubmitQueryResponse"})}},
			},
			{
				Method:      "post",
				Path:        "/queries/{queryId}/cancel",
				OperationID: "cancelQuery",
				Responses:   []ir.Response{{StatusCode: 201, Description: "created", Contents: jsonContent(ir.SchemaRef{Ref: "#/schemas/CancelQueryResponse"})}},
			},
			{
				Method:      "post",
				Path:        "/security/column-masks/{maskName}/bindings",
				OperationID: "bindColumnMask",
				Responses:   []ir.Response{{StatusCode: 201, Description: "created"}},
			},
			{
				Method:      "get",
				Path:        "/groups",
				OperationID: "listGroups",
				Responses: []ir.Response{
					{StatusCode: 200, Description: "ok", Contents: jsonContent(ir.SchemaRef{Ref: "#/schemas/PaginatedGroups"})},
					{StatusCode: 403, Description: "forbidden", Contents: jsonContent(ir.SchemaRef{Ref: "#/schemas/Error"})},
				},
			},
		},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenExecuteQuery201JSONResponse")
	require.Contains(t, content, "type GenSubmitQuery201JSONResponse")
	require.Contains(t, content, "type GenCancelQuery201JSONResponse")
	require.Contains(t, content, "type GenBindColumnMask201Response struct {")
	require.Contains(t, content, "type GenListGroups403JSONResponse struct {")
	require.NotContains(t, content, "type ExecuteQuery200JSONResponse")
	require.NotContains(t, content, "type SubmitQuery202JSONResponse")
	require.NotContains(t, content, "type CancelQuery200JSONResponse")
	require.NotContains(t, content, "type BindColumnMask204Response")
	require.NotContains(t, content, "type BadRequestResponseHeaders =")
	require.NotContains(t, content, "type UnauthorizedJSONResponse =")
}

func TestEmit_UsesIRResponseHeadersForVisitMethods(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: "v4",
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "t", Version: "1"},
		Endpoints: []ir.Endpoint{{
			Method:      "get",
			Path:        "/widgets/{id}",
			OperationID: "getWidget",
			Responses: []ir.Response{{
				StatusCode:  200,
				Description: "ok",
				Headers: []ir.Header{{
					Name:   "X-Trace-Id",
					Schema: ir.SchemaRef{Type: "string"},
				}},
				Contents: jsonContent(ir.SchemaRef{Type: "string"}),
			}},
		}},
	}

	b, err := Emit(doc, Options{})
	require.NoError(t, err)
	content := string(b)

	require.Contains(t, content, "type GenGetWidget200ResponseHeaders struct {")
	require.Contains(t, content, "\tXTraceId string")
	require.Contains(t, content, "w.Header().Set(\"X-Trace-Id\", fmt.Sprint(response.Headers.XTraceId))")
	require.Contains(t, content, "payload, err := json.Marshal(response.Body)")
	require.Contains(t, content, "_, err = w.Write(payload)")
}

func TestPathParamTypeName(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		param    ir.Parameter
		expected string
	}{
		{
			name:     "default string",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "string"}},
			expected: "string",
		},
		{
			name:     "int32",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "integer", Format: "int32"}},
			expected: "int32",
		},
		{
			name:     "int64",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "integer", Format: "int64"}},
			expected: "int64",
		},
		{
			name:     "integer default",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "integer"}},
			expected: "int",
		},
		{
			name:     "float",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "number", Format: "float"}},
			expected: "float32",
		},
		{
			name:     "double",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "number", Format: "double"}},
			expected: "float64",
		},
		{
			name:     "number default",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "number"}},
			expected: "float64",
		},
		{
			name:     "boolean",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "boolean"}},
			expected: "bool",
		},
		{
			name:     "unknown type fallback",
			param:    ir.Parameter{Schema: ir.SchemaRef{Type: "object"}},
			expected: "string",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			require.Equal(t, tc.expected, pathParamTypeName(tc.param))
		})
	}
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
