package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestDataExplorerSemanticDatasetDeepLinksHydrateDistinctModelBindings(t *testing.T) {
	const projectID = "project:test"
	const semanticModelID = "semantic:sales"
	const modelID = "model:orders"
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":        {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
			"order_history": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":        {Model: "orders"},
			"order_history": {Model: "orders"},
		},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	executor := &browserDataQueryStub{}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: modelID, ProjectID: projectID, ServingStateID: "state", Type: "model", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: semanticModelID, ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID:             projectID,
			Models:         map[string]semanticmodel.Table{modelID: {ModelName: "orders"}},
			SemanticModels: map[string]*semanticmodel.Model{semanticModelID: model},
			NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": modelID}},
		}, compiled: map[string]*semanticquery.CompiledModel{semanticModelID: compiled}},
		QueryExecutor: executor,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectID, nil
		},
		Environment: "dev",
		CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}

	for _, datasetID := range []string{"orders", "order_history"} {
		query := url.Values{
			"v":             {"1"},
			"mode":          {"explore"},
			"semanticModel": {semanticModelID},
			"dataset":       {datasetID},
			"dimension":     {datasetID + ".status"},
		}
		recorder := httptest.NewRecorder()
		_, explorer, ok := h.dataExplorerSignals(recorder, httptest.NewRequest(stdhttp.MethodGet, "/explore?"+query.Encode(), nil))
		if !ok {
			t.Fatalf("dataset %q deep link failed: status=%d body=%s", datasetID, recorder.Code, recorder.Body.String())
		}
		if len(explorer.Objects) != 2 {
			t.Fatalf("dataset %q objects = %#v, want both alias bindings", datasetID, explorer.Objects)
		}
		if explorer.SelectedObject == nil {
			t.Fatalf("dataset %q selected object is nil", datasetID)
		}
		selected := explorer.SelectedObject
		wantKey := explorerModelObjectKey(modelID, semanticModelID, datasetID)
		if selected.Key != wantKey || projectsignals.ValueOrZero(selected.DatasetID) != datasetID || selected.ResourceID != modelID {
			t.Fatalf("dataset %q selected object = %#v, want distinct binding key, alias, and backing Model", datasetID, selected)
		}
		if projectsignals.ValueOrZero(explorer.SelectedKey) != wantKey || projectsignals.ValueOrZero(explorer.Command.ObjectKey) != wantKey {
			t.Fatalf("dataset %q selection state = %#v/%#v, want hydrated binding key", datasetID, explorer.SelectedKey, explorer.Command.ObjectKey)
		}
		if projectsignals.ValueOrZero(explorer.Explore.Command.SemanticModelID) != semanticModelID || projectsignals.ValueOrZero(explorer.Explore.Command.DatasetID) != datasetID {
			t.Fatalf("dataset %q explore command = %#v, want canonical semantic target", datasetID, explorer.Explore.Command)
		}
		if executor.query.ModelID != semanticModelID || executor.query.Target != datasetID {
			t.Fatalf("dataset %q governed query = %#v, want semantic model and alias target", datasetID, executor.query)
		}
	}
}

func TestDataExploreCommandFromQueryRoundTripsDurableState(t *testing.T) {
	values, err := url.ParseQuery("v=1&mode=explore&semanticModel=semantic%3Asales&dataset=orders&dimension=orders.month&dimension=customers.state&metric=revenue&filter=%7B%22field%22%3A%22customers.state%22%2C%22operator%22%3A%22equals%22%2C%22values%22%3A%5B%22CA%22%5D%7D&sort=%7B%22field%22%3A%22revenue%22%2C%22direction%22%3A%22desc%22%7D&time=%7B%22field%22%3A%22orders.created_at%22%2C%22grain%22%3A%22month%22%7D&limit=250")
	if err != nil {
		t.Fatal(err)
	}
	command, err := dataExploreCommandFromQuery(values)
	if err != nil {
		t.Fatal(err)
	}
	if projectsignals.ValueOrZero(command.SemanticModelID) != "semantic:sales" || projectsignals.ValueOrZero(command.DatasetID) != "orders" {
		t.Fatalf("target = %q/%q", projectsignals.ValueOrZero(command.SemanticModelID), projectsignals.ValueOrZero(command.DatasetID))
	}
	if !reflect.DeepEqual(command.Dimensions, []string{"orders.month", "customers.state"}) || !reflect.DeepEqual(command.Metrics, []string{"revenue"}) {
		t.Fatalf("fields = %#v / %#v", command.Dimensions, command.Metrics)
	}
	if len(command.Filters) != 1 || command.Filters[0].Field != "customers.state" || command.Filters[0].Values[0] != "CA" {
		t.Fatalf("filters = %#v", command.Filters)
	}
	if len(command.Sort) != 1 || command.Sort[0].Field != "revenue" || command.Sort[0].Direction != "desc" {
		t.Fatalf("sort = %#v", command.Sort)
	}
	if command.Time == nil || command.Time.Field != "orders.created_at" || command.Time.Grain != "month" || command.Limit != 250 {
		t.Fatalf("time/limit = %#v / %d", command.Time, command.Limit)
	}
	if command.RequestSeq != 0 || command.ResetVersion != 0 {
		t.Fatalf("runtime state leaked into URL command: %#v", command)
	}
}
