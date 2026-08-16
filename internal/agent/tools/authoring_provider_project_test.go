package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestDashboardAuthoringRequiresAuthenticatedPrincipalAndResolver(t *testing.T) {
	app := &projectAuthoringFake{}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest()}
	definition := definitionByName(provider.Definitions(Scope{PrincipalID: ""}), GetDashboardToolName)
	result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "unauthenticated", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales"}`)})
	if err != nil || !result.IsError || toolErrorCode(result) != "authentication_required" {
		t.Fatalf("unauthenticated result=%#v err=%v", result, err)
	}
	provider = DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest()}
	definition = definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), GetDashboardToolName)
	result, err = definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "missing-resolver", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales"}`)})
	if err != nil || !result.IsError || toolErrorCode(result) != "catalog_unavailable" {
		t.Fatalf("missing resolver result=%#v err=%v", result, err)
	}
}

func TestDashboardAuthoringCatalogAuthorizationAndFixedProject(t *testing.T) {
	app := &projectAuthoringFake{list: catalog.ListResult{Items: []catalog.Dashboard{
		{ID: "dashboard_sales", Source: catalog.SourceProject},
		{ID: "dashboard_secret", Source: catalog.SourceInstance},
	}, Count: 2}}
	resolver := &projectResolverFake{deny: map[projectgraph.ResourceID]bool{"dashboard_secret": true}}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: resolver.Resolve}
	definition := definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), ListDashboardsToolName)
	result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "list", Arguments: json.RawMessage(`{}`)})
	if err != nil || result.IsError {
		t.Fatalf("list result=%#v err=%v", result, err)
	}
	value, ok := result.Content.(catalog.ListResult)
	if !ok || len(value.Items) != 1 || value.Items[0].ID != "dashboard_sales" {
		t.Fatalf("filtered dashboards=%#v", result.Content)
	}
	if app.listRequest.ProjectID != projectIDForTest() {
		t.Fatalf("list project=%q, want %q", app.listRequest.ProjectID, projectIDForTest())
	}
	get := definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), GetDashboardToolName)
	if _, err := get.Handler.Run(context.Background(), agentcore.ToolCall{ID: "get", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales"}`)}); err != nil {
		t.Fatal(err)
	}
	if app.getRequest.ProjectID != projectIDForTest() || app.getRequest.DashboardID != "dashboard_sales" {
		t.Fatalf("get request=%#v", app.getRequest)
	}
}

func TestDashboardAuthoringResolvesCreateForkAndLifecycleCapabilities(t *testing.T) {
	app := &projectAuthoringFake{}
	resolver := &projectResolverFake{}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: resolver.Resolve}
	scope := Scope{PrincipalID: "principal", ConversationID: "conversation"}
	definitions := provider.Definitions(scope)
	create := definitionByName(definitions, CreateDashboardDraftToolName)
	result, err := create.Handler.Run(context.Background(), agentcore.ToolCall{ID: "create-call", Arguments: json.RawMessage(`{"title":"Sales","semanticModelId":"semantic_sales"}`)})
	if err != nil || result.IsError {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	if app.create.ProjectID != projectIDForTest() || app.create.SemanticModel != "semantic_sales" {
		t.Fatalf("create request=%#v", app.create)
	}
	if resolver.capabilityFor("semantic_sales") != access.CapabilityResourceUse {
		t.Fatalf("semantic model capability=%q, want RESOURCE_USE", resolver.capabilityFor("semantic_sales"))
	}
	fork := definitionByName(definitions, ForkDashboardToolName)
	for _, kind := range []sourceadapter.SourceKind{sourceadapter.SourceProject, sourceadapter.SourceInstance} {
		args, _ := json.Marshal(map[string]any{"sourceKind": kind, "sourceDashboardId": "dashboard_sales"})
		if result, err := fork.Handler.Run(context.Background(), agentcore.ToolCall{ID: "fork-" + string(kind), Arguments: args}); err != nil || result.IsError {
			t.Fatalf("fork %s result=%#v err=%v", kind, result, err)
		}
		if app.fork.Source.Kind != kind || app.fork.Source.ProjectID != projectIDForTest() {
			t.Fatalf("fork source=%#v, want kind %s and fixed project", app.fork.Source, kind)
		}
	}
	lifecycle := definitionByName(definitions, ExecuteDashboardCommandToolName)
	for _, payload := range []struct {
		name string
		json string
		cap  access.Capability
	}{
		{"publish", `{"dashboardId":"dashboard_sales","draftId":"draft_1","expectedRevision":{"revisionId":"revision_1","number":1,"contentHash":"hash"},"publish":{}}`, access.CapabilityResourcePublish},
		{"archive", `{"dashboardId":"dashboard_sales","draftId":"draft_1","expectedRevision":{"revisionId":"revision_1","number":1,"contentHash":"hash"},"archive":{}}`, access.CapabilityResourceManage},
	} {
		result, err := lifecycle.Handler.Run(context.Background(), agentcore.ToolCall{ID: payload.name, Arguments: json.RawMessage(payload.json)})
		if err != nil || result.IsError {
			t.Fatalf("%s result=%#v err=%v", payload.name, result, err)
		}
		if resolver.capabilityFor("dashboard_sales") != payload.cap {
			t.Fatalf("%s capability=%q, want %q", payload.name, resolver.capabilityFor("dashboard_sales"), payload.cap)
		}
		if app.command.Provenance.ActorID != scope.PrincipalID || app.command.Provenance.ConversationID != scope.ConversationID || app.command.Provenance.ToolCallID != payload.name {
			t.Fatalf("%s provenance=%#v", payload.name, app.command.Provenance)
		}
	}
}

