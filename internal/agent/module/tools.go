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
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
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
	toolScope := ToolsScope(scope)
	return (agenttools.ProviderSet{
		Docs:      m.DocsToolProvider(),
		Catalog:   m.CatalogToolProvider(),
		Visual:    m.VisualToolProvider(),
		APIGen:    m.APIGenToolProvider(),
		Authoring: m.DashboardAuthoringToolProvider(),
	}).Definitions(toolScope)
}

func (m *Module) DashboardAuthoringToolProvider() agenttools.DashboardAuthoringProvider {
	return agenttools.DashboardAuthoringProvider{
		Application: m.dashboardAuthoring,
		ProjectID:   m.projectID,
		Resolve:     m.resolveResource,
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
		Resolve: m.resolveResource,
		QueryContext: func(ctx context.Context, scope agenttools.Scope) context.Context {
			if m.queryContext == nil {
				return ctx
			}
			return m.queryContext(ctx, scopeFromTools(scope))
		},
		Authorize: func(ctx context.Context, scope agenttools.Scope, request agenttools.VisualAuthorizationRequest) (agentcore.ToolResult, bool) {
			agentScope := scopeFromTools(scope)
			model := access.ItemObjectWithParent(access.SecurableSemanticModel, agentScope.ProjectID, request.Model, access.WorkspaceObject(agentScope.ProjectID))
			objects := []access.ObjectRef{
				access.ItemObjectWithParent(access.SecurableDataset, agentScope.ProjectID, request.Model+"/"+request.Dataset, model),
				model,
				access.WorkspaceObject(agentScope.ProjectID),
			}
			return m.authorizePrivilege(ctx, agentScope, access.PrivilegeQueryData, objects, "agent_tool", request.ToolName)
		},
		SemanticModel: func(projectID, modelID string) (model *semanticmodel.Model, ok bool) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return nil, false
			}
			return metrics.SemanticModel(modelID)
		},
		AggregateRows: func(ctx context.Context, projectID, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return nil, fmt.Errorf("unknown project %q", projectID)
			}
			return executeAggregateRows(ctx, metrics, modelID, request)
		},
		PreviewRows: func(ctx context.Context, projectID, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return nil, fmt.Errorf("unknown project %q", projectID)
			}
			return executePreviewRows(ctx, metrics, modelID, request)
		},
		Histogram: func(ctx context.Context, projectID, modelID string, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return nil, fmt.Errorf("unknown project %q", projectID)
			}
			return executeHistogram(ctx, metrics, modelID, request, binCount)
		},
		Distribution: func(ctx context.Context, projectID, modelID string, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
			metrics, ok := m.dashboardMetrics(projectID)
			if !ok || metrics == nil {
				return nil, fmt.Errorf("unknown project %q", projectID)
			}
			return executeDistribution(ctx, metrics, modelID, request, sort, limit)
		},
		QueryMetadata: func(ctx context.Context, projectID, modelID string) agenttools.VisualQueryMetadata {
			if m.queryMetadata != nil {
				return m.queryMetadata(ctx, projectID, modelID)
			}
			return agenttools.VisualQueryMetadata{ServingSnapshot: "unversioned"}
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
		ConversationID: scope.ConversationID,
		DevAuthBypass:  scope.DevAuthBypass,
		Credential: agenttools.CredentialScope{
			ProjectID:  scope.Credential.ProjectID,
			Restricted: scope.Credential.Restricted,
			Privileges: append([]string{}, scope.Credential.Privileges...),
		},
	}
}

func scopeFromTools(scope agenttools.Scope) agentcap.Scope {
	return agentcap.Scope{
		ProjectID:      scope.ProjectID,
		PrincipalID:    scope.PrincipalID,
		ConversationID: scope.ConversationID,
		DevAuthBypass:  scope.DevAuthBypass,
		Credential: agentcap.CredentialScope{
			ProjectID:  scope.Credential.ProjectID,
			Restricted: scope.Credential.Restricted,
			Privileges: append([]string{}, scope.Credential.Privileges...),
		},
	}
}

