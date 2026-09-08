package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
)

type setupRequiredMetrics struct {
	integrationMetrics
}

func (m setupRequiredMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	for _, target := range request.Targets {
		publish(consumer.Result{Target: target, Err: setupRequiredDataError{}})
	}
	return ctx.Err()
}

type setupRequiredDataError struct{}

func (setupRequiredDataError) Error() string       { return "source data is missing" }
func (setupRequiredDataError) SetupRequired() bool { return true }

func TestUpdatesStreamsRealRuntimeSignals(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name    string
		pageID  string
		signals map[string]any
		query   url.Values
		assert  func(t *testing.T, patches []map[string]any)
	}{
		{
			name:    "overview filtered to SP",
			pageID:  "overview",
			signals: map[string]any{},
			query: url.Values{
				"state": []string{mustTypedSetURLValue(t, "SP")},
			},
			assert: func(t *testing.T, patches []map[string]any) {
				t.Helper()

				requireFirstStatusLoading(t, patches)
				requireStatusLoading(t, patches, true)
				requireStatusLoading(t, patches, false)
				requireFilterValues(t, patches, "overview", "state", "SP")
				requireVisual(t, patches, "orders")
				requireTable(t, patches, "order_rows")
				requireNoTopLevelSignal(t, patches, "kpis")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patches := h.getUpdatesSignalsWithQueryTimeout(t, "executive-sales", tt.pageID, tt.signals, tt.query, time.Second)
			tt.assert(t, patches)
		})
	}
}

func TestUpdatesStreamsSetupRequiredPatchForMissingData(t *testing.T) {
	h := newHarness(t, withMetricsWrapper(func(metrics integrationMetrics) integrationMetrics {
		return setupRequiredMetrics{integrationMetrics: metrics}
	}))

	// The setup-required path performs the first dashboard refresh through the
	// full runtime harness; give it the same bounded budget as the other
	// integration stream assertions so cold CI runners do not cancel before
	// the patch is published.
	patches := h.getUpdatesSignalsWithQueryTimeout(
		t,
		"executive-sales",
		"overview",
		map[string]any{},
		nil,
		time.Second,
	)

	requireFirstStatusLoading(t, patches)
	requirePatch(t, patches, func(patch map[string]any) bool {
		status := mapAt(patch, "status")
		return status["setupRequired"] == true && strings.TrimSpace(stringValue(status["error"])) != ""
	})
}

func TestUpdatesIgnoresMalformedDatastarSignals(t *testing.T) {
	h := newHarness(t)
	// The malformed signal is ignored before the initial dashboard refresh,
	// but the refresh itself still exercises the full integration runtime. Give
	// cold CI runners the same bounded budget as the neighboring stream tests.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, h.updatesPath()+"?route=dashboard&dashboard=executive-sales&page=overview&datastar=%7Bnot-json", nil)
	rec := httptest.NewRecorder()

	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	requireVisual(t, ssetest.PatchSignals(t, rec.Body.String()), "orders")
}

func requireFirstStatusLoading(t *testing.T, patches []map[string]any) {
	t.Helper()
	if len(patches) == 0 {
		t.Fatal("no patches streamed")
	}
	status := mapAt(patches[0], "status")
	if status["loading"] != true {
		t.Fatalf("first patch status = %#v, want loading=true; patch=%#v", status, patches[0])
	}
}

func requireStatusLoading(t *testing.T, patches []map[string]any, loading bool) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		status := mapAt(patch, "status")
		return status["loading"] == loading
	})
}

func requireStatusError(t *testing.T, patches []map[string]any, setupRequired bool) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		status := mapAt(patch, "status")
		return stringValue(status["error"]) != "" && status["setupRequired"] == setupRequired
	})
}

func requireFilterValues(t *testing.T, patches []map[string]any, pageID, filterID string, want ...string) {
	t.Helper()
	bindingKey := dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopeReport, "", filterID)
	requirePatch(t, patches, func(patch map[string]any) bool {
		expression := mapAt(patch, "filterState", "appliedControls", bindingKey, "expression")
		values, ok := expression["values"].([]any)
		if len(want) == 0 && (!ok || expression["kind"] == "unfiltered") {
			return true
		}
		if !ok || len(values) != len(want) {
			return false
		}
		for i := range want {
			value, valueOK := values[i].(map[string]any)
			if !valueOK || value["value"] != want[i] {
				return false
			}
		}
		return true
	})
}

