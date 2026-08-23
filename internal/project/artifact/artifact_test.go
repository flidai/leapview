package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func projectFixture(t *testing.T) (projectgraph.ProjectGraph, manifest.Project) {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "pipeline:sales", Kind: projectgraph.KindPipeline, Name: "sales_refresh"},
		{ID: "dashboard:sales", Kind: projectgraph.KindDashboard, Name: "sales_dashboard"},
	}, []projectgraph.Edge{
		{From: "source:orders", To: "connection:warehouse"},
		{From: "model:orders", To: "source:orders"},
		{From: "semantic:sales", To: "model:orders"},
		{From: "pipeline:sales", To: "semantic:sales"},
		{From: "dashboard:sales", To: "semantic:sales"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return graphValue, manifest.Project{
		ID: "project:demo", Name: "demo", Title: "Demo",
		Connections: map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"source:orders": {Connection: "connection:warehouse"},
		},
		Models: map[string]semanticmodel.Table{
			"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}, Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeString}}},
		},
		SemanticModels: map[string]*semanticmodel.Model{
			"semantic:sales": {Name: "sales", Sources: map[string]semanticmodel.Source{"orders": {}}, Tables: map[string]semanticmodel.Table{"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeString}}}}},
		},
		DashboardDefinitions: map[string]dashboarddefinition.Definition{
			"dashboard:sales": {ID: "dashboard:sales", SemanticModel: "semantic:sales"},
		},
		RefreshPipelines: map[string]refreshschedule.Definition{
			"pipeline:sales": {ID: "pipeline:sales", Name: "sales_refresh", SemanticModelID: "semantic:sales"},
		},
		NameIndex: manifest.NameIndex{
			Connections:    map[string]string{"warehouse": "connection:warehouse"},
			Sources:        map[string]string{"orders": "source:orders"},
			Models:         map[string]string{"orders_model": "model:orders"},
			SemanticModels: map[string]string{"sales": "semantic:sales"},
			Dashboards:     map[string]string{"sales": "dashboard:sales"},
			Pipelines:      map[string]string{"sales_refresh": "pipeline:sales"},
		},
		DashboardSources: map[string]manifest.DashboardSource{
			"dashboard:sales": {Document: document.DashboardDocument{APIVersion: "leapview.dev/v1", Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "dashboard:sales", Name: "sales_dashboard"}, Spec: document.DashboardSpec{SemanticModel: "semantic:sales"}}, Path: "dashboards/sales.yaml"},
		},
		ResourceFiles: map[string]string{
			"project:demo":         "leapview.yaml",
			"connection:warehouse": "connections/warehouse.yaml",
			"source:orders":        "sources/orders.yaml",
			"model:orders":         "models/orders.yaml",
			"semantic:sales":       "semantic-models/sales.yaml",
			"pipeline:sales":       "pipelines/sales.yaml",
			"dashboard:sales":      "dashboards/sales.yaml",
		},
	}
}

func TestProjectIsDeterministicAndProjectWide(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	first, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "sqlite"}
	second, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == second.Digest() {
		t.Fatal("manifest mutation did not change project artifact digest")
	}
	decoded, err := Decode(first.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectID() != graphValue.ProjectID() || decoded.Graph().Digest() != graphValue.Digest() {
		t.Fatalf("project identity = (%q, %q), want (%q, %q)", decoded.ProjectID(), decoded.Graph().Digest(), graphValue.ProjectID(), graphValue.Digest())
	}
	models := decoded.Models()
	model, ok := models["semantic:sales"]
	if !ok {
		t.Fatal("semantic model projection missing")
	}
	if _, ok := model.Sources["orders"]; !ok {
		t.Fatalf("semantic runtime symbolic ref was rewritten: %#v", model.Sources)
	}
	if got := model.Tables["orders"].Dimensions["order_id"].Datatype; got != semanticmodel.DataTypeString {
		t.Fatalf("semantic logical datatype = %q, want %q after artifact round trip", got, semanticmodel.DataTypeString)
	}
	if got := decoded.ModelTables()["model:orders"].Dimensions["order_id"].Datatype; got != semanticmodel.DataTypeString {
		t.Fatalf("model logical datatype = %q, want %q after artifact round trip", got, semanticmodel.DataTypeString)
	}
	if got := decoded.Manifest().NameIndex.SemanticModels["sales"]; got != "semantic:sales" {
		t.Fatalf("name index semantic model = %q, want semantic:sales", got)
	}
	if got := decoded.RefreshDefinition().ConnectionIDs["warehouse"]; got != "connection:warehouse" {
		t.Fatalf("refresh connection ID = %q, want connection:warehouse", got)
	}
	refreshTable, ok := decoded.RefreshDefinition().ModelTables["orders_model"]
	if !ok {
		t.Fatal("refresh projection dropped project Model catalog")
	}
	if refreshTable.ModelName != "orders_model" || !reflect.DeepEqual(refreshTable.SourceDependencies, []string{"orders"}) {
		t.Fatalf("refresh Model table = %#v, want authored name with runtime source dependencies", refreshTable)
	}
	var wire map[string]any
	if err := json.Unmarshal(first.Canonical(), &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["workspaces"]; ok {
		t.Fatalf("project artifact retained workspace key: %#v", wire)
	}
	if _, ok := wire["identity"]; ok {
		t.Fatalf("project artifact retained serving identity: %#v", wire)
	}
}

func TestProjectRejectsRawConnectionSourceFromCompiledManifest(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.AuthoredResourceSources = map[string]string{
		"connection:warehouse": "kind: Connection\nspec:\n  credentials: leaked\n",
	}
	_, err := NewProject(graphValue, projectManifest)
	if err == nil || !strings.Contains(err.Error(), "forbidden graph kind") {
		t.Fatalf("NewProject() error = %v, want raw connection source rejection", err)
	}
}

func TestConnectionActivationCarriesCanonicalAccessPolicy(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed", Access: semanticmodel.ConnectionAccessPublic}
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatalf("NewProject() public connection: %v", err)
	}
	activations, err := project.ConnectionActivations()
	if err != nil {
		t.Fatalf("ConnectionActivations(): %v", err)
	}
	if len(activations) != 1 || activations[0].Access != semanticmodel.ConnectionAccessPublic {
		t.Fatalf("activation access = %#v, want public", activations)
	}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed"}
	omitted, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatalf("NewProject() omitted connection: %v", err)
	}
	omittedActivations, err := omitted.ConnectionActivations()
	if err != nil {
		t.Fatalf("omitted ConnectionActivations(): %v", err)
	}
	if activations[0].Access == omittedActivations[0].Access {
		t.Fatal("public and omitted activation access collapsed")
	}
}

