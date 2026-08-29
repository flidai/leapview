// Package postgres adapts the operation capability to the narrow HTTP
// idempotency port. The operation repository remains the sole PostgreSQL
// authority for leases, fencing, retention, and replay outcomes.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/http/idempotency"
	operation "github.com/flidai/leapview/internal/platform/operation/postgres"
)

const (
	operationScopePrefix = "http:"
	operationKey         = "request"
	maxScopeBytes        = 4096
	maxResponseBytes     = 32768
)

// DBTX is the native pgx surface accepted by operation.Repository.
type DBTX = operation.DBTX

// Store is a PostgreSQL-backed implementation of idempotency.Store.
type Store struct {
	repo *operation.Repository
}

var _ idempotency.Store = (*Store)(nil)

func NewStore(db DBTX) *Store {
	if db == nil {
		return &Store{}
	}
	return &Store{repo: operation.New(db)}
}

func NewStoreWithConfig(db DBTX, lease, retention time.Duration) *Store {
	if db == nil {
		return &Store{}
	}
	return &Store{repo: operation.NewWithConfig(db, lease, retention)}
}

func (s *Store) Claim(ctx context.Context, scope, digest, owner string, lease, lifetime time.Duration) (idempotency.Record, bool, error) {
	if s == nil || s.repo == nil {
		return idempotency.Record{}, false, idempotency.ErrNotFound
	}
	opScope, opKey, err := operationIdentity(scope)
	if err != nil {
		return idempotency.Record{}, false, err
	}
	digest, ok := normalizeDigest(digest)
	if !ok || strings.TrimSpace(owner) != owner || owner == "" || len(owner) > 255 {
		return idempotency.Record{}, false, operation.ErrInvalid
	}
	result, err := s.repo.AcquireWithAttempt(ctx, operation.AcquireInput{
		Scope: opScope, OperationType: "http_idempotency", IdempotencyKey: opKey,
		RequestDigest: "sha256:" + strings.ToLower(digest), OwnerID: owner,
		Lease: lease, Retention: lifetime,
	}, operationAttemptIdentity(owner, opScope))
	if errors.Is(err, operation.ErrBusy) {
		record, recordErr := recordFromOperation(result.Operation)
		return record, false, recordErr
	}
	if errors.Is(err, operation.ErrConflict) {
		// Acquire returns no operation on a digest conflict in order to avoid
		// exposing a second request's state. Load solely to preserve the HTTP
		// protocol's exact 409 conflict response.
		if existing, getErr := s.repo.Get(ctx, opScope, opKey); getErr == nil {
			record, recordErr := recordFromOperation(existing)
			return record, false, recordErr
		}
	}
	if err != nil {
		return idempotency.Record{}, false, err
	}
	record, recordErr := recordFromOperation(result.Operation)
	if recordErr != nil {
		return idempotency.Record{}, false, recordErr
	}
	return record, result.Status == operation.StatusAcquired && !result.Replay && result.Lease.OperationID != "", nil
}

func (s *Store) Load(ctx context.Context, scope string) (idempotency.Record, error) {
	if s == nil || s.repo == nil {
		return idempotency.Record{}, idempotency.ErrNotFound
	}
	opScope, opKey, err := operationIdentity(scope)
	if err != nil {
		return idempotency.Record{}, err
	}
	op, err := s.repo.Get(ctx, opScope, opKey)
	if errors.Is(err, operation.ErrNotFound) {
		return idempotency.Record{}, idempotency.ErrNotFound
	}
	if err != nil {
		return idempotency.Record{}, err
	}
	return recordFromOperation(op)
}

func (s *Store) Renew(ctx context.Context, scope, digest, owner string, generation int64, lease time.Duration) (time.Time, error) {
	leaseToken, err := s.leaseFor(ctx, scope, digest, owner, generation)
	if err != nil {
		return time.Time{}, err
	}
	renewed, err := s.repo.RenewLease(ctx, leaseToken, lease)
	if err != nil {
		return time.Time{}, mapLeaseError(err)
	}
	return renewed.LeaseExpiresAt, nil
}

func (s *Store) Complete(ctx context.Context, scope, digest, owner string, generation int64, status int, header http.Header, body []byte) error {
	if status < 100 || status > 999 || len(body) > maxResponseBytes {
		return operation.ErrInvalid
	}
	leaseToken, err := s.leaseFor(ctx, scope, digest, owner, generation)
	if err != nil {
		return err
	}
	canonical, err := encodeOutcome(status, header, body)
	if err != nil {
		return err
	}
	return mapLeaseError(s.repo.Complete(ctx, leaseToken, canonical))
}

func (s *Store) MarkIndeterminate(ctx context.Context, scope, digest, owner string, generation int64) error {
	leaseToken, err := s.leaseFor(ctx, scope, digest, owner, generation)
	if err != nil {
		return err
	}
	// The HTTP protocol does not carry external attempt identity. Bind one to
	// the exact owner/fence so a reconciliation-capable caller can still fence
	// this operation without inventing a second identity.
	if leaseToken.AttemptID == "" {
		identity := operationAttemptIdentity(owner, leaseToken.Scope)
		attempt, beginErr := s.repo.BeginAttempt(ctx, operation.BeginAttemptInput{Lease: leaseToken, AttemptIdentity: identity})
		if beginErr != nil {
			return mapLeaseError(beginErr)
		}
		leaseToken = attempt.Lease
	}
	return mapLeaseError(s.repo.MarkIndeterminate(ctx, leaseToken, []byte(`{"source":"http_protocol"}`)))
}

