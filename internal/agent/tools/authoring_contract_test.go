package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
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
