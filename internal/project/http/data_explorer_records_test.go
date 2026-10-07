package http

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/analytics/exploration"
)

func TestDataExplorerRecordsSurvivesURLAndIncrementalState(t *testing.T) {
	spec := defaultExplorationSpec()
	mode, dataset := exploration.ExplorationQueryModeRecords, "orders"
	spec.ModelID, spec.DatasetID, spec.Mode = "semantic:sales", &dataset, &mode
	spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "orders.status", Alias: stringPointer("Status")}}
	spec.Sort = []exploration.ExplorationSort{{Field: "Status", Direction: exploration.ExplorationSortDirectionDesc}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	command, err := dataExploreCommandFromQuery(url.Values{"v": {"2"}, "state": {string(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command.Spec, spec) {
		t.Fatalf("URL changed records spec: %#v", command.Spec)
	}
	restored := explorationSpecWithState(command.Spec, dataExploreStateFromSpec(command.Spec))
	if !reflect.DeepEqual(restored, spec) {
		t.Fatalf("incremental state changed records spec: %#v", restored)
	}
}

func TestDataExplorerModeAloneIsAuthoredState(t *testing.T) {
	spec := defaultExplorationSpec()
	mode := exploration.ExplorationQueryModeRecords
	spec.Mode = &mode
	if explorationSpecCanDefault(spec) || explorationSpecIsEmpty(spec) {
		t.Fatal("authored records mode treated as empty/defaultable")
	}
}
