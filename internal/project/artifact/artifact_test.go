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
	"github.com/flidai/leapview/internal/analytics/sourcedataidentity"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func sourceBundleFixture(t *testing.T) (projectgraph.ProjectGraph, manifest.ResourceManifest) {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
	}, []projectgraph.Edge{
		{From: "source:orders", To: "connection:warehouse"},
		{From: "model:orders", To: "source:orders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return graphValue, manifest.ResourceManifest{
		Connections: map[string]semanticmodel.Connection{
			"connection:warehouse": {Kind: "managed"},
		},
		Sources: map[string]semanticmodel.Source{
			"source:orders": {Connection: "connection:warehouse"},
		},
		Models: map[string]semanticmodel.Table{
			"model:orders": {
				Execution:          semanticmodel.ExecutionDefinition{Source: "source:orders"},
				SourceDependencies: []string{"source:orders"},
			},
		},
	}
}

func fullBundleFixture(t *testing.T) (projectgraph.ProjectGraph, manifest.ResourceManifest) {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
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
	return graphValue, manifest.ResourceManifest{
		Title:       "Demo",
		Connections: map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"source:orders": {Connection: "connection:warehouse"}},
		Models: map[string]semanticmodel.Table{
			"model:orders": {
				Execution:          semanticmodel.ExecutionDefinition{Source: "source:orders"},
				SourceDependencies: []string{"source:orders"},
				Dimensions:         map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeString}},
			},
		},
		SemanticModels: map[string]*semanticmodel.Model{
			"semantic:sales": {
				Name:     "sales",
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders_model"}},
				Sources:  map[string]semanticmodel.Source{"orders": {}},
				Tables: map[string]semanticmodel.Table{
					"orders": {
						Execution:  semanticmodel.ExecutionDefinition{Source: "orders"},
						Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeString}},
					},
				},
			},
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
			Dashboards:     map[string]string{"sales_dashboard": "dashboard:sales"},
			Pipelines:      map[string]string{"sales_refresh": "pipeline:sales"},
		},
		DashboardSources: map[string]manifest.DashboardSource{
			"dashboard:sales": {
				Document: document.DashboardDocument{
					APIVersion: "leapview.dev/v1", Kind: document.DashboardResourceKindDashboard,
					Metadata: document.DashboardMetadata{ID: "dashboard:sales", Name: "sales_dashboard"},
					Spec:     document.DashboardSpec{SemanticModel: "semantic:sales"},
				},
				Path: "dashboards/sales.yaml",
			},
		},
		ResourceFiles: map[string]string{
			"connection:warehouse": "connections/warehouse.yaml",
			"source:orders":        "sources/orders.yaml",
			"model:orders":         "models/orders.yaml",
			"semantic:sales":       "semantic-models/sales.yaml",
			"pipeline:sales":       "pipelines/sales.yaml",
			"dashboard:sales":      "dashboards/sales.yaml",
		},
	}
}

func cloneRelationIdentityManifest(value manifest.ResourceManifest) manifest.ResourceManifest {
	clone := value
	clone.Connections = make(map[string]semanticmodel.Connection, len(value.Connections))
	for id, connection := range value.Connections {
		clone.Connections[id] = connection
	}
	clone.Sources = make(map[string]semanticmodel.Source, len(value.Sources))
	for id, source := range value.Sources {
		fields := make(map[string]semanticmodel.SourceField, len(source.Fields))
		for name, field := range source.Fields {
			fields[name] = field
		}
		source.Fields = fields
		source.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), source.Schema.Columns...)
		clone.Sources[id] = source
	}
	clone.Models = make(map[string]semanticmodel.Table, len(value.Models))
	for id, table := range value.Models {
		columns := make(map[string]semanticmodel.ModelColumn, len(table.Columns))
		for name, column := range table.Columns {
			columns[name] = column
		}
		dimensions := make(map[string]semanticmodel.MetricDimension, len(table.Dimensions))
		for name, dimension := range table.Dimensions {
			dimensions[name] = dimension
		}
		table.Columns = columns
		table.Dimensions = dimensions
		table.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), table.Schema.Columns...)
		clone.Models[id] = table
	}
	return clone
}

