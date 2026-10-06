package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	productdocs "github.com/flidai/leapview/internal/agent/productdocs"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	DocsSearchToolName = "docs_search"
	DocsReadToolName   = "docs_read"
)

type Documentation interface {
	Search(context.Context, productdocs.SearchRequest) (productdocs.SearchResult, error)
	Read(context.Context, productdocs.ReadRequest) (productdocs.ReadResult, error)
}

type DocsProvider struct {
	Documentation Documentation
}

func (p DocsProvider) Definitions() []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{
		{
			Name:         DocsSearchToolName,
			Description:  "Search LeapView's version-matched product documentation. Returns ranked, bounded matches with stable document IDs and excerpts. Continue with nextCursor when hasMore is true, or use the optional path prefix to narrow broad searches.",
			InputSchema:  json.RawMessage(agentcontracts.DocsSearchInputSchemaJSON),
			OutputSchema: json.RawMessage(agentcontracts.DocsSearchResultSchemaJSON),
			Effect:       "read",
			Tags:         []string{"documentation", "search"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				return p.search(ctx, call.Arguments), nil
			}),
		},
		{
			Name:         DocsReadToolName,
			Description:  "Read a bounded line window from one LeapView document returned by docs_search. Continue with nextOffset when truncated is true.",
			InputSchema:  json.RawMessage(agentcontracts.DocsReadInputSchemaJSON),
			OutputSchema: json.RawMessage(agentcontracts.DocsReadResultSchemaJSON),
			Effect:       "read",
			Tags:         []string{"documentation"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				return p.read(ctx, call.Arguments), nil
			}),
		},
	}
}

func (p DocsProvider) search(ctx context.Context, arguments json.RawMessage) agentcore.ToolResult {
	var request productdocs.SearchRequest
	if err := json.Unmarshal(arguments, &request); err != nil {
		return ToolError("invalid_arguments", err.Error())
	}
	if strings.TrimSpace(request.Query) == "" {
		return ToolError("invalid_arguments", "query is required")
	}
	if p.Documentation == nil {
		return ToolError("documentation_unavailable", "documentation service is not configured")
	}
	result, err := p.Documentation.Search(ctx, request)
	if err != nil {
		return documentationToolError("docs_search_failed", err)
	}
	if result.Matches == nil {
		result.Matches = []productdocs.Reference{}
	}
	return agentcore.ToolResult{Content: result}
}

func (p DocsProvider) read(ctx context.Context, arguments json.RawMessage) agentcore.ToolResult {
	var request productdocs.ReadRequest
	if err := json.Unmarshal(arguments, &request); err != nil {
		return ToolError("invalid_arguments", err.Error())
	}
	if strings.TrimSpace(request.ID) == "" {
		return ToolError("invalid_arguments", "id is required")
	}
	if p.Documentation == nil {
		return ToolError("documentation_unavailable", "documentation service is not configured")
	}
	result, err := p.Documentation.Read(ctx, request)
	if err != nil {
		return documentationToolError("docs_read_failed", err)
	}
	return agentcore.ToolResult{Content: result}
}

func documentationToolError(fallback string, err error) agentcore.ToolResult {
	switch {
	case errors.Is(err, productdocs.ErrInvalid):
		return ToolError("invalid_arguments", err.Error())
	case errors.Is(err, productdocs.ErrNotFound):
		return ToolError("documentation_not_found", err.Error())
	case errors.Is(err, productdocs.ErrSnapshotChanged):
		return ToolError("documentation_snapshot_changed", "documentation changed during pagination; restart the search from its first page")
	default:
		return ToolError(fallback, err.Error())
	}
}
