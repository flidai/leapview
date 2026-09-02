package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	definition = definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), CreateDashboardDraftToolName)
	result, err = definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "missing-resolver", Arguments: json.RawMessage(`{"title":"Sales","semanticModelId":"semantic_sales"}`)})
	if err != nil || !result.IsError || toolErrorCode(result) != "catalog_unavailable" {
		t.Fatalf("missing resolver result=%#v err=%v", result, err)
	}
}

func TestDashboardAuthoringCatalogApplicationIsAuthoritativeAndProjectIsFixed(t *testing.T) {
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
	if !ok || len(value.Items) != 2 {
		t.Fatalf("dashboard catalog=%#v", result.Content)
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

func TestDashboardAuthoringResolvesActiveProjectPerOperation(t *testing.T) {
	app := &projectAuthoringFake{list: catalog.ListResult{Items: []catalog.Dashboard{{ID: "dashboard_sales", Source: catalog.SourceProject}}}}
	active := projectgraph.ResourceID("project:activated")
	provider := DashboardAuthoringProvider{
		Application: app,
		ProjectID:   projectgraph.ResourceID("project:stale"),
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return active, nil
		},
		Resolve: (&projectResolverFake{}).Resolve,
	}
	definition := definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), ListDashboardsToolName)
	result, err := definition.Handler.Run(context.Background(), agentcore.ToolCall{ID: "list", Arguments: json.RawMessage(`{}`)})
	if err != nil || result.IsError {
		t.Fatalf("list result=%#v err=%v", result, err)
	}
	if app.listRequest.ProjectID != active {
		t.Fatalf("list project = %q, want %q", app.listRequest.ProjectID, active)
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
		if app.command.Provenance.ActorID != scope.PrincipalID || app.command.Provenance.ConversationID != scope.ConversationID || app.command.Provenance.ToolCallID != payload.name {
			t.Fatalf("%s provenance=%#v", payload.name, app.command.Provenance)
		}
	}
	if resolver.capabilityFor("dashboard_sales") != access.CapabilityResourceRead {
		t.Fatalf("project fork capability=%q, want RESOURCE_READ", resolver.capabilityFor("dashboard_sales"))
	}
}

func TestDashboardAuthoringExportProvidesTypedYAMLDisplay(t *testing.T) {
	app := &projectAuthoringFake{}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: (&projectResolverFake{}).Resolve}
	export := definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), ExportDashboardYAMLToolName)
	result, err := export.Handler.Run(context.Background(), agentcore.ToolCall{
		ID: "export", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales","sourceKind":"project"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("export result=%#v err=%v", result, err)
	}
	display, ok := result.DisplayContent.(map[string]any)
	if !ok || display["type"] != "code" || display["language"] != "yaml" || display["content"] != "version: 1\n" {
		t.Fatalf("export display = %#v", result.DisplayContent)
	}
}

func TestDashboardAuthoringSourceToolsReadAndEditExactDraftYAML(t *testing.T) {
	app := &projectAuthoringFake{
		source: authoringapplication.SourceRead{
			DashboardID: "dashboard_sales", DraftID: "draft_1",
			Revision: dashboardauthoring.RevisionToken{RevisionID: "revision_1", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)},
			YAML:     "apiVersion: leapview.dev/v1\n",
		},
		editSourceResult: authoringapplication.SourceEditResult{
			Result: authoringservice.Result{Revision: dashboardauthoring.RevisionToken{RevisionID: "revision_2", Number: 2, ContentHash: "sha256:" + strings.Repeat("b", 64)}},
			YAML:   "apiVersion: leapview.dev/v1\n", Diff: "--- dashboard.yaml\n+++ dashboard.yaml\n", ChangedBlocks: 1,
		},
	}
	provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest()}
	scope := Scope{PrincipalID: "principal", ConversationID: "conversation"}
	read := definitionByName(provider.Definitions(scope), ReadDashboardSourceToolName)
	result, err := read.Handler.Run(context.Background(), agentcore.ToolCall{ID: "read-source", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales"}`)})
	if err != nil || result.IsError {
		t.Fatalf("read result=%#v err=%v", result, err)
	}
	display, ok := result.DisplayContent.(map[string]any)
	if !ok || display["language"] != "yaml" || display["content"] != app.source.YAML {
		t.Fatalf("read display = %#v", result.DisplayContent)
	}

	edit := definitionByName(provider.Definitions(scope), EditDashboardSourceToolName)
	result, err = edit.Handler.Run(context.Background(), agentcore.ToolCall{ID: "edit-source", Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales","draftId":"draft_1","expectedRevision":{"revisionId":"revision_1","number":1,"contentHash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"edits":[{"oldText":"Overview","newText":"Executive overview"}]}`)})
	if err != nil || result.IsError {
		t.Fatalf("edit result=%#v err=%v", result, err)
	}
	if app.editSource.Provenance.ActorID != "principal" || app.editSource.Provenance.ConversationID != "conversation" || app.editSource.Provenance.ToolCallID != "edit-source" {
		t.Fatalf("edit provenance = %#v", app.editSource.Provenance)
	}
	display, ok = result.DisplayContent.(map[string]any)
	if !ok || display["language"] != "diff" || display["content"] != app.editSourceResult.Diff {
		t.Fatalf("edit display = %#v", result.DisplayContent)
	}
}

func projectIDForTest() projectgraph.ResourceID { return projectgraph.ResourceID("project_demo") }

func toolErrorCode(result agentcore.ToolResult) string {
	content, _ := result.Content.(map[string]any)
	errValue, _ := content["error"].(map[string]any)
	code, _ := errValue["code"].(string)
	return code
}

func definitionByName(definitions []agentcore.ToolDefinition, name string) agentcore.ToolDefinition {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition
		}
	}
	panic("tool definition not found: " + name)
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
	list             catalog.ListResult
	listRequest      catalog.ListRequest
	getRequest       catalog.GetRequest
	create           authoringservice.CreateRequest
	fork             sourceadapter.ForkRequest
	command          dashboardauthoring.Command
	source           authoringapplication.SourceRead
	editSource       authoringapplication.SourceEditRequest
	editSourceResult authoringapplication.SourceEditResult
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
func (f *projectAuthoringFake) Execute(_ context.Context, _ projectgraph.ResourceID, command dashboardauthoring.Command) (authoringservice.Result, error) {
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
func (f *projectAuthoringFake) ReadSource(context.Context, authoringapplication.DraftRequest) (authoringapplication.SourceRead, error) {
	return f.source, nil
}
func (f *projectAuthoringFake) EditSource(_ context.Context, request authoringapplication.SourceEditRequest) (authoringapplication.SourceEditResult, error) {
	f.editSource = request
	return f.editSourceResult, nil
}

var _ DashboardAuthoring = (*projectAuthoringFake)(nil)
