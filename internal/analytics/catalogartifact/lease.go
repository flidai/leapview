// Package catalogartifact contains the narrow control-plane contracts used
// while serving an immutable catalog artifact. It deliberately has no
// dependency on the reader or on deployment storage implementations.
package catalogartifact

import (
	"context"
	"time"
)

// LeaseInput binds a query root to one immutable catalog and control-plane
// generation. Exactly one of CandidateID and GenerationID must be set.
type LeaseInput struct {
	ID                  string
	HolderID            string
	CandidateID         string
	GenerationID        string
	SealID              string
	CatalogDigest       string
	ObjectKey           string
	ObjectSize          int64
	ClosureDigest       string
	QualificationDigest string
	PhysicalPoolID      string
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

// QueryLease is returned by a target's durable lease adapter. ID must be
// stable across retries and Release must be idempotent.
type QueryLease struct {
	ID string
}

// LeaseRepository is the only control-plane capability used by a serving
// reader. Acquire serializes with catalog retirement; release occurs only
// after the read-only environment has closed.
type LeaseRepository interface {
	AcquireQueryLease(context.Context, LeaseInput) (QueryLease, error)
	ReleaseQueryLease(context.Context, string) error
}

// LeaseRenewer is an optional extension implemented by durable adapters that
// support long-running reads. Readers renew before expiry and stop the
// heartbeat before detaching, so a lease cannot lapse while DuckDB is still
// accessing the immutable root.
type LeaseRenewer interface {
	RenewQueryLease(context.Context, string, time.Time) error
}
