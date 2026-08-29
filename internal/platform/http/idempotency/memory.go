package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxMemoryRecords = 4096
	maxMemoryScope   = 4096
	maxMemoryBody    = 32768
	maxMemoryLease   = 24 * time.Hour
	maxMemoryLife    = 365 * 24 * time.Hour
)

// MemoryStore is an explicit, bounded-process idempotency capability for
// profile-only assemblies and unit tests. Production multi-node deployments
// must inject the PostgreSQL adapter instead.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{records: make(map[string]Record)} }

func (s *MemoryStore) Claim(ctx context.Context, scope, digest, owner string, lease, lifetime time.Duration) (Record, bool, error) {
	if s == nil {
		return Record{}, false, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]Record)
	}
	digest, ok := normalizeMemoryDigest(digest)
	if scope == "" || strings.TrimSpace(scope) != scope || len(scope) > maxMemoryScope || !ok || owner == "" || strings.TrimSpace(owner) != owner || len(owner) > 255 {
		return Record{}, false, ErrInvalid
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	if lifetime <= 0 {
		lifetime = 24 * time.Hour
	}
	if lease < time.Microsecond || lease > maxMemoryLease || lifetime < time.Microsecond || lifetime > maxMemoryLife {
		return Record{}, false, ErrInvalid
	}
	now := time.Now().UTC()
	if existing, ok := s.records[scope]; ok {
		if existing.State == "pending" && !existing.LeaseExpires.After(now) {
			existing.State = "indeterminate"
			existing.Status = http.StatusConflict
			existing.Header = http.Header{"Content-Type": []string{"application/problem+json"}}
			existing.Body = []byte(`{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and will not be executed again"}`)
			s.records[scope] = existing
		}
		return cloneRecord(existing), false, nil
	}
	if len(s.records) >= maxMemoryRecords {
		return Record{}, false, ErrInvalid
	}
	record := Record{State: "pending", Digest: digest, Owner: owner, LeaseGeneration: 1, LeaseExpires: now.Add(lease)}
	s.records[scope] = record
	return cloneRecord(record), true, nil
}

func (s *MemoryStore) Load(_ context.Context, scope string) (Record, error) {
	if s == nil {
		return Record{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[scope]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (s *MemoryStore) Renew(_ context.Context, scope, digest, owner string, generation int64, lease time.Duration) (time.Time, error) {
	if s == nil {
		return time.Time{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest, okDigest := normalizeMemoryDigest(digest)
	record, ok := s.records[scope]
	if !okDigest || !ok || record.Digest != digest || record.Owner != owner || record.LeaseGeneration != generation || record.State != "pending" || !record.LeaseExpires.After(time.Now()) {
		return time.Time{}, ErrLeaseLost
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	if lease < time.Microsecond || lease > maxMemoryLease {
		return time.Time{}, ErrInvalid
	}
	record.LeaseExpires = time.Now().UTC().Add(lease)
	s.records[scope] = record
	return record.LeaseExpires, nil
}

func (s *MemoryStore) Complete(_ context.Context, scope, digest, owner string, generation int64, status int, header http.Header, body []byte) error {
	if s == nil {
		return ErrInvalid
	}
	if status < 100 || status > 999 || len(body) > maxMemoryBody {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest, okDigest := normalizeMemoryDigest(digest)
	record, ok := s.records[scope]
	if !okDigest || !ok || record.Digest != digest || record.Owner != owner || record.LeaseGeneration != generation || record.State != "pending" || !record.LeaseExpires.After(time.Now()) {
		return ErrLeaseLost
	}
	record.State, record.Status = "completed", status
	record.Header, record.Body = header.Clone(), append([]byte(nil), body...)
	s.records[scope] = record
	return nil
}

func (s *MemoryStore) MarkIndeterminate(_ context.Context, scope, digest, owner string, generation int64) error {
	if s == nil {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest, okDigest := normalizeMemoryDigest(digest)
	record, ok := s.records[scope]
	if !okDigest || !ok || record.Digest != digest || record.Owner != owner || record.LeaseGeneration != generation || record.State != "pending" || !record.LeaseExpires.After(time.Now()) {
		return ErrLeaseLost
	}
	record.State = "indeterminate"
	record.Status = http.StatusConflict
	record.Header = http.Header{"Content-Type": []string{"application/problem+json"}}
	record.Body = []byte(`{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and will not be executed again"}`)
	s.records[scope] = record
	return nil
}

func cloneRecord(record Record) Record {
	record.Header = record.Header.Clone()
	record.Body = append([]byte(nil), record.Body...)
	return record
}

func normalizeMemoryDigest(digest string) (string, bool) {
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

var _ Store = (*MemoryStore)(nil)
