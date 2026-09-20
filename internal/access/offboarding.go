package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// OwnedObject is a durable object whose lifecycle is controlled by an owner
// principal.  Ownership is intentionally represented by the owning domain,
// rather than inferred from a role binding or a display field.
type OwnedObject struct {
	Kind                string `json:"kind"`
	ID                  string `json:"id"`
	Name                string `json:"name,omitempty"`
	OwnerPrincipalID    string `json:"ownerPrincipalId"`
	Lifecycle           string `json:"lifecycle"`
	Transferable        bool   `json:"transferable"`
	TombstoneOnOffboard bool   `json:"tombstoneOnOffboard"`
}

// OwnershipReport is the read model used by administrator offboarding.  It
// is a snapshot from the canonical ownership authority and is safe to expose
// because it contains no credentials or secret material.
type OwnershipReport struct {
	PrincipalID string        `json:"principalId"`
	Objects     []OwnedObject `json:"objects"`
}

var (
	// ErrOwnershipConflict means an offboarding operation would strand a live
	// object.  Callers should transfer or explicitly tombstone every object
	// before retrying deletion.
	ErrOwnershipConflict = errors.New("principal owns live objects")
	// ErrOffboardingUnavailable is returned when the owning domain has not
	// supplied an ownership authority.  Deletion must fail closed in that case.
	ErrOffboardingUnavailable = errors.New("principal ownership authority is unavailable")
)

// OwnershipConflictError retains the exact ownership evidence that blocked a
// destructive lifecycle operation.  Unwrap keeps HTTP/status mapping stable
// while allowing administrators to inspect object-level evidence.
type OwnershipConflictError struct {
	Report OwnershipReport
}

func (e *OwnershipConflictError) Error() string {
	if e == nil {
		return ErrOwnershipConflict.Error()
	}
	return fmt.Sprintf("%s: %d owned object(s) remain for principal %q", ErrOwnershipConflict, len(e.Report.Objects), e.Report.PrincipalID)
}

func (e *OwnershipConflictError) Unwrap() error { return ErrOwnershipConflict }

// OwnershipDBTX is the read-only database shape used to bind an ownership
// inventory to the caller-owned offboarding transaction. It deliberately
// contains no Begin/Commit/Rollback methods.
type OwnershipDBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// OwnershipAuthority is implemented by each owning domain's durable access
// adapter. The access lifecycle calls it before revoking a principal or
// service principal; it never guesses ownership by enumerating unrelated
// tables or mutable authorization grants.
type OwnershipAuthority interface {
	ListOwnedObjects(ctx context.Context, principalID string) (OwnershipReport, error)
}

// OwnershipMutator is the optional, domain-owned lifecycle surface used to
// resolve ownership before principal offboarding. Implementations must make
// transfer and tombstone operations idempotent and must use the database
// handle supplied through TransactionalOwnershipMutator when one is bound.
// Access deliberately does not know the tables or child rows belonging to a
// product object.
type OwnershipMutator interface {
	OwnershipAuthority
	TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (OwnershipReport, error)
	TombstoneOwnedObjects(ctx context.Context, principalID string) (OwnershipReport, error)
}

// TransactionalOwnershipMutator is the transaction-bound form used by the
// access lifecycle. The returned adapter must not begin, commit, or roll back
// the supplied handle.
type TransactionalOwnershipMutator interface {
	OwnershipMutator
	WithOwnershipMutationDB(OwnershipDBTX) OwnershipMutator
}

// OwnershipMutationRepository is implemented by an access authority that can
// run one ownership resolution across all configured product authorities in
// the same transaction as the principal lifecycle change.
type OwnershipMutationRepository interface {
	TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (OwnershipReport, error)
	TombstoneOwnedObjects(ctx context.Context, principalID string) (OwnershipReport, error)
}

// OwnershipGuard is the composed read-only guard used by identity lifecycle
// code. Keeping EnsureOffboardingSafe on the composition boundary means each
// domain adapter only reports its own objects and cannot make policy decisions
// about another domain.
type OwnershipGuard interface {
	OwnershipAuthority
	EnsureOffboardingSafe(ctx context.Context, principalID string) error
}

// TransactionalOwnershipAuthority can rebind a read-only adapter to the
// caller-owned transaction used for principal deletion. Implementations must
// return a new adapter and must not begin, commit, or roll back that handle.
type TransactionalOwnershipAuthority interface {
	OwnershipAuthority
	WithOwnershipDB(OwnershipDBTX) OwnershipAuthority
}

// TransactionalOwnershipGuard is the transaction-aware form of OwnershipGuard
// used to close the inspection/deletion race in PostgreSQL.
type TransactionalOwnershipGuard interface {
	OwnershipGuard
	WithOwnershipDB(OwnershipDBTX) OwnershipGuard
}