func TestProjectArtifactRoundTripPreservesLoweredSemanticModelBinding(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.SemanticModels["semantic:sales"] = &semanticmodel.Model{
		Name: "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"sales_orders": {Model: "orders_model"},
		},
		Tables: map[string]semanticmodel.Table{
			"sales_orders": {ModelName: "orders_model", Execution: semanticmodel.ExecutionDefinition{Source: "orders_model"}},
		},
	}
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(project.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	model := decoded.Models()["semantic:sales"]
	if got := model.Tables["sales_orders"].ModelName; got != "orders_model" {
		t.Fatalf("lowered ModelName = %q, want orders_model after artifact round trip", got)
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatalf("CompileDatasetBindings() after artifact round trip: %v", err)
	}
	if dataset, ok := compiled.Dataset("sales_orders"); !ok || dataset.ModelName() != "orders_model" {
		t.Fatalf("compiled dataset = %#v, ok=%v, want sales_orders bound to orders_model", dataset, ok)
	}
}

func TestProjectArtifactRoundTripPreservesPrivateRuntimeProjection(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	header := true
	pathLocation := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "orders.csv", Format: "csv"},
		Format:                 "csv",
		Options:                &projectcontracts.CSVReaderOptions{Header: &header},
	}}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed"}
	projectManifest.Sources["source:orders"] = semanticmodel.Source{Connection: "connection:warehouse", Format: "csv", Path: "orders.csv", PathLocation: pathLocation, EffectivePathLocation: pathLocation}
	projectManifest.Models["model:orders"] = semanticmodel.Table{Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}}
	projectManifest.AuthoredModelDefinitions = map[string]manifest.AuthoredModelDefinition{
		"model:orders": {Type: "sql", SQL: `SELECT * FROM source."orders"`},
	}
	model := projectManifest.SemanticModels["semantic:sales"]
	model.DefaultConnection = "warehouse"
	model.Connections = map[string]semanticmodel.Connection{"warehouse": projectManifest.Connections["connection:warehouse"]}
	model.Sources = map[string]semanticmodel.Source{"orders": {Connection: "warehouse", Format: "csv", Path: "orders.csv", PathLocation: pathLocation, EffectivePathLocation: pathLocation}}
	minimum, maximum := int64(1), int64(9)
	model.Tables = map[string]semanticmodel.Table{
		"orders":     {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, SQLAnalysisEvidence: &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: []string{"orders"}}, Checks: []semanticmodel.ModelCheck{{Fields: []string{"order_id"}, Minimum: &minimum, Maximum: &maximum}}, SourceDependencies: []string{"orders"}},
		"sql_orders": {Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT * FROM orders"}},
	}
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(project.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(project.Canonical(), decoded.Canonical()) || project.Digest() != decoded.Digest() {
		t.Fatalf("artifact canonical representation changed across round trip")
	}
	var malformed map[string]any
	if err := json.Unmarshal(project.Canonical(), &malformed); err != nil {
		t.Fatal(err)
	}
	runtimeWire := malformed["runtime"].(map[string]any)
	semanticModelsWire := runtimeWire["semanticModels"].(map[string]any)
	semanticWire := semanticModelsWire["semantic:sales"].(map[string]any)
	sourcesWire := semanticWire["sources"].(map[string]any)
	delete(sourcesWire["orders"].(map[string]any), "effectivePathLocation")
	malformedBytes, err := json.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(malformedBytes); err == nil || !strings.Contains(err.Error(), "path source requires") {
		t.Fatalf("Decode() error = %v, want missing typed path location rejection", err)
	}
	model = decoded.Models()["semantic:sales"]
	if model == nil || model.DefaultConnection != "warehouse" || model.Sources["orders"].PathLocation == nil || model.Sources["orders"].EffectivePathLocation == nil {
		t.Fatalf("runtime source projection was not restored: %#v", model)
	}
	if got := model.Tables["orders"].Execution; got.Source != "orders" || model.Tables["sql_orders"].Execution.SQL != "SELECT * FROM orders" {
		t.Fatalf("runtime table execution projection was not restored: %#v", model.Tables)
	}
	connection := model.Connections["warehouse"]
	if connection.Kind != "managed" || connection.Path != "" || connection.Host != "" || connection.Auth != nil || connection.Credentials != (semanticmodel.ConnectionCredentials{}) {
		t.Fatalf("runtime model connection changed: %#v", connection)
	}
	table := decoded.ModelTables()["model:orders"]
	if table.Execution.Source != "source:orders" {
		t.Fatalf("physical table execution projection was not restored: %#v", table.Execution)
	}
	refreshTable := decoded.RefreshDefinition().ModelTables["orders_model"]
	if refreshTable.Execution.Source != "orders" || !reflect.DeepEqual(refreshTable.SourceDependencies, []string{"orders"}) {
		t.Fatalf("refresh Model execution projection was not restored: %#v", refreshTable)
	}
	manifestCopy := decoded.Manifest()
	if manifestCopy.Models["model:orders"].Execution.Source != "source:orders" || manifestCopy.SemanticModels["semantic:sales"].Sources["orders"].PathLocation == nil {
		t.Fatalf("manifest accessor dropped private runtime projection: %#v", manifestCopy)
	}
	if got := manifestCopy.AuthoredModelDefinitions["model:orders"].SQL; got != `SELECT * FROM source."orders"` {
		t.Fatalf("manifest accessor dropped authored model SQL: %q", got)
	}
	manifestTable := manifestCopy.Models["model:orders"]
	manifestTable.Execution.Source = "changed"
	manifestCopy.Models["model:orders"] = manifestTable
	if decoded.Manifest().Models["model:orders"].Execution.Source != "source:orders" {
		t.Fatal("manifest accessor aliases retained runtime state")
	}
	encoded, err := json.Marshal(manifestCopy)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "target.example", "/target/path", "SELECT * FROM orders"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("public manifest leaked %q: %s", forbidden, encoded)
		}
	}
	mutatedTable := model.Tables["orders"]
	mutatedTable.Execution.Source = "changed"
	model.Tables["orders"] = mutatedTable
	if decoded.Models()["semantic:sales"].Tables["orders"].Execution.Source != "orders" {
		t.Fatal("artifact runtime model accessor aliases a previous clone")
	}
}

