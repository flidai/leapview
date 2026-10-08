package authoring

import (
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

// Filter applicability belongs to the query, including the records, pivot,
// histogram and distribution families created by the visual picker.
func TestEveryVisualFilterControlCompilesAndAppliesTypedValues(t *testing.T) {
	datatypes := []semanticmodel.LogicalDataType{semanticmodel.DataTypeString, semanticmodel.DataTypeBoolean, semanticmodel.DataTypeInteger, semanticmodel.DataTypeDecimal, semanticmodel.DataTypeFloat, semanticmodel.DataTypeDate, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ}
	model := &semanticmodel.Model{Name: "orders", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Dimensions: map[string]semanticmodel.SemanticDimension{}, Metrics: map[string]semanticmodel.Metric{}, Tables: map[string]semanticmodel.Table{}}
	fields := map[string]semanticmodel.MetricDimension{}
	for _, datatype := range datatypes {
		id := strings.ToLower(string(datatype))
		model.Dimensions[id] = semanticmodel.SemanticDimension{Datatype: datatype, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders." + id}}}
		fields[id] = semanticmodel.MetricDimension{Datatype: datatype}
	}
	for _, id := range []string{"value", "budget", "cost", "margin"} {
		model.Metrics[id] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders." + id}}
		fields[id] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeDecimal}
	}
	model.Tables["orders"] = semanticmodel.Table{ModelName: "orders", Dimensions: fields}
	bindings := &VisualTypeFieldBindings{Dataset: "orders", Dimensions: []string{"string", "date"}, Metrics: []string{"value", "budget", "cost", "margin"}, Details: []string{"string", "value"}}
	for _, entry := range CanonicalVisualCatalog() {
		_, revision := canonicalReducerFixture(t)
		doc := revision.Document
		if entry.Type == document.DashboardVisualTypeBar {
			doc.Spec.Visuals["base"] = defaultCanonicalVisual("kpi", "Base")
		}
		if err := setCanonicalVisualType(&doc, SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: entry.Type, ResolvedBindings: bindings}); err != nil {
			t.Fatal(err)
		}
		for _, datatype := range datatypes {
			for _, control := range []string{"singleSelect", "multiSelect", "text", "numericRange", "dateRange", "relativePeriod"} {
				t.Run(string(entry.Type)+"/"+string(datatype)+"/"+control, func(t *testing.T) {
					field := strings.ToLower(string(datatype))
					filterControl, err := canonicalBuilderFilterControl(control, "orders", nil)
					if err != nil {
						t.Fatal(err)
					}
					doc.Spec.Filters = []document.DashboardFilter{{ID: "test_filter", Label: "Filter", Dimension: field, Control: filterControl}}
					compiled, err := compiler.CompileCanonicalDashboardBuilderFilters(doc, model)
					numeric := datatype == semanticmodel.DataTypeInteger || datatype == semanticmodel.DataTypeDecimal || datatype == semanticmodel.DataTypeFloat
					temporal := datatype == semanticmodel.DataTypeDate || datatype == semanticmodel.DataTypeDateTime || datatype == semanticmodel.DataTypeDateTimeTZ
					applicable := control == "singleSelect" || control == "multiSelect" || control == "text" || control == "numericRange" && numeric || (control == "dateRange" || control == "relativePeriod") && temporal
					if !applicable {
						if err == nil {
							t.Fatal("unsupported control/type combination compiled")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					definition, binding := compiled.Definitions["test_filter"], compiled.Bindings["test_filter"]
					if !reflect.DeepEqual(binding.Targets, []string{"overview/base-component"}) {
						t.Fatalf("wrong filter consumers: %v", binding.Targets)
					}
					value := dashboardfilter.Value{Kind: definition.ValueKind, Value: "paid"}
					switch value.Kind {
					case dashboardfilter.ValueBoolean:
						value.Value = true
					case dashboardfilter.ValueInteger, dashboardfilter.ValueDecimal:
						value.Value = "42"
					case dashboardfilter.ValueDate:
						value.Value = "2026-10-08"
					case dashboardfilter.ValueTimestamp:
						value.Value = "2026-10-08T00:00:00Z"
					}
					expression := dashboardfilter.Expression{Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: []dashboardfilter.Value{value}}
					switch control {
					case "text":
						expression = dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorContains, Value: &value}
						if datatype != semanticmodel.DataTypeString {
							expression.Operator = dashboardfilter.OperatorEquals
						}
					case "numericRange", "dateRange":
						expression = dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRange, Lower: &dashboardfilter.Bound{Value: value, Inclusive: true}}
					case "relativePeriod":
						expression = dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRelativePeriod, Direction: dashboardfilter.DirectionPrevious, Count: 7, Unit: dashboardfilter.UnitDay, Anchor: dashboardfilter.AnchorFixed, AnchorValue: &value}
					}
					encoded, err := dashboardfilter.EncodeTypedV1(expression, definition.ValueKind)
					if err != nil {
						t.Fatal(err)
					}
					decoded, err := dashboardfilter.DecodeTypedV1(encoded, definition.ValueKind)
					if err != nil || !reflect.DeepEqual(decoded, expression) {
						t.Fatalf("URL round trip: %#v %v", decoded, err)
					}
					for _, mode := range []dashboardfilter.ApplicationMode{dashboardfilter.ApplicationImmediate, dashboardfilter.ApplicationDeferred} {
						machine := dashboardfilter.NewMachine(mode, map[string]dashboardfilter.BindingSpec{binding.Key: {ValueKind: definition.ValueKind, Default: binding.Default, Editable: true, Selection: binding.Selection, Time: definition.Time, Predicates: definition.Predicates}})
						if _, err := machine.Execute(dashboardfilter.Command{Kind: dashboardfilter.CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "select", BindingKey: binding.Key, Operation: dashboardfilter.MutationSet, Expression: &expression}); err != nil {
							t.Fatal(err)
						}
						if mode == dashboardfilter.ApplicationDeferred {
							if machine.State().AppliedControls[binding.Key].Expression.Kind != dashboardfilter.ExpressionUnfiltered {
								t.Fatal("deferred filter applied before Apply")
							}
							if _, err := machine.Execute(dashboardfilter.Command{Kind: dashboardfilter.CommandApply, BaseRevision: machine.State().Revision, ClientMutationID: "apply"}); err != nil {
								t.Fatal(err)
							}
						}
						if machine.State().AppliedControls[binding.Key].Expression.Kind != expression.Kind {
							t.Fatal("filter not applied")
						}
						if _, err := machine.Execute(dashboardfilter.Command{Kind: dashboardfilter.CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "reset", BindingKey: binding.Key, Operation: dashboardfilter.MutationResetBinding}); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}
