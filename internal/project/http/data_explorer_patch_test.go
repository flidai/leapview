package http

import (
	"encoding/json"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerSignalPatchClearsRemovedSpecSelections(t *testing.T) {
	spec := defaultExplorationSpec()
	spec.ModelID = "semantic:sales"
	command := projectsignals.DataExploreCommand{Spec: spec, RequestSeq: 12}
	explorer := projectsignals.DataExplorerSignal{
		Command: projectsignals.DataExplorerCommand{Explore: &command},
		Explore: projectsignals.DataExploreSignal{Command: command},
	}
	encoded, err := json.Marshal(dataExplorerSignalPatch(explorer))
	if err != nil {
		t.Fatal(err)
	}
	var patch map[string]any
	if err := json.Unmarshal(encoded, &patch); err != nil {
		t.Fatal(err)
	}
	object := func(value any, key string) map[string]any {
		t.Helper()
		return value.(map[string]any)[key].(map[string]any)
	}
	for _, projected := range []map[string]any{
		object(object(object(patch, "dataExplorerCommand"), "explore"), "spec"),
		object(object(object(object(patch, "dataExplorer"), "command"), "explore"), "spec"),
		object(object(object(patch, "dataExplorer"), "explore"), "command")["spec"].(map[string]any),
	} {
		for _, key := range []string{"datasetId", "time", "pivot", "table", "visualization"} {
			value, present := projected[key]
			if !present || value != nil {
				t.Fatalf("%s = %#v (present %v), want explicit null", key, value, present)
			}
		}
		if projected["modelId"] != spec.ModelID || projected["limit"] != float64(spec.Limit) {
			t.Fatalf("required spec fields changed: %#v", projected)
		}
	}
	// Persistence and URL serialization must remain canonical, without the
	// browser-only null tombstones.
	canonical, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var canonicalMap map[string]any
	if err := json.Unmarshal(canonical, &canonicalMap); err != nil {
		t.Fatal(err)
	}
	if _, present := canonicalMap["time"]; present {
		t.Fatal("browser patch changed canonical spec serialization")
	}
}

func TestExplorationSpecPatchPreservesTimeAndClearsItsRemovedRange(t *testing.T) {
	spec := defaultExplorationSpec()
	spec.Time = &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainMonth}
	encoded, err := json.Marshal(explorationSpecPatch(spec))
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	time := projected["time"].(map[string]any)
	for _, key := range []string{"alias", "range"} {
		if value, present := time[key]; !present || value != nil {
			t.Fatalf("%s = %#v (present %v), want null", key, value, present)
		}
	}
	if time["field"] != spec.Time.Field || time["grain"] != "month" {
		t.Fatalf("time selection lost: %#v", time)
	}
}

func TestExplorationSpecPatchPreservesPopulatedCanonicalSpec(t *testing.T) {
	const input = `{
		"schemaVersion":1,"modelId":"semantic:sales","datasetId":"orders",
		"dimensions":[{"field":"orders.region"}],"metrics":[{"field":"revenue"}],
		"filters":[{"field":"orders.region","datasetId":"orders","expression":{"kind":"comparison","operator":"equals","value":{"kind":"string","value":"EMEA"}}}],
		"sort":[{"field":"revenue","direction":"desc"}],"limit":100,
		"time":{"field":"orders.created_at","grain":"month","alias":"month","range":{"kind":"absolute","lower":{"value":{"kind":"date","value":"2026-01-01"},"inclusive":true}}},
		"pivot":{"rows":[{"field":"orders.region"}],"columns":[{"field":"orders.channel"}],"metrics":[{"field":"revenue"}],"window":{"limit":50,"offset":0}},
		"table":{"striped":false,"columns":[{"field":"revenue","label":"Revenue"}]},
		"visualization":{"kind":"pivot","rows":[{"field":"orders.region"}],"columns":[{"field":"orders.channel"}],"metrics":[{"field":"revenue"}]}
	}`
	var spec exploration.ExplorationSpec
	if err := json.Unmarshal([]byte(input), &spec); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := json.Marshal(explorationSpecPatch(spec))
	if err != nil {
		t.Fatal(err)
	}
	var restored exploration.ExplorationSpec
	if err := json.Unmarshal(patch, &restored); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("patch changed canonical spec:\ngot %s\nwant %s", got, want)
	}
}
