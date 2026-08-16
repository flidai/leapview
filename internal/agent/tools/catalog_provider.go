package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	CatalogSearchToolName = "catalog_search"
	CatalogListToolName   = "catalog_list"
	CatalogGetToolName    = "catalog_get"

	DefaultCatalogSearchLimit = 10
	MaxCatalogSearchLimit     = 25
	DefaultCatalogListLimit   = 25
	MaxCatalogListLimit       = 50
)

type CatalogType = agentcontracts.CatalogType

type CatalogRef = agentcontracts.CatalogRef

type CatalogItem struct {
	Ref         CatalogRef `json:"ref"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName,omitempty"`
	Description string     `json:"description,omitempty"`
	Domain      string     `json:"domain,omitempty"`
	Owner       string     `json:"owner,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
}

type CatalogSearchRequest struct {
	Query  string        `json:"query"`
	Kinds  []CatalogType `json:"kinds,omitempty"`
	Domain string        `json:"domain,omitempty"`
	Cursor string        `json:"cursor,omitempty"`
	Limit  int           `json:"limit,omitempty"`
}

type CatalogListRequest struct {
	Parent     *CatalogRef   `json:"parent,omitempty"`
	ChildKinds []CatalogType `json:"childKinds,omitempty"`
	Domain     string        `json:"domain,omitempty"`
	Cursor     string        `json:"cursor,omitempty"`
	Limit      int           `json:"limit,omitempty"`
}

type CatalogGetRequest struct {
	Ref CatalogRef `json:"ref"`
}

type CatalogPage struct {
	Items      []CatalogItem `json:"items"`
	Count      int           `json:"count"`
	HasMore    bool          `json:"hasMore"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type CatalogGetResult struct {
	Item    CatalogItem    `json:"item"`
	Details map[string]any `json:"details"`
}

// Catalog is the sole model-facing catalog port. Implementations must resolve
// against the immutable active serving-generation snapshot and an
// authorization subject set for Scope.PrincipalID.
type Catalog interface {
	Search(context.Context, Scope, CatalogSearchRequest) (CatalogPage, error)
	List(context.Context, Scope, CatalogListRequest) (CatalogPage, error)
	Get(context.Context, Scope, CatalogGetRequest) (CatalogGetResult, error)
}

type CatalogError struct {
	Code    string
	Message string
}

func (e *CatalogError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type CatalogProvider struct{ Catalog Catalog }

func (p CatalogProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{
		{
			Name: CatalogSearchToolName, Description: "Search authorized project resources by stable ID, name, description, or domain metadata.",
			InputSchema: json.RawMessage(agentcontracts.CatalogSearchInputSchemaJSON), OutputSchema: json.RawMessage(agentcontracts.CatalogPageSchemaJSON),
			Effect: "read", Tags: []string{"catalog", "search"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				var request CatalogSearchRequest
				if err := decodeCatalogArguments(call.Arguments, &request); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				request.Query = strings.TrimSpace(request.Query)
				if request.Query == "" {
					return ToolError("invalid_arguments", "query is required"), nil
				}
				if request.Limit == 0 {
					request.Limit = DefaultCatalogSearchLimit
				}
				if err := validateCatalogLimit(request.Limit, MaxCatalogSearchLimit); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if err := validateCatalogKinds(request.Kinds); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if p.Catalog == nil {
					return ToolError("catalog_unavailable", "catalog service is not configured"), nil
				}
				result, err := p.Catalog.Search(ctx, scope, request)
				if err != nil {
					return catalogToolError("catalog_search_failed", err), nil
				}
				return agentcore.ToolResult{Content: catalogPageResult(result)}, nil
			}),
		},
		{
			Name: CatalogListToolName, Description: "Browse authorized project resources. Returned refs are exact stable IDs for subsequent calls.",
			InputSchema: json.RawMessage(agentcontracts.CatalogListInputSchemaJSON), OutputSchema: json.RawMessage(agentcontracts.CatalogPageSchemaJSON),
			Effect: "read", Tags: []string{"catalog", "browse"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				var request CatalogListRequest
				if err := decodeCatalogArguments(call.Arguments, &request); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if request.Limit == 0 {
					request.Limit = DefaultCatalogListLimit
				}
				if err := validateCatalogLimit(request.Limit, MaxCatalogListLimit); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				request.ChildKinds = normalizedCatalogKinds(request.ChildKinds)
				if request.Parent != nil {
					if err := validateCatalogRef(*request.Parent); err != nil {
						return ToolError("invalid_arguments", err.Error()), nil
					}
				}
				if err := validateCatalogKinds(request.ChildKinds); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if p.Catalog == nil {
					return ToolError("catalog_unavailable", "catalog service is not configured"), nil
				}
				result, err := p.Catalog.List(ctx, scope, request)
				if err != nil {
					return catalogToolError("catalog_list_failed", err), nil
				}
				return agentcore.ToolResult{Content: catalogPageResult(result)}, nil
			}),
		},
		{
			Name: CatalogGetToolName, Description: "Resolve one exact authorized project resource ID and return its compact metadata.",
			InputSchema: json.RawMessage(agentcontracts.CatalogGetInputSchemaJSON), OutputSchema: json.RawMessage(agentcontracts.CatalogGetResultSchemaJSON),
			Effect: "read", Tags: []string{"catalog", "describe"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				var request CatalogGetRequest
				if err := decodeCatalogArguments(call.Arguments, &request); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if err := validateCatalogRef(request.Ref); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if p.Catalog == nil {
					return ToolError("catalog_unavailable", "catalog service is not configured"), nil
				}
				result, err := p.Catalog.Get(ctx, scope, request)
				if err != nil {
					return catalogToolError("catalog_get_failed", err), nil
				}
				return agentcore.ToolResult{Content: result}, nil
			}),
		},
	}
}

