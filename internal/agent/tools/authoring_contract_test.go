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
