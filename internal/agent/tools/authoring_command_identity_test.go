package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/google/uuid"
)

type commandIdentityAuthoring struct {
	projectAuthoringFake
	intent authoringapplication.IntentRequest
	audit  access.AuditIntent
}

func (f *commandIdentityAuthoring) ExecuteIntent(ctx context.Context, request authoringapplication.IntentRequest) (authoringservice.Result, error) {
	f.intent = request
	f.audit, _ = dashboardauthoring.AuditIntentFromContext(ctx)
	return authoringservice.Result{}, nil
}

func TestDashboardAuthoringToolCallIdentityIsNativeAndReplayStable(t *testing.T) {
	app := &commandIdentityAuthoring{}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: (&projectResolverFake{}).Resolve}
	scope := Scope{PrincipalID: "principal", ConversationID: "conversation"}
	call := agentcore.ToolCall{ID: "call_model_generated_123", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales","draftId":"0198f2c0-7c7a-7f00-8a11-000000000001","expectedRevision":{"revisionId":"revision_1","number":1,"contentHash":"hash"},"pageId":"pies","type":"pie","title":"Revenue by segment"}`)}
	run := func(scope Scope) string {
		t.Helper()
		definition := definitionByName(provider.Definitions(scope), AddDashboardVisualToolName)
		result, err := definition.Handler.Run(t.Context(), call)
		if err != nil || result.IsError {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		id := string(app.intent.Command.ID)
		if parsed, err := uuid.Parse(id); err != nil || parsed.Version() != 7 {
			t.Fatalf("provider call ID was not converted to native command identity: %q", id)
		}
		if app.intent.Command.Provenance.ToolCallID != call.ID {
			t.Fatal("original tool-call provenance was lost")
		}
		if app.audit.EventID != id || app.audit.ActorID != scope.PrincipalID || app.audit.Operation != "executeDashboardAuthoringCommand" {
			t.Fatalf("missing matching source audit intent: %#v", app.audit)
		}
		return id
	}
	first := run(scope)
	if run(scope) != first {
		t.Fatal("replay changed command identity")
	}
	scope.ConversationID = "another-conversation"
	if run(scope) == first {
		t.Fatal("command identity collided across conversations")
	}
}

func (f *commandIdentityAuthoring) Create(ctx context.Context, request authoringservice.CreateRequest) (authoringservice.Result, error) {
	f.create = request
	f.audit, _ = dashboardauthoring.AuditIntentFromContext(ctx)
	return createResultForTest(), nil
}

func TestDashboardDraftCreationSuppliesTransactionalAudit(t *testing.T) {
	app := &commandIdentityAuthoring{}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: (&projectResolverFake{}).Resolve}
	scope := Scope{PrincipalID: "principal", ConversationID: "conversation"}
	call := agentcore.ToolCall{ID: "model_create_call", Arguments: json.RawMessage(`{"title":"Finance overview","semanticModelId":"semantic_sales"}`)}
	result, err := definitionByName(provider.Definitions(scope), CreateDashboardDraftToolName).Handler.Run(t.Context(), call)
	if err != nil || result.IsError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	id, err := uuid.Parse(app.audit.EventID)
	if err != nil || id.Version() != 7 || app.audit.Operation != "createDashboardAuthoringDraft" || app.audit.ActorID != scope.PrincipalID {
		t.Fatalf("missing native creation audit: %#v", app.audit)
	}
	if app.create.IdempotencyKey != app.audit.EventID || app.create.ToolCallID != call.ID || app.create.ConversationID != scope.ConversationID {
		t.Fatalf("creation replay/provenance changed: %#v", app.create)
	}
}

func (f *commandIdentityAuthoring) Fork(ctx context.Context, request sourceadapter.ForkRequest) (authoringservice.Result, error) {
	f.fork = request
	f.audit, _ = dashboardauthoring.AuditIntentFromContext(ctx)
	return authoringservice.Result{}, nil
}

func TestDashboardForkSuppliesTransactionalAudit(t *testing.T) {
	app := &commandIdentityAuthoring{}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: (&projectResolverFake{}).Resolve}
	scope := Scope{PrincipalID: "principal", ConversationID: "conversation"}
	call := agentcore.ToolCall{ID: "model_fork_call", Arguments: json.RawMessage(`{"sourceKind":"project","sourceDashboardId":"dashboard_sales","title":"Finance overview"}`)}
	result, err := definitionByName(provider.Definitions(scope), ForkDashboardToolName).Handler.Run(t.Context(), call)
	if err != nil || result.IsError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	id, err := uuid.Parse(app.audit.EventID)
	if err != nil || id.Version() != 7 || app.audit.Operation != "forkDashboardAuthoringDraft" || app.audit.ActorID != scope.PrincipalID {
		t.Fatalf("missing fork audit: %#v", app.audit)
	}
	if app.fork.IdempotencyKey != app.audit.EventID || app.fork.ToolCallID != call.ID || app.fork.ConversationID != scope.ConversationID {
		t.Fatalf("fork provenance changed: %#v", app.fork)
	}
}
