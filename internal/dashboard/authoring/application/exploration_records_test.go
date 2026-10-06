package application_test

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestAppendRecordsExplorationPreservesPhysicalFieldsAndFilters(t *testing.T) {
	app, repo, _, _, initial := newExplorationAppendApplication(t, nil)
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID})
	if err != nil {
		t.Fatal(err)
	}
	mode, dataset, alias := exploration.ExplorationQueryModeRecords, "orders", "Status"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic-model:sales", Mode: &mode, DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.id"}, {Field: "orders.status", Alias: &alias}, {Field: "purchase_date"}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{{Field: "orders.status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{Kind: "set", Operator: "in", Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}}}}}},
		Sort:    []exploration.ExplorationSort{{Field: "Status", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 100}
	result, err := app.AppendExploration(t.Context(), explorationAppendRequest(target, initial.ID, spec))
	if err != nil {
		t.Fatalf("append records through canonical compiler: %v", err)
	}
	stored := repo.revisions[result.Revision.RevisionID].Document
	var records *document.RecordsDashboardQuery
	for _, visual := range stored.Spec.Visuals {
		if query, ok := visual.Query.Value.(*document.RecordsDashboardQuery); ok {
			records = query
			if visual.Type != document.DashboardVisualTypeTable {
				t.Fatalf("records visual type=%s", visual.Type)
			}
		}
	}
	if records == nil || records.Dataset != "orders" || len(records.Fields) != 3 {
		t.Fatalf("records query=%#v", records)
	}
	for index, want := range []string{"id", "status", "purchase_date"} {
		if records.Fields[index].Reference == nil || records.Fields[index].Reference.Field != want {
			t.Fatalf("record field %d=%#v", index, records.Fields[index])
		}
	}
	if *records.Fields[1].Reference.Alias != "Status" || records.Sort == nil || (*records.Sort)[0].Field != "Status" || (*records.Sort)[0].Direction != document.DashboardSortDirectionDesc {
		t.Fatalf("records aliases/sort=%#v", records)
	}
	if len(stored.Spec.Filters) != 1 || stored.Spec.Filters[0].Dimension != "status" || repo.appendCalls != 1 {
		t.Fatalf("filters=%#v appends=%d", stored.Spec.Filters, repo.appendCalls)
	}
}
