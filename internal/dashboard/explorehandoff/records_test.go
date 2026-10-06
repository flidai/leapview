package explorehandoff

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/lowering"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
)

func TestSpecForVisualPreservesRecordFieldsAndSort(t *testing.T) {
	definition := visualizationdefinition.Definition{Query: visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryDetail, ModelID: "semantic:sales", DatasetID: "primary",
		Detail: &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: []visualizationdefinition.FieldBinding{{FieldID: "orders.region", Alias: "Region"}, {FieldID: "orders.channel", Alias: "Channel"}}, DefaultSort: []visualizationdefinition.Sort{{FieldID: "Region", Direction: "desc"}}, Limit: 100},
	}}
	spec, ok := SpecForVisual(definition, exploreHandoffModel())
	if !ok {
		t.Fatal("records handoff unavailable")
	}
	if !exploration.IsRecords(spec) || len(spec.Dimensions) != 2 || spec.Dimensions[0].Field != "orders.region" || *spec.Dimensions[0].Alias != "Region" || spec.Sort[0].Field != "orders.region" || spec.Sort[0].Direction != "desc" {
		t.Fatalf("records spec = %#v", spec)
	}
	query, err := lowering.Query(spec)
	if err != nil {
		t.Fatal(err)
	}
	if query.Kind != dataquery.KindSemanticRows || query.Fields[0].Alias != "Region" || len(query.Metrics) != 0 {
		t.Fatalf("records query = %#v", query)
	}
}

func TestSpecForCompiledExecutiveSalesRecords(t *testing.T) {
	project, err := projectcompiler.LoadSourceRoot("../../../dashboards")
	if err != nil {
		t.Fatal(err)
	}
	report := project.Manifest.DashboardDefinitions["dashboard:executive-sales"]
	model := project.Manifest.SemanticModels[report.SemanticModel]
	count := 0
	for id, visual := range report.Visualizations {
		if visual.Query.Kind != visualizationdefinition.QueryDetail {
			continue
		}
		count++
		spec, ok := SpecForVisual(visual, model)
		if !ok {
			t.Fatalf("compiled records %q cannot Explore", id)
		}
		if !exploration.IsRecords(spec) || len(spec.Dimensions) != len(visual.Query.Detail.Fields) || len(spec.Sort) != len(visual.Query.Detail.DefaultSort) {
			t.Fatalf("records %q lost operands: %#v", id, spec)
		}
	}
	if count == 0 {
		t.Fatal("fixture has no records visual")
	}
}
