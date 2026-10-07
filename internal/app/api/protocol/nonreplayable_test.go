package protocol

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigenui "github.com/Yacobolo/toolbelt/apigen/runtime/ui"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// Keep a fixture for browser transport coverage until a product browser
// command is composed. credential_drafts_test.go covers the real API route.
func secretDraftContract() apiaggregate.GenOperationContract {
	return apiaggregate.GenOperationContract{
		OperationID: "saveTestCredentialDraft", Kind: apiaggregate.GenOperationKindCommand,
		Method: http.MethodPost, Path: "/test/credential-drafts", RequestBodyRequired: true,
		Command: &apiaggregate.GenCommandContract{
			Idempotency: "forbidden", AuthzMode: "authenticated",
			Audit:               apiaggregate.GenAuditPolicy{Required: true, Guarantee: "transactional"},
			AdditionalExposures: []apiaggregate.GenOperationSurface{apiaggregate.GenOperationSurfaceUI},
			UI:                  &apiaggregate.GenUIActionContract{ActionID: "credential.draft.save"},
		},
	}
}

func secretDraftLookup(method, path string) (apiaggregate.GenOperationContract, bool) {
	contract := secretDraftContract()
	return contract, method == contract.Method && path == contract.Path
}

type unreadSecretBody struct{ reads int }

func (b *unreadSecretBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*unreadSecretBody) Close() error               { return nil }

func TestNonReplayableAPIPolicyRejectsEverySuppliedKeyBeforeBodyRead(t *testing.T) {
	for _, header := range []http.Header{
		{"Idempotency-Key": {"secret-key"}}, {"Idempotency-Key": {""}},
		{"idempotency-key": {" "}}, {"Idempotency-Key": {"", "second-key"}},
	} {
		t.Run("supplied-header", func(t *testing.T) {
			store := &fakeIdempotencyStore{}
			p := &Protocol{store: store, config: Config{BearerToken: func(*http.Request) string { return "test-token" }}}
			called := false
			handler := p.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), secretDraftLookup)
			r := httptest.NewRequest(http.MethodPost, "/test/credential-drafts", nil)
			r.Header = header
			body := &unreadSecretBody{}
			r.Body = body
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "IDEMPOTENCY_KEY_FORBIDDEN") || called || body.reads != 0 || store.claimCalls.Load() != 0 {
				t.Fatalf("status=%d dispatched=%v reads=%d claims=%d", w.Code, called, body.reads, store.claimCalls.Load())
			}
		})
	}
}

func TestNonReplayableAPIPolicyAuthenticatesAndNeverCapturesOrReplays(t *testing.T) {
	store := &fakeIdempotencyStore{}
	accepted := false
	p := &Protocol{store: store, config: Config{
		BearerToken:   func(*http.Request) string { return "test-token" },
		AcceptsBearer: func(*http.Request) bool { return accepted },
	}}
	calls := 0
	body := &unreadSecretBody{}
	handler := p.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Body != body || body.reads != 0 || r.GetBody != nil {
			t.Fatal("protocol read or replaced the secret body")
		}
		w.WriteHeader(http.StatusCreated)
	}), secretDraftLookup)
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodPost, "/test/credential-drafts", nil)
		r.Body = body
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := http.StatusCreated
		if !accepted {
			want = http.StatusUnauthorized
		}
		if w.Code != want || w.Header().Get("Idempotency-Replayed") != "" {
			t.Fatalf("status=%d want=%d", w.Code, want)
		}
		accepted = true
	}
	if calls != 2 || store.claimCalls.Load() != 0 || store.completeCalls.Load() != 0 || body.reads != 0 {
		t.Fatalf("calls=%d claims=%d completions=%d reads=%d", calls, store.claimCalls.Load(), store.completeCalls.Load(), body.reads)
	}
}

