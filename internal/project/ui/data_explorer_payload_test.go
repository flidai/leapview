package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/exploration"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerBootstrapExplicitlyClearsTimeSettings(t *testing.T) {
	bound := &exploration.ExplorationTimeBound{
		Value: exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{
			Kind: "timestamp", Value: "2026-01-01T00:00:00Z",
		}},
		Inclusive: true,
	}
	absolute := func(lower, upper *exploration.ExplorationTimeBound) *exploration.ExplorationTimeSelection {
		return &exploration.ExplorationTimeSelection{
			Field: "orders.created_at", Grain: "month",
			Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{
				Kind: "absolute", Lower: lower, Upper: upper,
			}},
		}
	}
	for _, tc := range []struct {
		name     string
		time     *exploration.ExplorationTimeSelection
		cleared  string
		retained string
	}{
		{name: "time", cleared: "time"},
		{name: "range", time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: "month"}, cleared: "time.range", retained: "time.field"},
		{name: "lower", time: absolute(nil, bound), cleared: "time.range.lower", retained: "time.range.upper.value.value"},
		{name: "upper", time: absolute(bound, nil), cleared: "time.range.upper", retained: "time.range.lower.value.value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "commerce", Time: tc.time}
			command := uisignals.DataExploreCommand{Spec: spec}
			if tc.time != nil {
				command.Time = &uisignals.DataExploreTimeSignal{Field: tc.time.Field, Grain: string(tc.time.Grain)}
			}
			explorer := uisignals.DataExplorerSignal{
				Explore: uisignals.DataExploreSignal{Command: command},
				Command: uisignals.DataExplorerCommand{Explore: &command},
			}
			saved := DataExplorerSavedExplorationBootstrap{Enabled: true, State: uisignals.SavedExplorationStateSignal{
				Current: &uisignals.SavedExplorationCurrentSignal{ID: "saved-1", Spec: &spec},
				Command: uisignals.SavedExplorationCommandSignal{Action: "update", Spec: &spec},
			}}
			payload := DataExplorerBootstrapSignalsWithSavedExplorations(catalogFixture(), uisignals.DataExplorerPageSignal{}, explorer, saved)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{
				"dataExplorer.explore.command.spec",
				"dataExplorer.command.explore.spec",
				"dataExplorerCommand.explore.spec",
				"agentContext.exploration",
				"savedExplorations.current.spec",
				"savedExplorations.command.spec",
			} {
				if value := explorerWireValue(t, wire, prefix+"."+tc.cleared); value != nil {
					t.Errorf("%s.%s = %#v, want explicit null", prefix, tc.cleared, value)
				}
				if tc.retained != "" {
					want := "2026-01-01T00:00:00Z"
					if tc.retained == "time.field" {
						want = "orders.created_at"
					}
					if value := explorerWireValue(t, wire, prefix+"."+tc.retained); value != want {
						t.Errorf("%s.%s = %#v, want %q", prefix, tc.retained, value, want)
					}
				}
			}
			if tc.time == nil {
				for _, path := range []string{"dataExplorer.explore.command.time", "dataExplorer.command.explore.time", "dataExplorerCommand.explore.time"} {
					if value := explorerWireValue(t, wire, path); value != nil {
						t.Errorf("%s = %#v, want explicit null", path, value)
					}
				}
			}
		})
	}
}

// Checking presence separately from value catches omitempty regressions: a
// missing key would preserve the browser's old value during recursive patches.
func explorerWireValue(t *testing.T, root map[string]any, path string) any {
	t.Helper()
	var value any = root
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s: parent of %s is not an object: %#v", path, key, value)
		}
		value, ok = object[key]
		if !ok {
			t.Fatalf("%s: missing key %s", path, key)
		}
	}
	return value
}

func TestDataExplorerPayloadClearsExecutionErrorsOnRecovery(t *testing.T) {
	for _, failed := range []bool{true, false} {
		state := uisignals.DataExplorerSignal{}
		state.Explore.Status.State = "success"
		state.Explore.Result.RequestSeq = 2
		state.Explore.Result.RowsReturned = 281
		state.Explore.FilterSuggestions = &uisignals.DataExploreFilterSuggestionsSignal{}
		if failed {
			state.Explore.Status.State = "error"
			state.Explore.Status.Error = uisignals.Pointer("query failed")
			state.Explore.Result.Error = uisignals.Pointer("query failed")
			state.Explore.FilterSuggestions.Error = uisignals.Pointer("query failed")
		}
		raw, err := json.Marshal(DataExplorerPayload(state))
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"explore.status.error", "explore.result.error", "explore.filterSuggestions.error"} {
			var want any
			if failed {
				want = "query failed"
			}
			if got := explorerWireValue(t, wire, path); got != want {
				t.Errorf("%s = %#v, want %#v (failed=%v)", path, got, want, failed)
			}
		}
	}
}

func TestDataExplorerPayloadClearsRemovedPresentation(t *testing.T) {
	command := uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "sales"}}
	state := uisignals.DataExplorerSignal{
		Command: uisignals.DataExplorerCommand{Explore: &command},
		Explore: uisignals.DataExploreSignal{Command: command},
	}
	raw, err := json.Marshal(DataExplorerPayload(state))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"command.explore.spec", "explore.command.spec"} {
		for _, member := range []string{"visualization", "table", "pivot", "mode"} {
			if got := explorerWireValue(t, wire, prefix+"."+member); got != nil {
				t.Errorf("%s.%s = %#v, want explicit null", prefix, member, got)
			}
		}
	}
	for _, path := range []string{"command.explore.window", "explore.command.window", "explore.result.window"} {
		if got := explorerWireValue(t, wire, path); got != nil {
			t.Errorf("%s = %#v, want explicit null", path, got)
		}
	}
}
