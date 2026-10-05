package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestDashboardAuthoringListResultMatchesGeneratedOutputSchema(t *testing.T) {
	provider := DashboardAuthoringProvider{
		Application: &projectAuthoringFake{list: catalog.ListResult{
			Items:         []catalog.Dashboard{},
			Count:         3,
			InstanceCount: 2,
			ProjectCount:  1,
		}},
		ProjectID: projectIDForTest(),
	}
	toolCatalog, err := agentcore.NewToolCatalog(provider.Definitions(Scope{PrincipalID: "principal"}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := toolCatalog.Execute(context.Background(), agentcore.ToolCall{
		ID:        "list-contract",
		Name:      ListDashboardsToolName,
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("list_dashboards output failed generated schema validation: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_dashboards returned an error result: %#v", result)
	}
}

func TestDashboardVisibilityContractMatchesDomain(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(agentcontracts.DashboardAuthoringSetVisibilityInputSchemaJSON), &schema); err != nil {
		t.Fatal(err)
	}
	got := schema.Properties["visibility"].Enum
	want := []string{"private", "restricted", "organization"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visibility enum = %v, want %v", got, want)
	}
	for _, value := range got {
		if !dashboardauthoring.Visibility(value).Valid() {
			t.Fatalf("generated contract accepts visibility %q but the domain rejects it", value)
		}
	}
}

func TestDashboardSourceToolResultsMatchGeneratedOutputSchemas(t *testing.T) {
	app := &projectAuthoringFake{
		source:           authoringapplication.SourceRead{DashboardID: "dashboard_sales", DraftID: "draft_1", Revision: dashboardauthoring.RevisionToken{RevisionID: "revision_1", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}, YAML: "version: 1\n"},
		editSourceResult: authoringapplication.SourceEditResult{Result: authoringservice.Result{Revision: dashboardauthoring.RevisionToken{RevisionID: "revision_2", Number: 2, ContentHash: "sha256:" + strings.Repeat("b", 64)}}, YAML: "version: 1\n", Diff: "--- dashboard.yaml\n", ChangedBlocks: 1},
	}
	catalog, err := agentcore.NewToolCatalog((DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest()}).Definitions(Scope{PrincipalID: "principal"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []agentcore.ToolCall{
		{ID: "source-read", Name: ReadDashboardSourceToolName, Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales"}`)},
		{ID: "source-edit", Name: EditDashboardSourceToolName, Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales","draftId":"draft_1","expectedRevision":{"revisionId":"revision_1","number":1,"contentHash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"edits":[{"oldText":"Overview","newText":"Executive overview"}]}`)},
	} {
		result, executeErr := catalog.Execute(t.Context(), call)
		if executeErr != nil || result.IsError {
			t.Fatalf("%s output failed generated schema validation: result=%#v err=%v", call.Name, result, executeErr)
		}
	}
}

func TestCreateDashboardDraftReturnsBoundedReceipt(t *testing.T) {
	app := &projectAuthoringFake{createResult: createResultForTest()}
	catalog, err := agentcore.NewToolCatalog((DashboardAuthoringProvider{
		Application: app,
		ProjectID:   projectIDForTest(),
		Resolve:     (&projectResolverFake{}).Resolve,
	}).Definitions(Scope{PrincipalID: "principal"}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.Execute(t.Context(), agentcore.ToolCall{
		ID:        "create-receipt",
		Name:      CreateDashboardDraftToolName,
		Arguments: json.RawMessage(`{"title":"Sales","semanticModelId":"semantic_sales"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("create_dashboard_draft result=%#v err=%v", result, err)
	}
	receipt, ok := result.Content.(dashboardauthoring.ResourceCreateReceipt)
	if !ok {
		t.Fatalf("create_dashboard_draft content type=%T, want project api receipt", result.Content)
	}
	if receipt.ID != "dashboard_sales" || receipt.Status != "draft" {
		t.Fatalf("create_dashboard_draft receipt=%#v", receipt)
	}
}

func TestCreateDashboardDraftReceiptOmitsLifecycleRevisionAndOwner(t *testing.T) {
	app := &projectAuthoringFake{createResult: createResultForTest()}
	definition := definitionByName((DashboardAuthoringProvider{
		Application: app,
		ProjectID:   projectIDForTest(),
		Resolve:     (&projectResolverFake{}).Resolve,
	}).Definitions(Scope{PrincipalID: "principal"}), CreateDashboardDraftToolName)
	var schema struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(definition.OutputSchema, &schema); err != nil {
		t.Fatalf("decode create_dashboard_draft output schema: %v", err)
	}
	if schema.AdditionalProperties || len(schema.Properties) != 2 {
		t.Fatalf("create_dashboard_draft output schema=%s, want closed id/status object", definition.OutputSchema)
	}
	for _, field := range []string{"id", "status"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("create_dashboard_draft output schema missing %q: %s", field, definition.OutputSchema)
		}
	}
	result, err := definition.Handler.Run(t.Context(), agentcore.ToolCall{
		ID:        "create-receipt-shape",
		Arguments: json.RawMessage(`{"title":"Sales","semanticModelId":"semantic_sales"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("create_dashboard_draft result=%#v err=%v", result, err)
	}
	payload, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("create_dashboard_draft receipt fields=%v, want only id and status", fields)
	}
	for _, field := range []string{"id", "status"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("create_dashboard_draft receipt missing %q: %s", field, payload)
		}
	}
	for _, forbidden := range []string{"lifecycle", "revision", "ownerPrincipalId", "projectId", "draft", "published"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("create_dashboard_draft receipt leaked %q: %s", forbidden, payload)
		}
	}
}
