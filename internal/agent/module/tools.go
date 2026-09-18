package module

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	"github.com/flidai/leapview/internal/access"
	agentcap "github.com/flidai/leapview/internal/agent"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/go-chi/chi/v5"
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
	var catalog agenttools.Catalog
	if m.catalog != nil {
		catalog = credentialCatalog{base: m.catalog}
	}
	return agenttools.CatalogProvider{
		Catalog: catalog,
		SemanticModel: func(_ context.Context, scope agenttools.Scope, ref agenttools.CatalogRef) (*semanticmodel.Model, bool) {
			if ref.Kind != agenttools.CatalogType(agentcontracts.CatalogTypeSemanticModel) || m.dashboardMetrics == nil {
				return nil, false
			}
			metrics, ok := m.dashboardMetrics(scope.ProjectID)
			if !ok || metrics == nil {
				return nil, false
			}
			return metrics.SemanticModel(ref.ID)
		},
	}
}

func (m *Module) VisualToolProvider() agenttools.VisualProvider {
	return agenttools.VisualProvider{
		Resolve: resourceResolverForTools(m.resolveResource),
		Authorize: func(ctx context.Context, scope agenttools.Scope, request agenttools.VisualAuthorizationRequest) (agentcore.ToolResult, bool) {
			return m.authorizeVisualQuery(ctx, scope, request)
		},
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

func (m *Module) authorizeVisualQuery(ctx context.Context, scope agenttools.Scope, request agenttools.VisualAuthorizationRequest) (agentcore.ToolResult, bool) {
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return agenttools.ToolError("unauthorized", "agent visual tool requires an authenticated principal"), false
	}
	if scope.DevAuthBypass {
		return agentcore.ToolResult{}, true
	}
	if scope.Credential.PermissionProfile == "" && scope.Credential.Permissions == nil {
		if !agentCredentialAllowsCapability(scopeFromTools(scope), access.CapabilityResourceUse) {
			return agenttools.ToolError("forbidden", "credential is not allowed to query this visual"), false
		}
		return agentcore.ToolResult{}, true
	}
	model, err := projectgraph.NewResourceID(strings.TrimSpace(request.Model))
	if err != nil {
		return agenttools.ToolError("forbidden", "visual semantic model target is invalid"), false
	}
	resource, err := access.NewResourceRef(model, projectgraph.KindSemanticModel)
	if err != nil {
		return agenttools.ToolError("forbidden", "visual semantic model target is invalid"), false
	}
	query, err := access.NewExactPermissionPair(access.ActionSemanticQuery, projectgraph.ResourceID(scope.ProjectID), resource)
	if err != nil {
		return agenttools.ToolError("forbidden", "visual semantic model target is invalid"), false
	}
	required, err := access.RequiredPermissionPairs(query)
	if err != nil || scope.Credential.PermissionProfile != access.PermissionCatalogProfile || !permissionPairsAllowAll(scope.Credential.Permissions, required) {
		if err == nil {
			err = fmt.Errorf("credential permission pair set does not cover visual query")
		}
		m.recordToolAudit(ctx, scopeFromTools(scope), access.CapabilityResourceUse, "agent_tool", agenttools.QueryVisualToolName, "denied", err)
		return agenttools.ToolError("forbidden", "credential is not allowed to query this visual"), false
	}
	return agentcore.ToolResult{}, true
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
			ProjectID:         scope.Credential.ProjectID,
			Restricted:        scope.Credential.Restricted,
			Capabilities:      append([]string(nil), scope.Credential.Capabilities...),
			PermissionProfile: scope.Credential.PermissionProfile,
			Permissions:       clonePermissionPairs(scope.Credential.Permissions),
		},
	}
}

