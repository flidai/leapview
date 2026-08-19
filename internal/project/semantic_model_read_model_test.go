package project

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestSemanticModelAssetPayloadProjectsCompiledDefinition(t *testing.T) {
	model := &semanticmodel.Model{
		Name:        "sales",
		Title:       "Sales",
		Description: "Sales model",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Entities:  map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status": {Label: "Status", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"customers": {ModelName: "customers", Entities: map[string]semanticmodel.EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Dataset: "orders", Aggregation: "count"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
		StructuredRelationships: map[string]semanticmodel.RelationshipSpec{
			"orders_customer": {From: semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Fields: []string{"customer_id"}}, To: semanticmodel.RelationshipEndpointSpec{Dataset: "customers", Fields: []string{"customer_id"}}},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customer", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
	}

	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	payload := SemanticModelAssetPayload(model, compiled)
	datasets, ok := payload["Datasets"].(map[string]any)
	if !ok || len(datasets) != 2 {
		t.Fatalf("datasets = %#v, want two projected datasets", payload["Datasets"])
	}
	datasetDetails, ok := payload["DatasetDetails"].(map[string]any)
	if !ok || len(datasetDetails) != 2 {
		t.Fatalf("dataset details = %#v, want two physical projections", payload["DatasetDetails"])
	}
	metrics, ok := payload["Metrics"].(map[string]any)
	if !ok || len(metrics) != 1 {
		t.Fatalf("metrics = %#v, want one projected metric", payload["Metrics"])
	}
	relationships, ok := payload["Relationships"].([]any)
	if !ok || len(relationships) != 1 {
		t.Fatalf("compiled relationships = %#v, want one projected relationship", payload["Relationships"])
	}
	if _, exists := payload["StructuredRelationships"]; exists {
		t.Fatalf("asset payload leaked authored relationship representation: %#v", payload["StructuredRelationships"])
	}
	orders, ok := datasetDetails["orders"].(map[string]any)
	if !ok || orders["GrainEntity"] != "order_id" {
		t.Fatalf("orders dataset = %#v, want grain entity", datasets["orders"])
	}
}

func TestSemanticModelAssetPayloadRejectsMismatchedCompiledBindings(t *testing.T) {
	model := &semanticmodel.Model{
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables:   map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
	}
	other := &semanticmodel.Model{
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"customers": {Model: "customers"}},
		Tables:   map[string]semanticmodel.Table{"customers": {ModelName: "customers"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(other)
	if err != nil {
		t.Fatal(err)
	}
	if payload := SemanticModelAssetPayload(model, compiled); payload != nil {
		t.Fatalf("payload = %#v, want nil for mismatched activation binding", payload)
	}
}
