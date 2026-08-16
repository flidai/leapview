package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

func TestPageSliceForRequestPreservesEmptyArray(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/api/v1/projects", nil)
	items, _, ok := pageSliceForRequest[string](response, request, nil)
	assertEmptyJSONArray(t, items, ok)
}

func assertEmptyJSONArray[T any](t *testing.T, items []T, ok bool) {
	t.Helper()
	encoded, err := json.Marshal(items)
	if err != nil || !ok || string(encoded) != "[]" {
		t.Fatalf("empty page = %s, ok=%v, error=%v; want []", encoded, ok, err)
	}
}
