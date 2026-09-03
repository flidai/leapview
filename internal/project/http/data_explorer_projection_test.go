package http

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectview "github.com/flidai/leapview/internal/project"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestExplorerDatasetsProjectsCompositeAndUniqueGrains(t *testing.T) {
	model := &semanticmodel.Model{Name: "inventory", Tables: map[string]semanticmodel.Table{
		"order_lines": {
			ModelName: "order_lines",
			Entities: map[string]semanticmodel.EntityDefinition{
				"order_line":   {Type: "primary", Fields: []string{"order_id", "line_number"}},
				"customer_ref": {Type: "unique", Fields: []string{"customer_id", "region_code"}},
			},
			GrainEntity: "order_line",
		},
		"customer_snapshots": {
			ModelName: "customer_snapshots",
			Entities: map[string]semanticmodel.EntityDefinition{
				"external_key": {Type: "unique", Fields: []string{"tenant_id", "customer_id"}},
			},
			GrainEntity: "external_key",
		},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{
		"order_lines": {Model: "order_lines"}, "customer_snapshots": {Model: "customer_snapshots"},
	}}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}

	datasets := explorerDatasets(model, compiled)
	if len(datasets) != 2 {
		t.Fatalf("datasets = %#v, want two datasets", datasets)
	}
	byID := map[string]projectsignals.DataExploreDatasetSignal{}
	for _, dataset := range datasets {
		byID[dataset.ID] = dataset
	}
	orderLines := byID["order_lines"]
	if orderLines.GrainEntity != "order_line" || !reflect.DeepEqual(orderLines.GrainFields, []string{"order_id", "line_number"}) {
		t.Fatalf("composite grain = %#v/%#v, want order_line and authored tuple", orderLines.GrainEntity, orderLines.GrainFields)
	}
	if len(orderLines.Entities) != 2 || orderLines.Entities[0].Name != "customer_ref" || orderLines.Entities[0].Type != "unique" || !reflect.DeepEqual(orderLines.Entities[0].Fields, []string{"customer_id", "region_code"}) {
		t.Fatalf("entities = %#v, want sorted names and ordered fields", orderLines.Entities)
	}
	if orderLines.Entities[1].Grain == nil || !*orderLines.Entities[1].Grain {
		t.Fatalf("grain entity signal = %#v, want order_line marked grain", orderLines.Entities[1])
	}

	customerSnapshots := byID["customer_snapshots"]
	if customerSnapshots.GrainEntity != "external_key" || !reflect.DeepEqual(customerSnapshots.GrainFields, []string{"tenant_id", "customer_id"}) {
		t.Fatalf("unique grain = %#v/%#v, want non-primary composite tuple", customerSnapshots.GrainEntity, customerSnapshots.GrainFields)
	}
	if len(customerSnapshots.Entities) != 1 || customerSnapshots.Entities[0].Type != "unique" {
		t.Fatalf("unique entity signal = %#v, want type unique", customerSnapshots.Entities)
	}
}

func TestBuildDataExplorerProjectionUsesAuthorizedAssetsAndRichManifest(t *testing.T) {
	model := &semanticmodel.Model{
		Name:  "sales",
		Title: "Sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Entities:  map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order", Description: "Orders table",
				Columns: map[string]semanticmodel.ModelColumn{
					"order_id": {Name: "order_id", Type: "integer"},
					"status":   {Name: "status", Type: "string", Description: "Order status"},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status": {Label: "Status", Type: "string", Description: "Order status"},
				},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Label: "Orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	project := projectmanifest.Project{
		Sources: map[string]semanticmodel.Source{
			"source:orders": {Description: "Orders source", Fields: map[string]semanticmodel.SourceField{"id": {Name: "id", Type: "integer"}}},
		},
		Models: map[string]semanticmodel.Table{
			"model:orders": model.Tables["orders"],
		},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders", Title: "Orders source"},
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, compiledProjectionModels(t, project))
	if len(projection.SemanticModels) != 1 || projection.SemanticModels[0].ID != "semantic:sales" {
		t.Fatalf("semantic models = %#v, want only authorized semantic model", projection.SemanticModels)
	}
	if projection.SelectedSemanticModel == nil || projection.SelectedSemanticModel.ID != "semantic:sales" {
		t.Fatalf("selected semantic model = %#v, want semantic:sales", projection.SelectedSemanticModel)
	}
	if len(projection.Datasets) != 1 || projection.Datasets[0].ID != "orders" {
		t.Fatalf("datasets = %#v, want orders", projection.Datasets)
	}
	if len(projection.Objects) != 2 {
		t.Fatalf("objects = %#v, want source and authorized model", projection.Objects)
	}
	var modelObject projectsignals.DataExplorerObjectSignal
	for _, object := range projection.Objects {
		if object.Layer == "model" {
			modelObject = object
		}
	}
	if modelObject.Key != "model:model:orders:semantic:sales:orders" || modelObject.ResourceID != "model:orders" || modelObject.SemanticModelID == nil || *modelObject.SemanticModelID != "semantic:sales" || projectsignals.ValueOrZero(modelObject.DatasetID) != "orders" {
		t.Fatalf("model object = %#v, want binding identity key and backing model resource", modelObject)
	}
	if modelObject.ColumnCount != 2 || modelObject.Columns == nil || len(*modelObject.Columns) != 2 {
		t.Fatalf("model object columns = %#v, want rich table columns", modelObject)
	}
	if len(projection.Fields) != 2 || projection.Fields[0].ID != "orders.status" || projection.Fields[1].ID != "order_count" {
		t.Fatalf("fields = %#v, want dimension and metric", projection.Fields)
	}
}

