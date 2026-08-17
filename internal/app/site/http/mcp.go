package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type docsCatalogInput struct {
	Kind  string `json:"kind,omitempty" jsonschema:"optional item kind: doc, cli, api, or all"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of compact references to return; defaults to 100"`
}

type docsSearchInput struct {
	Query string `json:"query" jsonschema:"search terms, command ID, API operation ID, or documentation concept"`
	Kind  string `json:"kind,omitempty" jsonschema:"optional item kind: doc, cli, api, or all"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results; defaults to 8 and is capped at 25"`
}

type docsReadInput struct {
	ID     string `json:"id" jsonschema:"stable identifier from catalog or search, such as doc:charts/line, cli:deploy, or api:getProject"`
	Format string `json:"format,omitempty" jsonschema:"response format: markdown or json; defaults to markdown"`
}

type docsReference struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	URL     string `json:"url"`
}

type docsCatalogOutput struct {
	SchemaVersion int             `json:"schemaVersion"`
	Items         []docsReference `json:"items"`
}

type docsSearchOutput struct {
	Query   string          `json:"query"`
	Results []docsReference `json:"results"`
}

type docsReadOutput struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

func newDocumentationMCPHandler() stdhttp.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "leapview-docs", Title: "LeapView documentation", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: "Read-only generated LeapView documentation. Search before reading a focused document, command, or API operation. This server cannot execute LeapView commands or API operations."},
	)
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)}
	mcp.AddTool(server, &mcp.Tool{
		Name: "docs_catalog", Title: "List documentation", Description: "List compact stable references for LeapView documentation, CLI commands, and API operations. Use filters and a bounded limit instead of loading full references.", Annotations: annotations,
	}, docsCatalogTool)
	mcp.AddTool(server, &mcp.Tool{
		Name: "docs_search", Title: "Search documentation", Description: "Search generated LeapView documentation and return compact references. Searches human guides, exact CLI command IDs, and API operation IDs without executing anything.", Annotations: annotations,
	}, docsSearchTool)
	mcp.AddTool(server, &mcp.Tool{
		Name: "docs_read", Title: "Read documentation", Description: "Read one focused document, CLI command, or API operation by its stable ID in Markdown or JSON. Use an ID returned by docs_catalog or docs_search.", Annotations: annotations,
	}, docsReadTool)

	handler := mcp.NewStreamableHTTPHandler(func(*stdhttp.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	protection := stdhttp.NewCrossOriginProtection()
	return protection.Handler(handler)
}

func boolPointer(value bool) *bool { return &value }

func docsCatalogTool(_ context.Context, _ *mcp.CallToolRequest, input docsCatalogInput) (*mcp.CallToolResult, docsCatalogOutput, error) {
	kind, err := normalizeDocsKind(input.Kind)
	if err != nil {
		return nil, docsCatalogOutput{}, err
	}
	limit := boundedLimit(input.Limit, 100)
	items := documentationReferences(kind)
	if len(items) > limit {
		items = items[:limit]
	}
	return nil, docsCatalogOutput{SchemaVersion: 1, Items: items}, nil
}

func docsSearchTool(_ context.Context, _ *mcp.CallToolRequest, input docsSearchInput) (*mcp.CallToolResult, docsSearchOutput, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nil, docsSearchOutput{}, fmt.Errorf("query is required")
	}
	kind, err := normalizeDocsKind(input.Kind)
	if err != nil {
		return nil, docsSearchOutput{}, err
	}
	limit := boundedLimit(input.Limit, 8)
	queryLower := strings.ToLower(query)
	results := make([]docsReference, 0, limit)
	seen := map[string]struct{}{}
	add := func(reference docsReference) {
		if len(results) >= limit {
			return
		}
		if _, exists := seen[reference.ID]; exists {
			return
		}
		seen[reference.ID] = struct{}{}
		results = append(results, reference)
	}
	if kind == "all" || kind == "api" {
		for _, operation := range machineDocs.api {
			if machineReferenceMatches(queryLower, operation.OperationID, operation.Summary, operation.Method, operation.Path, strings.Join(operation.Tags, " ")) {
				add(apiReference(operation))
			}
		}
	}
	if kind == "all" || kind == "cli" {
		for _, command := range machineDocs.cli {
			if machineReferenceMatches(queryLower, command.ID, command.Title, command.Summary, command.Usage) {
				add(cliReference(command))
			}
		}
	}
	if kind == "all" || kind == "doc" {
		for _, document := range searchSiteDocuments(query) {
			add(documentReference(document))
		}
	}
	return nil, docsSearchOutput{Query: query, Results: results}, nil
}

