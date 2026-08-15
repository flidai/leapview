package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	"github.com/flidai/leapview/internal/platform"
	protocolgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	apiidempotencysqlite "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/workspace"
)

func TestAPIGenResponseBufferNormalizesLegacyErrorsAsProblemDetails(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets", nil)
	req.Header.Set("X-Request-ID", "req_problem")
	recorder := httptest.NewRecorder()
	buffer := apiprotocol.NewResponseBuffer(recorder, req)
	buffer.Header().Set("Content-Type", "application/json")
	buffer.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = buffer.Write([]byte(`{"code":422,"message":"invalid field","details":{"field":"name"}}`))
	buffer.Flush()

	if recorder.Code != http.StatusUnprocessableEntity || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = %d %q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	var problem map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	for key, want := range map[string]any{
		"status":    float64(422),
		"detail":    "invalid field",
		"instance":  "/api/v1/workspaces/sales/assets",
		"requestId": "req_problem",
	} {
		if problem[key] != want {
			t.Errorf("problem[%s] = %#v, want %#v", key, problem[key], want)
		}
	}
	for _, key := range []string{"type", "title", "code", "errors"} {
		if _, ok := problem[key]; !ok {
			t.Errorf("problem missing %s: %#v", key, problem)
		}
	}
}

func TestAPIGenResponseBufferPreservesBoundedProblemCode(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/sales/query", nil)
	recorder := httptest.NewRecorder()
	buffer := apiprotocol.NewResponseBuffer(recorder, req)
	buffer.Header().Set("Content-Type", "application/json")
	buffer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = buffer.Write([]byte(`{"code":503,"message":"overloaded","details":{"problemCode":"WORKLOAD_OVERLOADED"}}`))
	buffer.Flush()

	var problem protocolgen.ProblemDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != "WORKLOAD_OVERLOADED" || problem.Type != "https://leapview.dev/problems/workload_overloaded" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestAPIGenResponseBufferCompletesProblemDetailsIdentifiers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces?limit=bad", nil)
	req.Header.Set("X-Request-ID", "req_existing_problem")
	recorder := httptest.NewRecorder()
	buffer := apiprotocol.NewResponseBuffer(recorder, req)
	buffer.Header().Set("Content-Type", "application/problem+json")
	buffer.WriteHeader(http.StatusBadRequest)
	_, _ = buffer.Write([]byte(`{"type":"https://leapview.dev/problems/invalid","title":"Bad Request","status":400,"detail":"invalid limit","instance":"","code":"INVALID_LIMIT","requestId":"","errors":null}`))
	buffer.Flush()

	var problem protocolgen.ProblemDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Instance != "/api/v1/workspaces" || problem.RequestId != "req_existing_problem" || problem.Errors == nil {
		t.Fatalf("problem identifiers were not completed: %#v", problem)
	}
}

func TestAPIGenResponseBufferPreservesContractedDeleteBodyStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/p", nil)
	rec := httptest.NewRecorder()
	buffer := apiprotocol.NewResponseBuffer(rec, req)
	buffer.Header().Set("Content-Type", "application/json")
	buffer.WriteHeader(http.StatusOK)
	_, _ = buffer.Write([]byte(`{"status":"deleted"}`))
	buffer.Flush()
	if rec.Code != http.StatusOK || rec.Body.String() == "" {
		t.Fatalf("DELETE response = %d body=%q, want 200 body", rec.Code, rec.Body.String())
	}
}

