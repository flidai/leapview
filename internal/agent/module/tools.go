package module

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	"github.com/flidai/leapview/internal/access"
	agentcap "github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func (m *Module) configureTools() {
	if m.service != nil && m.enableSystemPrompt {
		m.service.SetSystemPromptProvider(func(ctx context.Context) (string, error) {
			return m.handler.SystemPrompt(ctx)
		})
	}
	if m.service == nil {
		return
	}
	m.service.AppendToolProviders(
		func(scope agentcap.Scope) []agentcore.ToolDefinition {
			return m.ToolDefinitions(scope)
		},
	)
}

// ToolDefinitions is the single governed tool catalog consumed by the
// built-in agent and protocol adapters such as MCP.
func (m *Module) ToolDefinitions(scope agentcap.Scope) []agentcore.ToolDefinition {
	scope = m.executionScope(scope)
	toolScope := ToolsScope(scope)
	definitions := (agenttools.ProviderSet{
		Docs:      m.DocsToolProvider(),
		Catalog:   m.CatalogToolProvider(),
		Visual:    m.VisualToolProvider(),
		APIGen:    m.APIGenToolProvider(),
		Authoring: m.DashboardAuthoringToolProvider(),
	}).Definitions(toolScope)
	return wrapToolContext(definitions, m.toolContext, scope)
}

// executionScope revalidates durable scope flags against the current process
// configuration. A run queued by a development server must not retain its
// bypass if a production server later resumes it.
func (m *Module) executionScope(scope agentcap.Scope) agentcap.Scope {
	if m == nil || !m.allowDevAuthBypass {
		scope.DevAuthBypass = false
	}
	return scope
}

func wrapToolContext(definitions []agentcore.ToolDefinition, decorate func(context.Context, agentcap.Scope) context.Context, scope agentcap.Scope) []agentcore.ToolDefinition {
	if decorate == nil {
		return definitions
	}
	for index := range definitions {
		handler := definitions[index].Handler
		definitions[index].Handler = agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
			return handler.Run(decorate(ctx, scope), call)
		})
	}
	return definitions
}

func (m *Module) DashboardAuthoringToolProvider() agenttools.DashboardAuthoringProvider {
	return agenttools.DashboardAuthoringProvider{
		Application:      m.dashboardAuthoring,
		ProjectID:        m.projectID,
		ResolveProjectID: m.projectIDResolver,
		Resolve:          resourceResolverForTools(m.resolveResource),
	}
}

func (m *Module) DocsToolProvider() agenttools.DocsProvider {
	return agenttools.DocsProvider{Documentation: m.documentation}
}

func (m *Module) CatalogToolProvider() agenttools.CatalogProvider {
	return agenttools.CatalogProvider{Catalog: m.catalog}
}

func (m *Module) VisualToolProvider() agenttools.VisualProvider {
	return agenttools.VisualProvider{
		Resolve: resourceResolverForTools(m.resolveResource),
		SemanticModel: func(projectID, modelID string) (model *semanticmodel.Model, ok bool) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return nil, false
			}
			return metrics.SemanticModel(modelID)
		},
		QueryDefinition: func(ctx context.Context, projectID string, definition dashboarddefinition.Definition, pageID, visualID string, filters dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return visualizationir.VisualizationEnvelope{}, fmt.Errorf("unknown project runtime for semantic model %q", definition.SemanticModel)
			}
			port, ok := metrics.(queryruntime.DefinitionVisualizationMetrics)
			if !ok {
				return visualizationir.VisualizationEnvelope{}, fmt.Errorf("active runtime does not provide compiled visualization execution")
			}
			filters = port.DefaultFiltersForDefinition(definition)
			return port.QueryVisualizationForDefinition(ctx, definition, pageID, filters, visualID)
		},
		QueryMetadata: func(ctx context.Context, projectID, modelID string) agenttools.VisualQueryMetadata {
			if m.queryMetadata != nil {
				return m.queryMetadata(ctx, projectID, modelID)
			}
			return agenttools.VisualQueryMetadata{}
		},
	}
}

func resourceResolverForTools(resolve ResourceResolver) agenttools.ResourceResolver {
	if resolve == nil {
		return nil
	}
	return func(ctx context.Context, scope agenttools.Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
		return resolve(ctx, moduleScopeFromTools(scope), id, kind, capability)
	}
}

func moduleScopeFromTools(scope agenttools.Scope) Scope {
	return Scope{
		ProjectID: scope.ProjectID, PrincipalID: scope.PrincipalID, GroupIDs: append([]string(nil), scope.GroupIDs...), ConversationID: scope.ConversationID,
		DevAuthBypass: scope.DevAuthBypass,
		Credential: CredentialScope{
			ProjectID: scope.Credential.ProjectID, Restricted: scope.Credential.Restricted,
			Capabilities: append([]string(nil), scope.Credential.Capabilities...),
		},
	}
}

