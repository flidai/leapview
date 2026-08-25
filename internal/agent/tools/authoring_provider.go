package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// Dashboard authoring tools intentionally expose the existing application
// facade rather than a second persistence or reducer implementation.
const (
	ListDashboardsToolName          = "list_dashboards"
	GetDashboardToolName            = "get_dashboard"
	GetDashboardDraftToolName       = "get_dashboard_draft"
	CreateDashboardDraftToolName    = "create_dashboard_draft"
	ExecuteDashboardCommandToolName = "execute_dashboard_command"
	ForkDashboardToolName           = "fork_dashboard"
	PreviewDashboardDraftToolName   = "preview_dashboard_draft"
	ExportDashboardYAMLToolName     = "export_dashboard_yaml"
	SetDashboardVisibilityToolName  = "set_dashboard_visibility"
	AddDashboardPageToolName        = "add_dashboard_page"
	AddDashboardVisualToolName      = "add_dashboard_visual"
	AssignDashboardFieldToolName    = "assign_dashboard_field"
)

// DashboardAuthoring is the exact transport-neutral application surface used
// by both headless HTTP and agent tools.
type DashboardAuthoring interface {
	List(context.Context, catalog.ListRequest) (catalog.ListResult, error)
	Get(context.Context, catalog.GetRequest) (catalog.Dashboard, error)
	Draft(context.Context, authoringapplication.DraftRequest) (authoringapplication.DraftRead, error)
	Create(context.Context, authoringservice.CreateRequest) (authoringservice.Result, error)
	Execute(context.Context, projectgraph.ResourceID, dashboardauthoring.Command) (authoringservice.Result, error)
	ExecuteIntent(context.Context, authoringapplication.IntentRequest) (authoringservice.Result, error)
	Fork(context.Context, sourceadapter.ForkRequest) (authoringservice.Result, error)
	Preview(context.Context, previewservice.PreviewRequest) (previewservice.Preview, error)
	ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error)
}

type DashboardAuthoringProvider struct {
	Application DashboardAuthoring
	// ProjectID is retained for compositions that have a statically bound
	// runtime. ResolveProjectID is authoritative when configured and resolves
	// the exact active serving project for each operation.
	ProjectID        projectgraph.ResourceID
	ResolveProjectID func(context.Context) (projectgraph.ResourceID, error)
	Resolve          ResourceResolver
}

type dashboardAuthoringListInput struct {
}

type dashboardAuthoringGetInput struct {
	DashboardID string `json:"dashboardId"`
}

type dashboardAuthoringCreateInput struct {
	Title         string `json:"title"`
	SemanticModel string `json:"semanticModelId"`
	DashboardID   string `json:"dashboardId,omitempty"`
	Slug          string `json:"slug,omitempty"`
}

// dashboardAuthoringCommandInput embeds the closed domain command so the
// JSON contract has no model-controlled actor or provenance fields.
type dashboardAuthoringCommandInput struct {
	dashboardauthoring.Command
}

type dashboardAuthoringForkInput struct {
	SourceKind      sourceadapter.SourceKind       `json:"sourceKind"`
	SourceDashboard dashboardauthoring.DashboardID `json:"sourceDashboardId"`
	Title           string                         `json:"title,omitempty"`
	Slug            string                         `json:"slug,omitempty"`
}

type dashboardAuthoringPreviewInput struct {
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	Page             string                           `json:"page"`
	Filters          dashboard.Filters                `json:"filters,omitempty"`
}

type dashboardAuthoringVisibilityInput struct {
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	Visibility       dashboardauthoring.Visibility    `json:"visibility"`
}

type dashboardAuthoringAddPageInput struct {
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	PageID           string                           `json:"pageId,omitempty"`
	Title            string                           `json:"title,omitempty"`
}

type dashboardAuthoringAddVisualInput struct {
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	PageID           string                           `json:"pageId"`
	VisualID         string                           `json:"visualId,omitempty"`
	ComponentID      string                           `json:"componentId,omitempty"`
	Type             string                           `json:"type"`
	Title            string                           `json:"title,omitempty"`
}

type dashboardAuthoringAssignFieldInput struct {
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	PageID           string                           `json:"pageId"`
	VisualID         string                           `json:"visualId"`
	FieldID          string                           `json:"fieldId"`
	Role             dashboardauthoring.FieldRole     `json:"role"`
}

type dashboardAuthoringExportInput struct {
	SourceKind  sourceadapter.SourceKind       `json:"sourceKind"`
	DashboardID dashboardauthoring.DashboardID `json:"dashboardId"`
}

func (p DashboardAuthoringProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	if p.Application == nil {
		return nil
	}
	return p.definitions(scope)
}