func (m *Module) APIGenToolProvider() agenttools.APIGenProvider {
	return agenttools.APIGenProvider{
		Operations: m.apiOperations,
		AuthorizeOperation: func(ctx context.Context, scope agenttools.Scope, operation agenttools.APIGenOperation, request *http.Request) (agentcore.ToolResult, bool) {
			return m.authorizeAPIGenOperation(ctx, scopeFromTools(scope), operation, request)
		},
		Authorize: func(ctx context.Context, scope agenttools.Scope, operationID string) (agentcore.ToolResult, bool) {
			return m.authorizeAPIGenOperation(ctx, scopeFromTools(scope), operationContract(m.apiOperations, operationID), nil)
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
			ProjectID:         scope.Credential.ProjectID,
			Restricted:        scope.Credential.Restricted,
			Capabilities:      append([]string(nil), scope.Credential.Capabilities...),
			PermissionProfile: scope.Credential.PermissionProfile,
			Permissions:       clonePermissionPairs(scope.Credential.Permissions),
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
			ProjectID:         scope.Credential.ProjectID,
			Restricted:        scope.Credential.Restricted,
			Capabilities:      append([]string(nil), scope.Credential.Capabilities...),
			PermissionProfile: scope.Credential.PermissionProfile,
			Permissions:       clonePermissionPairs(scope.Credential.Permissions),
		},
	}
}

func operationContract(operations []agenttools.APIGenOperation, operationID string) agenttools.APIGenOperation {
	for _, operation := range operations {
		if operation.Contract.OperationID == operationID {
			return operation
		}
	}
	return agenttools.APIGenOperation{Contract: agenttools.OperationContract{OperationID: operationID}}
}

func (m *Module) authorizeAPIGenOperation(ctx context.Context, scope agentcap.Scope, operation agenttools.APIGenOperation, request *http.Request) (agentcore.ToolResult, bool) {
	operationID := operation.Contract.OperationID
	capability, hasCapability := operationCapability(operation.Contract)
	if !hasCapability && strings.TrimSpace(operation.Contract.Action) == "" {
		return agenttools.ToolError("forbidden", "operation has no generated resource capability metadata"), false
	}
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return agenttools.ToolError("unauthorized", "agent tool requires an authenticated principal"), false
	}
	typedCredential := scope.Credential.PermissionProfile != "" || scope.Credential.Permissions != nil
	typedOperation := strings.TrimSpace(operation.Contract.Action) != "" || strings.TrimSpace(operation.Contract.Resolver) != ""
	if typedOperation {
		if !scope.DevAuthBypass && !typedCredential {
			m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "denied", fmt.Errorf("typed operation requires typed credential scope"))
			return agenttools.ToolError("forbidden", "typed credential scope is required for this operation"), false
		}
		if !scope.DevAuthBypass {
			pairs, err := agentAPIGenPermissionPairs(operation, request, scope.ProjectID)
			if err != nil || scope.Credential.PermissionProfile != access.PermissionCatalogProfile || !permissionPairsAllowAll(scope.Credential.Permissions, pairs) {
				if err == nil {
					err = fmt.Errorf("credential permission pair set does not cover operation")
				}
				m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "denied", err)
				return agenttools.ToolError("forbidden", "credential is not allowed to call this operation"), false
			}
		}
	} else if typedCredential {
		// Typed credentials are exact action/target ceilings. They must not be
		// projected back into a legacy capability-only operation.
		m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "denied", fmt.Errorf("typed credential cannot use legacy operation authority"))
		return agenttools.ToolError("forbidden", "typed credential scope cannot use this operation"), false
	}
	if !hasCapability {
		return agenttools.ToolError("forbidden", "operation has no generated resource capability metadata"), false
	}
	if (!typedOperation || !typedCredential) && !agentCredentialAllowsCapability(scope, capability) {
		m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "denied", fmt.Errorf("credential restriction"))
		return agenttools.ToolError("forbidden", "credential is not allowed to call this tool"), false
	}
	m.recordToolAudit(ctx, scope, capability, "agent_tool", operationID, "success", nil)
	return agentcore.ToolResult{}, true
}

