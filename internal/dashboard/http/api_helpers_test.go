package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

func TestPageSliceForRequestPreservesEmptyArray(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/?limit=10", nil)
	request.Header.Set("X-Serving-Snapshot", "state-1")

	items, nextCursor, ok := pageSliceForRequest[string](recorder, request, []string{})
	if !ok {
		t.Fatalf("page slice failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if items == nil || len(items) != 0 || nextCursor != "" {
		t.Fatalf("empty page = %#v cursor=%q, want non-nil empty slice", items, nextCursor)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal empty page: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty page JSON = %s, want []", encoded)
	}
}

func TestPageSliceForRequestRequiresServingSnapshot(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/?limit=10", nil)
	if _, _, ok := pageSliceForRequest[string](recorder, request, []string{"item"}); ok || recorder.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("missing serving snapshot status=%d ok=%v body=%s", recorder.Code, ok, recorder.Body.String())
	}
}