func mustSourceDataIdentityEvidence(t *testing.T, project SourceBundle, revisions, bindingKinds map[string]string) map[projectgraph.ResourceID]sourcedataidentity.Evidence {
	t.Helper()
	evidence, err := project.SourceDataIdentityEvidence(revisions, bindingKinds)
	if err != nil {
		t.Fatalf("SourceDataIdentityEvidence() error = %v", err)
	}
	return evidence
}

func TestRelationExecutionDigestsForInputsReuseExactArtifactEvidence(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	projectManifest.SemanticModels["semantic:sales"].Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"orders": {Model: "orders_model"},
	}
	project, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	base, err := project.RelationExecutionDigestsForInputs(
		map[string]string{"connection:warehouse": "revision-a"},
		map[string]string{"connection:warehouse": "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	revisionChanged, err := project.RelationExecutionDigestsForInputs(
		map[string]string{"connection:warehouse": "revision-b"},
		map[string]string{"connection:warehouse": "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if base["model:orders"] == revisionChanged["model:orders"] {
		t.Fatal("managed-data revision did not rotate relation execution identity")
	}
	bindingChanged, err := project.RelationExecutionDigestsForInputs(
		map[string]string{"connection:warehouse": "revision-a"},
		map[string]string{"connection:warehouse": "sqlite"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if base["model:orders"] == bindingChanged["model:orders"] {
		t.Fatal("binding kind did not rotate relation execution identity")
	}

	dashboardOnly := project.Manifest()
	dashboard := dashboardOnly.DashboardDefinitions["dashboard:sales"]
	dashboard.Title = "Presentation-only change"
	dashboardOnly.DashboardDefinitions["dashboard:sales"] = dashboard
	changedProject, err := NewSourceBundle(project.Graph(), dashboardOnly)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := changedProject.RelationExecutionDigestsForInputs(
		map[string]string{"connection:warehouse": "revision-a"},
		map[string]string{"connection:warehouse": "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if base["model:orders"] != unchanged["model:orders"] {
		t.Fatal("dashboard-only change rotated relation execution identity")
	}

	semanticID, _ := projectgraph.NewResourceID("semantic:sales")
	sourceEvidence := mustSourceDataIdentityEvidence(t, project, map[string]string{
		"connection:warehouse": "sha256:" + strings.Repeat("a", 64),
	}, map[string]string{"connection:warehouse": "managed"})
	projection, err := project.SemanticModelRelationEvidence(
		semanticID,
		sourceEvidence,
		map[string]string{"connection:warehouse": "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection) != 1 || projection[0].Dataset != "orders" || projection[0].RelationID != "model:orders" || projection[0].ExecutionDigest == "" {
		t.Fatalf("semantic relation projection = %#v", projection)
	}
	missing, err := project.SemanticModelRelationEvidence(semanticID, nil, map[string]string{"connection:warehouse": "managed"})
	if err != nil || len(missing) != 0 {
		t.Fatalf("SemanticModelRelationEvidence() missing evidence = %#v, %v; want empty fail-closed projection", missing, err)
	}
}

func TestLegacyRelationContextPreservesSourceDependenciesProjection(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	projectManifest.SemanticModels["semantic:sales"].Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"orders": {Model: "orders_model"},
	}
	table := projectManifest.Models["model:orders"]
	table.SourceDependencies = nil
	projectManifest.Models["model:orders"] = table
	project, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}

	contexts, err := project.RelationExecutionContexts(
		map[string]string{"connection:warehouse": "revision-a"},
		map[string]string{"connection:warehouse": "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	const legacyContext = `{"pins":[],"sources":{},"connections":{}}`
	if got := contexts["model:orders"]; got != legacyContext {
		t.Fatalf("legacy relation context = %s, want %s", got, legacyContext)
	}
	digests, err := project.RelationExecutionDigestsForInputs(
		map[string]string{"connection:warehouse": "revision-a"},
		map[string]string{"connection:warehouse": "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantDigests, err := project.RelationExecutionDigestsByContext(map[string]string{"model:orders": legacyContext})
	if err != nil {
		t.Fatal(err)
	}
	if digests["model:orders"] != wantDigests["model:orders"] {
		t.Fatal("legacy relation digest changed when only Execution.Source supplied lineage")
	}

	semanticID, _ := projectgraph.NewResourceID("semantic:sales")
	missing, err := project.SemanticModelRelationEvidence(semanticID, nil, map[string]string{"connection:warehouse": "managed"})
	if err != nil || len(missing) != 0 {
		t.Fatalf("result identity accepted missing direct-source evidence: %#v, %v", missing, err)
	}
	sourceEvidence := mustSourceDataIdentityEvidence(t, project, map[string]string{
		"connection:warehouse": "sha256:" + strings.Repeat("a", 64),
	}, map[string]string{"connection:warehouse": "managed"})
	available, err := project.SemanticModelRelationEvidence(semanticID, sourceEvidence, map[string]string{"connection:warehouse": "managed"})
	if err != nil || len(available) != 1 {
		t.Fatalf("result identity direct-source projection = %#v, %v; want one evidenced relation", available, err)
	}
}

func TestResultIdentitySQLLineageRequiresValidatedCompleteEvidence(t *testing.T) {
	graphValue, baseManifest := fullBundleFixture(t)
	baseManifest.SemanticModels["semantic:sales"].Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"orders": {Model: "orders_model"},
	}
	semanticID, _ := projectgraph.NewResourceID("semantic:sales")

	projectEvidence := func(t *testing.T, table semanticmodel.Table) []DatasetRelationEvidence {
		t.Helper()
		projectManifest := cloneRelationIdentityManifest(baseManifest)
		projectManifest.Models["model:orders"] = table
		project, err := NewSourceBundle(graphValue, projectManifest)
		if err != nil {
			t.Fatal(err)
		}
		sourceEvidence := mustSourceDataIdentityEvidence(t, project, map[string]string{
			"connection:warehouse": "sha256:" + strings.Repeat("a", 64),
		}, map[string]string{"connection:warehouse": "managed"})
		relations, err := project.SemanticModelRelationEvidence(semanticID, sourceEvidence, map[string]string{"connection:warehouse": "managed"})
		if err != nil {
			t.Fatal(err)
		}
		return relations
	}

	table := baseManifest.Models["model:orders"]
	table.Execution = semanticmodel.ExecutionDefinition{SQL: "SELECT * FROM orders"}
	table.SourceDependencies = nil
	table.SQLAnalysisEvidence = nil
	if got := projectEvidence(t, table); len(got) != 0 {
		t.Fatalf("SQL model with missing lineage produced relation evidence: %#v", got)
	}

	table.SourceDependencies = []string{"source:orders"}
	table.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{Validated: false, SourceRefs: []string{"orders"}}
	if got := projectEvidence(t, table); len(got) != 0 {
		t.Fatalf("SQL model with unvalidated lineage produced relation evidence: %#v", got)
	}

	table.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{Validated: true}
	if got := projectEvidence(t, table); len(got) != 0 {
		t.Fatalf("SQL model with empty validated lineage produced relation evidence: %#v", got)
	}

	table.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: []string{"orders"}}
	if got := projectEvidence(t, table); len(got) != 1 {
		t.Fatalf("SQL model with complete managed lineage = %#v, want one relation", got)
	}
}

func TestResultIdentityRelationEvidenceIgnoresPresentationAndRotatesOnExecution(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	projectManifest.SemanticModels["semantic:sales"].Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"orders": {Model: "orders_model"},
	}
	table := projectManifest.Models["model:orders"]
	table.Columns = map[string]semanticmodel.ModelColumn{"order_id": {Name: "order_id", Datatype: semanticmodel.DataTypeString}}
	table.Schema.Columns = []semanticmodel.ColumnSchema{{Name: "order_id", PhysicalType: "VARCHAR"}}
	projectManifest.Models["model:orders"] = table
	source := projectManifest.Sources["source:orders"]
	source.Fields = map[string]semanticmodel.SourceField{"order_id": {Name: "order_id", Datatype: semanticmodel.DataTypeString}}
	source.Schema.Columns = []semanticmodel.ColumnSchema{{Name: "order_id", PhysicalType: "VARCHAR"}}
	projectManifest.Sources["source:orders"] = source

	digestFor := func(value manifest.ResourceManifest) string {
		t.Helper()
		project, err := NewSourceBundle(graphValue, value)
		if err != nil {
			t.Fatal(err)
		}
		semanticID, err := projectgraph.NewResourceID("semantic:sales")
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := project.SemanticModelRelationEvidence(semanticID, mustSourceDataIdentityEvidence(t, project, map[string]string{
			"connection:warehouse": "sha256:" + strings.Repeat("a", 64),
		}, map[string]string{"connection:warehouse": "managed"}), map[string]string{"connection:warehouse": "managed"})
		if err != nil || len(evidence) != 1 {
			t.Fatalf("SemanticModelRelationEvidence() = %#v, %v", evidence, err)
		}
		return evidence[0].ExecutionDigest
	}

	base := digestFor(projectManifest)
	presentation := cloneRelationIdentityManifest(projectManifest)
	connection := presentation.Connections["connection:warehouse"]
	connection.Description = "Warehouse shown to authors"
	presentation.Connections["connection:warehouse"] = connection
	source = presentation.Sources["source:orders"]
	source.Description = "Order source help"
	field := source.Fields["order_id"]
	field.Description = "Order identifier help"
	source.Fields["order_id"] = field
	source.Schema.Columns[0].Comment = "Displayed warehouse comment"
	presentation.Sources["source:orders"] = source
	table = presentation.Models["model:orders"]
	table.Description = "Order model help"
	dimension := table.Dimensions["order_id"]
	dimension.Label = "Order ID"
	dimension.Description = "Displayed dimension help"
	table.Dimensions["order_id"] = dimension
	column := table.Columns["order_id"]
	column.Description = "Displayed column help"
	table.Columns["order_id"] = column
	table.Schema.Columns[0].Comment = "Displayed model comment"
	presentation.Models["model:orders"] = table
	if got := digestFor(presentation); got != base {
		t.Fatalf("presentation-only relation metadata rotated result identity: %q != %q", got, base)
	}

	execution := cloneRelationIdentityManifest(projectManifest)
	table = execution.Models["model:orders"]
	column = table.Columns["order_id"]
	column.Datatype = semanticmodel.DataTypeInteger
	table.Columns["order_id"] = column
	execution.Models["model:orders"] = table
	if got := digestFor(execution); got == base {
		t.Fatal("execution-affecting relation change did not rotate result identity")
	}
}

func TestConnectionActivationCarriesCanonicalAccessPolicy(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed", Access: semanticmodel.ConnectionAccessPublic}
	project, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatalf("NewSourceBundle() public connection: %v", err)
	}
	activations, err := project.ConnectionActivations()
	if err != nil {
		t.Fatalf("ConnectionActivations(): %v", err)
	}
	if len(activations) != 1 || activations[0].Access != semanticmodel.ConnectionAccessPublic {
		t.Fatalf("activation access = %#v, want public", activations)
	}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed"}
	omitted, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatalf("NewSourceBundle() omitted connection: %v", err)
	}
	omittedActivations, err := omitted.ConnectionActivations()
	if err != nil {
		t.Fatalf("omitted ConnectionActivations(): %v", err)
	}
	if activations[0].Access == omittedActivations[0].Access {
		t.Fatal("public and omitted activation access collapsed")
	}
}

func TestSourceDataIdentityEvidenceAdaptsOnlyManagedContentRevisions(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	project, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	first := mustSourceDataIdentityEvidence(t, project, map[string]string{
		"connection:warehouse": "sha256:" + strings.Repeat("a", 64),
	}, map[string]string{"connection:warehouse": "managed"})
	evidence := first["source:orders"]
	if !evidence.Available() || evidence.SourceID() != "source:orders" {
		t.Fatalf("managed source evidence = %#v, want available source:orders", evidence)
	}
	second := mustSourceDataIdentityEvidence(t, project, map[string]string{
		"connection:warehouse": "sha256:" + strings.Repeat("b", 64),
	}, map[string]string{"connection:warehouse": "managed"})
	if second["source:orders"].EquivalenceDigest() == evidence.EquivalenceDigest() {
		t.Fatal("managed manifest revision did not rotate source-data identity")
	}
	if got := mustSourceDataIdentityEvidence(t, project, nil, map[string]string{"connection:warehouse": "managed"}); len(got) != 0 {
		t.Fatalf("missing managed revision produced fallback evidence: %#v", got)
	}
	if got := mustSourceDataIdentityEvidence(t, project, map[string]string{"connection:warehouse": "revision-a"}, map[string]string{"connection:warehouse": "managed"}); len(got) != 0 {
		t.Fatalf("malformed managed revision produced fallback evidence: %#v", got)
	}
	if got := mustSourceDataIdentityEvidence(t, project, map[string]string{
		"connection:warehouse": "sha256:" + strings.Repeat("c", 64),
	}, map[string]string{"connection:warehouse": "sqlite"}); len(got) != 0 {
		t.Fatalf("connector binding mismatch produced source evidence: %#v", got)
	}

	externalManifest := project.Manifest()
	externalManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "http"}
	pathLocation := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "https://example.test/orders.csv", Format: "csv"},
		Format:                 "csv",
	}}
	source := externalManifest.Sources["source:orders"]
	source.Path = "https://example.test/orders.csv"
	source.Format = "csv"
	source.PathLocation = pathLocation
	source.EffectivePathLocation = pathLocation
	externalManifest.Sources["source:orders"] = source
	external, err := NewSourceBundle(graphValue, externalManifest)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustSourceDataIdentityEvidence(t, external, map[string]string{
		"connection:warehouse": "sha256:" + strings.Repeat("c", 64),
	}, map[string]string{"connection:warehouse": "http"}); len(got) != 0 {
		t.Fatalf("unsupported external connector accepted digest-shaped fallback evidence: %#v", got)
	}
}

