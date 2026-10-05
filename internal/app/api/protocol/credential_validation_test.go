package protocol

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
)

// Validation must run as a fresh probe for every explicit submission. The
// production route registry proves the operation cannot capture or replay a
// raw request body through durable idempotency middleware.
func TestCredentialValidationRouteNeverCapturesOrReplaysRequest(t *testing.T) {
	const path = "/api/v1/projects/project/targets/lvinst_test/connection-bindings/warehouse/credential-drafts/018f3f83-7b2f-7b37-9f9e-000000009981/validate"
	contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(http.MethodPost, path)
	if !ok || contract.OperationID != "validateCredentialDraft" || contract.Command == nil || contract.Command.Idempotency != "forbidden" {
		t.Fatal("credential validation must have the generated non-replayable policy")
	}
	store := &fakeIdempotencyStore{}
	p := &Protocol{store: store, config: Config{BearerToken: func(*http.Request) string { return "token" }}}
	calls := 0
	handler := p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Body.(*unreadSecretBody).reads != 0 || r.GetBody != nil {
			t.Fatal("protocol inspected or copied the validation request body")
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		name   string
		header http.Header
		status int
	}{
		{"first validation", nil, http.StatusOK},
		{"repeat validation", nil, http.StatusOK},
		{"supplied key", http.Header{"Idempotency-Key": {"must-not-be-reflected"}}, http.StatusBadRequest},
		{"empty key", http.Header{"Idempotency-Key": {""}}, http.StatusBadRequest},
		{"nil key", http.Header{"idempotency-key": nil}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, path, nil)
			if tc.header != nil {
				r.Header = tc.header
			}
			body := &unreadSecretBody{}
			r.Body = body
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || body.reads != 0 || w.Header().Get("Idempotency-Replayed") != "" || strings.Contains(w.Body.String(), "must-not-be-reflected") {
				t.Fatalf("status=%d body reads=%d replay=%q body=%s", w.Code, body.reads, w.Header().Get("Idempotency-Replayed"), w.Body.String())
			}
		})
	}
	if calls != 2 || store.claimCalls.Load() != 0 || store.completeCalls.Load() != 0 || store.markCalls.Load() != 0 {
		t.Fatalf("calls=%d; replay store touched: claims=%d complete=%d mark=%d", calls, store.claimCalls.Load(), store.completeCalls.Load(), store.markCalls.Load())
	}
}
