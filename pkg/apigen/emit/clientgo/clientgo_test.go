package clientgo

import (
	"go/format"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
	"github.com/stretchr/testify/require"
)

func TestEmit_GeneratesTypedClientOverGenericTransport(t *testing.T) {
	t.Helper()

	requestRef := ir.SchemaRef{Ref: "CreateWidgetRequest"}
	responseRef := ir.SchemaRef{Ref: "Widget"}
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/v1"},
		Info:          ir.Info{Title: "Widgets", Version: "1.0.0"},
		Schemas: map[string]ir.Schema{
			"WidgetDeletedAuditPayload": {
				Type: "object", Properties: map[string]ir.SchemaProperty{"widgetId": {Schema: ir.SchemaRef{Type: "string"}}}, Required: []string{"widgetId"},
			},
			"CreateWidgetRequest": {
				Type: "object",
				Properties: map[string]ir.SchemaProperty{
					"name": {Schema: ir.SchemaRef{Type: "string"}},
				},
				Required: []string{"name"},
			},
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
				Path:        "/accounts/{account}/widgets",
				OperationID: "createWidget",
				Parameters: []ir.Parameter{
					{Name: "account", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"}},
					{Name: "dry_run", In: "query", Schema: ir.SchemaRef{Type: "boolean"}},
					{Name: "X-Request-ID", In: "header", Schema: ir.SchemaRef{Type: "string"}},
				},
				RequestBody: &ir.RequestBody{
					Required: true,
					Contents: []ir.BodyContent{{
						ContentType: "application/json",
						BodyKind:    "json",
						Schema:      &requestRef,
					}},
				},
				Responses: []ir.Response{{
					StatusCode:  201,
					Description: "created",
					Contents: []ir.BodyContent{{
						ContentType: "application/json",
						BodyKind:    "json",
						Schema:      &responseRef,
					}},
				}},
			},
			{
				Method:      "delete",
				Path:        "/widgets/{widget}",
				OperationID: "deleteWidget",
				Kind:        "command",
				Parameters: []ir.Parameter{{
					Name: "widget", In: "path", Required: true, Schema: ir.SchemaRef{Type: "string"},
				}},
				Responses: []ir.Response{
					{StatusCode: 204, Description: "deleted"},
					{StatusCode: 404, Description: "not found"},
					{StatusCode: 409, Description: "conflict"},
				},
				Command: &ir.Command{
					Owner: "WidgetAPI",
					Audit: ir.AuditPolicy{Required: true, SuccessAction: "widget.deleted", Guarantee: "best-effort", Payload: &ir.AuditPayload{Schema: ir.SchemaRef{Ref: "WidgetDeletedAuditPayload"}, SchemaVersion: 1, Retention: "security", Fields: []ir.AuditField{{Name: "widgetId", Sensitivity: "internal"}}}},
					Failures: []ir.CommandFailure{
						{Kind: "conflict", StatusCode: 409, Code: "WIDGET_CONFLICT", PublicDetail: "Widget conflict."},
						{Kind: "not_found", StatusCode: 404, Code: "WIDGET_NOT_FOUND", PublicDetail: "Widget not found."},
					},
				},
			},
		},
	}

	content, err := Emit(doc, Options{PackageName: "widgetapi"})
	require.NoError(t, err)
	_, err = format.Source(content)
	require.NoError(t, err, string(content))
	generated := string(content)

	require.Contains(t, generated, `GenOperationCreateWidget = "createWidget"`)
	require.Contains(t, generated, "type GenCreateWidgetClientRequest struct {")
	require.Contains(t, generated, "Account string")
	require.Contains(t, generated, "Params GenCreateWidgetClientParams")
	require.Contains(t, generated, "Headers GenCreateWidgetClientHeaders")
	require.Contains(t, generated, "Body GenSchemaCreateWidgetRequest")
	require.Contains(t, generated, "type GenCreateWidgetClientResponse struct {")
	require.Contains(t, generated, "Body GenSchemaWidget")
	require.Contains(t, generated, "StatusCode int")
	require.Contains(t, generated, "func (client *GenClient) CreateWidget(ctx context.Context, request GenCreateWidgetClientRequest) (GenCreateWidgetClientResponse, error)")
	require.Contains(t, generated, `Path: "/v1/accounts/{account}/widgets"`)
	require.Contains(t, generated, `PathParams: map[string]string{"account": apigenclient.FormatValue(request.Account)}`)
	require.Contains(t, generated, `apigenclient.AddQuery(query, "dry_run", request.Params.DryRun, false)`)
	require.Contains(t, generated, `apigenclient.AddHeader(headers, "X-Request-ID", request.Headers.XRequestID)`)
	require.Contains(t, generated, "Body: request.Body")
	require.Contains(t, generated, `Accept: "application/json"`)
	require.Contains(t, generated, "type GenDeleteWidgetClientResponse struct {")
	require.Contains(t, generated, "type GenDeleteWidgetFailure interface {")
	require.Contains(t, generated, "Problem() apigenclient.ProblemDetails")
	require.Contains(t, generated, "func MatchGenDeleteWidgetFailure[T any](failure GenDeleteWidgetFailure, onConflict func(apigenclient.ProblemDetails) T, onNotFound func(apigenclient.ProblemDetails) T) T")
	require.Contains(t, generated, `case "WIDGET_NOT_FOUND":`)
	require.Contains(t, generated, `if problem.Response.StatusCode != 404`)
	require.Contains(t, generated, "func (client *GenClient) DeleteWidget(ctx context.Context, request GenDeleteWidgetClientRequest) (GenDeleteWidgetClientResponse, error)")
	require.Contains(t, generated, "err = genDeleteWidgetFailureFromError(err)")
	require.NotContains(t, generated, "internal/app")
}

func TestEmit_RejectsIncompatibleSuccessSchemas(t *testing.T) {
	t.Helper()

	first := ir.SchemaRef{Ref: "First"}
	second := ir.SchemaRef{Ref: "Second"}
	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Example", Version: "1.0.0"},
		Schemas: map[string]ir.Schema{
			"First":  {Type: "object"},
			"Second": {Type: "object"},
		},
		Endpoints: []ir.Endpoint{{
			Method:      "get",
			Path:        "/example",
			OperationID: "getExample",
			Responses: []ir.Response{
				{StatusCode: 200, Description: "ok", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &first}}},
				{StatusCode: 202, Description: "accepted", Contents: []ir.BodyContent{{ContentType: "application/json", BodyKind: "json", Schema: &second}}},
			},
		}},
	}

	_, err := Emit(doc, Options{})
	require.ErrorContains(t, err, `operation "getExample"`)
	require.ErrorContains(t, err, "incompatible JSON body schemas")
}

func TestEmit_RejectsCollidingGeneratedOperationNames(t *testing.T) {
	t.Helper()

	doc := ir.Document{
		SchemaVersion: ir.CurrentSchemaVersion,
		API:           ir.API{BasePath: "/"},
		Info:          ir.Info{Title: "Example", Version: "1.0.0"},
		Endpoints: []ir.Endpoint{
			{Method: "get", Path: "/first", OperationID: "get-widget", Responses: []ir.Response{{StatusCode: 204, Description: "ok"}}},
			{Method: "get", Path: "/second", OperationID: "get_widget", Responses: []ir.Response{{StatusCode: 204, Description: "ok"}}},
		},
	}

	_, err := Emit(doc, Options{})
	require.ErrorContains(t, err, `operation IDs "get-widget" and "get_widget"`)
	require.ErrorContains(t, err, "GenGetWidget")
}
