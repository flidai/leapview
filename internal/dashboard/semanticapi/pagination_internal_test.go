package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

func TestPageSliceForRequestPreservesEmptyArray(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/api/v1/projects/sales/semantic-models", nil)
	request.Header.Set("X-Serving-Snapshot", "state-1")
	items, _, ok := pageSliceForRequest[string](response, request, nil)
	encoded, err := json.Marshal(items)
	if err != nil || !ok || string(encoded) != "[]" {
		t.Fatalf("empty page = %s, ok=%v, error=%v; want []", encoded, ok, err)
	}
}

func TestPageSliceForRequestRequiresServingSnapshot(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/api/v1/projects/sales/semantic-models", nil)
	if _, _, ok := pageSliceForRequest[string](response, request, []string{"item"}); ok || response.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("missing serving snapshot status=%d ok=%v body=%s", response.Code, ok, response.Body.String())
	}
}