func TestAPIGenResponseBufferSSEFlushDoesNotReplayBytes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("X-Request-ID", "req_sse")
	rec := httptest.NewRecorder()
	buffer := apiprotocol.NewResponseBuffer(rec, req)
	buffer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	buffer.Header().Set("X-Stream", "live")
	buffer.WriteHeader(http.StatusAccepted)
	_, _ = buffer.Write([]byte("data: one\n\n"))
	buffer.Flush()
	_, _ = buffer.Write([]byte("data: two\n\n"))
	buffer.Flush()
	_, _ = buffer.Write([]byte("data: three\n\n"))
	buffer.Flush()
	if got, want := rec.Body.String(), "data: one\n\ndata: two\n\ndata: three\n\n"; got != want {
		t.Fatalf("SSE body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusAccepted || rec.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || rec.Header().Get("X-Stream") != "live" {
		t.Fatalf("SSE response status/headers = %d %#v", rec.Code, rec.Header())
	}
}

func TestPublicAPIRouterErrorsUseAuthenticatedProblemDetails(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	handler := server.Routes()

	for _, tc := range []struct {
		name          string
		method        string
		path          string
		authorization string
		wantStatus    int
		wantCode      string
		wantAllow     string
	}{
		{name: "unknown route", method: http.MethodGet, path: "/api/v1/not-a-route", authorization: "Bearer dev", wantStatus: http.StatusNotFound, wantCode: "API_ROUTE_NOT_FOUND"},
		{name: "unsupported method", method: http.MethodPost, path: "/api/v1/workspaces", authorization: "Bearer dev", wantStatus: http.StatusMethodNotAllowed, wantCode: "METHOD_NOT_ALLOWED", wantAllow: http.MethodGet},
		{name: "unknown route does not disclose authentication", method: http.MethodGet, path: "/api/v1/not-a-route", wantStatus: http.StatusNotFound, wantCode: "API_ROUTE_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", tc.authorization)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tc.wantStatus || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("response = %d %q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			if got := response.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			var problem protocolgen.ProblemDetails
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Code != tc.wantCode || problem.Instance != tc.path || problem.RequestId == "" || problem.RequestId != response.Header().Get("X-Request-ID") || problem.Errors == nil {
				t.Fatalf("problem = %#v headers=%#v", problem, response.Header())
			}
		})
	}
}

func TestPublicAPIRouterEnforcesJSONContentTypeAcrossPartitions(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	handler := server.Routes()

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "access", path: "/api/v1/me/api-tokens"},
		{name: "dashboard", path: "/api/v1/workspaces/sales/semantic-models/sales/query"},
		{name: "managed data", path: "/api/v1/projects/demo/connections/upload/upload-sessions"},
		{name: "deployment", path: "/api/v1/projects/demo/deployments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer dev")
			request.Header.Set("Content-Type", "text/plain")
			request.Header.Set("Idempotency-Key", "content-type-test")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnsupportedMediaType || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("response = %d %q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			var problem protocolgen.ProblemDetails
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Code != "UNSUPPORTED_MEDIA_TYPE" || problem.RequestId == "" {
				t.Fatalf("problem = %#v", problem)
			}
		})
	}
}

