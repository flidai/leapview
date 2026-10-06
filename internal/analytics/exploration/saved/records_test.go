package saved

import (
	"testing"

	canonical "github.com/flidai/leapview/internal/analytics/exploration"
)

func TestRecordsPayloadRoundTripPreservesQueryMode(t *testing.T) {
	spec := testSpec()
	mode, dataset := canonical.ExplorationQueryModeRecords, "orders"
	spec.Mode, spec.DatasetID = &mode, &dataset
	spec.Metrics = []canonical.ExplorationMetricRef{}
	payload, err := NewExplorationSpecPayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExplorationSpecPayload(payload.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decoded.Spec()
	if err != nil {
		t.Fatal(err)
	}
	if !canonical.IsRecords(restored) {
		t.Fatalf("records mode lost after save: %#v", restored)
	}
}