func TestSourceDataIdentityAliasCapacityRejectsOverflow(t *testing.T) {
	t.Parallel()

	if got, err := sourceDataIdentityAliasCapacity(3); err != nil || got != 6 {
		t.Fatalf("sourceDataIdentityAliasCapacity(3) = %d, %v; want 6, nil", got, err)
	}
	maximumInt := int(^uint(0) >> 1)
	maximumSafe := maximumInt / 2
	if got, err := sourceDataIdentityAliasCapacity(maximumSafe); err != nil || got != maximumSafe*2 {
		t.Fatalf("sourceDataIdentityAliasCapacity(maximumSafe) = %d, %v; want %d, nil", got, err, maximumSafe*2)
	}
	if got, err := sourceDataIdentityAliasCapacity(maximumSafe + 1); err == nil || got != 0 {
		t.Fatalf("sourceDataIdentityAliasCapacity(overflow) = %d, %v; want 0, error", got, err)
	}
}

func TestSourceBundleRoundTripPreservesLoweredSemanticModelBinding(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	projectManifest.SemanticModels["semantic:sales"] = &semanticmodel.Model{
		Name: "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"sales_orders": {Model: "orders_model"},
		},
		Tables: map[string]semanticmodel.Table{
			"sales_orders": {ModelName: "orders_model", Execution: semanticmodel.ExecutionDefinition{Source: "orders_model"}},
		},
	}
	project, err := NewSourceBundle(graphValue, projectManifest)
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

