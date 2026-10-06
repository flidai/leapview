package http

import semanticmodel "github.com/flidai/leapview/internal/analytics/model"

func browserSemanticExploreTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Label: "Status", Type: "string"},
				"id":     {Label: "Order ID", Type: "number", Datatype: semanticmodel.DataTypeInteger},
			}},
			"customers": {ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{
				"id": {Label: "Customer ID", Type: "number", Datatype: semanticmodel.DataTypeInteger},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":    {Model: "orders"},
			"customers": {Model: "customers"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"orders":         {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
			"customer_count": {Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.id"}},
			"order_share":    {Type: "ratio", Numerator: "orders", Denominator: "customer_count"},
		},
	}
}
