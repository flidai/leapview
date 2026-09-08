// Package operation defines the storage-neutral operation capability consumed
// by native refresh admission. The composition adapter lives under
// internal/app/refreshpostgres and projects the shared platform authority into
// this narrow contract.
package operation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Tx is the native PostgreSQL transaction surface required by the shared
// operation authority. Capability callers retain transaction ownership.
type Tx = pgx.Tx

type Status string

const (
	Acquired      Status = "acquired"
	Replay        Status = "replay"
	Busy          Status = "busy"
	Indeterminate Status = "indeterminate"
)

type AcquireInput struct {
	Scope, OperationType, IdempotencyKey string
	RequestDigest, OwnerID               string
	Lease, Retention                     time.Duration
}

type Record struct {
	Scope, OperationType, IdempotencyKey string
	RequestDigest, OperationID, OwnerID  string
	State                                string
	FencingGeneration                    int64
	LeaseExpiresAt                       time.Time
	Outcome                              json.RawMessage
}

type Lease struct {
	Scope, IdempotencyKey, OperationID, OwnerID string
	FencingGeneration                           int64
	LeaseExpiresAt                              time.Time
}

type AcquireResult struct {
	Status    Status
	Operation Record
	Lease     Lease
	Replay    bool
}

type Authority interface {
	AcquireTx(context.Context, Tx, AcquireInput) (AcquireResult, error)
	CompleteTx(context.Context, Tx, Lease, json.RawMessage) error
	Get(context.Context, string, string) (Record, error)
}