func TestProjectArtifactRejectsTargetConnectionState(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed", Host: "target.example"}
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), "target-owned state") {
		t.Fatalf("NewProject() error = %v, want target-owned connection rejection", err)
	}
}

func TestProjectArtifactRejectsInvalidRuntimePathUnion(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	model := projectManifest.SemanticModels["semantic:sales"]
	model.Sources = map[string]semanticmodel.Source{"orders": {PathLocation: &projectcontracts.PathSourceLocation{Value: (*projectcontracts.CSVPathSourceLocation)(nil)}}}
	if _, err := NewProject(graphValue, projectManifest); err == nil {
		t.Fatal("invalid runtime path union unexpectedly accepted")
	}
}

func TestProjectArtifactRejectsMalformedV2RuntimePayload(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	base := func() map[string]any {
		var wire map[string]any
		if err := json.Unmarshal(project.Canonical(), &wire); err != nil {
			t.Fatal(err)
		}
		return wire
	}
	decode := func(wire map[string]any) error {
		data, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Decode(data)
		return err
	}
	t.Run("version one", func(t *testing.T) {
		wire := base()
		wire["version"] = float64(1)
		var unsupported UnsupportedVersionError
		if err := decode(wire); !errors.As(err, &unsupported) {
			t.Fatalf("Decode() error = %v, want unsupported v1", err)
		}
	})
	t.Run("missing runtime key", func(t *testing.T) {
		wire := base()
		runtime := wire["runtime"].(map[string]any)
		delete(runtime, "sources")
		if err := decode(wire); err == nil || !strings.Contains(err.Error(), "runtime projection") {
			t.Fatalf("Decode() error = %v, want missing runtime projection", err)
		}
	})
	t.Run("extra runtime key", func(t *testing.T) {
		wire := base()
		runtime := wire["runtime"].(map[string]any)
		sources := runtime["sources"].(map[string]any)
		sources["unexpected"] = map[string]any{}
		if err := decode(wire); err == nil || !strings.Contains(err.Error(), "key set") {
			t.Fatalf("Decode() error = %v, want extra runtime key rejection", err)
		}
	})
	t.Run("zero execution", func(t *testing.T) {
		wire := base()
		runtime := wire["runtime"].(map[string]any)
		models := runtime["models"].(map[string]any)
		models["model:orders"] = map[string]any{}
		if err := decode(wire); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("Decode() error = %v, want zero execution rejection", err)
		}
	})
	t.Run("both execution", func(t *testing.T) {
		wire := base()
		runtime := wire["runtime"].(map[string]any)
		models := runtime["models"].(map[string]any)
		models["model:orders"] = map[string]any{"Source": "source:orders", "SQL": "SELECT 1"}
		if err := decode(wire); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("Decode() error = %v, want both execution rejection", err)
		}
	})
	t.Run("target connection state", func(t *testing.T) {
		wire := base()
		manifest := wire["manifest"].(map[string]any)
		connections := manifest["connections"].(map[string]any)
		connection := connections["connection:warehouse"].(map[string]any)
		connection["Path"] = "/target/path"
		if err := decode(wire); err == nil || !strings.Contains(err.Error(), "target-owned state") {
			t.Fatalf("Decode() error = %v, want target connection rejection", err)
		}
	})
}

