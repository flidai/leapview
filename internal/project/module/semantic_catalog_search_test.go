package module

import (
	"slices"
	"testing"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func TestSemanticCatalogSearchTermsUsesActiveLeaseAndOmitsProtectedMembers(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":    {Model: "orders"},
			"customers": {Model: "customers"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{
					"order":        {Type: "primary", Fields: []string{"order_id"}},
					"customer_ref": {Type: "foreign", Fields: []string{"customer_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id":      {Datatype: semanticmodel.DataTypeInteger},
					"customer_id":   {Datatype: semanticmodel.DataTypeInteger},
					"secret_region": {Datatype: semanticmodel.DataTypeString},
				},
			},
			"customers": {
				ModelName: "customers", GrainEntity: "customer",
				Entities: map[string]semanticmodel.EntityDefinition{
					"customer": {Type: "primary", Fields: []string{"customer_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Datatype: semanticmodel.DataTypeInteger},
					"region":      {Datatype: semanticmodel.DataTypeString},
					"created_at":  {Datatype: semanticmodel.DataTypeDate},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"},
			ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
		}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"order_id":      {Label: "Order ID", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.order_id"}}},
			"secret_region": {Label: "Secret Region", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.secret_region"}}},
			"path_region":   {Label: "Path Region", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "customers.region", Path: []string{"orders_customers"}}}},
			"secret_time":   {Type: "date", Datatype: semanticmodel.DataTypeDate, NativeGrain: "day", Grains: []string{"day"}, Label: "Secret Time", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "customers.created_at", Path: []string{"orders_customers"}}}},
		},
		Filters: map[string]semanticmodel.SemanticFilterSpec{
			"secret_region_filter": {Field: "orders.secret_region", Operator: "equals", Value: "north"},
			"path_region_filter":   {Field: "customers.region", Path: []string{"orders_customers"}, Operator: "equals", Value: "north"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":           {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Label: "Order Count"},
			"secret_metric":         {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Label: "Secret Metric"},
			"filter_protected":      {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Where: []string{"secret_region_filter"}, Label: "Filter Protected"},
			"path_filter_protected": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Where: []string{"path_region_filter"}, Label: "Path Filter Protected"},
			"time_protected":        {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, TimeDimension: "secret_time", Label: "Time Protected"},
			"hidden_metric":         {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Label: "Hidden Metric", Hidden: true},
			"derived_secret":        {Type: "derived", Expression: "${filter_protected} * 2", Label: "Derived Secret"},
		},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{
			Dimensions: map[string][]string{"secret_region": {"finance"}},
			Metrics:    map[string][]string{"secret_metric": {"finance"}},
			Datasets:   map[string]semanticmodel.SemanticDatasetAccessSpec{"customers": {RequiredAccessGrants: []string{"finance"}}},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := semanticCatalogLease{
		runtime: projectDefinitionRuntimeStub{
			definition: projectmanifest.ResourceManifest{SemanticModels: map[string]*semanticmodel.Model{"semantic_sales": model}},
			compiled:   map[string]*semanticquery.CompiledModel{"semantic_sales": planner.CompiledModel()},
		},
		snapshot: snapshot,
	}
	terms, err := SemanticCatalogSearchTerms()(t.Context(), lease, "principal_1", "semantic_sales")
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"order_id", "Order ID", "order_count", "Order Count"} {
		if !slices.Contains(terms, term) {
			t.Errorf("search terms %#v do not contain %q", terms, term)
		}
	}
	for _, term := range []string{
		"secret_region", "Secret Region", "path_region", "Path Region", "secret_time", "Secret Time",
		"secret_metric", "Secret Metric", "filter_protected", "Filter Protected", "path_filter_protected", "Path Filter Protected",
		"time_protected", "Time Protected", "hidden_metric", "Hidden Metric", "derived_secret", "Derived Secret",
	} {
		if slices.Contains(terms, term) {
			t.Errorf("search terms expose protected metadata %q: %#v", term, terms)
		}
	}
}
