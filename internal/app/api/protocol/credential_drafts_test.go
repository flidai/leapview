package protocol

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
)

// Use the production route registry: a fixture-only policy test cannot prove
// that the actual secret-writing operation bypasses durable replay storage.
func TestCredentialDraftRouteNeverCapturesOrReplaysSecretBody(t *testing.T) {
	const path = "/api/v1/projects/project/targets/lvinst_test/connection-bindings/warehouse/credential-drafts"
	contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(http.MethodPost, path)
	if !ok || contract.OperationID != "saveCredentialDraft" || contract.Command == nil || contract.Command.Idempotency != "forbidden" {
		t.Fatal("credential draft save must have the generated non-replayable policy")
	}
	store := &fakeIdempotencyStore{}
	p := &Protocol{store: store, config: Config{BearerToken: func(*http.Request) string { return "token" }}}
	calls := 0
	handler := p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Body.(*unreadSecretBody).reads != 0 || r.GetBody != nil {
			t.Fatal("protocol inspected or copied the secret body")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	for _, tc := range []struct {
		name   string
		header http.Header
		status int
	}{
		{"first submission", nil, http.StatusCreated},
		{"new submission", nil, http.StatusCreated},
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
				t.Fatalf("status=%d, body reads=%d, unexpected replay or disclosure", w.Code, body.reads)
			}
		})
	}
	if calls != 2 || store.claimCalls.Load() != 0 || store.completeCalls.Load() != 0 || store.markCalls.Load() != 0 {
		t.Fatalf("calls=%d; replay store touched: claims=%d complete=%d mark=%d", calls, store.claimCalls.Load(), store.completeCalls.Load(), store.markCalls.Load())
	}
}