func agentAPIGenPermissionPairs(operation agenttools.APIGenOperation, request *http.Request, projectID string) ([]access.PermissionPair, error) {
	project, err := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	requirement, err := access.NewTypedOperationRequirementService().Requirement(access.Action(operation.Contract.Action), operation.Contract.Resolver)
	if err != nil {
		return nil, err
	}
	if requirement.Resolver == access.TypedOperationResolverInstance {
		return nil, fmt.Errorf("typed instance operation target is not available to the agent route")
	}
	if request == nil {
		return nil, fmt.Errorf("APIGen request is required for typed operation target resolution")
	}
	resourceID := strings.TrimSpace(chi.URLParam(request, typedOperationPathParameter(requirement.Resolver)))
	if resourceID == "" && requirement.Resolver == access.TypedOperationResolverDashboard {
		resourceID = typedDashboardRequestID(request)
	}
	if resourceID == "" && (requirement.Resolver == access.TypedOperationResolverProject || requirement.Resolver == access.TypedOperationResolverDelivery) {
		resourceID = project.String()
	}
	if resourceID == "" {
		return nil, fmt.Errorf("typed APIGen operation %q target is unavailable", operation.Contract.OperationID)
	}
	kind, ok := typedOperationResourceKind(requirement.Resolver)
	if !ok {
		return nil, fmt.Errorf("typed APIGen operation %q resolver %q has no agent target mapping", operation.Contract.OperationID, requirement.Resolver)
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(resourceID), kind)
	if err != nil {
		return nil, err
	}
	return requirement.ResolvePairs(project, resource)
}

func permissionPairsAllowAll(granted, requested []access.PermissionPair) bool {
	for _, pair := range requested {
		if !access.PermissionSetAllows(granted, pair) {
			return false
		}
	}
	return true
}

func typedOperationPathParameter(resolver access.TypedOperationResolver) string {
	switch resolver {
	case access.TypedOperationResolverDashboard:
		return "dashboard"
	case access.TypedOperationResolverSemanticModel, access.TypedOperationResolverModel:
		return "model"
	case access.TypedOperationResolverConnection:
		return "connection"
	case access.TypedOperationResolverSource:
		return "source"
	case access.TypedOperationResolverPipeline:
		return "pipeline"
	case access.TypedOperationResolverProject, access.TypedOperationResolverDelivery:
		return "project"
	default:
		return ""
	}
}

func typedOperationResourceKind(resolver access.TypedOperationResolver) (projectgraph.Kind, bool) {
	switch resolver {
	case access.TypedOperationResolverDashboard:
		return projectgraph.KindDashboard, true
	case access.TypedOperationResolverSemanticModel:
		return projectgraph.KindSemanticModel, true
	case access.TypedOperationResolverConnection:
		return projectgraph.KindConnection, true
	case access.TypedOperationResolverSource:
		return projectgraph.KindSource, true
	case access.TypedOperationResolverModel:
		return projectgraph.KindModel, true
	case access.TypedOperationResolverPipeline:
		return projectgraph.KindPipeline, true
	case access.TypedOperationResolverProject, access.TypedOperationResolverDelivery:
		return projectgraph.KindProjectNamespace, true
	default:
		return "", false
	}
}