func TestSourceBundleRoundTripPreservesPrivateRuntimeProjection(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
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
	project, err := NewSourceBundle(graphValue, projectManifest)
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

func TestSourceBundleRejectsInvalidRuntimePathUnion(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	model := projectManifest.SemanticModels["semantic:sales"]
	model.Sources = map[string]semanticmodel.Source{"orders": {PathLocation: &projectcontracts.PathSourceLocation{Value: (*projectcontracts.CSVPathSourceLocation)(nil)}}}
	if _, err := NewSourceBundle(graphValue, projectManifest); err == nil {
		t.Fatal("invalid runtime path union unexpectedly accepted")
	}
}

func TestSourceBundleIsPortableAndDeterministic(t *testing.T) {
	graphValue, projectManifest := sourceBundleFixture(t)
	first, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Canonical(), second.Canonical()) || first.Digest() != second.Digest() {
		t.Fatal("identical source bundles are not deterministic")
	}
	if strings.Contains(string(first.Canonical()), `"projectId"`) || strings.Contains(string(first.Canonical()), `"projectDigest"`) {
		t.Fatalf("source bundle retained project identity fields: %s", first.Canonical())
	}
	decoded, err := Decode(first.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Canonical(), decoded.Canonical()) || first.Digest() != decoded.Digest() {
		t.Fatal("source bundle roundtrip changed canonical identity")
	}
}