func (m *Module) authorizeAPIGenOperation(ctx context.Context, scope agentcap.Scope, operationID string) (agentcore.ToolResult, bool) {
	privilege, ok := m.apigenOperationPrivilege(operationID)
	if !ok {
		return agenttools.ToolError("forbidden", "operation has no generated LeapView privilege metadata"), false
	}
	if operationID == "search" || (operationID == "listProjects" && strings.TrimSpace(scope.ProjectID) == "") {
		if strings.TrimSpace(scope.PrincipalID) == "" {
			return agenttools.ToolError("unauthorized", "agent tool requires an authenticated principal"), false
		}
		if !agentCredentialAllowsPrivilege(scope, privilege) {
			return agenttools.ToolError("forbidden", "credential is not allowed to call this tool"), false
		}
		return agentcore.ToolResult{}, true
	}
	return m.authorizePrivilege(ctx, scope, privilege, []access.ObjectRef{access.WorkspaceObject(scope.ProjectID)}, "agent_tool", operationID)
}

func (m *Module) authorizePrivilege(ctx context.Context, scope agentcap.Scope, privilege access.Privilege, objects []access.ObjectRef, targetType, targetID string) (agentcore.ToolResult, bool) {
	if scope.PrincipalID == "" {
		return agenttools.ToolError("unauthorized", "agent tool requires an authenticated principal"), false
	}
	if !agentCredentialAllowsPrivilege(scope, privilege) {
		m.recordToolAudit(ctx, scope, privilege, targetType, targetID, "denied", fmt.Errorf("credential restriction"))
		return agenttools.ToolError("forbidden", "credential is not allowed to call this tool"), false
	}
	if scope.DevAuthBypass {
		return agentcore.ToolResult{}, true
	}
	if m.authorizeAnyObject == nil {
		return agenttools.ToolError("authorization_failed", "authorization is unavailable"), false
	}
	allowed, err := m.authorizeAnyObject(ctx, scope.PrincipalID, privilege, objects)
	if err != nil {
		m.recordToolAudit(ctx, scope, privilege, targetType, targetID, "error", err)
		return agenttools.ToolError("authorization_failed", err.Error()), false
	}
	if !allowed {
		m.recordToolAudit(ctx, scope, privilege, targetType, targetID, "denied", nil)
		return agenttools.ToolError("forbidden", "principal does not have privilege to call this tool"), false
	}
	m.recordToolAudit(ctx, scope, privilege, targetType, targetID, "success", nil)
	return agentcore.ToolResult{}, true
}

func (m *Module) recordToolAudit(ctx context.Context, scope agentcap.Scope, privilege access.Privilege, targetType, targetID, status string, cause error) {
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
		WorkspaceID:   scope.ProjectID,
		PrincipalID:   scope.PrincipalID,
		Action:        "agent_tool.called",
		TargetType:    targetType,
		TargetID:      targetID,
		Privilege:     privilege,
		Status:        status,
		RequestID:     metadata.RequestID,
		CorrelationID: metadata.CorrelationID,
		MetadataJSON:  string(bytes),
	})
}

func agentCredentialAllowsPrivilege(scope agentcap.Scope, privilege access.Privilege) bool {
	credential := scope.Credential
	if credential.WorkspaceID != "" && credential.WorkspaceID != scope.ProjectID {
		return false
	}
	if !credential.Restricted {
		return true
	}
	for _, allowed := range credential.Privileges {
		if allowed == string(privilege) {
			return true
		}
	}
	return false
}

func (m *Module) apigenOperationPrivilege(operationID string) (access.Privilege, bool) {
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
	return access.ParsePrivilege(value)
}

func apiGenToolContracts(operations []agenttools.APIGenOperation) map[string]agenttool.Contract {
	contracts := make(map[string]agenttool.Contract, len(operations))
	for _, operation := range operations {
		contracts[operation.Tool.Name] = operation.Tool
	}
	return contracts
}