func docsReadTool(_ context.Context, _ *mcp.CallToolRequest, input docsReadInput) (*mcp.CallToolResult, docsReadOutput, error) {
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "json" {
		return nil, docsReadOutput{}, fmt.Errorf("format must be markdown or json")
	}
	kind, id, found := strings.Cut(strings.TrimSpace(input.ID), ":")
	if !found || id == "" {
		return nil, docsReadOutput{}, fmt.Errorf("id must use doc:, cli:, or api: prefix")
	}
	switch kind {
	case "doc":
		document, ok := siteDocumentBySlug(id)
		if !ok {
			return nil, docsReadOutput{}, fmt.Errorf("documentation %q was not found", input.ID)
		}
		if format == "markdown" {
			return nil, docsReadOutput{ID: input.ID, ContentType: "text/markdown", Content: document.markdown}, nil
		}
		contents, _ := json.MarshalIndent(map[string]any{"slug": document.slug, "title": document.title, "summary": document.summary, "markdown": document.markdown}, "", "  ")
		return nil, docsReadOutput{ID: input.ID, ContentType: "application/json", Content: string(contents)}, nil
	case "cli":
		raw, ok := machineDocs.cliByID[id]
		if !ok {
			return nil, docsReadOutput{}, fmt.Errorf("CLI command %q was not found", input.ID)
		}
		if format == "json" {
			return nil, docsReadOutput{ID: input.ID, ContentType: "application/json", Content: string(prettyJSON(raw))}, nil
		}
		var command machineCLICommand
		if err := json.Unmarshal(raw, &command); err != nil {
			return nil, docsReadOutput{}, err
		}
		return nil, docsReadOutput{ID: input.ID, ContentType: "text/markdown", Content: renderMachineCLICommand(command)}, nil
	case "api":
		raw, ok := machineDocs.apiByID[id]
		if !ok {
			return nil, docsReadOutput{}, fmt.Errorf("API operation %q was not found", input.ID)
		}
		if format == "json" {
			return nil, docsReadOutput{ID: input.ID, ContentType: "application/json", Content: string(focusedAPIOperationJSON(raw))}, nil
		}
		var operation machineAPIOperation
		if err := json.Unmarshal(raw, &operation); err != nil {
			return nil, docsReadOutput{}, err
		}
		return nil, docsReadOutput{ID: input.ID, ContentType: "text/markdown", Content: renderMachineAPIOperation(operation)}, nil
	default:
		return nil, docsReadOutput{}, fmt.Errorf("id must use doc:, cli:, or api: prefix")
	}
}

func normalizeDocsKind(kind string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "all"
	}
	switch kind {
	case "all", "doc", "cli", "api":
		return kind, nil
	default:
		return "", fmt.Errorf("kind must be all, doc, cli, or api")
	}
}

func boundedLimit(limit, fallback int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > 25 {
		return 25
	}
	return limit
}

func documentationReferences(kind string) []docsReference {
	items := []docsReference{}
	if kind == "all" || kind == "doc" {
		for _, document := range allSiteDocuments() {
			items = append(items, documentReference(document))
		}
	}
	if kind == "all" || kind == "cli" {
		for _, command := range machineDocs.cli {
			items = append(items, cliReference(command))
		}
	}
	if kind == "all" || kind == "api" {
		for _, operation := range machineDocs.api {
			items = append(items, apiReference(operation))
		}
	}
	return items
}

func documentReference(document siteDocument) docsReference {
	return docsReference{ID: "doc:" + document.slug, Kind: "doc", Title: document.title, Summary: document.summary, URL: "/docs/" + document.slug}
}

func cliReference(command machineCLICommand) docsReference {
	return docsReference{ID: "cli:" + command.ID, Kind: "cli", Title: command.Title, Summary: command.Summary, URL: "/docs/cli/commands/" + command.ID + ".md"}
}

func apiReference(operation machineAPIOperation) docsReference {
	return docsReference{ID: "api:" + operation.OperationID, Kind: "api", Title: operation.Summary, Summary: operation.Method + " " + operation.Path, URL: "/docs/api/operations/" + operation.OperationID + ".md"}
}

func machineReferenceMatches(query string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func sortedDocumentationReferences(items []docsReference) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind == items[j].Kind {
			return items[i].ID < items[j].ID
		}
		return items[i].Kind < items[j].Kind
	})
}
