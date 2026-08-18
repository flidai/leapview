package compiler

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestCompilePreservesInputAndIsDeterministic(t *testing.T) {
	document := dashboardauthoring.Dashboard{
		ID: "orders", Title: "Orders", SemanticModel: "model",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"status": {
				Label: "Status", Field: "orders.status",
				Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}},
			},
		},
		Visuals: dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
			"orders": {Title: "Orders", Query: dashboardauthoring.TableQuery{Dataset: "orders", Fields: []string{"orders.status"}, Metrics: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "order_count"}}}},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}}}}},
	}
	before, err := document.Clone()
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	models := map[string]*semanticmodel.Model{
		"model": {
			Name:     "model",
			Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
			Tables: map[string]semanticmodel.Table{
				"orders": {
					ModelName:   "orders",
					GrainEntity: "status",
					Entities: map[string]semanticmodel.EntityDefinition{
						"status": {Type: "primary", Fields: []string{"status"}},
					},
					Dimensions: map[string]semanticmodel.MetricDimension{
						"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
					},
				},
			},
			Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}, Empty: "zero"}},
		},
	}

	first, err := Compile(document, models)
	if err != nil {
		t.Fatalf("first Compile() error = %v", err)
	}
	second, err := Compile(document, models)
	if err != nil {
		t.Fatalf("second Compile() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated Compile() differs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !reflect.DeepEqual(document, before) {
		t.Fatalf("Compile() mutated authored document:\nbefore=%#v\nafter=%#v", before, document)
	}
}
