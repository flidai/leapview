package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
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
	Execute(context.Context, string, dashboardauthoring.Command) (authoringservice.Result, error)
	ExecuteIntent(context.Context, authoringapplication.IntentRequest) (authoringservice.Result, error)
	Fork(context.Context, sourceadapter.ForkRequest) (authoringservice.Result, error)
	Preview(context.Context, previewservice.PreviewRequest) (previewservice.Preview, error)
	ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error)
}

// DashboardAuthoringAuthorize performs a transport-level privilege check in
// addition to the application's object authorizer. It is optional so focused
// provider tests can use a small fake application; production always wires it.
type DashboardAuthoringAuthorize func(context.Context, Scope, string, dashboardauthoring.AuthorizationAction) (agentcore.ToolResult, bool)

type DashboardAuthoringProvider struct {
	Application DashboardAuthoring
	Authorize   DashboardAuthoringAuthorize
}

type dashboardAuthoringListInput struct {
	Workspace string `json:"workspace"`
}

type dashboardAuthoringGetInput struct {
	Workspace string `json:"workspace"`
	Dashboard string `json:"dashboard"`
}

type dashboardAuthoringCreateInput struct {
	Workspace     string `json:"workspace"`
	Title         string `json:"title"`
	SemanticModel string `json:"semanticModel"`
	DashboardID   string `json:"dashboardId,omitempty"`
	Slug          string `json:"slug,omitempty"`
}

// dashboardAuthoringCommandInput embeds the closed domain command so the
// JSON contract has one explicit top-level workspace and no model-controlled
// actor or provenance fields.
type dashboardAuthoringCommandInput struct {
	Workspace string `json:"workspace"`
	dashboardauthoring.Command
}

type dashboardAuthoringForkInput struct {
	SourceKind      sourceadapter.SourceKind       `json:"sourceKind"`
	SourceWorkspace string                         `json:"sourceWorkspace"`
	SourceDashboard dashboardauthoring.DashboardID `json:"sourceDashboard"`
	TargetWorkspace string                         `json:"targetWorkspace,omitempty"`
	Title           string                         `json:"title,omitempty"`
	Slug            string                         `json:"slug,omitempty"`
}