// contractDefinitions returns the complete static contract, including when a
// runtime application is unavailable. Reference generation uses this to keep
// the published catalog aligned with the same provider definitions without
// advertising an unusable provider at runtime.
func (p DashboardAuthoringProvider) contractDefinitions(scope Scope) []agentcore.ToolDefinition {
	return p.definitions(scope)
}

func (p DashboardAuthoringProvider) definitions(scope Scope) []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{
		p.definition(ListDashboardsToolName, "List the authorized project dashboard catalog.", "read", agentcontracts.DashboardAuthoringListInputSchemaJSON, agentcontracts.DashboardAuthoringListResultSchemaJSON, []string{"dashboard", "authoring", "catalog"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringListInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionView)
			if !ok {
				return result
			}
			value, err := p.Application.List(ctx, catalog.ListRequest{ProjectID: project, ActorID: scope.PrincipalID})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(GetDashboardToolName, "Get one authorized dashboard's governed metadata.", "read", agentcontracts.DashboardAuthoringGetInputSchemaJSON, agentcontracts.DashboardAuthoringGetResultSchemaJSON, []string{"dashboard", "authoring", "catalog"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringGetInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionView)
			if !ok {
				return result
			}
			id, result, ok := authoredDashboardID(input.DashboardID)
			if !ok {
				return result
			}
			value, err := p.Application.Get(ctx, catalog.GetRequest{ProjectID: project, ActorID: scope.PrincipalID, DashboardID: id})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: map[string]any{"dashboard": value}}
		}),
		p.definition(GetDashboardDraftToolName, "Read the exact current private draft and retained revision for one dashboard.", "read", agentcontracts.DashboardAuthoringDraftGetInputSchemaJSON, agentcontracts.DashboardAuthoringDraftGetResultSchemaJSON, []string{"dashboard", "authoring", "draft"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringGetInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			id, result, ok := authoredDashboardID(input.DashboardID)
			if !ok {
				return result
			}
			value, err := p.Application.Draft(ctx, authoringapplication.DraftRequest{ProjectID: project, ActorID: scope.PrincipalID, DashboardID: id})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: map[string]any{"lifecycle": value.Lifecycle, "revision": value.Revision}}
		}),
		p.definition(SetDashboardVisibilityToolName, "Set a private dashboard draft's visibility using an exact expected revision.", "write", agentcontracts.DashboardAuthoringSetVisibilityInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringVisibilityInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, SetVisibility: &dashboardauthoring.SetVisibilityPayload{Visibility: input.Visibility}}
			return p.executeIntent(ctx, scope, call, command)
		}),
		p.definition(AddDashboardPageToolName, "Add one dashboard page to a private draft using an exact expected revision.", "write", agentcontracts.DashboardAuthoringAddPageInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringAddPageInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, AddPage: &dashboardauthoring.AddPagePayload{PageID: input.PageID, Title: input.Title}}
			return p.executeIntent(ctx, scope, call, command)
		}),
		p.definition(AddDashboardVisualToolName, "Add a governed dashboard visual to a private draft using an exact expected revision.", "write", agentcontracts.DashboardAuthoringAddVisualInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringAddVisualInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, AddVisual: &dashboardauthoring.AddVisualPayload{PageID: input.PageID, VisualID: input.VisualID, ComponentID: input.ComponentID, Type: input.Type, Title: input.Title}}
			return p.executeIntent(ctx, scope, call, command)
		}),
		p.definition(AssignDashboardFieldToolName, "Assign one governed semantic field to a dashboard visual in a private draft using an exact expected revision.", "write", agentcontracts.DashboardAuthoringAssignFieldInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringAssignFieldInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, AssignField: &dashboardauthoring.AssignFieldPayload{PageID: input.PageID, VisualID: input.VisualID, FieldID: input.FieldID, Role: input.Role}}
			return p.executeIntent(ctx, scope, call, command)
		}),
		p.definition(CreateDashboardDraftToolName, "Create a private dashboard draft owned by the authenticated principal. Agent retries that reuse the same invocation identity and payload replay the original draft; reusing that identity with a different payload is rejected.", "write", agentcontracts.DashboardAuthoringCreateInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "create"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringCreateInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			semanticModel, result, ok := p.resolveTarget(ctx, scope, input.SemanticModel, projectgraph.KindSemanticModel, access.CapabilityResourceUse)
			if !ok {
				return result
			}
			value, err := p.Application.Create(ctx, authoringservice.CreateRequest{ProjectID: project, ActorID: scope.PrincipalID, DashboardID: dashboardauthoring.DashboardID(strings.TrimSpace(input.DashboardID)), Title: input.Title, Slug: input.Slug, SemanticModel: semanticModel, Visibility: dashboardauthoring.VisibilityPrivate, Origin: dashboardauthoring.OriginAgent, ConversationID: scope.ConversationID, ToolCallID: call.ID, IdempotencyKey: call.ID})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(ExecuteDashboardCommandToolName, "Publish or archive one dashboard authoring revision using a closed, typed command and exact expected revision. Actor and agent provenance are server-bound.", "destructive", agentcontracts.DashboardAuthoringCommandInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "command"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringCommandInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			if !isLifecycleCommand(input.Command) {
				return ToolError("invalid_arguments", "execute_dashboard_command accepts only publish or archive commands")
			}
			project, result, ok := p.prepare(ctx, scope, authoringActionForCommand(input.Command))
			if !ok {
				return result
			}
			id, result, ok := authoredDashboardID(string(input.Command.DashboardID))
			if !ok {
				return result
			}
			input.Command.DashboardID = id
			input.Command.ID = dashboardauthoring.CommandID(strings.TrimSpace(call.ID))
			input.Command.Provenance = dashboardauthoring.Provenance{Origin: dashboardauthoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: scope.ConversationID, ToolCallID: call.ID}
			value, err := p.Application.Execute(ctx, project, input.Command)
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(ForkDashboardToolName, "Fork an authorized project or instance dashboard source into a private draft. Agent retries that reuse the same invocation identity and payload replay the original draft.", "write", agentcontracts.DashboardAuthoringForkInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "fork"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringForkInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			sourceID, result, ok := authoredDashboardID(string(input.SourceDashboard))
			if !ok {
				return result
			}
			if input.SourceKind == sourceadapter.SourceProject {
				resolved, resolvedResult, resolvedOK := p.resolveTarget(ctx, scope, string(sourceID), projectgraph.KindDashboard, access.CapabilityResourceRead)
				if !resolvedOK {
					return resolvedResult
				}
				sourceID = dashboardauthoring.DashboardID(resolved.String())
			}
			value, err := p.Application.Fork(ctx, sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: input.SourceKind, ProjectID: project, DashboardID: sourceID}, TargetProjectID: project, ActorID: scope.PrincipalID, Title: input.Title, Slug: input.Slug, Origin: dashboardauthoring.OriginAgent, ConversationID: scope.ConversationID, ToolCallID: call.ID, IdempotencyKey: call.ID})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(PreviewDashboardDraftToolName, "Preview an exact private dashboard draft revision and page against the active governed runtime.", "read", agentcontracts.DashboardAuthoringPreviewInputSchemaJSON, agentcontracts.DashboardAuthoringPreviewResultSchemaJSON, []string{"dashboard", "authoring", "preview"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringPreviewInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			id, result, ok := authoredDashboardID(string(input.DashboardID))
			if !ok {
				return result
			}
			value, err := p.Application.Preview(ctx, previewservice.PreviewRequest{ProjectID: project, ActorID: scope.PrincipalID, DashboardID: id, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, PageID: input.Page, Filters: input.Filters})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(ExportDashboardYAMLToolName, "Export an authorized authored dashboard source as canonical project YAML.", "read", agentcontracts.DashboardAuthoringExportInputSchemaJSON, agentcontracts.DashboardAuthoringExportResultSchemaJSON, []string{"dashboard", "authoring", "export"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringExportInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionView)
			if !ok {
				return result
			}
			id, result, ok := authoredDashboardID(string(input.DashboardID))
			if !ok {
				return result
			}
			if input.SourceKind == sourceadapter.SourceProject {
				resolved, resolvedResult, resolvedOK := p.resolveTarget(ctx, scope, string(id), projectgraph.KindDashboard, access.CapabilityResourceRead)
				if !resolvedOK {
					return resolvedResult
				}
				id = dashboardauthoring.DashboardID(resolved.String())
			}
			request := sourceadapter.ExportRequest{Source: sourceadapter.SourceRef{Kind: input.SourceKind, ProjectID: project, DashboardID: id}, ActorID: scope.PrincipalID}
			var value []byte
			var err error
			value, err = p.Application.ExportYAML(ctx, request)
			if err != nil {
				return authoringToolError(err)
			}
			yaml := string(value)
			return agentcore.ToolResult{
				Content: map[string]any{"yaml": yaml},
				DisplayContent: map[string]any{
					"type": "code", "language": "yaml", "content": yaml,
				},
			}
		}),
	}
}