func TestSourceBundleHasNoManifestGraphIdentity(t *testing.T) {
	graphValue, projectManifest := sourceBundleFixture(t)
	if _, err := NewSourceBundle(graphValue, projectManifest); err != nil {
		t.Fatalf("portable manifest rejected: %v", err)
	}
}

func TestSourceBundleRejectsControlPlaneGraphAndTargetState(t *testing.T) {
	_, projectManifest := sourceBundleFixture(t)
	controlPlaneGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project:demo", Kind: projectgraph.KindProjectNamespace, Name: "demo"}}, nil)
	if err == nil {
		t.Fatal("graph constructor accepted control-plane Project node")
	}
	if _, err := NewSourceBundle(controlPlaneGraph, projectManifest); err == nil {
		t.Fatal("source bundle accepted invalid control-plane graph")
	}
	graphValue, _ := sourceBundleFixture(t)
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "managed", Path: "/target/only"}
	if _, err := NewSourceBundle(graphValue, projectManifest); err == nil || !strings.Contains(err.Error(), "target-owned state") {
		t.Fatalf("target-owned connection state error = %v", err)
	}
}

func TestSourceBundleRejectsIncompleteSemanticModelClosure(t *testing.T) {
	graphValue, projectManifest := fullBundleFixture(t)
	projectManifest.SemanticModels["semantic:sales"].Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"orders": {Model: "orders_model"},
	}

	resources := graphValue.Resources()
	edges := make([]projectgraph.Edge, 0, len(graphValue.Edges())-1)
	for _, edge := range graphValue.Edges() {
		if edge.From == "semantic:sales" && edge.To == "model:orders" {
			continue
		}
		edges = append(edges, edge)
	}
	incomplete, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewSourceBundle(incomplete, projectManifest)
	if err == nil || !strings.Contains(err.Error(), `semantic model "semantic:sales" dataset "orders"`) || !strings.Contains(err.Error(), "missing its graph edge") {
		t.Fatalf("NewSourceBundle() error = %v, want incomplete semantic closure rejection", err)
	}
}