func TestProjectAcceptsCompleteGraphManifest(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if project.ProjectID() != "project:demo" || len(project.Graph().Resources()) != 7 {
		t.Fatalf("project = (%q, %d resources), want complete project graph", project.ProjectID(), len(project.Graph().Resources()))
	}
}

func TestProjectRejectsManifestSemanticModelMissingFromGraph(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	delete(projectManifest.SemanticModels, "semantic:sales")
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), `graph resource "semantic:sales" (semantic_model) is absent from manifest semanticModels`) {
		t.Fatalf("NewProject() error = %v, want deterministic missing semantic model diagnostic", err)
	}
}

func TestProjectRejectsManifestSemanticModelWrongGraphKind(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.SemanticModels["connection:warehouse"] = projectManifest.SemanticModels["semantic:sales"]
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), `manifest semanticModels key "connection:warehouse" resolves to graph kind "connection", want "semantic_model"`) {
		t.Fatalf("NewProject() error = %v, want deterministic wrong-kind diagnostic", err)
	}
}

func TestProjectRejectsDanglingSourceConnectionReference(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	source := projectManifest.Sources["source:orders"]
	source.Connection = "connection:missing"
	projectManifest.Sources["source:orders"] = source
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), `manifest source "source:orders" connection reference "connection:missing" is missing from graph`) {
		t.Fatalf("NewProject() error = %v, want dangling source connection diagnostic", err)
	}
}