func projectIDForTest() projectgraph.ResourceID { return projectgraph.ResourceID("project_demo") }

func toolErrorCode(result agentcore.ToolResult) string {
	content, _ := result.Content.(map[string]any)
	errValue, _ := content["error"].(map[string]any)
	code, _ := errValue["code"].(string)
	return code
}

type projectResolverFake struct {
	deny     map[projectgraph.ResourceID]bool
	requests map[projectgraph.ResourceID]access.Capability
}

func (f *projectResolverFake) Resolve(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
	if f.requests == nil {
		f.requests = map[projectgraph.ResourceID]access.Capability{}
	}
	f.requests[id] = capability
	if f.deny != nil && f.deny[id] {
		return "", errors.New("denied")
	}
	return id, nil
}

func (f *projectResolverFake) capabilityFor(id string) access.Capability {
	return f.requests[projectgraph.ResourceID(id)]
}

type projectAuthoringFake struct {
	list        catalog.ListResult
	listRequest catalog.ListRequest
	getRequest  catalog.GetRequest
	create      authoringservice.CreateRequest
	fork        sourceadapter.ForkRequest
	command     dashboardauthoring.Command
}

func (f *projectAuthoringFake) List(_ context.Context, request catalog.ListRequest) (catalog.ListResult, error) {
	f.listRequest = request
	return f.list, nil
}
func (f *projectAuthoringFake) Get(_ context.Context, request catalog.GetRequest) (catalog.Dashboard, error) {
	f.getRequest = request
	return catalog.Dashboard{ID: projectgraph.ResourceID(request.DashboardID)}, nil
}
func (f *projectAuthoringFake) Draft(context.Context, authoringapplication.DraftRequest) (authoringapplication.DraftRead, error) {
	return authoringapplication.DraftRead{}, nil
}
func (f *projectAuthoringFake) Create(_ context.Context, request authoringservice.CreateRequest) (authoringservice.Result, error) {
	f.create = request
	return authoringservice.Result{}, nil
}
func (f *projectAuthoringFake) Execute(_ context.Context, _ string, command dashboardauthoring.Command) (authoringservice.Result, error) {
	f.command = command
	return authoringservice.Result{}, nil
}
func (f *projectAuthoringFake) ExecuteIntent(context.Context, authoringapplication.IntentRequest) (authoringservice.Result, error) {
	return authoringservice.Result{}, nil
}
func (f *projectAuthoringFake) Fork(_ context.Context, request sourceadapter.ForkRequest) (authoringservice.Result, error) {
	f.fork = request
	return authoringservice.Result{}, nil
}
func (f *projectAuthoringFake) Preview(context.Context, previewservice.PreviewRequest) (previewservice.Preview, error) {
	return previewservice.Preview{}, nil
}
func (f *projectAuthoringFake) ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error) {
	return []byte("version: 1\n"), nil
}

var _ DashboardAuthoring = (*projectAuthoringFake)(nil)
