package lowering

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
)

func TestRecordsModeLowersWithoutAggregation(t *testing.T) {
	var spec exploration.ExplorationSpec
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"modelId":"semantic:sales","datasetId":"orders","mode":"records","dimensions":[{"field":"orders.status","alias":"status"}],"metrics":[],"filters":[{"field":"orders.status","expression":{"kind":"set","operator":"in","values":[{"kind":"string","value":"shipped"}]}}],"sort":[{"field":"status","direction":"desc"}],"limit":100}`), &spec); err != nil {
		t.Fatal(err)
	}
	query, err := Query(spec)
	if err != nil {
		t.Fatal(err)
	}
	if query.Kind != dataquery.KindSemanticRows || query.Target != "orders" || len(query.Fields) != 1 || query.Fields[0].Alias != "status" || len(query.Metrics) != 0 || query.Limit != 101 || len(query.Filters) != 1 || len(query.Sort) != 1 || query.Sort[0].Field != "status" {
		t.Fatalf("records query = %#v", query)
	}
	if query.Filters[0].Values[0] != "shipped" {
		t.Fatalf("records filters = %#v", query.Filters)
	}
}

func TestOmittedAndExplicitAggregateModesKeepGrouping(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		spec := basicSpec()
		if explicit {
			mode := exploration.ExplorationQueryModeAggregate
			spec.Mode = &mode
		}
		query, err := Query(spec)
		if err != nil {
			t.Fatal(err)
		}
		if query.Kind != dataquery.KindSemanticAggregate {
			t.Fatalf("explicit=%t: kind=%q", explicit, query.Kind)
		}
	}
}