type dashboardAuthoringPreviewInput struct {
	Workspace        string                           `json:"workspace"`
	Dashboard        dashboardauthoring.DashboardID   `json:"dashboard"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	Page             string                           `json:"page"`
	Filters          dashboard.Filters                `json:"filters,omitempty"`
}

type dashboardAuthoringVisibilityInput struct {
	Workspace        string                           `json:"workspace"`
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	Visibility       dashboardauthoring.Visibility    `json:"visibility"`
}

type dashboardAuthoringAddPageInput struct {
	Workspace        string                           `json:"workspace"`
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	PageID           string                           `json:"pageId,omitempty"`
	Title            string                           `json:"title,omitempty"`
}

type dashboardAuthoringAddVisualInput struct {
	Workspace        string                           `json:"workspace"`
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
	Workspace        string                           `json:"workspace"`
	DashboardID      dashboardauthoring.DashboardID   `json:"dashboardId"`
	DraftID          dashboardauthoring.DraftID       `json:"draftId"`
	ExpectedRevision dashboardauthoring.RevisionToken `json:"expectedRevision"`
	PageID           string                           `json:"pageId"`
	VisualID         string                           `json:"visualId"`
	FieldID          string                           `json:"fieldId"`
	Role             dashboardauthoring.FieldRole     `json:"role"`
}

type dashboardAuthoringExportInput struct {
	SourceKind sourceadapter.SourceKind       `json:"sourceKind"`
	Workspace  string                         `json:"workspace"`
	Dashboard  dashboardauthoring.DashboardID `json:"dashboard"`
}

func (p DashboardAuthoringProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	if p.Application == nil {
		return nil
	}
	return []agentcore.ToolDefinition{
		p.definition(ListDashboardsToolName, "List the authorized dashboard catalog for one workspace.", "read", agentcontracts.DashboardAuthoringListInputSchemaJSON, agentcontracts.DashboardAuthoringListResultSchemaJSON, []string{"dashboard", "authoring", "catalog"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringListInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, dashboardauthoring.AuthorizationActionView)
			if !ok {
				return result
			}
			value, err := p.Application.List(ctx, catalog.ListRequest{WorkspaceID: workspace, ActorID: scope.PrincipalID})
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
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, dashboardauthoring.AuthorizationActionView)
			if !ok {
				return result
			}
			id := dashboardauthoring.DashboardID(strings.TrimSpace(input.Dashboard))
			value, err := p.Application.Get(ctx, catalog.GetRequest{WorkspaceID: workspace, ActorID: scope.PrincipalID, DashboardID: id})
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
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			value, err := p.Application.Draft(ctx, authoringapplication.DraftRequest{WorkspaceID: workspace, ActorID: scope.PrincipalID, DashboardID: dashboardauthoring.DashboardID(strings.TrimSpace(input.Dashboard))})
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
			return p.executeIntent(ctx, scope, call, input.Workspace, command)
		}),
		p.definition(AddDashboardPageToolName, "Add one dashboard page to a private draft using an exact expected revision.", "write", agentcontracts.DashboardAuthoringAddPageInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringAddPageInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, AddPage: &dashboardauthoring.AddPagePayload{PageID: input.PageID, Title: input.Title}}
			return p.executeIntent(ctx, scope, call, input.Workspace, command)
		}),
		p.definition(AddDashboardVisualToolName, "Add a governed dashboard visual to a private draft using an exact expected revision.", "write", agentcontracts.DashboardAuthoringAddVisualInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringAddVisualInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, AddVisual: &dashboardauthoring.AddVisualPayload{PageID: input.PageID, VisualID: input.VisualID, ComponentID: input.ComponentID, Type: input.Type, Title: input.Title}}
			return p.executeIntent(ctx, scope, call, input.Workspace, command)
		}),
		p.definition(AssignDashboardFieldToolName, "Assign one governed semantic field to a dashboard visual in a private draft using an exact expected revision.", "write", agentcontracts.DashboardAuthoringAssignFieldInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "intent"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringAssignFieldInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			command := dashboardauthoring.Command{DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, AssignField: &dashboardauthoring.AssignFieldPayload{PageID: input.PageID, VisualID: input.VisualID, FieldID: input.FieldID, Role: input.Role}}
			return p.executeIntent(ctx, scope, call, input.Workspace, command)
		}),
		p.definition(CreateDashboardDraftToolName, "Create a private dashboard draft owned by the authenticated principal. Each invocation creates a new draft; retries are not idempotent.", "write", agentcontracts.DashboardAuthoringCreateInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "create"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringCreateInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			value, err := p.Application.Create(ctx, authoringservice.CreateRequest{WorkspaceID: workspace, ActorID: scope.PrincipalID, DashboardID: dashboardauthoring.DashboardID(strings.TrimSpace(input.DashboardID)), Title: input.Title, Slug: input.Slug, SemanticModel: input.SemanticModel, Visibility: dashboardauthoring.VisibilityPrivate, Origin: dashboardauthoring.OriginAgent, ConversationID: scope.ConversationID, ToolCallID: call.ID})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(ExecuteDashboardCommandToolName, "Apply one closed, typed dashboard authoring command using an exact expected revision. Actor and agent provenance are server-bound.", "write", agentcontracts.DashboardAuthoringCommandInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "command"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringCommandInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			if !isLifecycleCommand(input.Command) {
				return ToolError("invalid_arguments", "execute_dashboard_command accepts only publish or archive commands")
			}
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, authoringActionForCommand(input.Command))
			if !ok {
				return result
			}
			input.Command.ID = dashboardauthoring.CommandID(strings.TrimSpace(call.ID))
			input.Command.Provenance = dashboardauthoring.Provenance{Origin: dashboardauthoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: scope.ConversationID, ToolCallID: call.ID}
			value, err := p.Application.Execute(ctx, workspace, input.Command)
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: value}
		}),
		p.definition(ForkDashboardToolName, "Fork an authorized workspace or retained project dashboard source into a private draft. Each invocation creates a new draft; retries are not idempotent.", "write", agentcontracts.DashboardAuthoringForkInputSchemaJSON, agentcontracts.DashboardAuthoringResultSchemaJSON, []string{"dashboard", "authoring", "fork"}, func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			var input dashboardAuthoringForkInput
			if err := decodeAuthoringArguments(call.Arguments, &input); err != nil {
				return ToolError("invalid_arguments", err.Error())
			}
			workspace, result, ok := p.prepare(ctx, scope, input.TargetWorkspaceOrSource(), dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			_ = workspace // target is normalized by the application/source adapter.
			value, err := p.Application.Fork(ctx, sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: input.SourceKind, WorkspaceID: strings.TrimSpace(input.SourceWorkspace), DashboardID: input.SourceDashboard}, TargetWorkspaceID: strings.TrimSpace(input.TargetWorkspace), ActorID: scope.PrincipalID, Title: input.Title, Slug: input.Slug, Origin: dashboardauthoring.OriginAgent, ConversationID: scope.ConversationID, ToolCallID: call.ID})
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
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, dashboardauthoring.AuthorizationActionEdit)
			if !ok {
				return result
			}
			value, err := p.Application.Preview(ctx, previewservice.PreviewRequest{WorkspaceID: workspace, ActorID: scope.PrincipalID, DashboardID: input.Dashboard, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, PageID: input.Page, Filters: input.Filters})
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
			workspace, result, ok := p.prepare(ctx, scope, input.Workspace, dashboardauthoring.AuthorizationActionView)
			if !ok {
				return result
			}
			value, err := p.Application.ExportYAML(ctx, sourceadapter.ExportRequest{Source: sourceadapter.SourceRef{Kind: input.SourceKind, WorkspaceID: workspace, DashboardID: input.Dashboard}, ActorID: scope.PrincipalID})
			if err != nil {
				return authoringToolError(err)
			}
			return agentcore.ToolResult{Content: map[string]any{"yaml": string(value)}}
		}),
	}
}

func (p DashboardAuthoringProvider) definition(name, description, effect, input, output string, tags []string, run func(context.Context, agentcore.ToolCall) agentcore.ToolResult) agentcore.ToolDefinition {
	return agentcore.ToolDefinition{Name: name, Description: description, InputSchema: json.RawMessage(input), OutputSchema: json.RawMessage(output), Effect: effect, Tags: tags, Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
		return run(ctx, call), nil
	})}
}

func (p DashboardAuthoringProvider) prepare(ctx context.Context, scope Scope, requested string, action dashboardauthoring.AuthorizationAction) (string, agentcore.ToolResult, bool) {
	workspace := strings.TrimSpace(requested)
	if workspace == "" {
		return "", ToolError("invalid_arguments", "workspace is required"), false
	}
	if scope.Credential.WorkspaceID != "" && scope.Credential.WorkspaceID != workspace {
		return "", ToolError("access_denied", "credential is restricted to another workspace"), false
	}
	if scope.WorkspaceID != "" && scope.WorkspaceID != workspace {
		return "", ToolError("access_denied", "workspace is outside the authenticated agent scope"), false
	}
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return "", ToolError("authentication_required", "agent authoring tools require an authenticated principal"), false
	}
	if p.Authorize != nil {
		if result, ok := p.Authorize(ctx, scope, workspace, action); !ok {
			return "", result, false
		}
	}
	return workspace, agentcore.ToolResult{}, true
}

func (p DashboardAuthoringProvider) executeIntent(ctx context.Context, scope Scope, call agentcore.ToolCall, requested string, command dashboardauthoring.Command) agentcore.ToolResult {
	workspace, result, ok := p.prepare(ctx, scope, requested, dashboardauthoring.AuthorizationActionEdit)
	if !ok {
		return result
	}
	command.ID = dashboardauthoring.CommandID(strings.TrimSpace(call.ID))
	command.Provenance = dashboardauthoring.Provenance{Origin: dashboardauthoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: scope.ConversationID, ToolCallID: call.ID}
	value, err := p.Application.ExecuteIntent(ctx, authoringapplication.IntentRequest{WorkspaceID: workspace, ActorID: scope.PrincipalID, Command: command})
	if err != nil {
		return authoringToolError(err)
	}
	return agentcore.ToolResult{Content: value}
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

func (input dashboardAuthoringForkInput) TargetWorkspaceOrSource() string {
	if value := strings.TrimSpace(input.TargetWorkspace); value != "" {
		return value
	}
	return strings.TrimSpace(input.SourceWorkspace)
}

var _ DashboardAuthoring = (*authoringapplication.Application)(nil)