func TestSourceBundleRejectsMissingAndForeignSemanticModelReferences(t *testing.T) {
	for _, reference := range []string{"missing_model", "foreign_project.orders_model"} {
		t.Run(reference, func(t *testing.T) {
			graphValue, projectManifest := fullBundleFixture(t)
			projectManifest.SemanticModels["semantic:sales"].Datasets = map[string]semanticmodel.SemanticDatasetSpec{
				"orders": {Model: reference},
			}

			_, err := NewSourceBundle(graphValue, projectManifest)
			if err == nil || !strings.Contains(err.Error(), `semantic model "semantic:sales" dataset "orders"`) || !strings.Contains(err.Error(), "is missing from graph") {
				t.Fatalf("NewSourceBundle() error = %v, want closed-graph reference rejection", err)
			}
		})
	}
}

func TestSourceBundleDecodersRejectTamperingUnknownDuplicateAndLegacyVersion(t *testing.T) {
	graphValue, projectManifest := sourceBundleFixture(t)
	bundle, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(bundle.Canonical(), &wire); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := json.Unmarshal(wire["version"], &version); err != nil {
		t.Fatal(err)
	}
	wire["version"] = json.RawMessage(`2`)
	legacy, _ := json.Marshal(wire)
	var unsupported UnsupportedVersionError
	if _, err := Decode(legacy); !errors.As(err, &unsupported) {
		t.Fatalf("legacy source bundle error = %v, want unsupported version", err)
	}
	unknown := strings.Replace(string(bundle.Canonical()), `{"version":3,`, `{"unknown":true,"version":3,`, 1)
	if _, err := Decode([]byte(unknown)); err == nil {
		t.Fatal("Decode accepted unknown source bundle field")
	}
	duplicate := strings.Replace(string(bundle.Canonical()), `{"version":3,`, `{"VERSION":3,"version":3,`, 1)
	if _, err := Decode([]byte(duplicate)); err == nil {
		t.Fatal("Decode accepted duplicate source bundle field")
	}
	trailing := string(bundle.Canonical()) + ` {"trailing":true}`
	if _, err := Decode([]byte(trailing)); err == nil {
		t.Fatal("Decode accepted trailing source bundle JSON")
	}
}

