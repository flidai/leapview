package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/go-chi/chi/v5"
)

type Scope struct {
	ProjectID      string
	PrincipalID    string
	ConversationID string
	DevAuthBypass  bool
	Credential     CredentialScope
}

type CredentialScope struct {
	ProjectID    string
	Restricted   bool
	Capabilities []string
}

type APIGenAuthorizeFunc func(ctx context.Context, scope Scope, operationID string) (agentcore.ToolResult, bool)

type APIGenDispatchFunc func(scope Scope, operationID string, writer http.ResponseWriter, request *http.Request) bool

type APIGenProvider struct {
	Authorize  APIGenAuthorizeFunc
	Dispatch   APIGenDispatchFunc
	Operations []APIGenOperation
}

const maxAgentQueryRows = 50

func (p APIGenProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	definitions := make([]agentcore.ToolDefinition, 0, len(p.Operations))
	for _, operation := range p.Operations {
		outputSchema := requireToolObjectSchema(operation.Tool.OutputSchema)
		if operation.Tool.Name == "query_dashboard_visual" {
			outputSchema = json.RawMessage(agentcontracts.DashboardVisualQueryResultSchemaJSON)
		}
		definitions = append(definitions, agentcore.ToolDefinition{
			Name:         operation.Tool.Name,
			Description:  operation.Tool.Description,
			InputSchema:  boundCuratedQueryInputSchema(operation.Tool.Name, operation.Tool.InputSchema),
			OutputSchema: outputSchema,
			Effect:       string(operation.Tool.Effect),
			Tags:         append([]string(nil), operation.Tool.Tags...),
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				return p.Run(ctx, scope, operation, call), nil
			}),
		})
	}
	return definitions
}

func boundCuratedQueryInputSchema(toolName string, input json.RawMessage) json.RawMessage {
	if toolName != "query_semantic_model" && toolName != "query_dashboard_visual" {
		return append(json.RawMessage(nil), input...)
	}
	var schema map[string]any
	if err := json.Unmarshal(input, &schema); err != nil {
		return append(json.RawMessage(nil), input...)
	}
	properties, _ := schema["properties"].(map[string]any)
	limit, _ := properties["limit"].(map[string]any)
	if limit == nil {
		return append(json.RawMessage(nil), input...)
	}
	limit["maximum"] = maxAgentQueryRows
	encoded, err := json.Marshal(schema)
	if err != nil {
		return append(json.RawMessage(nil), input...)
	}
	return encoded
}

func requireToolObjectSchema(input json.RawMessage) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal(input, &schema); err != nil || schema == nil {
		return append(json.RawMessage(nil), input...)
	}
	if _, ok := schema["type"]; ok {
		return append(json.RawMessage(nil), input...)
	}
	schema["type"] = "object"
	output, err := json.Marshal(schema)
	if err != nil {
		return append(json.RawMessage(nil), input...)
	}
	return output
}

func (p APIGenProvider) Run(ctx context.Context, scope Scope, operation APIGenOperation, call agentcore.ToolCall) agentcore.ToolResult {
	if p.Authorize == nil {
		return apigenAgentToolError("authorization_failed", "agent tool authorizer is not configured")
	}
	arguments := normalizeCuratedQueryArguments(operation.Tool.Name, call.Arguments)
	request, err := agenttool.BuildRequest(operation.Tool, arguments, agenttool.Context{"project": scope.ProjectID})
	if err != nil {
		return agentToolRuntimeError(err)
	}
	request = withAPIGenRouteContext(request, operation.Tool.Path)
	runScope := scope
	runScope.ProjectID = strings.TrimSpace(chi.URLParam(request, "project"))
	if errResult, ok := p.Authorize(ctx, runScope, operation.Contract.OperationID); !ok {
		return errResult
	}
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{
		Surface:     dataquery.SurfaceAgent,
		Operation:   dataquery.OperationAgentQuery,
		PrincipalID: runScope.PrincipalID,
		RequestID:   call.ID,
		ObjectType:  "agent_tool",
		ObjectID:    operation.Tool.Name,
	})
	if operation.Tool.Name == "query_dashboard_visual" {
		ctx = dashboardapi.WithAgentVisualProjection(ctx)
	}
	request = request.WithContext(ctx)
	if strings.TrimSpace(call.ID) != "" {
		request.Header.Set("X-Request-ID", call.ID)
	}
	request = withAPIGenRouteContext(request, operation.Tool.Path)
	if p.Dispatch == nil {
		return apigenAgentToolError("operation_not_found", "APIGen operation dispatcher is not configured")
	}
	capture := newResponseCapture(request)
	if ok := p.Dispatch(runScope, operation.Contract.OperationID, capture, request); !ok {
		return apigenAgentToolError("operation_not_found", "APIGen operation is not dispatchable")
	}
	responseContract := operation.Tool
	if operation.Tool.Name == "query_dashboard_visual" {
		responseContract.OutputSchema = json.RawMessage(agentcontracts.DashboardVisualQueryResultSchemaJSON)
	}
	result, err := agenttool.ProjectResponse(responseContract, capture.Response())
	if err != nil {
		return agentToolRuntimeError(err)
	}
	if result.IsError {
		return apigenAgentToolError(agentToolHTTPErrorCode(result.StatusCode), agentToolHTTPErrorMessage(result.Content, result.StatusCode))
	}
	return agentcore.ToolResult{Content: result.Content, IsError: result.IsError}
}

func agentToolHTTPErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "invalid_arguments"
	case http.StatusUnauthorized:
		return "authentication_required"
	case http.StatusForbidden:
		return "access_denied"
	case http.StatusNotFound:
		return "resource_not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		return "agent_tool_failed"
	}
}

func agentToolHTTPErrorMessage(content any, status int) string {
	if envelope, ok := content.(map[string]any); ok {
		if body, ok := envelope["body"].(map[string]any); ok {
			for _, key := range []string{"message", "detail", "title"} {
				if message, ok := body[key].(string); ok && strings.TrimSpace(message) != "" {
					return strings.TrimSpace(message)
				}
			}
		}
	}
	message := strings.TrimSpace(http.StatusText(status))
	if message == "" {
		message = "agent tool request failed"
	}
	return message
}

func normalizeCuratedQueryArguments(toolName string, arguments json.RawMessage) json.RawMessage {
	var input map[string]any
	if err := json.Unmarshal(arguments, &input); err != nil {
		return arguments
	}
	switch toolName {
	case "query_dashboard_visual":
		dashboardID, _ := input["dashboard"].(string)
		input["page"] = stripCatalogRefPrefix(input["page"], dashboardID)
		input["visual"] = stripCatalogRefPrefix(input["visual"], dashboardID)
	case "query_semantic_model":
		modelID, _ := input["model"].(string)
		for _, key := range []string{"dimensions", "metrics", "time", "sort", "filters"} {
			normalizeCatalogFieldValues(input[key], modelID)
		}
	}
	normalized, err := json.Marshal(input)
	if err != nil {
		return arguments
	}
	return normalized
}

func normalizeCatalogFieldValues(value any, modelID string) {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			normalizeCatalogFieldValues(item, modelID)
		}
	case map[string]any:
		for key, item := range current {
			if key == "field" || key == "dataset" {
				current[key] = stripCatalogRefPrefix(item, modelID)
				continue
			}
			normalizeCatalogFieldValues(item, modelID)
		}
	}
}

func stripCatalogRefPrefix(value any, parentID string) any {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(parentID) == "" {
		return value
	}
	if local, ok := strings.CutPrefix(text, strings.TrimSpace(parentID)+"."); ok && strings.TrimSpace(local) != "" {
		return local
	}
	return value
}

func stripCatalogRefString(value, parentID string) string {
	normalized, _ := stripCatalogRefPrefix(value, parentID).(string)
	return normalized
}

type responseCapture struct {
	request *http.Request
	header  http.Header
	body    bytes.Buffer
	status  int
}

func newResponseCapture(request *http.Request) *responseCapture {
	return &responseCapture{request: request, header: make(http.Header)}
}

func (r *responseCapture) Header() http.Header { return r.header }

func (r *responseCapture) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *responseCapture) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(body)
}

func (r *responseCapture) Response() *http.Response {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Header:        r.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(r.body.Bytes())),
		ContentLength: int64(r.body.Len()),
		Request:       r.request,
	}
}

func withAPIGenRouteContext(request *http.Request, pathTemplate string) *http.Request {
	templateSegments := strings.Split(strings.Trim(pathTemplate, "/"), "/")
	requestSegments := strings.Split(strings.Trim(request.URL.EscapedPath(), "/"), "/")
	if len(templateSegments) != len(requestSegments) {
		return request
	}
	routeContext := chi.NewRouteContext()
	for index, segment := range templateSegments {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		value, err := url.PathUnescape(requestSegments[index])
		if err == nil {
			routeContext.URLParams.Add(name, value)
		}
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func agentToolRuntimeError(err error) agentcore.ToolResult {
	if runtimeErr, ok := err.(*agenttool.Error); ok {
		return apigenAgentToolError(runtimeErr.Code, runtimeErr.Message)
	}
	return apigenAgentToolError("agent_tool_failed", err.Error())
}

func apigenAgentToolError(code, message string) agentcore.ToolResult {
	return agentcore.ToolResult{
		IsError: true,
		Content: map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
	}
}

func ToolError(code, message string) agentcore.ToolResult {
	return apigenAgentToolError(code, message)
}