func catalogPageResult(page CatalogPage) CatalogPage {
	if page.Items == nil {
		page.Items = []CatalogItem{}
	}
	page.Count = len(page.Items)
	page.HasMore = strings.TrimSpace(page.NextCursor) != ""
	return page
}

func decodeCatalogArguments(arguments json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("arguments must contain one JSON object")
		}
		return err
	}
	return nil
}

func validateCatalogLimit(limit, maximum int) error {
	if limit < 1 || limit > maximum {
		return fmt.Errorf("limit must be between 1 and %d", maximum)
	}
	return nil
}

func validateCatalogKinds(kinds []CatalogType) error {
	for _, kind := range kinds {
		switch kind {
		case agentcontracts.CatalogTypeProject, agentcontracts.CatalogTypeConnection, agentcontracts.CatalogTypeSource, agentcontracts.CatalogTypeModel, agentcontracts.CatalogTypeSemanticModel, agentcontracts.CatalogTypePipeline, agentcontracts.CatalogTypeDashboard:
		default:
			return fmt.Errorf("unsupported catalog kind %q", kind)
		}
	}
	return nil
}

func validateCatalogRef(ref CatalogRef) error {
	if _, err := projectgraph.NewResourceID(strings.TrimSpace(ref.ID)); err != nil {
		return fmt.Errorf("ref.id is invalid: %v", err)
	}
	return validateCatalogKinds([]CatalogType{ref.Kind})
}

func normalizedCatalogKinds(kinds []CatalogType) []CatalogType {
	seen := map[CatalogType]struct{}{}
	out := make([]CatalogType, 0, len(kinds))
	for _, kind := range kinds {
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	return out
}

func catalogToolError(fallback string, err error) agentcore.ToolResult {
	var catalogErr *CatalogError
	if errors.As(err, &catalogErr) && strings.TrimSpace(catalogErr.Code) != "" {
		return ToolError(catalogErr.Code, catalogErr.Message)
	}
	return ToolError(fallback, err.Error())
}
