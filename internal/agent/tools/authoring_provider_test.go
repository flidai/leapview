package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestDashboardAuthoringDefinitionsExposeGovernedEffects(t *testing.T) {
	provider := DashboardAuthoringProvider{Application: &fakeDashboardAuthoring{}}
	definitions := provider.Definitions(Scope{WorkspaceID: "sales", PrincipalID: "principal"})
	if len(definitions) != 12 {
		t.Fatalf("definitions = %d, want 12", len(definitions))
	}
	wantEffects := map[string]string{
		ListDashboardsToolName: "read", GetDashboardToolName: "read", GetDashboardDraftToolName: "read",
		CreateDashboardDraftToolName: "write", ExecuteDashboardCommandToolName: "write", ForkDashboardToolName: "write",
		PreviewDashboardDraftToolName: "read", ExportDashboardYAMLToolName: "read",
		SetDashboardVisibilityToolName: "write", AddDashboardPageToolName: "write", AddDashboardVisualToolName: "write", AssignDashboardFieldToolName: "write",
	}
	for _, definition := range definitions {
		if got := definition.Effect; got != wantEffects[definition.Name] {
			t.Fatalf("tool %q effect = %q, want %q", definition.Name, got, wantEffects[definition.Name])
		}
		if !json.Valid(definition.InputSchema) || !json.Valid(definition.OutputSchema) {
			t.Fatalf("tool %q has invalid schemas", definition.Name)
		}
	}
}

func TestDashboardAuthoringCommandBindsIdentityAndRejectsModelEvidence(t *testing.T) {
	app := &fakeDashboardAuthoring{}
	provider := DashboardAuthoringProvider{Application: app}
	definitions := provider.Definitions(Scope{WorkspaceID: "sales", PrincipalID: "principal", ConversationID: "conversation"})
	definition := definitionByName(definitions, ExecuteDashboardCommandToolName)
	valid := json.RawMessage(`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"publish":{}}`)
	result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call-1", Name: ExecuteDashboardCommandToolName, Arguments: valid})
	if err != nil || result.IsError {
		t.Fatalf("valid command result = %#v, err=%v", result, err)
	}
	if app.command.Provenance.ActorID != "principal" || app.command.Provenance.Origin != dashboardauthoring.OriginAgent || app.command.Provenance.ConversationID != "conversation" || app.command.Provenance.ToolCallID != "call-1" {
		t.Fatalf("bound provenance = %#v", app.command.Provenance)
	}
	if app.command.ID != "call-1" {
		t.Fatalf("command id = %q, want call-1", app.command.ID)
	}
	if app.workspace != "sales" {
		t.Fatalf("workspace = %q, want sales", app.workspace)
	}

	catalog, err := agentcore.NewToolCatalog(definitions)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","id":"attacker-id","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"publish":{}}`,
		`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"provenance":{"origin":"ui","actorId":"attacker"},"publish":{}}`,
		`{"workspace":"sales","dashboardId":"orders","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"publish":{}}`,
	} {
		if _, err := catalog.Execute(context.Background(), agentcore.ToolCall{ID: "call-2", Name: ExecuteDashboardCommandToolName, Arguments: json.RawMessage(invalid)}); !errors.Is(err, agentcore.ErrInvalidToolArguments) {
			t.Fatalf("invalid command error = %v, want invalid tool arguments", err)
		}
	}
}

func TestDashboardAuthoringRoutesLifecycleAndBuilderCommandsToTheirApplicationEntrypoints(t *testing.T) {
	app := &fakeDashboardAuthoring{}
	definitions := (DashboardAuthoringProvider{Application: app}).Definitions(Scope{WorkspaceID: "sales", PrincipalID: "principal"})

	executeDefinition := definitionByName(definitions, ExecuteDashboardCommandToolName)
	publish := json.RawMessage(`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"publish":{}}`)
	if _, err := executeDefinition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "publish-call", Name: ExecuteDashboardCommandToolName, Arguments: publish}); err != nil {
		t.Fatalf("publish command: %v", err)
	}
	if app.executeCalls != 1 || app.intentCalls != 0 {
		t.Fatalf("publish calls = execute:%d intent:%d, want execute:1 intent:0", app.executeCalls, app.intentCalls)
	}
	for _, payload := range []string{"setVisibility", "metadata"} {
		invalid := json.RawMessage(`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"` + payload + `":{}}`)
		result, err := executeDefinition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "invalid-" + payload, Name: ExecuteDashboardCommandToolName, Arguments: invalid})
		if err != nil || !result.IsError || app.executeCalls != 1 {
			t.Fatalf("%s payload result=%#v err=%v executeCalls=%d; generic path must reject bounded builder payloads", payload, result, err, app.executeCalls)
		}
	}

	intentDefinition := definitionByName(definitions, SetDashboardVisibilityToolName)
	visibility := json.RawMessage(`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"visibility":"private"}`)
	if _, err := intentDefinition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "intent-call", Name: SetDashboardVisibilityToolName, Arguments: visibility}); err != nil {
		t.Fatalf("builder intent: %v", err)
	}
	if app.executeCalls != 1 || app.intentCalls != 1 {
		t.Fatalf("intent calls = execute:%d intent:%d, want execute:1 intent:1", app.executeCalls, app.intentCalls)
	}
}