func TestProjectRejectsWrongKindModelDependency(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	model := projectManifest.Models["model:orders"]
	model.ModelDependencies = []string{"semantic:sales"}
	projectManifest.Models["model:orders"] = model
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), `manifest model "model:orders" model dependency reference "semantic:sales" resolves to graph kind "semantic_model", want "model"`) {
		t.Fatalf("NewProject() error = %v, want wrong-kind model dependency diagnostic", err)
	}
}

func TestProjectRejectsDashboardIdentityAndSemanticReferenceDrift(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	definition := projectManifest.DashboardDefinitions["dashboard:sales"]
	definition.SemanticModel = "semantic:missing"
	projectManifest.DashboardDefinitions["dashboard:sales"] = definition
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), `manifest dashboard "dashboard:sales" semantic model reference "semantic:missing" is missing from graph`) {
		t.Fatalf("NewProject() error = %v, want dangling dashboard semantic model diagnostic", err)
	}

	_, projectManifest = projectFixture(t)
	source := projectManifest.DashboardSources["dashboard:sales"]
	source.Document.Metadata.ID = "dashboard:other"
	projectManifest.DashboardSources["dashboard:sales"] = source
	if _, err := NewProject(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), `manifest dashboardSources key "dashboard:sales" does not match document id "dashboard:other"`) {
		t.Fatalf("NewProject() error = %v, want dashboard identity diagnostic", err)
	}
}

func TestProjectDefensivelyCopiesManifestProjections(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "mutated"}
	connections := project.Connections()
	connections["connection:warehouse"] = semanticmodel.Connection{Kind: "mutated"}
	if got := project.Connections()["connection:warehouse"].Kind; got != "managed" {
		t.Fatalf("connection projection leaked mutation: %q", got)
	}
	source, ok := project.AuthoredDashboardSource("dashboard:sales")
	if !ok {
		t.Fatal("authored dashboard source missing")
	}
	source.Path = "mutated.yaml"
	if got, _ := project.AuthoredDashboardSource("dashboard:sales"); got.Path != "dashboards/sales.yaml" {
		t.Fatal("authored source projection leaked mutation")
	}
}

func TestProjectRejectsIdentityMismatch(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.ID = "project:other"
	if _, err := NewProject(graphValue, projectManifest); !errors.Is(err, projectgraph.ErrProjectIdentityMismatch) {
		t.Fatalf("NewProject() error = %v, want identity mismatch", err)
	}
}

func TestDecodeRejectsVersionUnknownDuplicateAndIdentity(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data string
		want func(error) bool
	}{
		{name: "version", data: `{"version":99}`, want: func(err error) bool { var unsupported UnsupportedVersionError; return errors.As(err, &unsupported) }},
		{name: "unknown", data: strings.Replace(string(project.Canonical()), `{"version":2,`, `{"unknown":true,"version":2,`, 1), want: func(err error) bool { return strings.Contains(err.Error(), "unknown field") }},
		{name: "duplicate case", data: strings.Replace(string(project.Canonical()), `{"version":2,`, `{"VERSION":2,"version":2,`, 1), want: func(err error) bool { return strings.Contains(err.Error(), "duplicate JSON field") }},
		{name: "trailing", data: string(project.Canonical()) + ` {"trailing":true}`, want: func(err error) bool { return strings.Contains(err.Error(), "trailing") }},
		{name: "identity", data: replaceManifestID(string(project.Canonical()), "project:other"), want: func(err error) bool { return errors.Is(err, projectgraph.ErrProjectIdentityMismatch) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.data))
			if err == nil || !test.want(err) {
				t.Fatalf("Decode() error = %v", err)
			}
		})
	}
}

func replaceManifestID(value, replacement string) string {
	var wire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &wire); err != nil {
		return value
	}
	var project map[string]any
	if err := json.Unmarshal(wire["manifest"], &project); err != nil {
		return value
	}
	project["id"] = replacement
	manifest, err := json.Marshal(project)
	if err != nil {
		return value
	}
	wire["manifest"] = manifest
	result, err := json.Marshal(wire)
	if err != nil {
		return value
	}
	return string(result)
}

func TestProjectRoundTripRetainsAuthoredSourceProvenance(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(project.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	source, ok := decoded.AuthoredDashboardSource("dashboard:sales")
	if !ok || source.Path != "dashboards/sales.yaml" || source.Document.Metadata.ID != "dashboard:sales" {
		t.Fatalf("source = %#v, present = %v", source, ok)
	}
}

func TestCloneValueDoesNotSilentlyReturnZeroOnEncodingFailure(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("cloneValue() did not report an impossible encoding failure")
		}
	}()
	_ = cloneValue(func() {})
}
