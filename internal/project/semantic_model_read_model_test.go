package project

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestSemanticModelAssetPayloadProjectsCompiledDefinition(t *testing.T) {
	model := &semanticmodel.Model{
		Name:        "sales",
		Title:       "Sales",
		Description: "Sales model",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status": {Label: "Status", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"customers": {Entities: map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id"},
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
	}

	payload := SemanticModelAssetPayload(model)
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
	relationships, ok := payload["StructuredRelationships"].(map[string]any)
	if !ok || len(relationships) != 1 {
		t.Fatalf("structured relationships = %#v, want one projected relationship", payload["StructuredRelationships"])
	}
	orders, ok := datasetDetails["orders"].(map[string]any)
	if !ok || orders["GrainEntity"] != "order_id" {
		t.Fatalf("orders dataset = %#v, want grain entity", datasets["orders"])
	}
}
