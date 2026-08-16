package protocol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	cursorsigningsqlite "github.com/flidai/leapview/internal/platform/http/cursorsigning/sqlite"
	apiidempotencysqlite "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type Config struct {
	Database        *sql.DB
	BearerToken     func(*http.Request) string
	AcceptsBearer   func(*http.Request) bool
	PrincipalID     func(*http.Request) (string, bool)
	ReplayAuthorize func(*http.Request) bool
	PublicRequest   func(*http.Request) bool
	CursorSnapshot  func(*http.Request) string
	ProductName     string
}

type Protocol struct {
	config      Config
	store       idempotencyStore
	mu          sync.Mutex
	idempotency map[string]*apiIdempotencyRecord
	lease       time.Duration
	renewEvery  time.Duration
	leaseFailed atomic.Bool
}

type idempotencyStore interface {
	Claim(context.Context, string, string, string, time.Duration, time.Duration) (apiidempotencysqlite.Record, bool, error)
	Load(context.Context, string) (apiidempotencysqlite.Record, error)
	Renew(context.Context, string, string, string, int64, time.Duration) (time.Time, error)
	Complete(context.Context, string, string, string, int64, int, http.Header, []byte) error
	Abandon(context.Context, string, string, string, int64) error
	MarkIndeterminate(context.Context, string, string, string, int64) error
}

func (p *Protocol) SetReplayAuthorize(authorize func(*http.Request) bool) {
	if p != nil {
		p.config.ReplayAuthorize = authorize
	}
}

func Build(ctx context.Context, config Config) (*Protocol, error) {
	var store idempotencyStore
	if config.Database != nil {
		if err := cursorsigningsqlite.Configure(ctx, config.Database); err != nil {
			return nil, err
		}
		store = apiidempotencysqlite.NewStore(config.Database)
	}
	return &Protocol{config: config, store: store, idempotency: map[string]*apiIdempotencyRecord{}, lease: IdempotencyLease, renewEvery: IdempotencyLease / 3}, nil
}

func (p *Protocol) LeaseRenewalError() error {
	if p != nil && p.leaseFailed.Load() {
		return errors.New("idempotency lease renewal is unhealthy")
	}
	return nil
}

type apiIdempotencyRecord struct {
	digest  string
	ready   chan struct{}
	expires time.Time
	status  int
	header  http.Header
	body    []byte
}

const apiCursorLifetime = 15 * time.Minute
const IdempotencyLifetime = 24 * time.Hour
const IdempotencyLease = 30 * time.Second
const maxInMemoryIdempotencyRecords = 4096

