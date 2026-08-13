package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	agenttools "github.com/flidai/leapview/internal/agent/tools"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (m *Module) MCPHandler() http.Handler {
	transport := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		server, err := m.mcpServer(r)
		if err != nil {
			if m.logger != nil {
				m.logger.ErrorContext(r.Context(), "build MCP tool catalog failed", "error", err)
			}
			return nil
		}
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       m.logger,
	})
	var handler http.Handler = transport
	if m.mcpProtect != nil {
		handler = m.mcpProtect(handler)
	}
	originProtection := http.NewCrossOriginProtection()
	return originProtection.Handler(handler)
}

func (m *Module) mcpServer(r *http.Request) (*mcp.Server, error) {
	if m.mcpScope == nil {
		return nil, fmt.Errorf("MCP scope resolver is unavailable")
	}
	scope, ok := m.mcpScope(r)
	if !ok {
		return nil, fmt.Errorf("MCP request is missing its authenticated principal")
	}
	definitions := m.ToolDefinitions(scope)
	sort.SliceStable(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	catalog, err := agentcore.NewToolCatalog(definitions)
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(m.buildVersion)
	if version == "" {
		version = "dev"
	}
	title := strings.TrimSpace(m.productName)
	if title == "" {
		title = "Application"
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "leapview",
		Title:   title,
		Version: version,
	}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	for _, definition := range catalog.Definitions() {
		definition := definition
		annotations := agenttools.AnnotationsForEffect(definition.Effect)
		server.AddTool(&mcp.Tool{
			Name:         definition.Name,
			Description:  definition.Description,
			InputSchema:  definition.InputSchema,
			OutputSchema: definition.OutputSchema,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    annotations.ReadOnlyHint,
				DestructiveHint: &annotations.DestructiveHint,
				IdempotentHint:  annotations.IdempotentHint,
				OpenWorldHint:   &annotations.OpenWorldHint,
			},
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := request.Params.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			result, err := catalog.Execute(ctx, agentcore.ToolCall{
				ID:        fmt.Sprintf("mcp_%d", time.Now().UnixNano()),
				Name:      definition.Name,
				Arguments: arguments,
			})
			if err != nil {
				if errors.Is(err, agentcore.ErrInvalidToolArguments) {
					return mcpErrorResult("invalid_arguments", err.Error()), nil
				}
				return mcpErrorResult("tool_execution_failed", err.Error()), nil
			}
			return mcpResult(result)
		})
	}
	return server, nil
}

func mcpResult(result agentcore.ToolResult) (*mcp.CallToolResult, error) {
	if result.Content == nil {
		return mcpErrorResult("tool_result_invalid", "tool returned no structured content"), nil
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		return mcpErrorResult("tool_result_invalid", "tool output was not JSON serializable"), nil
	}
	var structured map[string]any
	if err := json.Unmarshal(encoded, &structured); err != nil || structured == nil {
		return mcpErrorResult("tool_result_invalid", "tool output must be a JSON object"), nil
	}
	if result.IsError {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
			IsError: true,
		}, nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: structured,
		IsError:           result.IsError,
	}, nil
}

func mcpErrorResult(code, message string) *mcp.CallToolResult {
	structured := map[string]any{"error": map[string]any{"code": code, "message": message}}
	encoded, _ := json.Marshal(structured)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		IsError: true,
	}
}