func TestAPIGenTransportErrorsUseProblemDetailsWithoutLeakingCause(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?limit=bad", nil)
	req.Header.Set("X-Request-ID", "req_transport")
	recorder := httptest.NewRecorder()
	apiprotocol.TransportErrorResponder{}.RespondTransportError(req.Context(), recorder, req, apigenapi.GenTransportError{
		OperationID: "listProjects", Kind: "query_parameter", StatusCode: http.StatusBadRequest,
		Code: "INVALID_REQUEST", PublicDetail: "Invalid query parameter.", Cause: errors.New("secret parser detail"),
	})

	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = %d %q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "secret parser detail") {
		t.Fatalf("transport cause leaked to client: %s", recorder.Body.String())
	}
	var problem protocolgen.ProblemDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != "INVALID_REQUEST" || problem.RequestId != "req_transport" || problem.Instance != "/api/v1/projects" || problem.Detail != "Invalid query parameter." {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestAPIGenTransportErrorsIdentifyInvalidParameterWithoutLeakingValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?limit=secret-value", nil)
	recorder := httptest.NewRecorder()
	apiprotocol.TransportErrorResponder{}.RespondTransportError(req.Context(), recorder, req, apigenapi.GenTransportError{
		OperationID: "listProjects", Kind: "query_parameter", StatusCode: http.StatusBadRequest,
		Code: "INVALID_REQUEST", PublicDetail: "Invalid query parameter.", Cause: errors.New(`invalid query parameter "limit": invalid integer "secret-value"`),
	})

	if strings.Contains(recorder.Body.String(), "secret-value") {
		t.Fatalf("transport value leaked to client: %s", recorder.Body.String())
	}
	var problem protocolgen.ProblemDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Detail != `Invalid query parameter "limit".` || len(problem.Errors) != 1 || problem.Errors[0].Field != "limit" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestPublicProtocolIdempotencyReplaysAndRejectsDigestReuse(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	calls := 0
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/api/v1/principals/p-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p-1"}`))
	}))

	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/principals", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer token-a")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "principal-create")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	first := request(`{"email":"a@example.com"}`)
	second := request(`{"email":"a@example.com"}`)
	if first.Code != http.StatusCreated || second.Code != first.Code || second.Body.String() != first.Body.String() || calls != 1 {
		t.Fatalf("replay first=%d/%s second=%d/%s calls=%d", first.Code, first.Body.String(), second.Code, second.Body.String(), calls)
	}
	if second.Header().Get("Idempotency-Replayed") != "true" || second.Header().Get("Location") != first.Header().Get("Location") {
		t.Fatalf("replay headers = %#v", second.Header())
	}
	conflict := request(`{"email":"different@example.com"}`)
	if conflict.Code != http.StatusConflict || calls != 1 || conflict.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("conflict=%d body=%s calls=%d", conflict.Code, conflict.Body.String(), calls)
	}
}

func TestDashboardAuthoringCreateUsesPublicProtocolIdempotency(t *testing.T) {
	store := testStore(t)
	if _, err := store.SQLDB().ExecContext(context.Background(), `INSERT OR IGNORE INTO principals (id, email, display_name) VALUES ('dev', 'dev@localhost', 'Local Developer')`); err != nil {
		t.Fatalf("seed developer principal: %v", err)
	}
	protocolApp := testProtocolAuthoringApplication(t, store)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Authoring: protocolApp}))
	handler := server.Routes()
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/authoring/drafts", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer dev")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "authoring-create-protocol")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	body := `{"title":"Protocol Integration Dashboard","semanticModel":"sales"}`
	first, second := request(body), request(body)
	if first.Code != http.StatusCreated || second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatalf("create replay first=%d/%s second=%d/%s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay header = %#v", second.Header())
	}
	conflict := request(`{"title":"Protocol Integration Dashboard Different","semanticModel":"sales"}`)
	if conflict.Code != http.StatusConflict || conflict.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("create key conflict = %d/%s headers=%#v", conflict.Code, conflict.Body.String(), conflict.Header())
	}
}

type protocolAuthoringAuthorizer struct{}

func (protocolAuthoringAuthorizer) Authorize(context.Context, authoringservice.AuthorizationRequest) error {
	return nil
}

type protocolAuthoringCompiler struct{}

func (protocolAuthoringCompiler) Compile(context.Context, string, string, authoring.Dashboard) (authoringservice.Compilation, error) {
	return authoringservice.Compilation{}, nil
}

func testProtocolAuthoringApplication(t *testing.T, store *platform.Store) *authoringapplication.Application {
	t.Helper()
	repository := authoringsqlite.NewRepository(store.SQLDB())
	authorizer := protocolAuthoringAuthorizer{}
	sequence := 0
	next := func(prefix string) (string, error) {
		sequence++
		return fmt.Sprintf("%s-%d", prefix, sequence), nil
	}
	service, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository, Authorizer: authorizer, Compiler: protocolAuthoringCompiler{},
		Now: func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) {
			value, err := next("dashboard")
			return authoring.DashboardID(value), err
		},
		NewDraftID: func() (authoring.DraftID, error) { value, err := next("draft"); return authoring.DraftID(value), err },
		NewRevisionID: func() (authoring.RevisionID, error) {
			value, err := next("revision")
			return authoring.RevisionID(value), err
		},
	})
	if err != nil {
		t.Fatalf("build authoring service: %v", err)
	}
	app, err := authoringapplication.New(authoringapplication.Options{
		Authoring: service, Repository: repository, Authorizer: authorizer,
		AcquireRuntime: func(context.Context, string) (runtimehost.Lease, error) {
			return nil, errors.New("runtime unavailable")
		},
	})
	if err != nil {
		t.Fatalf("build authoring application: %v", err)
	}
	return app
}