func mustTypedSetURLValue(t *testing.T, values ...string) string {
	t.Helper()
	typed := make([]dashboardfilter.Value, len(values))
	for index, value := range values {
		typed[index] = dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: value}
	}
	encoded, err := dashboardfilter.EncodeTypedV1(dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: typed,
	}, dashboardfilter.ValueString)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func requireVisual(t *testing.T, patches []map[string]any, visualID string) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		return hasKey(mapAt(patch, "visuals"), visualID)
	})
}

func requireTable(t *testing.T, patches []map[string]any, tableID string) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		visual := mapAt(patch, "visuals", tableID)
		kind := mapAt(visual, "spec")["kind"]
		return kind == "table" || kind == "matrix" || kind == "pivot"
	})
}

func requireNoFilter(t *testing.T, patches []map[string]any, filterID string) {
	t.Helper()
	bindingKey := dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopeReport, "", filterID)
	for _, patch := range patches {
		if hasKey(mapAt(patch, "filterState", "appliedControls"), bindingKey) {
			t.Fatalf("patch streamed unexpected filter %q: %#v", filterID, patch)
		}
	}
}

func requireNoSelection(t *testing.T, patches []map[string]any) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		selections, ok := patch["interactionSelections"].([]any)
		return ok && len(selections) == 0
	})
}

func requireSelection(t *testing.T, patches []map[string]any, sourceID, field, value string) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		selections, ok := patch["interactionSelections"].([]any)
		if !ok {
			return false
		}
		for _, rawSelection := range selections {
			selection, ok := rawSelection.(map[string]any)
			if !ok || selection["sourceId"] != sourceID {
				continue
			}
			entries, ok := selection["entries"].([]any)
			if !ok {
				continue
			}
			for _, rawEntry := range entries {
				entry, ok := rawEntry.(map[string]any)
				if !ok {
					continue
				}
				mappings, ok := entry["mappings"].([]any)
				if !ok {
					continue
				}
				for _, rawMapping := range mappings {
					mapping, ok := rawMapping.(map[string]any)
					if ok && mapping["field"] == field && mapping["value"] == value {
						return true
					}
				}
			}
		}
		return false
	})
}

func requireTableBlock(t *testing.T, patches []map[string]any, tableID, blockID string, start, requestSeq int) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		block := tableBlock(patch, tableID, blockID)
		return numberValue(block["start"]) == float64(start) && numberValue(block["requestSeq"]) == float64(requestSeq)
	})
}

func requireTableResetVersion(t *testing.T, patches []map[string]any, tableID string, resetVersion int) {
	t.Helper()
	requirePatch(t, patches, func(patch map[string]any) bool {
		table := visualizationDataState(patch, tableID)
		return numberValue(table["resetVersion"]) == float64(resetVersion)
	})
}

func requireNoTopLevelSignal(t *testing.T, patches []map[string]any, signal string) {
	t.Helper()
	for _, patch := range patches {
		if hasKey(patch, signal) {
			t.Fatalf("patch streamed unexpected top-level signal %q: %#v", signal, patch)
		}
	}
}

func tableBlock(patch map[string]any, tableID, blockID string) map[string]any {
	return mapAt(visualizationDataState(patch, tableID), "blocks", blockID)
}

func visualizationDataState(patch map[string]any, visualID string) map[string]any {
	encoded, ok := mapAt(patch, "visuals", visualID, "dataState")["payload"].(string)
	if !ok || encoded == "" {
		return map[string]any{}
	}
	var state map[string]any
	if json.Unmarshal([]byte(encoded), &state) != nil {
		return map[string]any{}
	}
	return state
}

func requirePatch(t *testing.T, patches []map[string]any, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for _, patch := range patches {
		if match(patch) {
			return patch
		}
	}
	t.Fatalf("no patch matched predicate; patches: %#v", patches)
	return nil
}

func hasKey(source map[string]any, key string) bool {
	if source == nil {
		return false
	}
	_, ok := source[key]
	return ok
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func numberValue(value any) float64 {
	number, _ := value.(float64)
	return number
}

func mapAt(source map[string]any, path ...string) map[string]any {
	current := source
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}
