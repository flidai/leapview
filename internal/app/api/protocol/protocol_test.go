package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	apiidempotencysqlite "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite"
)

func TestBuildConstructsProtocolPersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	protocol, err := Build(t.Context(), Config{Database: store.SQLDB()})
	if err != nil {
		t.Fatal(err)
	}
	if protocol.store == nil {
		t.Fatal("API protocol did not construct its idempotency store")
	}
}

func TestAdversarialIdempotencyNeverStoresOneTimeCredentials(t *testing.T) {
	status, header, body := safeIdempotencyResponse(http.StatusCreated, http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"token":"plaintext-secret","id":"x"}`))
	if status != http.StatusConflict || header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("safe response = %d %#v", status, header)
	}
	if strings.Contains(string(body), "plaintext-secret") {
		t.Fatal("plaintext credential persisted in replay representation")
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestAdversarialDurableIdempotencyDatabaseAndBackupExcludeOneTimeCredential(t *testing.T) {
	ctx := t.Context()
	databasePath := filepath.Join(t.TempDir(), "idempotency.db")
	store, err := platform.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	protocol, err := Build(ctx, Config{Database: store.SQLDB(), BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true }, ReplayAuthorize: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "one-time-plaintext-secret"
	handler := protocol.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"` + secret + `","id":"created"}`))
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/principals", strings.NewReader(`{"email":"user@example.com"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "secret-response")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("first response = %d %s", recorder.Code, recorder.Body.String())
	}
	var storedBody []byte
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT response_body FROM api_idempotency_records`).Scan(&storedBody); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(storedBody), secret) {
		t.Fatal("one-time credential persisted in idempotency database")
	}
	backupPath := filepath.Join(t.TempDir(), "idempotency-backup.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(backupBytes), secret) {
		t.Fatal("one-time credential persisted in database backup")
	}
}

func TestAdversarialIdempotencyAllowsNonSecretReplay(t *testing.T) {
	status, _, body := safeIdempotencyResponse(http.StatusCreated, http.Header{}, []byte(`{"id":"x","name":"ok"}`))
	if status != http.StatusCreated || string(body) != `{"id":"x","name":"ok"}` {
		t.Fatalf("safe response changed non-secret body")
	}
}

func TestAdversarialReplayReauthorizesCurrentCredentialAndGrants(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var allowed atomic.Bool
	allowed.Store(true)
	p, err := Build(t.Context(), Config{Database: store.SQLDB(), BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true }, ReplayAuthorize: func(*http.Request) bool { return allowed.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/groups", strings.NewReader(`{"name":"x"}`))
		r.Header.Set("Authorization", "Bearer credential")
		r.Header.Set("Idempotency-Key", "replay-auth")
		rec := httptest.NewRecorder()
		p.Middleware(next).ServeHTTP(rec, r)
		return rec
	}
	if got := request().Code; got != http.StatusOK {
		t.Fatalf("first status = %d", got)
	}
	allowed.Store(false)
	if got := request().Code; got != http.StatusForbidden {
		t.Fatalf("revoked/reduced-grant replay status = %d", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestAdversarialInMemoryReplayReauthorizesCurrentCredentialAndGrants(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	p, err := Build(t.Context(), Config{BearerToken: func(*http.Request) string { return "credential" }, AcceptsBearer: func(*http.Request) bool { return true }, ReplayAuthorize: func(*http.Request) bool { return allowed.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/groups", strings.NewReader(`{"name":"x"}`))
		r.Header.Set("Authorization", "Bearer credential")
		r.Header.Set("Idempotency-Key", "replay-auth-memory")
		rec := httptest.NewRecorder()
		p.Middleware(next).ServeHTTP(rec, r)
		return rec
	}
	if got := request().Code; got != http.StatusOK {
		t.Fatalf("first status = %d", got)
	}
	allowed.Store(false)
	if got := request().Code; got != http.StatusForbidden {
		t.Fatalf("revoked/reduced-grant replay status = %d", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestAdversarialIdempotencyCanonicalizesEquivalentBearerHeaders(t *testing.T) {
	bearer := func(r *http.Request) string {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			return fields[1]
		}
		return ""
	}
	p, err := Build(t.Context(), Config{BearerToken: bearer, AcceptsBearer: func(*http.Request) bool { return true }, PrincipalID: func(*http.Request) (string, bool) { return "principal", true }})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"created"}`))
	})
	request := func(authorization string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/groups", strings.NewReader(`{"name":"x"}`))
		r.Header.Set("Authorization", authorization)
		r.Header.Set("Idempotency-Key", "canonical-bearer")
		rec := httptest.NewRecorder()
		p.Middleware(next).ServeHTTP(rec, r)
		return rec
	}
	if got := request("Bearer credential").Code; got != http.StatusCreated {
		t.Fatalf("first status = %d", got)
	}
	replay := request("bearer   credential")
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay = %d headers=%#v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestAdversarialReplaySanitizesLegacyCredentialRecord(t *testing.T) {
	recorder := httptest.NewRecorder()
	replayStoredIdempotentResponse(recorder, http.StatusCreated, http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"token":"legacy-plaintext-secret","id":"created"}`))
	if recorder.Code != http.StatusConflict || strings.Contains(recorder.Body.String(), "legacy-plaintext-secret") {
		t.Fatalf("legacy replay = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdversarialLeaseLossCancelsHandlerAndQuarantinesOutcome(t *testing.T) {
	store := &fakeIdempotencyStore{record: apiidempotencysqlite.Record{State: "pending", Digest: "digest", Owner: "owner", LeaseGeneration: 1, LeaseExpires: time.Now().Add(time.Second)}, execute: true}
	store.renew = func() (time.Time, error) { return time.Time{}, apiidempotencysqlite.ErrLeaseLost }
	p := testLeaseProtocol(store, 50*time.Millisecond, 2*time.Millisecond)

	handlerCancelled := make(chan struct{})
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(handlerCancelled)
	})
	recorder := invokeIdempotentProtocol(p, next, "lease-loss")
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "IDEMPOTENCY_LEASE_LOST") {
		t.Fatalf("lease loss response = %d %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-handlerCancelled:
	default:
		t.Fatal("handler context was not cancelled")
	}
	if store.completeCalls.Load() != 0 || store.markCalls.Load() != 1 {
		t.Fatalf("complete=%d mark=%d", store.completeCalls.Load(), store.markCalls.Load())
	}
	if p.LeaseRenewalError() == nil {
		t.Fatal("lease loss did not fail readiness")
	}
}

func TestAdversarialTransientRenewalFailureRecoversBeforeExpiry(t *testing.T) {
	store := &fakeIdempotencyStore{record: apiidempotencysqlite.Record{State: "pending", Digest: "digest", Owner: "owner", LeaseGeneration: 1, LeaseExpires: time.Now().Add(time.Second)}, execute: true}
	var renewals atomic.Int32
	store.renew = func() (time.Time, error) {
		if renewals.Add(1) == 1 {
			return time.Time{}, errors.New("database temporarily busy")
		}
		return time.Now().Add(100 * time.Millisecond), nil
	}
	p := testLeaseProtocol(store, 100*time.Millisecond, 2*time.Millisecond)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	})
	recorder := invokeIdempotentProtocol(p, next, "transient-renewal")
	if recorder.Code != http.StatusCreated || store.completeCalls.Load() != 1 || store.markCalls.Load() != 0 {
		t.Fatalf("response=%d complete=%d mark=%d", recorder.Code, store.completeCalls.Load(), store.markCalls.Load())
	}
	if p.LeaseRenewalError() != nil {
		t.Fatal("transient renewal failure poisoned readiness")
	}
}

type fakeIdempotencyStore struct {
	mu            sync.Mutex
	record        apiidempotencysqlite.Record
	execute       bool
	renew         func() (time.Time, error)
	completeCalls atomic.Int32
	markCalls     atomic.Int32
}

func (s *fakeIdempotencyStore) Claim(_ context.Context, _ string, digest, owner string, lease, _ time.Duration) (apiidempotencysqlite.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record.Digest = digest
	s.record.Owner = owner
	s.record.LeaseExpires = time.Now().Add(lease)
	return s.record, s.execute, nil
}
func (s *fakeIdempotencyStore) Load(context.Context, string) (apiidempotencysqlite.Record, error) {
	return s.record, nil
}
func (s *fakeIdempotencyStore) Renew(context.Context, string, string, string, int64, time.Duration) (time.Time, error) {
	return s.renew()
}
func (s *fakeIdempotencyStore) Complete(context.Context, string, string, string, int64, int, http.Header, []byte) error {
	s.completeCalls.Add(1)
	return nil
}
func (s *fakeIdempotencyStore) Abandon(context.Context, string, string, string, int64) error {
	return nil
}
func (s *fakeIdempotencyStore) MarkIndeterminate(context.Context, string, string, string, int64) error {
	s.markCalls.Add(1)
	return nil
}

func testLeaseProtocol(store idempotencyStore, lease, renewEvery time.Duration) *Protocol {
	return &Protocol{
		config: Config{
			BearerToken:   func(*http.Request) string { return "credential" },
			AcceptsBearer: func(*http.Request) bool { return true },
		},
		store: store, idempotency: map[string]*apiIdempotencyRecord{}, lease: lease, renewEvery: renewEvery,
	}
}

func invokeIdempotentProtocol(p *Protocol, next http.Handler, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/groups", strings.NewReader(`{"name":"x"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	p.Middleware(next).ServeHTTP(recorder, request)
	return recorder
}
