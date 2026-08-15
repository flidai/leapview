package tools

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/agent/productdocs"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// ProviderSet is the canonical composition of the tool providers consumed by
// built-in chat, MCP, the CLI catalog, and generated documentation.
type ProviderSet struct {
	Docs    DocsProvider
	Catalog CatalogProvider
	Visual  VisualProvider
	APIGen  APIGenProvider
	Authoring DashboardAuthoringProvider
}

func (p ProviderSet) Definitions(scope Scope) []agentcore.ToolDefinition {
	definitions := p.Docs.Definitions()
	definitions = append(definitions, p.Catalog.Definitions(scope)...)
	definitions = append(definitions, p.Visual.Definitions(scope)...)
	definitions = append(definitions, p.APIGen.Definitions(scope)...)
	definitions = append(definitions, p.Authoring.Definitions(scope)...)
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Name < definitions[j].Name
	})
	return definitions
}

type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

func AnnotationsForEffect(effect string) ToolAnnotations {
	return ToolAnnotations{
		ReadOnlyHint:    effect == "" || effect == "read",
		DestructiveHint: effect == "destructive",
		IdempotentHint:  effect != "destructive",
		OpenWorldHint:   false,
	}
}

// ToolReference is the generated, transport-independent public contract for
// one canonical agent tool.
type ToolReference struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Privilege    string          `json:"privilege"`
	Effect       string          `json:"effect"`
	OperationID  string          `json:"operationId"`
	Defaults     map[string]any  `json:"defaults"`
	Tags         []string        `json:"tags"`
	Annotations  ToolAnnotations `json:"annotations"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

// ReferenceCatalog derives public metadata and schemas from the same provider
// definitions used at runtime. It fails closed if registry and provider
// composition drift.
func ReferenceCatalog(operations []APIGenOperation) ([]ToolReference, error) {
	definitions := (ProviderSet{APIGen: APIGenProvider{Operations: operations}}).Definitions(Scope{})
	if len(definitions) != len(ToolNames(operations)) {
		return nil, fmt.Errorf("canonical definitions count %d does not match registry count %d", len(definitions), len(ToolNames(operations)))
	}
	metadata := referenceMetadata(operations)
	references := make([]ToolReference, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Effect != "read" {
			return nil, fmt.Errorf("canonical tool %q has unsupported effect %q", definition.Name, definition.Effect)
		}
		entry, ok := metadata[definition.Name]
		if !ok {
			return nil, fmt.Errorf("canonical tool %q has no reference metadata", definition.Name)
		}
		if !json.Valid(definition.InputSchema) || !json.Valid(definition.OutputSchema) {
			return nil, fmt.Errorf("canonical tool %q has an invalid schema", definition.Name)
		}
		references = append(references, ToolReference{
			Name: definition.Name, Description: definition.Description,
			Privilege: entry.privilege, Effect: definition.Effect, OperationID: entry.operationID,
			Defaults: entry.defaults, Tags: append([]string(nil), definition.Tags...),
			Annotations:  AnnotationsForEffect(definition.Effect),
			InputSchema:  append(json.RawMessage(nil), definition.InputSchema...),
			OutputSchema: append(json.RawMessage(nil), definition.OutputSchema...),
		})
	}
	return references, nil
}

type toolReferenceMetadata struct {
	privilege   string
	operationID string
	defaults    map[string]any
}

func referenceMetadata(operations []APIGenOperation) map[string]toolReferenceMetadata {
	metadata := map[string]toolReferenceMetadata{
		CatalogSearchToolName: {privilege: "VIEW_ITEM", operationID: "manual", defaults: map[string]any{"limit": DefaultCatalogSearchLimit}},
		CatalogListToolName:   {privilege: "VIEW_ITEM", operationID: "manual", defaults: map[string]any{"limit": DefaultCatalogListLimit}},
		CatalogGetToolName:    {privilege: "VIEW_ITEM", operationID: "manual", defaults: map[string]any{}},
		QueryVisualToolName:   {privilege: "QUERY_DATA", operationID: "manual", defaults: map[string]any{"limit": maxVisualRows}},
		DocsSearchToolName:    {privilege: "USE_AGENT", operationID: "manual", defaults: map[string]any{"limit": productdocs.DefaultSearchLimit}},
		DocsReadToolName:      {privilege: "USE_AGENT", operationID: "manual", defaults: map[string]any{"limit": productdocs.DefaultReadLimit, "offset": 1}},
	}
	for _, operation := range operations {
		defaults := map[string]any{}
		for _, binding := range operation.Tool.Bindings {
			if binding.Argument != "" && binding.Default != nil {
				defaults[binding.Argument] = binding.Default
			}
		}
		metadata[operation.Tool.Name] = toolReferenceMetadata{
			privilege: operationPrivilege(operation.Contract), operationID: operation.Contract.OperationID, defaults: defaults,
		}
	}
	return metadata
}
