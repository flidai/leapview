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

func TestSuggestionResponseClearsFailureOnSuccessfulRetry(t *testing.T) {
	suggestions := &projectsignals.DataExploreFilterSuggestionsSignal{
		Field: "orders.status", RequestSeq: 4, SuggestionRequestSeq: 19,
		Error: projectsignals.Pointer("suggestions unavailable"),
	}
	explorer := projectsignals.DataExplorerSignal{Explore: projectsignals.DataExploreSignal{
		Command:           projectsignals.DataExploreCommand{Action: projectsignals.Pointer("configure"), FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status"}},
		FilterSuggestions: suggestions,
	}}
	for _, failed := range []bool{true, false} {
		if !failed {
			suggestions.Error = nil
			suggestions.SuggestionRequestSeq++
		}
		raw, err := json.Marshal(dataExplorerSuggestionsPatch(explorer))
		if err != nil {
			t.Fatal(err)
		}
		var patch struct {
			DataExplorer struct {
				Explore struct {
					FilterSuggestions map[string]json.RawMessage `json:"filterSuggestions"`
				} `json:"explore"`
			} `json:"dataExplorer"`
		}
		if err := json.Unmarshal(raw, &patch); err != nil {
			t.Fatal(err)
		}
		fields := patch.DataExplorer.Explore.FilterSuggestions
		errorValue, present := fields["error"]
		if !present {
			t.Fatalf("failed=%t: recursive suggestion patch omitted error: %s", failed, raw)
		}
		if failed {
			if string(errorValue) != `"suggestions unavailable"` {
				t.Fatalf("failure error=%s", errorValue)
			}
		} else if string(errorValue) != "null" {
			t.Fatalf("successful retry must clear the previous error with null: %s", raw)
		}
		var sequence int64
		if err := json.Unmarshal(fields["suggestionRequestSeq"], &sequence); err != nil {
			t.Fatal(err)
		}
		if sequence != suggestions.SuggestionRequestSeq || string(fields["field"]) != `"orders.status"` {
			t.Fatalf("retry identity changed: %s", raw)
		}
	}
}
