package http

import (
	"encoding/json"
	"testing"

	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestSuggestionResponseOnlyPatchesItsIndependentLane(t *testing.T) {
	suggestions := &projectsignals.DataExploreFilterSuggestionsSignal{Field: "orders.status", RequestSeq: 4, SuggestionRequestSeq: 19}
	explorer := projectsignals.DataExplorerSignal{Explore: projectsignals.DataExploreSignal{
		Command:           projectsignals.DataExploreCommand{Action: projectsignals.Pointer("configure"), FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status"}},
		FilterSuggestions: suggestions,
		Status:            projectsignals.DataExploreStatusSignal{State: "stale", RequestSeq: 4},
	}}
	patch := dataExplorerSignalPatch(projectsignals.DataExplorerPageSignal{}, explorer)
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || len(decoded["dataExplorer"]) != 1 || len(decoded["dataExplorer"]["explore"]) != 1 {
		t.Fatalf("suggestions replaced query/result context: %s", raw)
	}
	var actual projectsignals.DataExploreFilterSuggestionsSignal
	if err := json.Unmarshal(decoded["dataExplorer"]["explore"]["filterSuggestions"], &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Field != suggestions.Field || actual.SuggestionRequestSeq != suggestions.SuggestionRequestSeq {
		t.Fatalf("suggestion lane payload lost: %s", raw)
	}
	explorer.Explore.Command.Action = projectsignals.Pointer("run")
	if dataExplorerSuggestionsPatch(explorer) != nil {
		t.Fatal("run responses must still publish their results")
	}
}