func (m *Module) APIGenToolProvider() agenttools.APIGenProvider {
	return agenttools.APIGenProvider{
		Operations: m.apiOperations,
		Authorize: func(ctx context.Context, scope agenttools.Scope, operationID string) (agentcore.ToolResult, bool) {
			return m.authorizeAPIGenOperation(ctx, scopeFromTools(scope), operationID)
		},
		Dispatch: func(scope agenttools.Scope, operationID string, writer http.ResponseWriter, request *http.Request) bool {
			if m.dispatchAPIGen == nil {
				return false
			}
			return m.dispatchAPIGen(scopeFromTools(scope), operationID, writer, request)
		},
	}
}

func ToolsScope(scope agentcap.Scope) agenttools.Scope {
	return agenttools.Scope{
		ProjectID:      scope.ProjectID,
		PrincipalID:    scope.PrincipalID,
		GroupIDs:       append([]string(nil), scope.GroupIDs...),
		ConversationID: scope.ConversationID,
		DevAuthBypass:  scope.DevAuthBypass,
		Credential: agenttools.CredentialScope{
			ProjectID:    scope.Credential.ProjectID,
			Restricted:   scope.Credential.Restricted,
			Capabilities: append([]string(nil), scope.Credential.Capabilities...),
		},
	}
}

func scopeFromTools(scope agenttools.Scope) agentcap.Scope {
	return agentcap.Scope{
		ProjectID:      scope.ProjectID,
		PrincipalID:    scope.PrincipalID,
		GroupIDs:       append([]string(nil), scope.GroupIDs...),
		ConversationID: scope.ConversationID,
		DevAuthBypass:  scope.DevAuthBypass,
		Credential: agentcap.CredentialScope{
			ProjectID:    scope.Credential.ProjectID,
			Restricted:   scope.Credential.Restricted,
			Capabilities: append([]string(nil), scope.Credential.Capabilities...),
		},
	}
}

func (m *Module) authorizeAPIGenOperation(ctx context.Context, scope agentcap.Scope, operationID string) (agentcore.ToolResult, bool) {
	capability, ok := m.apigenOperationCapability(operationID)
	if !ok {
		return agenttools.ToolError("forbidden", "operation has no generated resource capability metadata"), false
	}
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return agenttools.ToolError("unauthorized", "agent tool requires an authenticated principal"), false
	}
	if !agentCredentialAllowsCapability(scope, capability) {
		m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "denied", fmt.Errorf("credential restriction"))
		return agenttools.ToolError("forbidden", "credential is not allowed to call this tool"), false
	}
	m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "success", nil)
	return agentcore.ToolResult{}, true
}

func (m *Module) recordToolAudit(ctx context.Context, scope agentcap.Scope, capability access.Capability, targetType, targetID, status string, cause error) {
	if m == nil || m.recordAudit == nil {
		return
	}
	metadata := dataquery.MetadataFromContext(ctx)
	payload := map[string]any{}
	if cause != nil {
		payload["error"] = cause.Error()
	}
	bytes, _ := json.Marshal(payload)
	_ = m.recordAudit(ctx, access.AuditEventInput{
		PrincipalID:   scope.PrincipalID,
		Action:        "agent_tool.called",
		ResourceKind:  targetType,
		ResourceID:    targetID,
		Capability:    capability,
		Status:        status,
		RequestID:     metadata.RequestID,
		CorrelationID: metadata.CorrelationID,
		MetadataJSON:  string(bytes),
	})
}

func agentCredentialAllowsCapability(scope agentcap.Scope, capability access.Capability) bool {
	credential := scope.Credential
	if !credential.Restricted || credential.Capabilities == nil {
		return true
	}
	for _, allowed := range credential.Capabilities {
		if strings.EqualFold(strings.TrimSpace(allowed), string(capability)) {
			return true
		}
	}
	return false
}

func (m *Module) apigenOperationCapability(operationID string) (access.Capability, bool) {
	var contract agenttools.OperationContract
	found := false
	for _, operation := range m.apiOperations {
		if operation.Contract.OperationID == operationID {
			contract, found = operation.Contract, true
			break
		}
	}
	if !found || !contract.Protected || contract.AuthzMode != "privilege" {
		return "", false
	}
	authz, ok := contract.Extensions["x-authz"].(map[string]any)
	if !ok || authz["mode"] != "privilege" {
		return "", false
	}
	value, ok := authz["privilege"].(string)
	if !ok {
		return "", false
	}
	capability, err := access.ParseCapability(value)
	return capability, err == nil
}

func apiGenToolContracts(operations []agenttools.APIGenOperation) map[string]agenttool.Contract {
	contracts := make(map[string]agenttool.Contract, len(operations))
	for _, operation := range operations {
		contracts[operation.Tool.Name] = operation.Tool
	}
	return contracts
}