func TestBuildDataExplorerProjectionPreservesSemanticDatasetAlias(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"order_facts": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"order_facts": {Model: "orders"}},
	}
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": {ModelName: "orders"}},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, compiledProjectionModels(t, project))
	if len(projection.Objects) != 1 || projectsignals.ValueOrZero(projection.Objects[0].DatasetID) != "order_facts" {
		t.Fatalf("objects = %#v, want semantic dataset alias order_facts", projection.Objects)
	}
}

func TestBuildDataExplorerProjectionKeysEachSemanticDatasetBinding(t *testing.T) {
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
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": {ModelName: "orders"}},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	compiled := compiledProjectionModels(t, project)
	first := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, compiled)
	second := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, compiled)
	if len(first.Objects) != 2 || len(second.Objects) != 2 {
		t.Fatalf("objects = %#v / %#v, want one object per dataset alias", first.Objects, second.Objects)
	}
	wantKeys := []string{
		"model:model:orders:semantic:sales:order_history",
		"model:model:orders:semantic:sales:orders",
	}
	for index, wantKey := range wantKeys {
		left, right := first.Objects[index], second.Objects[index]
		if left.Key != wantKey || right.Key != wantKey || left.Key != right.Key {
			t.Fatalf("objects[%d] = %#v / %#v, want stable key %q", index, left, right, wantKey)
		}
		if left.ResourceID != "model:orders" || projectsignals.ValueOrZero(left.SemanticModelID) != "semantic:sales" {
			t.Fatalf("objects[%d] identity = %#v, want backing model and semantic model", index, left)
		}
	}

	for _, datasetID := range []string{"orders", "order_history"} {
		projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{
			SemanticModelID: projectsignals.Optional("semantic:sales"),
			DatasetID:       projectsignals.Optional(datasetID),
		}, compiled)
		if projection.SelectedDataset == nil || projection.SelectedDataset.ID != datasetID {
			t.Fatalf("selected dataset for %q = %#v, want alias selected independently", datasetID, projection.SelectedDataset)
		}
		if projectsignals.ValueOrZero(projection.Command.SemanticModelID) != "semantic:sales" || projectsignals.ValueOrZero(projection.Command.DatasetID) != datasetID {
			t.Fatalf("command for %q = %#v, want semantic model and alias", datasetID, projection.Command)
		}
		var found bool
		for _, object := range projection.Objects {
			if projectsignals.ValueOrZero(object.DatasetID) != datasetID {
				continue
			}
			found = true
			if object.Key != explorerModelObjectKey("model:orders", "semantic:sales", datasetID) || object.ResourceID != "model:orders" {
				t.Fatalf("object for %q = %#v, want independent binding selection and backing Model", datasetID, object)
			}
		}
		if !found {
			t.Fatalf("no object found for dataset alias %q", datasetID)
		}
	}
}

func TestBuildDataExplorerProjectionCommandSelectsModelDatasetAndFields(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
			"customers": {ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"region": {Label: "Region"}}},
		},
		Metrics:  map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Label: "Revenue", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
	}
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	command := projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"), Dimensions: []string{"customers.region"}, Metrics: []string{"revenue"}}
	projection := BuildDataExplorerProjection([]projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModel), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}, project, command, compiledProjectionModels(t, project))
	if projection.SelectedDataset == nil || projection.SelectedDataset.ID != "customers" {
		t.Fatalf("selected dataset = %#v, want customers", projection.SelectedDataset)
	}
	if len(projection.Fields) != 3 {
		t.Fatalf("fields = %#v, want two dimensions and one metric", projection.Fields)
	}
	for _, field := range projection.Fields {
		switch field.ID {
		case "customers.region":
			if !field.Selected || !field.Compatible {
				t.Fatalf("selected field = %#v, want selected and compatible", field)
			}
		case "revenue":
			if !field.Selected || field.Compatible {
				t.Fatalf("cross-table metric = %#v, want selected and incompatible", field)
			}
		}
	}
}