func (s *Store) leaseFor(ctx context.Context, scope, digest, owner string, generation int64) (operation.Lease, error) {
	digest, ok := normalizeDigest(digest)
	if s == nil || s.repo == nil || !ok || strings.TrimSpace(owner) != owner || owner == "" || generation <= 0 {
		return operation.Lease{}, operation.ErrInvalid
	}
	opScope, opKey, err := operationIdentity(scope)
	if err != nil {
		return operation.Lease{}, err
	}
	op, err := s.repo.Get(ctx, opScope, opKey)
	if errors.Is(err, operation.ErrNotFound) {
		return operation.Lease{}, idempotency.ErrNotFound
	}
	if err != nil {
		return operation.Lease{}, err
	}
	if op.RequestDigest != "sha256:"+strings.ToLower(digest) || op.OwnerID != owner || op.FencingGeneration != generation {
		return operation.Lease{}, idempotency.ErrLeaseLost
	}
	return operation.Lease{Scope: op.Scope, IdempotencyKey: op.IdempotencyKey, OperationID: op.OperationID, OwnerID: op.OwnerID, FencingGeneration: op.FencingGeneration, LeaseExpiresAt: op.LeaseExpiresAt, AttemptID: op.AttemptID, AttemptIdentity: op.AttemptIdentity}, nil
}

func operationIdentity(scope string) (string, string, error) {
	if scope == "" || strings.TrimSpace(scope) != scope || len(scope) > maxScopeBytes {
		return "", "", operation.ErrInvalid
	}
	// Keep the full HTTP scope out of operation columns. The digest is
	// deterministic and bounded while preserving independent identities for
	// principal/credential/path/key combinations.
	sum := sha256.Sum256([]byte(scope))
	return operationScopePrefix + hex.EncodeToString(sum[:]), operationKey, nil
}

func operationAttemptIdentity(owner, scope string) string {
	sum := sha256.Sum256([]byte(owner + ":" + scope))
	return "http:" + hex.EncodeToString(sum[:])
}

type outcome struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

func encodeOutcome(status int, header http.Header, body []byte) (json.RawMessage, error) {
	headers := make(map[string][]string, len(header))
	for key, values := range header {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonicalKey == "" || len(canonicalKey) > 256 || len(values) > 64 {
			return nil, operation.ErrInvalid
		}
		if _, exists := headers[canonicalKey]; exists {
			return nil, operation.ErrInvalid
		}
		copyValues := append([]string(nil), values...)
		for _, value := range copyValues {
			if len(value) > 4096 {
				return nil, operation.ErrInvalid
			}
		}
		headers[canonicalKey] = copyValues
	}
	encoded := outcome{Status: status, Headers: headers, Body: base64.RawStdEncoding.EncodeToString(body)}
	value, err := json.Marshal(encoded)
	if err != nil || len(value) > maxResponseBytes {
		return nil, operation.ErrInvalid
	}
	return value, nil
}

func recordFromOperation(op operation.Operation) (idempotency.Record, error) {
	record := idempotency.Record{State: string(op.State), Digest: strings.TrimPrefix(op.RequestDigest, "sha256:"), Owner: op.OwnerID, LeaseExpires: op.LeaseExpiresAt, LeaseGeneration: op.FencingGeneration}
	if op.State == operation.StateIndeterminate {
		record.Status = http.StatusConflict
		record.Header = http.Header{"Content-Type": []string{"application/problem+json"}}
		record.Body = append([]byte(nil), operation.UnknownOutcome...)
		return record, nil
	}
	if op.State != operation.StateCompleted && op.State != operation.StateFailed {
		return record, nil
	}
	var stored outcome
	if err := json.Unmarshal(op.Outcome, &stored); err != nil {
		return idempotency.Record{}, idempotency.ErrInvalid
	}
	if stored.Status < 100 || stored.Status > 999 {
		return idempotency.Record{}, idempotency.ErrInvalid
	}
	record.Status = stored.Status
	record.Header = make(http.Header, len(stored.Headers))
	for key, values := range stored.Headers {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonicalKey == "" || len(canonicalKey) > 256 || len(values) > 64 {
			return idempotency.Record{}, idempotency.ErrInvalid
		}
		if _, exists := record.Header[canonicalKey]; exists {
			return idempotency.Record{}, idempotency.ErrInvalid
		}
		record.Header[canonicalKey] = append([]string(nil), values...)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(stored.Body)
	if err != nil || len(decoded) > maxResponseBytes {
		return idempotency.Record{}, idempotency.ErrInvalid
	}
	record.Body = decoded
	return record, nil
}

func normalizeDigest(digest string) (string, bool) {
	digest = strings.TrimSpace(digest)
	if strings.HasPrefix(digest, "sha256:") {
		digest = strings.TrimPrefix(digest, "sha256:")
	}
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return "", false
	}
	_, err := hex.DecodeString(digest)
	return digest, err == nil
}

func mapLeaseError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, operation.ErrStaleFence), errors.Is(err, operation.ErrLeaseExpired), errors.Is(err, operation.ErrAlreadyTerminal):
		return idempotency.ErrLeaseLost
	case errors.Is(err, operation.ErrNotFound):
		return idempotency.ErrNotFound
	default:
		return err
	}
}
