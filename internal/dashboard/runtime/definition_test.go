package runtime

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestNewProjectDefinitionCopiesInputsAndAllowsEmptyDashboards(t *testing.T) {
	projectID := projectgraph.ResourceID("project_1")
	modelID := projectgraph.ResourceID("model_1")
	model := &semanticmodel.Model{Name: "display-name"}
	models := map[projectgraph.ResourceID]*semanticmodel.Model{modelID: model}
	dashboards := map[projectgraph.ResourceID]dashboarddefinition.Definition{"dashboard_1": {ID: "dashboard_1", SemanticModel: "model_1", Pages: []dashboard.Page{{ID: "overview"}}}}
	definition, err := NewProjectDefinition(projectID, "Title", "Description", models, dashboards)
	if err != nil {
		t.Fatal(err)
	}
	model.Name = "mutated"
	delete(models, modelID)
	dashboards["dashboard_1"].Pages[0].ID = "mutated"
	if got := definition.Models()[modelID].Name; got != "display-name" {
		t.Fatalf("model mutation leaked into immutable definition: %q", got)
	}
	if got := definition.Dashboards()["dashboard_1"].Pages[0].ID; got != "overview" {
		t.Fatalf("dashboard mutation leaked into immutable definition: %q", got)
	}
}

func TestProjectDefinitionRuntimeModelCloneRetainsExecutionAndRedactsTargetState(t *testing.T) {
	header := true
	nullable := true
	columnNullable := false
	delimiter := ";"
	revisionAt := time.Unix(123, 0).UTC()
	pathLocation := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "orders.csv", Format: "csv"},
		Options:                &projectcontracts.CSVReaderOptions{Header: &header, Delimiter: &delimiter},
	}}
	readerDefaults := &projectcontracts.ReaderDefaults{Csv: &projectcontracts.CSVReaderOptions{Header: &header, Delimiter: &delimiter}}
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"warehouse": {
				Kind: "managed", Access: semanticmodel.ConnectionAccessPublic, Description: "portable",
				Path: "/target/path", Root: "/target/root", Scope: "target-scope", Host: "target.example",
				Port: 5432, Database: "target-db", Username: "target-user", SSLMode: "require",
				Credentials:    semanticmodel.ConnectionCredentials{Provider: "target", Secret: "secret", Endpoint: "https://secret.example"},
				RuntimeOptions: semanticmodel.ConnectionRuntimeOptions{Path: "/runtime/path", DataPath: "/runtime/data"},
				Auth:           semanticmodel.ConnectionAuth{"token": "secret"},
				ReaderDefaults: readerDefaults,
			},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {
				Connection: "warehouse", Format: "csv", Path: "orders.csv", PathLocation: pathLocation,
				EffectivePathLocation: pathLocation,
				Fields:                map[string]semanticmodel.SourceField{"order_id": {Name: "order_id", Nullable: &nullable}},
				Freshness: &semanticmodel.SourceFreshnessSpec{
					Basis: "field", Field: "updated_at", RevisionAt: &revisionAt,
					WarningAfter: &semanticmodel.FreshnessDurationSpec{Amount: 5, Unit: "minute"},
					ErrorAfter:   &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "hour"},
				},
				Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "order_id", Nullable: &columnNullable}}},
			},
		},
		Tables: map[string]semanticmodel.Table{
			"orders":     {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}},
			"sql_orders": {Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT * FROM orders"}},
		},
	}
	definition, err := NewProjectDefinition("project_1", "", "", map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": model}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned := definition.Models()["model_1"]
	if cloned == nil {
		t.Fatal("runtime model clone is nil")
	}
	connection := cloned.Connections["warehouse"]
	if connection.Auth != nil || connection.Credentials != (semanticmodel.ConnectionCredentials{}) || connection.Host != "" || connection.Port != 0 || connection.Database != "" || connection.Username != "" || connection.SSLMode != "" || connection.RuntimeOptions != (semanticmodel.ConnectionRuntimeOptions{}) || connection.Path != "" || connection.Root != "" || connection.Scope != "" {
		t.Fatalf("runtime clone retained target connection state: %#v", connection)
	}
	if connection.ReaderDefaults == nil || connection.ReaderDefaults.Csv == nil || connection.ReaderDefaults.Csv.Header == nil || *connection.ReaderDefaults.Csv.Header != header {
		t.Fatalf("runtime clone lost portable reader defaults: %#v", connection.ReaderDefaults)
	}
	source := cloned.Sources["orders"]
	if source.PathLocation == nil || source.EffectivePathLocation == nil || source.Fields["order_id"].Nullable == nil || source.Freshness == nil || source.Schema.Columns[0].Nullable == nil {
		t.Fatal("runtime clone lost typed source state")
	}
	if got := cloned.Tables["orders"].Execution; got.Source != "orders" || got.SQL != "" {
		t.Fatalf("runtime clone lost table execution definition: %#v", got)
	}
	if got := cloned.Tables["sql_orders"].Execution; got.Source != "" || got.SQL != "SELECT * FROM orders" {
		t.Fatalf("runtime clone lost SQL execution definition: %#v", got)
	}
	encoded, err := json.Marshal(cloned)
	if err != nil {
		t.Fatalf("marshal runtime clone: %v", err)
	}
	for _, forbidden := range []string{"secret", "target.example", "/target/path", "SELECT * FROM orders"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("runtime clone JSON leaked %q: %s", forbidden, encoded)
		}
	}
	// Every retained pointer-bearing nested object must be detached from the
	// authored model, while target-owned connection state remains absent.
	*connection.ReaderDefaults.Csv.Header = false
	*source.Fields["order_id"].Nullable = false
	*source.Schema.Columns[0].Nullable = true
	*source.Freshness.RevisionAt = time.Unix(999, 0).UTC()
	*source.Freshness.WarningAfter = semanticmodel.FreshnessDurationSpec{Amount: 99, Unit: "day"}
	*source.PathLocation.Value.(*projectcontracts.CSVPathSourceLocation).Options.Header = false
	if *readerDefaults.Csv.Header != header || *pathLocation.Value.(*projectcontracts.CSVPathSourceLocation).Options.Header != header || *nullable != true || *model.Sources["orders"].Schema.Columns[0].Nullable != columnNullable || model.Sources["orders"].Freshness.RevisionAt.Equal(*source.Freshness.RevisionAt) || model.Sources["orders"].Freshness.WarningAfter.Amount == 99 {
		t.Fatal("runtime clone aliases authored nested source state")
	}
}