func typedDashboardRequestID(request *http.Request) string {
	if request == nil || request.Body == nil {
		return ""
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return ""
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Dashboard string `json:"dashboard"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Dashboard)
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
		ProjectID:     strings.TrimSpace(scope.ProjectID),
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
	if !credential.Restricted {
		return true
	}
	if credential.Capabilities == nil {
		return false
	}
	for _, allowed := range credential.Capabilities {
		if strings.EqualFold(strings.TrimSpace(allowed), string(capability)) {
			return true
		}
	}
	return false
}

// CredentialAllowsResource applies the credential ceiling to a resolved
// graph resource. Typed credentials are checked against the exact
// action/resource pair (including catalog prerequisites); they never fall
// back to a legacy capability with the same broad meaning.
func CredentialAllowsResource(scope Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) bool {
	if !scope.Credential.Restricted || scope.DevAuthBypass {
		return true
	}
	if scope.Credential.PermissionProfile == "" && scope.Credential.Permissions == nil {
		return agentCredentialAllowsCapability(scopeFromModule(scope), capability)
	}
	action, ok := typedResourceAction(kind, capability)
	if !ok || scope.Credential.PermissionProfile != access.PermissionCatalogProfile {
		return false
	}
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(scope.ProjectID))
	if err != nil {
		return false
	}
	resource, err := access.NewResourceRef(id, kind)
	if err != nil {
		return false
	}
	pair, err := access.NewExactPermissionPair(action, projectID, resource)
	if err != nil {
		return false
	}
	required, err := access.RequiredPermissionPairs(pair)
	return err == nil && permissionPairsAllowAll(scope.Credential.Permissions, required)
}

func scopeFromModule(scope Scope) agentcap.Scope {
	return agentcap.Scope{ProjectID: scope.ProjectID, PrincipalID: scope.PrincipalID, DevAuthBypass: scope.DevAuthBypass, Credential: agentcap.CredentialScope{
		Restricted: scope.Credential.Restricted, Capabilities: append([]string(nil), scope.Credential.Capabilities...),
		PermissionProfile: scope.Credential.PermissionProfile, Permissions: clonePermissionPairs(scope.Credential.Permissions),
	}}
}

func typedResourceAction(kind projectgraph.Kind, capability access.Capability) (access.Action, bool) {
	if capability == access.CapabilityResourceShare {
		return access.ActionResourceShare, true
	}
	switch capability {
	case access.CapabilityResourceRead:
		switch kind {
		case projectgraph.KindDashboard:
			return access.ActionDashboardRead, true
		case projectgraph.KindSemanticModel:
			return access.ActionSemanticRead, true
		case projectgraph.KindSource:
			return access.ActionSourceRead, true
		case projectgraph.KindModel:
			return access.ActionModelRead, true
		case projectgraph.KindPipeline:
			return access.ActionPipelineRead, true
		case projectgraph.KindConnection:
			return access.ActionConnectionRead, true
		}
	case access.CapabilityResourceUse:
		switch kind {
		case projectgraph.KindSemanticModel:
			return access.ActionSemanticQuery, true
		case projectgraph.KindConnection:
			return access.ActionConnectionUse, true
		case projectgraph.KindPipeline:
			return access.ActionPipelineRun, true
		case projectgraph.KindSource:
			return access.ActionSourceRead, true
		case projectgraph.KindModel:
			return access.ActionModelRead, true
		}
	case access.CapabilityResourceEdit:
		switch kind {
		case projectgraph.KindDashboard:
			return access.ActionDashboardUpdate, true
		case projectgraph.KindSemanticModel:
			return access.ActionSemanticUpdate, true
		case projectgraph.KindSource:
			return access.ActionSourceUpdate, true
		case projectgraph.KindModel:
			return access.ActionModelUpdate, true
		case projectgraph.KindPipeline:
			return access.ActionPipelineUpdate, true
		case projectgraph.KindConnection:
			return access.ActionConnectionManage, true
		}
	case access.CapabilityResourceManage:
		switch kind {
		case projectgraph.KindDashboard:
			return access.ActionDashboardDelete, true
		case projectgraph.KindSemanticModel:
			return access.ActionSemanticDelete, true
		case projectgraph.KindSource:
			return access.ActionSourceDelete, true
		case projectgraph.KindModel:
			return access.ActionModelDelete, true
		case projectgraph.KindPipeline:
			return access.ActionPipelineDelete, true
		case projectgraph.KindConnection:
			return access.ActionConnectionManage, true
		}
	case access.CapabilityResourcePublish:
		if kind == projectgraph.KindDashboard {
			return access.ActionDashboardPublish, true
		}
	}
	return "", false
}

func (m *Module) apigenOperationCapability(operationID string) (access.Capability, bool) {
	for _, operation := range m.apiOperations {
		if operation.Contract.OperationID == operationID {
			return operationCapability(operation.Contract)
		}
	}
	return "", false
}

func operationCapability(contract agenttools.OperationContract) (access.Capability, bool) {
	if !contract.Protected || contract.AuthzMode != "privilege" {
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