func TestBuildDataExplorerProjectionInfersSafeBaseForCrossTableFields(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Field: "orders.customer_id", Table: "orders"},
					"status":      {Field: "orders.status", Table: "orders"},
				},
			},
			"customers": {
				ModelName: "customers",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Field: "customers.customer_id", Table: "customers"},
					"region":      {Field: "customers.region", Table: "customers"},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
	}
	project := projectmanifest.Project{
		Models: map[string]semanticmodel.Table{
			"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"],
		},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	command := projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
		Dimensions: []string{"customers.region", "orders.status"},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModel), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	customersProjection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
		Dimensions: []string{"customers.region"},
	}, compiledProjectionModels(t, project))
	foundOrdersStatus := false
	for _, field := range customersProjection.Fields {
		if field.ID == "orders.status" {
			foundOrdersStatus = true
			if field.Compatible || projectsignals.ValueOrZero(field.RebaseDatasetID) != "orders" {
				t.Fatalf("orders status = %#v, want unsafe reverse field that can rebase to orders", field)
			}
		}
	}
	if !foundOrdersStatus {
		t.Fatal("orders status field was not projected")
	}

	projection := BuildDataExplorerProjection(assets, project, command, compiledProjectionModels(t, project))

	if projection.SelectedDataset == nil || projection.SelectedDataset.ID != "orders" {
		t.Fatalf("selected dataset = %#v, want safely inferred orders base", projection.SelectedDataset)
	}
	if projectsignals.ValueOrZero(projection.Command.DatasetID) != "orders" {
		t.Fatalf("resolved command = %#v, want orders base", projection.Command)
	}
	if len(projection.Warnings) != 1 || projection.Warnings[0] != "Grain changed from Customers to Orders to support the selected fields." {
		t.Fatalf("warnings = %#v", projection.Warnings)
	}
}

func TestBuildDataExplorerProjectionDoesNotUseGraphPayloadAsSchema(t *testing.T) {
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Type: "integer"}}}},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", Columns: map[string]semanticmodel.ModelColumn{"id": {Type: "integer"}}}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	projection := BuildDataExplorerProjection([]projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders", Payload: map[string]any{"kind": "model"}},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales", Payload: map[string]any{"kind": "semantic_model"}},
	}, project, projectsignals.DataExploreCommand{}, compiledProjectionModels(t, project))
	if len(projection.Objects) != 1 || projection.Objects[0].ColumnCount != 1 {
		t.Fatalf("objects = %#v, want one manifest-backed object", projection.Objects)
	}
}

func TestBuildDataExplorerProjectionFailsClosedWhenCompiledBindingsUnavailable(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	project := projectmanifest.Project{SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}
	assets := []projectview.DevelopAssetView{{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"}}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, nil)
	if len(projection.Datasets) != 0 || len(projection.Fields) != 0 {
		t.Fatalf("projection exposed uncompiled semantic metadata: %#v", projection)
	}
	if len(projection.Warnings) != 1 || projection.Warnings[0] != "Compiled semantic dataset bindings are unavailable for the active serving generation." {
		t.Fatalf("warnings = %#v, want compiled-binding unavailability", projection.Warnings)
	}
}

func TestBuildDataExplorerProjectionRejectsUnavailableActivationBindings(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "wrong_orders"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	project := projectmanifest.Project{SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}
	assets := []projectview.DevelopAssetView{{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"}}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{}, nil)
	if len(projection.Datasets) != 0 || len(projection.Fields) != 0 {
		t.Fatalf("projection exposed invalid authoring bindings: %#v", projection)
	}
	if len(projection.Warnings) != 1 || projection.Warnings[0] != "Compiled semantic dataset bindings are unavailable for the active serving generation." {
		t.Fatalf("warnings = %#v, want activation-binding unavailability", projection.Warnings)
	}
}

func TestDataExplorerMetricsResolveSingleAndMultiRootOwnership(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "Order ID"}}},
			"customers": {ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "Customer ID"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":    {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
			"customer_count": {Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.id"}},
			"order_rate":     {Type: "derived", Expression: "${order_count} * 2"},
			"order_share":    {Type: "ratio", Numerator: "order_count", Denominator: "customer_count"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
	}
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModel), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
	}, compiledProjectionModels(t, project))
	fields := map[string]projectsignals.DataExploreFieldSignal{}
	for _, field := range projection.Fields {
		fields[field.ID] = field
	}
	if got := fields["order_rate"]; got.DatasetID != "orders" || got.Compatible {
		t.Fatalf("single-root derived metric = %#v, want orders ownership and incompatibility from customers", got)
	}
	for _, name := range []string{"order_share"} {
		got := fields[name]
		if got.DatasetID != "" || !got.Compatible {
			t.Fatalf("multi-root metric %q = %#v, want visible without false ownership", name, got)
		}
	}
}

func compiledProjectionModels(t *testing.T, project projectmanifest.Project) map[string]*semanticquery.CompiledModel {
	t.Helper()
	compiled := make(map[string]*semanticquery.CompiledModel, len(project.SemanticModels))
	for id, model := range project.SemanticModels {
		if model == nil {
			continue
		}
		value, err := semanticquery.CompileDatasetBindings(model)
		if err != nil {
			t.Fatalf("compile semantic model %q: %v", id, err)
		}
		compiled[id] = value
	}
	return compiled
}