func TestPublicProtocolIdempotencyReplaysAfterServerRestart(t *testing.T) {
	store := testStore(t)
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/api/v1/principals/p-restart")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p-restart"}`))
	})
	request := func(server *appTestHarness) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/principals", bytes.NewBufferString(`{"email":"restart@example.com"}`))
		req.Header.Set("Authorization", "Bearer token-a")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "restart-safe")
		rec := httptest.NewRecorder()
		server.publicProtocolMiddleware(next).ServeHTTP(rec, req)
		return rec
	}

	first := request(assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{})))
	second := request(assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{})))
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || calls != 1 {
		t.Fatalf("first=%d second=%d calls=%d firstBody=%s secondBody=%s", first.Code, second.Code, calls, first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "true" || second.Header().Get("Location") != first.Header().Get("Location") {
		t.Fatalf("restart replay headers = %#v", second.Header())
	}
}

func TestDurableIdempotencyQuarantinesExpiredPendingLeaseAfterRestart(t *testing.T) {
	store := testStore(t)
	db := store.SQLDB()
	now := time.Now().UTC()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO api_idempotency_records(
		scope, request_digest, state, owner_id, lease_expires_at, created_at, updated_at, expires_at
	) VALUES (?, ?, 'pending', ?, ?, ?, ?, ?)`,
		"stale-scope", "same-digest", "dead-server", now.Add(-time.Minute).Format(time.RFC3339Nano),
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed stale lease: %v", err)
	}
	record, execute, err := apiidempotencysqlite.NewStore(db).Claim(context.Background(), "stale-scope", "same-digest", "replacement-server", apiprotocol.IdempotencyLease, apiprotocol.IdempotencyLifetime)
	if err != nil {
		t.Fatalf("reclaim stale lease: %v", err)
	}
	if execute || record.State != "completed" || record.Status != http.StatusConflict || record.Digest != "same-digest" {
		t.Fatalf("quarantined record = %#v execute=%v", record, execute)
	}
}

func TestDurableIdempotencyDoesNotReplayTransientServerFailures(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{}))
	calls := 0
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/releases", bytes.NewBufferString(`{"projectDigest":"sha256:test"}`))
		req.Header.Set("Authorization", "Bearer dev")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "retry-transient")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	first, second := request(), request()
	if first.Code != http.StatusServiceUnavailable || second.Code != http.StatusCreated || calls != 2 {
		t.Fatalf("first=%d second=%d calls=%d", first.Code, second.Code, calls)
	}
	if second.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("successful retry was incorrectly replayed: %#v", second.Header())
	}
}

func TestPublicProtocolMapsStreamedBodyLimitTo413(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	handler := requestBodyLimit(RequestBodyLimitConfig{Enabled: true, MaxBytes: 4})(server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/principals", strings.NewReader(`{"email":"long@example.com"}`))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "too-large")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "CONTENT_TOO_LARGE") {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicProtocolRequiresIdempotencyKeyForMutationsOnly(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/principals", http.StatusBadRequest},
		{"/api/v1/workspaces/sales/semantic-models/orders/query", http.StatusNoContent},
		{"/api/v1/workspaces/sales/targets/demo/environments/dev/connection-bindings/warehouse/plan", http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(`{}`))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("POST %s = %d, want %d body=%s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestPublicProtocolAlwaysRequiresBearerCredentials(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, tc := range []struct {
		name          string
		authorization string
		want          int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "browser scheme", authorization: "Basic ZGV2OmRldg==", want: http.StatusUnauthorized},
		{name: "empty bearer", authorization: "Bearer", want: http.StatusUnauthorized},
		{name: "bearer", authorization: "Bearer dev", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusUnauthorized && rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("content type = %q body=%s", rec.Header().Get("Content-Type"), rec.Body.String())
			}
		})
	}
}

