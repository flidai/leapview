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
	Docs      DocsProvider
	Catalog   CatalogProvider
	Visual    VisualProvider
	APIGen    APIGenProvider
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

// referenceDefinitions composes the complete transport-independent catalog
// for generated references. Runtime Definitions intentionally omits authoring
// when its application facade is unavailable; reference generation still
// needs the static schemas and metadata for that provider.
func (p ProviderSet) referenceDefinitions(scope Scope) []agentcore.ToolDefinition {
	definitions := p.Docs.Definitions()
	definitions = append(definitions, p.Catalog.Definitions(scope)...)
	definitions = append(definitions, p.Visual.Definitions(scope)...)
	definitions = append(definitions, p.APIGen.Definitions(scope)...)
	definitions = append(definitions, p.Authoring.contractDefinitions(scope)...)
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
	switch effect {
	case "read":
		return ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	case "destructive":
		return ToolAnnotations{DestructiveHint: true}
	case "write":
		// Writes are neither read-only nor destructive, and a transport retry
		// may receive a fresh tool-call identity and create another resource.
		return ToolAnnotations{}
	default:
		return ToolAnnotations{}
	}
}

// ToolReference is the generated, transport-independent public contract for
// one canonical agent tool.
type ToolReference struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	AuthzMode    string          `json:"authzMode"`
	Privilege    string          `json:"privilege,omitempty"`
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
	definitions := (ProviderSet{APIGen: APIGenProvider{Operations: operations}}).referenceDefinitions(Scope{})
	if len(definitions) != len(ToolNames(operations)) {
		return nil, fmt.Errorf("canonical definitions count %d does not match registry count %d", len(definitions), len(ToolNames(operations)))
	}
	metadata := referenceMetadata(operations)
	references := make([]ToolReference, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Effect != "read" && definition.Effect != "write" && definition.Effect != "destructive" {
			return nil, fmt.Errorf("canonical tool %q has unsupported effect %q", definition.Name, definition.Effect)
		}
		entry, ok := metadata[definition.Name]
		if !ok {
			return nil, fmt.Errorf("canonical tool %q has no reference metadata", definition.Name)
		}
		if entry.authzMode != "authenticated" && entry.authzMode != "privilege" {
			return nil, fmt.Errorf("canonical tool %q has unsupported authorization mode %q", definition.Name, entry.authzMode)
		}
		if entry.authzMode == "privilege" && entry.privilege == "" {
			return nil, fmt.Errorf("canonical tool %q has no required privilege", definition.Name)
		}
		if entry.authzMode == "authenticated" && entry.privilege != "" {
			return nil, fmt.Errorf("authenticated tool %q must not declare a privilege", definition.Name)
		}
		if !json.Valid(definition.InputSchema) || !json.Valid(definition.OutputSchema) {
			return nil, fmt.Errorf("canonical tool %q has an invalid schema", definition.Name)
		}
		references = append(references, ToolReference{
			Name: definition.Name, Description: definition.Description,
			AuthzMode: entry.authzMode, Privilege: entry.privilege,
			Effect: definition.Effect, OperationID: entry.operationID,
			Defaults: entry.defaults, Tags: append([]string(nil), definition.Tags...),
			Annotations:  AnnotationsForEffect(definition.Effect),
			InputSchema:  append(json.RawMessage(nil), definition.InputSchema...),
			OutputSchema: append(json.RawMessage(nil), definition.OutputSchema...),
		})
	}
	return references, nil
}

type toolReferenceMetadata struct {
	authzMode   string
	privilege   string
	operationID string
	defaults    map[string]any
}

func referenceMetadata(operations []APIGenOperation) map[string]toolReferenceMetadata {
	metadata := map[string]toolReferenceMetadata{
		AddDashboardPageToolName:        {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		AddDashboardVisualToolName:      {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		AssignDashboardFieldToolName:    {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		CatalogSearchToolName:           {authzMode: "privilege", privilege: "RESOURCE_READ", operationID: "manual", defaults: map[string]any{"limit": DefaultCatalogSearchLimit}},
		CatalogListToolName:             {authzMode: "privilege", privilege: "RESOURCE_READ", operationID: "manual", defaults: map[string]any{"limit": DefaultCatalogListLimit}},
		CatalogGetToolName:              {authzMode: "privilege", privilege: "RESOURCE_READ", operationID: "manual", defaults: map[string]any{}},
		CreateDashboardDraftToolName:    {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		ExecuteDashboardCommandToolName: {authzMode: "privilege", privilege: "RESOURCE_MANAGE", operationID: "manual", defaults: map[string]any{}},
		ExportDashboardYAMLToolName:     {authzMode: "privilege", privilege: "RESOURCE_READ", operationID: "manual", defaults: map[string]any{}},
		ForkDashboardToolName:           {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		GetDashboardDraftToolName:       {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		GetDashboardToolName:            {authzMode: "privilege", privilege: "RESOURCE_READ", operationID: "manual", defaults: map[string]any{}},
		ListDashboardsToolName:          {authzMode: "privilege", privilege: "RESOURCE_READ", operationID: "manual", defaults: map[string]any{}},
		PreviewDashboardDraftToolName:   {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		QueryVisualToolName:             {authzMode: "privilege", privilege: "RESOURCE_USE", operationID: "manual", defaults: map[string]any{"limit": maxVisualRows}},
		SetDashboardVisibilityToolName:  {authzMode: "privilege", privilege: "RESOURCE_EDIT", operationID: "manual", defaults: map[string]any{}},
		DocsSearchToolName:              {authzMode: "authenticated", operationID: "manual", defaults: map[string]any{"limit": productdocs.DefaultSearchLimit}},
		DocsReadToolName:                {authzMode: "authenticated", operationID: "manual", defaults: map[string]any{"limit": productdocs.DefaultReadLimit, "offset": 1}},
	}
	for _, operation := range operations {
		defaults := map[string]any{}
		for _, binding := range operation.Tool.Bindings {
			if binding.Argument != "" && binding.Default != nil {
				defaults[binding.Argument] = binding.Default
			}
		}
		metadata[operation.Tool.Name] = toolReferenceMetadata{
			authzMode: operation.Contract.AuthzMode, privilege: operationPrivilege(operation.Contract),
			operationID: operation.Contract.OperationID, defaults: defaults,
		}
	}
	return metadata
}
