package http

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectview "github.com/flidai/leapview/internal/project"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestBuildDataExplorerProjectionUsesAuthorizedAssetsAndRichManifest(t *testing.T) {
	model := &semanticmodel.Model{
		Name:  "sales",
		Title: "Sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order", Description: "Orders table",
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
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{})
	if len(projection.Models) != 1 || projection.Models[0].ID != "semantic:sales" {
		t.Fatalf("models = %#v, want only authorized semantic model", projection.Models)
	}
	if projection.SelectedModel == nil || projection.SelectedModel.ID != "semantic:sales" {
		t.Fatalf("selected model = %#v, want semantic:sales", projection.SelectedModel)
	}
	if len(projection.Datasets) != 1 || projection.Datasets[0].ID != "orders" {
		t.Fatalf("datasets = %#v, want orders", projection.Datasets)
	}
	if len(projection.Objects) != 2 {
		t.Fatalf("objects = %#v, want source and authorized model", projection.Objects)
	}
	var modelObject projectsignals.DataExplorerObjectSignal
	for _, object := range projection.Objects {
		if object.Layer == "model_table" {
			modelObject = object
		}
	}
	if modelObject.Key != "model_table:model:orders" || modelObject.ModelID == nil || *modelObject.ModelID != "semantic:sales" {
		t.Fatalf("model object = %#v, want canonical object and semantic model context", modelObject)
	}
	if modelObject.ColumnCount != 2 || modelObject.Columns == nil || len(*modelObject.Columns) != 2 {
		t.Fatalf("model object columns = %#v, want rich table columns", modelObject)
	}
	if len(projection.Fields) != 2 || projection.Fields[0].ID != "orders.status" || projection.Fields[1].ID != "order_count" {
		t.Fatalf("fields = %#v, want dimension and metric", projection.Fields)
	}
}

func TestBuildDataExplorerProjectionCommandSelectsModelDatasetAndFields(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
			"customers": {Dimensions: map[string]semanticmodel.MetricDimension{"region": {Label: "Region"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Label: "Revenue", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}}},
	}
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	command := projectsignals.DataExploreCommand{ModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"), Dimensions: []string{"customers.region"}, Metrics: []string{"revenue"}}
	projection := BuildDataExplorerProjection([]projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModelTable), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}, project, command)
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
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Field: "orders.customer_id", Table: "orders"},
					"status":      {Field: "orders.status", Table: "orders"},
				},
			},
			"customers": {
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Field: "customers.customer_id", Table: "customers"},
					"region":      {Field: "customers.region", Table: "customers"},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
		}},
	}
	project := projectmanifest.Project{
		Models: map[string]semanticmodel.Table{
			"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"],
		},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	command := projectsignals.DataExploreCommand{
		ModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
		Dimensions: []string{"customers.region", "orders.status"},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModelTable), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	customersProjection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{
		ModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
		Dimensions: []string{"customers.region"},
	})
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

	projection := BuildDataExplorerProjection(assets, project, command)

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
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Type: "integer"}}}}}},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
	}
	projection := BuildDataExplorerProjection([]projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders", Payload: map[string]any{"kind": "model"}},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales", Payload: map[string]any{"kind": "semantic_model"}},
	}, project, projectsignals.DataExploreCommand{})
	if len(projection.Objects) != 1 || projection.Objects[0].ColumnCount != 1 {
		t.Fatalf("objects = %#v, want one manifest-backed object", projection.Objects)
	}
}

func TestDataExplorerMetricsResolveSingleAndMultiRootOwnership(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "Order ID"}}},
			"customers": {Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "Customer ID"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":    {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
			"customer_count": {Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.id"}},
			"order_rate":     {Type: "derived", Expression: "${order_count} * 2"},
			"order_share":    {Type: "ratio", Numerator: "order_count", Denominator: "customer_count"},
		},
	}
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModelTable), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	projection := BuildDataExplorerProjection(assets, project, projectsignals.DataExploreCommand{
		ModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
	})
	fields := map[string]projectsignals.DataExploreFieldSignal{}
	for _, field := range projection.Fields {
		fields[field.ID] = field
	}
	if got := fields["order_rate"]; got.ModelTable != "orders" || projectsignals.ValueOrZero(got.Dataset) != "orders" || got.Compatible {
		t.Fatalf("single-root derived metric = %#v, want orders ownership and incompatibility from customers", got)
	}
	for _, name := range []string{"order_share"} {
		got := fields[name]
		if got.ModelTable != "" || got.Dataset != nil || !got.Compatible {
			t.Fatalf("multi-root metric %q = %#v, want visible without false ownership", name, got)
		}
	}
}