func TestPublicProtocolValidatesConfiguredDevelopmentBearer(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{Auth: NewAuth(nil, AuthConfig{DevBypass: true, DevAPIToken: "local-secret"})})
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for token, want := range map[string]int{"wrong": http.StatusUnauthorized, "local-secret": http.StatusNoContent} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("token %q status = %d, want %d body=%s", token, rec.Code, want, rec.Body.String())
		}
	}
}

func TestPublicListCursorsAreSignedBoundAndUnwrapped(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets?limit=1", nil)
	recorder := httptest.NewRecorder()
	buffer := apiprotocol.NewResponseBuffer(recorder, req)
	buffer.Header().Set("Content-Type", "application/json")
	_, _ = buffer.Write([]byte(`{"items":[{"id":"a"}],"page":{"nextCursor":"raw-row-id"}}`))
	buffer.Flush()
	var response struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || !strings.HasPrefix(response.Page.NextCursor, "g1.") {
		t.Fatalf("signed cursor response=%s err=%v", recorder.Body.String(), err)
	}

	server := newAppTestHarness(fakeMetrics{})
	seen := ""
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("pageToken")
		w.WriteHeader(http.StatusNoContent)
	}))
	next := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets?limit=1&pageToken="+url.QueryEscape(response.Page.NextCursor), nil)
	next.Header.Set("Authorization", "Bearer token")
	nextRec := httptest.NewRecorder()
	handler.ServeHTTP(nextRec, next)
	if nextRec.Code != http.StatusNoContent || seen != "raw-row-id" {
		t.Fatalf("cursor unwrap status=%d seen=%q body=%s", nextRec.Code, seen, nextRec.Body.String())
	}

	cross := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/other/assets?limit=1&pageToken="+url.QueryEscape(response.Page.NextCursor), nil)
	cross.Header.Set("Authorization", "Bearer token")
	crossRec := httptest.NewRecorder()
	handler.ServeHTTP(crossRec, cross)
	if crossRec.Code != http.StatusBadRequest {
		t.Fatalf("cross-resource cursor status=%d body=%s", crossRec.Code, crossRec.Body.String())
	}
}

func TestPublicListCursorRejectsUnavailableServingSnapshot(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{WorkspaceRepo: apiSnapshotWorkspaceRepository{summary: workspace.Summary{ID: "sales", ActiveServingStateID: "state-new"}}}))
	first := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets?limit=1", nil)
	first.Header.Set(apiprotocol.CursorSnapshotHeader, "state-old")
	cursor := apiprotocol.SignPageCursor(first, "last-asset")
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	next := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets?limit=1&pageToken="+url.QueryEscape(cursor), nil)
	next.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, next)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "SNAPSHOT_UNAVAILABLE") {
		t.Fatalf("snapshot change status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicListCursorSurvivesServerRestartFromDurableKeyRing(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets?limit=1", nil)
	cursor := apiprotocol.SignPageCursor(request, "asset-a")
	if err := cursorsigning.Configure("transient", map[string][]byte{"transient": bytes.Repeat([]byte{9}, 32)}); err != nil {
		t.Fatal(err)
	}
	server = assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{}))
	handler := server.publicProtocolMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("pageToken"); got != "asset-a" {
			t.Fatalf("unwrapped cursor = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	next := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/assets?limit=1&pageToken="+url.QueryEscape(cursor), nil)
	next.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, next)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