type apiCursor struct {
	Value    string `json:"value"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Expires  int64  `json:"expires"`
}

const CursorSnapshotHeader = "X-LeapView-Cursor-Snapshot"

func (p *Protocol) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p != nil && p.config.PublicRequest != nil && p.config.PublicRequest(r) {
			PrepareRequest(w, r)
			next.ServeHTTP(w, r)
			return
		}
		if !p.Authenticate(w, r) {
			return
		}
		r.Header.Set(CursorSnapshotHeader, p.cursorSnapshot(r))
		if !unwrapAPIPageCursor(w, r) {
			return
		}
		if !requiresAPIIdempotency(r) {
			next.ServeHTTP(w, r)
			return
		}
		p.serveIdempotent(w, r, next)
	})
}

func (p *Protocol) Authenticate(w http.ResponseWriter, r *http.Request) bool {
	PrepareRequest(w, r)
	if p == nil || p.config.BearerToken == nil || p.config.BearerToken(r) == "" {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "BEARER_REQUIRED", "The public API accepts bearer credentials only", nil)
		return false
	}
	if p.config.AcceptsBearer != nil && !p.config.AcceptsBearer(r) {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "INVALID_BEARER", "The bearer credential is invalid", nil)
		return false
	}
	return true
}

func PrepareRequest(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = apitransport.NewRequestID()
		r.Header.Set("X-Request-ID", requestID)
	}
	w.Header().Set("X-Request-ID", requestID)
}

func unwrapAPIPageCursor(w http.ResponseWriter, r *http.Request) bool {
	query := r.URL.Query()
	token := strings.TrimSpace(query.Get("pageToken"))
	if token == "" {
		return true
	}
	if hasNativeCursorPrefix(token) {
		return true
	}
	if !strings.HasPrefix(token, "g1.") {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CURSOR", "The page cursor is invalid or expired", nil)
		return false
	}
	payload, err := cursorsigning.Verify("g1", token)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CURSOR", "The page cursor is invalid or expired", nil)
		return false
	}
	var cursor apiCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Expires < time.Now().Unix() || cursor.Scope != apiCursorScope(r) {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CURSOR", "The page cursor is invalid or expired", nil)
		return false
	}
	if cursor.Snapshot != apiCursorSnapshotForRequest(r) {
		apitransport.WriteProblem(w, r, http.StatusConflict, "SNAPSHOT_UNAVAILABLE", "The serving snapshot bound to this cursor is no longer available", nil)
		return false
	}
	query.Set("pageToken", cursor.Value)
	r.URL.RawQuery = query.Encode()
	return true
}

func SignPageCursor(r *http.Request, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "g1.") || hasNativeCursorPrefix(value) {
		return value
	}
	payload, _ := json.Marshal(apiCursor{Value: value, Scope: apiCursorScope(r), Snapshot: apiCursorSnapshotForRequest(r), Expires: time.Now().Add(apiCursorLifetime).Unix()})
	return cursorsigning.Sign("g1", payload)
}

func hasNativeCursorPrefix(value string) bool {
	for _, prefix := range []string{"q1.", "q2.", "d1.", "d2.", "e1.", "s1."} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func apiCursorSnapshotForRequest(r *http.Request) string {
	if r != nil {
		if snapshot := strings.TrimSpace(r.Header.Get(CursorSnapshotHeader)); snapshot != "" {
			return snapshot
		}
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		for index, segment := range segments {
			if segment == "projects" && index+1 < len(segments) {
				return "project:" + segments[index+1] + ":unversioned"
			}
		}
	}
	return "instance"
}

func (p *Protocol) cursorSnapshot(r *http.Request) string {
	if p != nil && p.config.CursorSnapshot != nil {
		if snapshot := strings.TrimSpace(p.config.CursorSnapshot(r)); snapshot != "" {
			return snapshot
		}
	}
	return apiCursorSnapshotForRequest(r)
}

func apiCursorScope(r *http.Request) string {
	query := r.URL.Query()
	query.Del("pageToken")
	digest := sha256.Sum256([]byte(r.Method + "\n" + r.URL.Path + "\n" + query.Encode()))
	return hex.EncodeToString(digest[:])
}

func SignResponseCursor(r *http.Request, body []byte) []byte {
	if r == nil || IsQueryRequest(r) || len(body) == 0 {
		return body
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return body
	}
	page, ok := value["page"].(map[string]any)
	if !ok {
		return body
	}
	next, _ := page["nextCursor"].(string)
	signed := SignPageCursor(r, next)
	if signed == next {
		return body
	}
	page["nextCursor"] = signed
	encoded, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return append(encoded, '\n')
}

func requiresAPIIdempotency(r *http.Request) bool {
	if r == nil {
		return false
	}
	if contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(r.Method, r.URL.Path); ok {
		return contract.Command != nil && contract.Command.Idempotency == "required"
	}
	return false
}

func IsQueryRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost {
		return false
	}
	if contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(r.Method, r.URL.Path); ok {
		return contract.Kind == apiaggregate.GenOperationKindQuery
	}
	return false
}

func (p *Protocol) serveIdempotent(w http.ResponseWriter, r *http.Request, next http.Handler) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key must contain 1 to 200 characters", nil)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			apitransport.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "CONTENT_TOO_LARGE", "The request body exceeds the configured size limit", nil)
			return
		}
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "The request body could not be read", nil)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	digest := apiRequestDigest(r, body)
	callerScope := ""
	if p.config.PrincipalID != nil {
		callerScope, _ = p.config.PrincipalID(r)
	}
	credentialScope := canonicalCredentialScope(p.config, r)
	if callerScope == "" {
		scopeHash := sha256.Sum256([]byte(credentialScope))
		callerScope = hex.EncodeToString(scopeHash[:])
	}
	credentialHash := sha256.Sum256([]byte(credentialScope))
	scope := callerScope + ":" + hex.EncodeToString(credentialHash[:]) + ":" + r.Method + ":" + r.URL.EscapedPath() + ":" + key
	if p.store != nil {
		p.serveDurableIdempotent(w, r, next, scope, digest)
		return
	}

	p.mu.Lock()
	p.pruneInMemoryIdempotency(time.Now())
	if existing := p.idempotency[scope]; existing != nil {
		if existing.digest != digest {
			p.mu.Unlock()
			apitransport.WriteProblem(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different request", nil)
			return
		}
		ready := existing.ready
		p.mu.Unlock()
		if !p.replayAuthorized(r) {
			apitransport.WriteProblem(w, r, http.StatusForbidden, "IDEMPOTENCY_REPLAY_UNAUTHORIZED", "The current principal is not authorized to replay this request", nil)
			return
		}
		select {
		case <-ready:
			if !p.replayAuthorized(r) {
				apitransport.WriteProblem(w, r, http.StatusForbidden, "IDEMPOTENCY_REPLAY_UNAUTHORIZED", "The current principal is not authorized to replay this request", nil)
				return
			}
			replayIdempotentResponse(w, existing)
		case <-r.Context().Done():
			apitransport.WriteProblem(w, r, http.StatusRequestTimeout, "IDEMPOTENCY_WAIT_CANCELLED", "The original request did not finish before this request was cancelled", nil)
		}
		return
	}
	if len(p.idempotency) >= maxInMemoryIdempotencyRecords {
		p.mu.Unlock()
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_CAPACITY_EXHAUSTED", "Idempotency capacity is exhausted; configure durable platform storage", nil)
		return
	}
	record := &apiIdempotencyRecord{digest: digest, ready: make(chan struct{}), expires: time.Now().Add(IdempotencyLifetime)}
	p.idempotency[scope] = record
	p.mu.Unlock()

	capture := newProtocolResponseCapture()
	next.ServeHTTP(capture, r)
	record.status = capture.statusCode()
	record.header = capture.header.Clone()
	record.body = append([]byte(nil), capture.body.Bytes()...)
	record.status, record.header, record.body = safeIdempotencyResponse(record.status, record.header, record.body)
	close(record.ready)
	capture.flush(w)
}

func (p *Protocol) serveDurableIdempotent(w http.ResponseWriter, r *http.Request, next http.Handler, scope, digest string) {
	owner := apitransport.NewRequestID()
	record, execute, err := p.store.Claim(r.Context(), scope, digest, owner, p.lease, IdempotencyLifetime)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "IDEMPOTENCY_UNAVAILABLE", "Idempotency state is unavailable", nil)
		return
	}
	if record.Digest != digest {
		apitransport.WriteProblem(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different request", nil)
		return
	}
	if record.State == "indeterminate" {
		apitransport.WriteProblem(w, r, http.StatusConflict, "IDEMPOTENCY_OUTCOME_UNKNOWN", "The original request outcome is indeterminate and will not be executed again", nil)
		return
	}
	if !execute {
		if !p.replayAuthorized(r) {
			apitransport.WriteProblem(w, r, http.StatusForbidden, "IDEMPOTENCY_REPLAY_UNAUTHORIZED", "The current principal is not authorized to replay this request", nil)
			return
		}
		if record.Status == 0 {
			record, execute, err = waitForAPIIdempotency(r, p.store, scope, digest, owner)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					apitransport.WriteProblem(w, r, http.StatusRequestTimeout, "IDEMPOTENCY_WAIT_CANCELLED", "The original request did not finish before this request was cancelled", nil)
					return
				}
				apitransport.WriteProblem(w, r, http.StatusInternalServerError, "IDEMPOTENCY_UNAVAILABLE", "Idempotency state is unavailable", nil)
				return
			}
		}
		if record.Digest != digest {
			apitransport.WriteProblem(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different request", nil)
			return
		}
		if record.State == "indeterminate" {
			apitransport.WriteProblem(w, r, http.StatusConflict, "IDEMPOTENCY_OUTCOME_UNKNOWN", "The original request outcome is indeterminate and will not be executed again", nil)
			return
		}
		if !execute {
			if !p.replayAuthorized(r) {
				apitransport.WriteProblem(w, r, http.StatusForbidden, "IDEMPOTENCY_REPLAY_UNAUTHORIZED", "The current principal is not authorized to replay this request", nil)
				return
			}
			replayStoredIdempotentResponse(w, record.Status, record.Header, record.Body)
			return
		}
	}

	handlerCtx, cancelHandler := context.WithCancelCause(r.Context())
	defer cancelHandler(nil)
	leaseCtx, stopLease := context.WithCancel(context.Background())
	leaseLost := make(chan error, 1)
	go p.renewAPIIdempotencyLease(leaseCtx, scope, digest, owner, record.LeaseGeneration, record.LeaseExpires, func(err error) {
		p.leaseFailed.Store(true)
		cancelHandler(err)
		select {
		case leaseLost <- err:
		default:
		}
	})
	capture := newProtocolResponseCapture()
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		next.ServeHTTP(capture, r.Clone(handlerCtx))
	}()
	stopLease()
	record.Status = capture.statusCode()
	record.Header = capture.header.Clone()
	record.Body = append([]byte(nil), capture.body.Bytes()...)
	storedStatus, storedHeader, storedBody := safeIdempotencyResponse(record.Status, record.Header, record.Body)
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancelPersist()
	if panicValue != nil {
		_ = p.store.MarkIndeterminate(persistCtx, scope, digest, owner, record.LeaseGeneration)
		panic(panicValue)
	}
	select {
	case <-leaseLost:
		_ = p.store.MarkIndeterminate(persistCtx, scope, digest, owner, record.LeaseGeneration)
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_LEASE_LOST", "The request outcome is indeterminate after idempotency lease loss", nil)
		return
	default:
	}
	if record.Status >= http.StatusInternalServerError {
		if err := p.store.Abandon(persistCtx, scope, digest, owner, record.LeaseGeneration); err != nil {
			if errors.Is(err, apiidempotencysqlite.ErrLeaseLost) {
				p.leaseFailed.Store(true)
				_ = p.store.MarkIndeterminate(persistCtx, scope, digest, owner, record.LeaseGeneration)
			}
			apitransport.WriteProblem(w, r, http.StatusInternalServerError, "IDEMPOTENCY_UNAVAILABLE", "The failed request lease could not be released", nil)
			return
		}
		capture.flush(w)
		return
	}
	if err := p.store.Complete(persistCtx, scope, digest, owner, record.LeaseGeneration, storedStatus, storedHeader, storedBody); err != nil {
		if errors.Is(err, apiidempotencysqlite.ErrLeaseLost) {
			p.leaseFailed.Store(true)
		}
		_ = p.store.MarkIndeterminate(persistCtx, scope, digest, owner, record.LeaseGeneration)
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "IDEMPOTENCY_UNAVAILABLE", "The response could not be committed to durable idempotency state", nil)
		return
	}
	capture.flush(w)
}

func canonicalCredentialScope(config Config, r *http.Request) string {
	if config.BearerToken != nil {
		if token := strings.TrimSpace(config.BearerToken(r)); token != "" {
			return token
		}
	}
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return fields[1]
	}
	return strings.Join(fields, " ")
}

func (p *Protocol) replayAuthorized(r *http.Request) bool {
	return p.config.ReplayAuthorize == nil || p.config.ReplayAuthorize(r)
}

func (p *Protocol) pruneInMemoryIdempotency(now time.Time) {
	for scope, record := range p.idempotency {
		if now.Before(record.expires) {
			continue
		}
		select {
		case <-record.ready:
			delete(p.idempotency, scope)
		default:
		}
	}
}

func waitForAPIIdempotency(r *http.Request, store idempotencyStore, scope, digest, owner string) (apiidempotencysqlite.Record, bool, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := store.Load(r.Context(), scope)
		if err != nil {
			return apiidempotencysqlite.Record{}, false, err
		}
		if record.Digest != digest || record.Status != 0 || record.State != "pending" {
			return record, false, nil
		}
		select {
		case <-r.Context().Done():
			return apiidempotencysqlite.Record{}, false, r.Context().Err()
		case <-ticker.C:
		}
	}
}

func (p *Protocol) renewAPIIdempotencyLease(ctx context.Context, scope, digest, owner string, generation int64, deadline time.Time, lost func(error)) {
	ticker := time.NewTicker(p.renewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expires, err := p.store.Renew(ctx, scope, digest, owner, generation, p.lease)
			if err == nil {
				deadline = expires
				continue
			}
			if errors.Is(err, apiidempotencysqlite.ErrLeaseLost) || !time.Now().Before(deadline) {
				lost(err)
				return
			}
		}
	}
}

func apiRequestDigest(r *http.Request, body []byte) string {
	digest := sha256.New()
	_, _ = fmt.Fprintf(digest, "%s\n%s\n%s\n%s\n", r.Method, r.URL.EscapedPath(), r.URL.Query().Encode(), strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))))
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil))
}

func replayIdempotentResponse(w http.ResponseWriter, record *apiIdempotencyRecord) {
	replayStoredIdempotentResponse(w, record.status, record.header, record.body)
}

func replayStoredIdempotentResponse(w http.ResponseWriter, status int, header http.Header, body []byte) {
	status, header, body = safeIdempotencyResponse(status, header, body)
	for key, values := range header {
		if strings.EqualFold(key, "X-Request-ID") {
			if len(values) > 0 {
				w.Header().Set(key, values[len(values)-1])
			}
			continue
		}
		w.Header().Del(key)
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(status)
	if len(body) != 0 {
		_, _ = w.Write(body)
	}
}

// One-time credentials must never become durable replay material. Keep the
// original response for the first caller, but persist a deliberately
// non-replayable outcome whenever the JSON payload contains a secret field.
func safeIdempotencyResponse(status int, header http.Header, body []byte) (int, http.Header, []byte) {
	var value any
	if json.Unmarshal(body, &value) != nil || !containsCredentialField(value) {
		return status, header, body
	}
	outHeader := http.Header{"Content-Type": []string{"application/problem+json"}}
	outBody := []byte(`{"code":"IDEMPOTENCY_RESPONSE_NOT_REPLAYABLE","detail":"The original response contained a one-time credential and cannot be replayed"}`)
	return http.StatusConflict, outHeader, outBody
}

func containsCredentialField(value any) bool {
	secretNames := map[string]bool{"token": true, "access_token": true, "accessToken": true, "refresh_token": true, "refreshToken": true, "clientSecret": true, "client_secret": true, "secret": true, "password": true, "device_code": true, "deviceCode": true, "verification_uri_complete": true}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if secretNames[key] || containsCredentialField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsCredentialField(child) {
				return true
			}
		}
	}
	return false
}

type protocolResponseCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newProtocolResponseCapture() *protocolResponseCapture {
	return &protocolResponseCapture{header: http.Header{}}
}

func (w *protocolResponseCapture) Header() http.Header { return w.header }

func (w *protocolResponseCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *protocolResponseCapture) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(value)
}

func (w *protocolResponseCapture) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *protocolResponseCapture) flush(target http.ResponseWriter) {
	for key, values := range w.header {
		if strings.EqualFold(key, "X-Request-ID") {
			if len(values) > 0 {
				target.Header().Set(key, values[len(values)-1])
			}
			continue
		}
		target.Header().Del(key)
		for _, value := range values {
			target.Header().Add(key, value)
		}
	}
	target.WriteHeader(w.statusCode())
	if w.body.Len() != 0 {
		_, _ = target.Write(w.body.Bytes())
	}
}

func (p *Protocol) OpenAPIDescription(w http.ResponseWriter, r *http.Request) {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "OPENAPI_UNAVAILABLE", "The API description is unavailable", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(spec)
}

func (p *Protocol) PublicDocs(w http.ResponseWriter, _ *http.Request) {
	productName := strings.TrimSpace(p.config.ProductName)
	if productName == "" {
		productName = "Application"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>%s API</title></head><body><main><h1>%s API v1</h1><p><a href="/api/openapi.json">OpenAPI description</a></p></main></body></html>`, productName, productName)
}