func TestProjectDefinitionRuntimeModelClonePropagatesInvalidPathUnion(t *testing.T) {
	model := &semanticmodel.Model{Sources: map[string]semanticmodel.Source{
		"broken": {PathLocation: &projectcontracts.PathSourceLocation{Value: (*projectcontracts.CSVPathSourceLocation)(nil)}},
	}}
	if _, err := NewProjectDefinition("project_1", "", "", map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": model}, nil); err == nil {
		t.Fatal("invalid generated path union unexpectedly accepted")
	}
}

func TestNewProjectDefinitionRejectsResourceIdentityMismatches(t *testing.T) {
	model := &semanticmodel.Model{Name: "display"}
	models := map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": model}
	if _, err := NewProjectDefinition("project_1", "", "", models, map[projectgraph.ResourceID]dashboarddefinition.Definition{
		"dashboard_1": {ID: "dashboard_1", SemanticModel: "missing_model"},
	}); err == nil {
		t.Fatal("unknown semantic model unexpectedly accepted")
	}
	if _, err := NewProjectDefinition("project_1", "", "", models, map[projectgraph.ResourceID]dashboarddefinition.Definition{
		"dashboard_1": {ID: "other_dashboard", SemanticModel: "model_1"},
	}); err == nil {
		t.Fatal("mismatched dashboard identity unexpectedly accepted")
	}
	if _, err := NewProjectDefinition("other_project", "", "", models, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDefinitionResourceIDsAreCanonicalOrder(t *testing.T) {
	models := map[projectgraph.ResourceID]*semanticmodel.Model{
		"model_z": {Name: "z"},
		"model_a": {Name: "a"},
	}
	dashboards := map[projectgraph.ResourceID]dashboarddefinition.Definition{
		"dashboard_z": {ID: "dashboard_z", SemanticModel: "model_a"},
		"dashboard_a": {ID: "dashboard_a", SemanticModel: "model_z"},
	}
	definition, err := NewProjectDefinition("project_1", "", "", models, dashboards)
	if err != nil {
		t.Fatal(err)
	}
	if got := definition.ModelIDs(); len(got) != 2 || got[0] != "model_a" || got[1] != "model_z" {
		t.Fatalf("model IDs = %v, want canonical order", got)
	}
	if got := definition.DashboardIDs(); len(got) != 2 || got[0] != "dashboard_a" || got[1] != "dashboard_z" {
		t.Fatalf("dashboard IDs = %v, want canonical order", got)
	}
}

func TestNewFromGenerationRejectsWrongProjectIdentity(t *testing.T) {
	definition, err := NewProjectDefinition("project_1", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_2", "production", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromGeneration(t.Context(), "", nil, identity, definition); err == nil {
		t.Fatal("wrong project identity unexpectedly accepted")
	}
}
