// Package idempotency defines the narrow HTTP idempotency capability used by
// protocol handlers. Implementations may be backed by PostgreSQL, SQLite
// fixtures, or an in-process test double; the protocol does not select a
// database engine.
package idempotency

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Record is the replay material and lease metadata for one request identity.
// Implementations must return copies of Body and Header so callers cannot
// mutate durable state through a loaded value.
type Record struct {
	State           string
	Digest          string
	Owner           string
	OwnerSession    string
	LeaseExpires    time.Time
	LeaseGeneration int64
	Status          int
	Header          http.Header
	Body            []byte
}

// Store is the engine-neutral port consumed by the public HTTP protocol.
// Claim returns execute=true only for the worker that owns a new lease. A
// duplicate pending request returns execute=false and may be awaited through
// Load. Generic Claim implementations bind an external attempt (or quarantine
// in a fixture) when that lease expires; reviewed reentrant endpoints opt into
// ReclaimableStore. A terminal record is replayed exactly by the protocol.
type Store interface {
	Claim(context.Context, string, string, string, time.Duration, time.Duration) (Record, bool, error)
	Load(context.Context, string) (Record, error)
	Renew(context.Context, string, string, string, int64, time.Duration) (time.Time, error)
	Complete(context.Context, string, string, string, int64, int, http.Header, []byte) error
	MarkIndeterminate(context.Context, string, string, string, int64) error
}

// ReclaimableStore is the explicit opt-in surface for endpoints whose
// mutation is durably content-addressed/reentrant. Generic HTTP idempotency
// must use Store.Claim, which binds an external attempt and quarantines an
// expired lease instead of risking a duplicate mutation. Protocol assembly
// may call ClaimReclaimable only for a reviewed operation-ID allowlist.
type ReclaimableStore interface {
	Store
	ClaimReclaimable(context.Context, string, string, string, time.Duration, time.Duration) (Record, bool, error)
}

// Errors are capability-neutral and allow protocol handlers to preserve
// replay/conflict semantics without importing an implementation package.
var (
	ErrLeaseLost = errors.New("idempotency lease lost")
	ErrConflict  = errors.New("idempotency request conflict")
	ErrNotFound  = errors.New("idempotency record not found")
	ErrInvalid   = errors.New("invalid idempotency record")
)