func (p DashboardAuthoringProvider) definition(name, description, effect, input, output string, tags []string, run func(context.Context, agentcore.ToolCall) agentcore.ToolResult) agentcore.ToolDefinition {
	return agentcore.ToolDefinition{Name: name, Description: description, InputSchema: json.RawMessage(input), OutputSchema: json.RawMessage(output), Effect: effect, Tags: tags, Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
		return run(ctx, call), nil
	})}
}

func (p DashboardAuthoringProvider) prepare(ctx context.Context, scope Scope, action dashboardauthoring.AuthorizationAction) (projectgraph.ResourceID, agentcore.ToolResult, bool) {
	_ = action
	project := p.ProjectID
	var err error
	if p.ResolveProjectID != nil {
		project, err = p.ResolveProjectID(ctx)
	}
	if err == nil {
		err = project.Validate()
	}
	if err != nil {
		return "", ToolError("catalog_unavailable", "trusted project identity is not configured"), false
	}
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return "", ToolError("authentication_required", "agent authoring tools require an authenticated principal"), false
	}
	return project, agentcore.ToolResult{}, true
}

func (p DashboardAuthoringProvider) resolveTarget(ctx context.Context, scope Scope, raw string, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, agentcore.ToolResult, bool) {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(raw))
	if err != nil {
		return "", ToolError("invalid_arguments", fmt.Sprintf("invalid %s resource ID: %v", kind, err)), false
	}
	if p.Resolve == nil {
		return "", ToolError("catalog_unavailable", "authorized project catalog is not configured"), false
	}
	resolved, err := p.Resolve(ctx, scope, id, kind, capability)
	if err != nil {
		return "", ToolError("catalog_not_found", "resource is unknown or unauthorized"), false
	}
	if _, err := projectgraph.NewResourceID(resolved.String()); err != nil {
		return "", ToolError("catalog_unavailable", "catalog returned an invalid resource ID"), false
	}
	return resolved, agentcore.ToolResult{}, true
}