func TestSourceBundleDefensivelyCopiesManifest(t *testing.T) {
	graphValue, projectManifest := sourceBundleFixture(t)
	bundle, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "sqlite"}
	if got := bundle.Connections()["connection:warehouse"].Kind; got != "managed" {
		t.Fatalf("bundle retained mutable manifest input: %q", got)
	}
	connections := bundle.Connections()
	connections["connection:warehouse"] = semanticmodel.Connection{Kind: "mutated"}
	if got := bundle.Connections()["connection:warehouse"].Kind; got != "managed" {
		t.Fatalf("bundle connection output escaped: %q", got)
	}
}

func TestSourceBundlePreservesRuntimeProjectionAcrossRoundTrip(t *testing.T) {
	graphValue, projectManifest := sourceBundleFixture(t)
	bundle, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.ModelTables()["model:orders"].Execution.Source; got != "source:orders" {
		t.Fatalf("model execution projection = %q", got)
	}
	projection := bundle.RuntimeProjection()
	if projection.Models["model:orders"].Source != "source:orders" {
		t.Fatalf("runtime model projection = %#v", projection.Models["model:orders"])
	}
	decoded, err := Decode(bundle.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.ModelTables()["model:orders"].Execution.Source; got != "source:orders" {
		t.Fatalf("decoded model execution projection = %q", got)
	}
}

func TestSourceBundleRejectsMalformedRuntimeProjection(t *testing.T) {
	graphValue, projectManifest := sourceBundleFixture(t)
	bundle, err := NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(bundle.Canonical(), &wire); err != nil {
		t.Fatal(err)
	}
	var runtime map[string]json.RawMessage
	if err := json.Unmarshal(wire["runtime"], &runtime); err != nil {
		t.Fatal(err)
	}
	delete(runtime, "models")
	wire["runtime"], _ = json.Marshal(runtime)
	malformed, _ := json.Marshal(wire)
	if _, err := Decode(malformed); err == nil || !strings.Contains(err.Error(), "runtime projection") {
		t.Fatalf("missing runtime projection error = %v", err)
	}
}