func TestDashboardAuthoringStaleRevisionReturnsStructuredDiagnostics(t *testing.T) {
	app := &fakeDashboardAuthoring{executeErr: dashboardauthoring.ErrStaleRevision}
	definition := definitionByName((DashboardAuthoringProvider{Application: app}).Definitions(Scope{WorkspaceID: "sales", PrincipalID: "principal"}), ExecuteDashboardCommandToolName)
	result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call-1", Name: ExecuteDashboardCommandToolName, Arguments: json.RawMessage(`{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"publish":{}}`)})
	if err != nil || !result.IsError {
		t.Fatalf("stale result = %#v, err=%v", result, err)
	}
	content, ok := result.Content.(map[string]any)
	if !ok {
		t.Fatalf("error content = %#v", result.Content)
	}
	errorValue, _ := content["error"].(map[string]any)
	if errorValue["code"] != "stale_revision" {
		t.Fatalf("error code = %#v", errorValue["code"])
	}
	if _, ok := errorValue["diagnostics"]; !ok {
		t.Fatalf("stale error missing diagnostics: %#v", errorValue)
	}
}

func TestDashboardAuthoringPreflightUsesActionSpecificAuthorization(t *testing.T) {
	var actions []dashboardauthoring.AuthorizationAction
	provider := DashboardAuthoringProvider{
		Application: &fakeDashboardAuthoring{},
		Authorize: func(_ context.Context, _ Scope, _ string, action dashboardauthoring.AuthorizationAction) (agentcore.ToolResult, bool) {
			actions = append(actions, action)
			return agentcore.ToolResult{}, true
		},
	}
	scope := Scope{WorkspaceID: "sales", PrincipalID: "principal"}
	definitions := provider.Definitions(scope)
	for _, item := range []struct {
		name string
		args string
		want dashboardauthoring.AuthorizationAction
	}{
		{ListDashboardsToolName, `{"workspace":"sales"}`, dashboardauthoring.AuthorizationActionView},
		{GetDashboardDraftToolName, `{"workspace":"sales","dashboard":"orders"}`, dashboardauthoring.AuthorizationActionEdit},
		{ExecuteDashboardCommandToolName, `{"workspace":"sales","dashboardId":"orders","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"hash"},"publish":{}}`, dashboardauthoring.AuthorizationActionPublish},
	} {
		definition := definitionByName(definitions, item.name)
		if _, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call-1", Name: item.name, Arguments: json.RawMessage(item.args)}); err != nil {
			t.Fatalf("%s handler: %v", item.name, err)
		}
		if got := actions[len(actions)-1]; got != item.want {
			t.Fatalf("%s action = %q, want %q", item.name, got, item.want)
		}
	}
}

func definitionByName(definitions []agentcore.ToolDefinition, name string) agentcore.ToolDefinition {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition
		}
	}
	return agentcore.ToolDefinition{}
}

type fakeDashboardAuthoring struct {
	workspace    string
	command      dashboardauthoring.Command
	executeErr   error
	executeCalls int
	intentCalls  int
}

func (f *fakeDashboardAuthoring) List(context.Context, catalog.ListRequest) (catalog.ListResult, error) {
	return catalog.ListResult{Items: []catalog.Dashboard{}, Count: 0}, nil
}
func (f *fakeDashboardAuthoring) Get(context.Context, catalog.GetRequest) (catalog.Dashboard, error) {
	return catalog.Dashboard{}, nil
}
func (f *fakeDashboardAuthoring) Draft(context.Context, authoringapplication.DraftRequest) (authoringapplication.DraftRead, error) {
	return authoringapplication.DraftRead{}, nil
}
func (f *fakeDashboardAuthoring) Create(context.Context, authoringservice.CreateRequest) (authoringservice.Result, error) {
	return authoringservice.Result{}, nil
}
func (f *fakeDashboardAuthoring) Execute(_ context.Context, workspace string, command dashboardauthoring.Command) (authoringservice.Result, error) {
	f.executeCalls++
	f.workspace, f.command = workspace, command
	if f.executeErr != nil {
		return authoringservice.Result{}, f.executeErr
	}
	return authoringservice.Result{Revision: dashboardauthoring.RevisionToken{RevisionID: "rev-1", Number: 1, ContentHash: "hash"}}, nil
}
func (f *fakeDashboardAuthoring) ExecuteIntent(ctx context.Context, request authoringapplication.IntentRequest) (authoringservice.Result, error) {
	f.intentCalls++
	f.workspace, f.command = request.WorkspaceID, request.Command
	if f.executeErr != nil {
		return authoringservice.Result{}, f.executeErr
	}
	return authoringservice.Result{Revision: dashboardauthoring.RevisionToken{RevisionID: "rev-1", Number: 1, ContentHash: "hash"}}, nil
}
func (f *fakeDashboardAuthoring) Fork(context.Context, sourceadapter.ForkRequest) (authoringservice.Result, error) {
	return authoringservice.Result{}, nil
}
func (f *fakeDashboardAuthoring) Preview(context.Context, previewservice.PreviewRequest) (previewservice.Preview, error) {
	return previewservice.Preview{}, nil
}
func (f *fakeDashboardAuthoring) ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error) {
	return []byte("version: 1\n"), nil
}

var _ DashboardAuthoring = (*fakeDashboardAuthoring)(nil)
