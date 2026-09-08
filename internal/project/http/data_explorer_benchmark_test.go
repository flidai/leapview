package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
)

// BenchmarkCanonicalExplorationURLDecodeAndHydration keeps URL restoration's
// two expensive boundaries visible: strict canonical JSON decoding and the
// active-generation projection that hydrates fields, datasets, and selection.
func BenchmarkCanonicalExplorationURLDecodeAndHydration(b *testing.B) {
	spec := benchmarkCanonicalExplorationSpec()
	state, err := json.Marshal(spec)
	if err != nil {
		b.Fatal(err)
	}
	query := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(state)}}
	rawQuery := query.Encode()

	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(rawQuery)))
		for range b.N {
			values, err := url.ParseQuery(rawQuery)
			if err != nil {
				b.Fatal(err)
			}
			command, err := dataExploreCommandFromQuery(values)
			if err != nil {
				b.Fatal(err)
			}
			if command.Spec.ModelID != spec.ModelID {
				b.Fatalf("decoded model = %q, want %q", command.Spec.ModelID, spec.ModelID)
			}
		}
	})

	h, _ := newDataExplorerURLTestHandler(b)
	request := httptest.NewRequest(stdhttp.MethodGet, "/updates?"+rawQuery, nil)
	b.Run("hydrate", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			recorder := httptest.NewRecorder()
			_, explorer, ok := h.dataExplorerSignalsForURL(recorder, request, false)
			if !ok {
				b.Fatalf("hydration failed: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if explorer.Explore.Command.Spec.ModelID != spec.ModelID || len(explorer.Explore.Fields) == 0 {
				b.Fatalf("hydrated explorer = %#v, want model and fields", explorer.Explore.Command)
			}
		}
	})
}

func benchmarkCanonicalExplorationSpec() exploration.ExplorationSpec {
	dataset := "orders"
	return exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		DatasetID:     &dataset,
		Dimensions:    []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters:       []exploration.ExplorationFilter{testStringFilter("orders.status", "in", "paid", "pending")},
		Sort:          []exploration.ExplorationSort{{Field: "revenue", Direction: exploration.ExplorationSortDirectionDesc}},
		Time:          &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainMonth},
		Limit:         100,
	}
}