func TestNonReplayableBrowserPolicyUsesServerBindingAndCurrentAuthorization(t *testing.T) {
	store := &fakeIdempotencyStore{}
	p := &Protocol{store: store}
	binding := apigenui.MustNonReplayableAction("credential.draft.save", "saveTestCredentialDraft")
	lookup := func(id string) (apiaggregate.GenOperationContract, bool) {
		return secretDraftContract(), id == binding.OperationID()
	}
	// The public constructor always consults the production registry. A
	// fabricated action cannot register a new exception to the replay policy.
	if _, err := p.BrowserNonReplayableMutationMiddleware(binding, func(*http.Request) bool { return true }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("unknown generated operation enabled non-replayable middleware")
	}
	allowed, calls := true, 0
	handler, err := p.browserNonReplayableMutationMiddleware(binding, func(*http.Request) bool { return allowed }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Body.(*unreadSecretBody).reads != 0 {
			t.Fatal("browser policy consumed the secret body")
		}
		w.WriteHeader(http.StatusCreated)
	}), lookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		claim  string
		key    bool
		allow  bool
		status int
	}{
		{binding.OperationID(), false, true, http.StatusCreated},
		{binding.OperationID(), false, true, http.StatusCreated},
		{binding.OperationID(), true, true, http.StatusBadRequest},
		{"otherCommand", false, true, http.StatusBadRequest},
		{binding.OperationID() + ",otherCommand", false, true, http.StatusBadRequest},
		{"", false, true, http.StatusBadRequest},
		{binding.OperationID(), false, false, http.StatusForbidden},
	} {
		allowed = tc.allow
		r := httptest.NewRequest(http.MethodPost, "/browser/credential-drafts", nil)
		r.Body = &unreadSecretBody{}
		r.Header.Set("X-Request-ID", "018f4f2e-0000-7000-8000-000000000802")
		r.Header.Set(uicommand.HeaderOperationID, tc.claim)
		if tc.key {
			r.Header.Set("Idempotency-Key", "")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || r.Body.(*unreadSecretBody).reads != 0 {
			t.Fatalf("status=%d want=%d reads=%d", w.Code, tc.status, r.Body.(*unreadSecretBody).reads)
		}
	}
	if calls != 2 || store.claimCalls.Load() != 0 || store.completeCalls.Load() != 0 {
		t.Fatalf("calls=%d claims=%d", calls, store.claimCalls.Load())
	}
	ordinary := apigenui.MustAction("credential.draft.save", binding.OperationID())
	if _, err := p.browserNonReplayableMutationMiddleware(ordinary, func(*http.Request) bool { return true }, handler, lookup); err == nil {
		t.Fatal("ordinary binding enabled non-replayable middleware")
	}
	badLookup := func(string) (apiaggregate.GenOperationContract, bool) {
		contract := secretDraftContract()
		contract.Command.Idempotency = "required"
		return contract, true
	}
	if _, err := p.browserNonReplayableMutationMiddleware(binding, func(*http.Request) bool { return true }, handler, badLookup); err == nil {
		t.Fatal("required command enabled non-replayable middleware")
	}
}

func TestNonReplayableBrowserPolicyPreservesIngressBodyLimit(t *testing.T) {
	p := &Protocol{store: &fakeIdempotencyStore{}}
	binding := apigenui.MustNonReplayableAction("credential.draft.save", "saveTestCredentialDraft")
	lookup := func(string) (apiaggregate.GenOperationContract, bool) { return secretDraftContract(), true }
	handler, err := p.browserNonReplayableMutationMiddleware(binding, func(*http.Request) bool { return true }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}), lookup)
	if err != nil {
		t.Fatal(err)
	}
	handler = httpmiddleware.RequestBodyLimit(httpmiddleware.RequestBodyLimitConfig{Enabled: true, MaxBytes: 8})(handler)
	for _, length := range []int64{32, -1} {
		r := httptest.NewRequest(http.MethodPost, "/browser/credential-drafts", strings.NewReader(strings.Repeat("s", 32)))
		r.ContentLength = length
		r.Header.Set("X-Request-ID", "018f4f2e-0000-7000-8000-000000000802")
		r.Header.Set(uicommand.HeaderOperationID, binding.OperationID())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("length=%d status=%d", length, w.Code)
		}
	}
}