func (p DashboardAuthoringProvider) executeIntent(ctx context.Context, scope Scope, call agentcore.ToolCall, command dashboardauthoring.Command) agentcore.ToolResult {
	project, result, ok := p.prepare(ctx, scope, dashboardauthoring.AuthorizationActionEdit)
	if !ok {
		return result
	}
	id, result, ok := authoredDashboardID(string(command.DashboardID))
	if !ok {
		return result
	}
	command.DashboardID = id
	command.ID = dashboardauthoring.CommandID(strings.TrimSpace(call.ID))
	command.Provenance = dashboardauthoring.Provenance{Origin: dashboardauthoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: scope.ConversationID, ToolCallID: call.ID}
	value, err := p.Application.ExecuteIntent(ctx, authoringapplication.IntentRequest{ProjectID: project, ActorID: scope.PrincipalID, Command: command})
	if err != nil {
		return authoringToolError(err)
	}
	return agentcore.ToolResult{Content: value}
}

func authoredDashboardID(raw string) (dashboardauthoring.DashboardID, agentcore.ToolResult, bool) {
	id := dashboardauthoring.DashboardID(strings.TrimSpace(raw))
	if err := dashboardauthoring.ValidateDashboardID(id); err != nil {
		return "", ToolError("invalid_arguments", "dashboardId is invalid"), false
	}
	return id, agentcore.ToolResult{}, true
}

func decodeAuthoringArguments(arguments json.RawMessage, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return nil
}

func authoringActionForCommand(command dashboardauthoring.Command) dashboardauthoring.AuthorizationAction {
	action, err := command.RequiredAction()
	if err != nil {
		// Execute performs the canonical validation and returns the structured
		// invalid-payload error. EDIT is the least-privileged preflight action.
		return dashboardauthoring.AuthorizationActionEdit
	}
	return action
}

func isLifecycleCommand(command dashboardauthoring.Command) bool {
	action, err := command.RequiredAction()
	return err == nil && (action == dashboardauthoring.AuthorizationActionPublish || action == dashboardauthoring.AuthorizationActionArchive)
}

func authoringToolError(err error) agentcore.ToolResult {
	code := "authoring_failed"
	switch {
	case errors.Is(err, dashboardauthoring.ErrStaleRevision):
		code = "stale_revision"
	case errors.Is(err, dashboardauthoring.ErrCommandReuse):
		code = "command_reuse"
	case errors.Is(err, dashboardauthoring.ErrConflict):
		code = "conflict"
	case errors.Is(err, dashboardauthoring.ErrNotFound), errors.Is(err, catalog.ErrNotFound), errors.Is(err, previewservice.ErrNotFound):
		code = "not_found"
	case errors.Is(err, dashboardauthoring.ErrInvalidAuthoring), errors.Is(err, dashboardauthoring.ErrInvalidPayload), errors.Is(err, dashboardauthoring.ErrInvalidIdentifier):
		code = "invalid_arguments"
	}
	diagnostics := configschema.Diagnostics(err)
	content := map[string]any{"error": map[string]any{"code": code, "message": err.Error(), "diagnostics": diagnostics}}
	return agentcore.ToolResult{IsError: true, Content: content}
}

var _ DashboardAuthoring = (*authoringapplication.Application)(nil)
