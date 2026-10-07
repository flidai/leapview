package saved

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	canonical "github.com/flidai/leapview/internal/analytics/exploration"
)

func TestSpecPayloadIsVersionedCanonicalAndDefensive(t *testing.T) {
	spec := testSpec()
	payload, err := NewExplorationSpecPayload(spec)
	if err != nil {
		t.Fatalf("new payload: %v", err)
	}
	if payload.Version() != ExplorationSpecVersion || !payload.Available() {
		t.Fatalf("payload metadata = version %d available %v", payload.Version(), payload.Available())
	}
	canonicalBytes := payload.Canonical()
	if !bytes.HasPrefix(canonicalBytes, []byte(`{"version":1,"spec":`)) {
		t.Fatalf("payload is not versioned canonical JSON: %s", canonicalBytes)
	}
	if payload.ContentHash() == "" {
		t.Fatal("payload content hash is empty")
	}

	canonicalBytes[0] = 'x'
	if bytes.Equal(canonicalBytes, payload.Canonical()) {
		t.Fatal("canonical accessor exposed mutable storage")
	}

	decoded, err := DecodeExplorationSpecPayload(payload.Canonical())
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !bytes.Equal(decoded.Canonical(), payload.Canonical()) || decoded.ContentHash() != payload.ContentHash() {
		t.Fatal("decoded payload changed canonical identity")
	}
	decodedSpec, err := decoded.Spec()
	if err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if !bytes.Equal(mustJSON(t, decodedSpec), mustJSON(t, spec)) {
		t.Fatal("decoded spec differs from source")
	}
}

func TestSpecPayloadRejectsUnknownFieldsNonCanonicalVersionAndOversize(t *testing.T) {
	specJSON := mustJSON(t, testSpec())
	unknown := `{"version":1,"spec":` + strings.TrimSpace(string(specJSON)) + `,"extra":true}`
	if _, err := DecodeExplorationSpecPayload([]byte(unknown)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unknown field error = %v, want invalid payload", err)
	}
	wrongVersion := `{"version":2,"spec":` + strings.TrimSpace(string(specJSON)) + `}`
	if _, err := DecodeExplorationSpecPayload([]byte(wrongVersion)); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("version error = %v, want unsupported version", err)
	}
	if _, err := DecodeExplorationSpecPayload([]byte(strings.Repeat("x", MaxSpecPayloadBytes+1))); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversize error = %v, want payload too large", err)
	}
	duplicate := `{"version":1,"version":1,"spec":` + strings.TrimSpace(string(specJSON)) + `}`
	if _, err := DecodeExplorationSpecPayload([]byte(duplicate)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("duplicate key error = %v, want invalid payload", err)
	}
	deep := `{"version":1,"spec":` + strings.Repeat(`[`, 33) + `null` + strings.Repeat(`]`, 33) + `}`
	if _, err := DecodeExplorationSpecPayload([]byte(deep)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("deep payload error = %v, want invalid payload", err)
	}
	invalid := testSpec()
	invalid.ModelID = "bad model id"
	if _, err := NewExplorationSpecPayload(invalid); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("invalid spec error = %v, want invalid payload", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return data
}

func testSpec() canonical.ExplorationSpec {
	return canonical.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []canonical.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []canonical.ExplorationFilter{},
		Sort:          []canonical.ExplorationSort{},
		Limit:         100,
	}
}
