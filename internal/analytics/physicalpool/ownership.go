package physicalpool

import (
	"context"
	"errors"
	"time"
)

var ErrOwnershipConflict = errors.New("physical-pool namespace ownership conflict")
var ErrDeletionLeaseConflict = errors.New("physical-pool deletion lease conflict")

// OwnershipClaim is the marker written inside the physical namespace before
// admission grants deletion authority. It is deliberately non-secret and
// content-addressed to the exact pool/admission/control-plane owner.
type OwnershipClaim struct {
	PoolID              PoolID
	CompatibilityDigest string
	EvidenceDigest      string
	OwnerID             string
}

func (claim OwnershipClaim) Validate() error {
	if err := validateDigest(string(claim.PoolID)); err != nil {
		return err
	}
	if err := validateDigest(claim.CompatibilityDigest); err != nil {
		return err
	}
	if err := validateDigest(claim.EvidenceDigest); err != nil {
		return err
	}
	return validateCanonicalString(claim.OwnerID)
}

// NamespaceOwnership is implemented by local and object-backed stores. The
// conditional create must be performed against the physical namespace, not a
// per-instance metadata database.
type NamespaceOwnership interface {
	AcquireNamespaceOwnership(context.Context, OwnershipClaim) error
	VerifyNamespaceOwnership(context.Context, OwnershipClaim) error
}

// NamespaceDeletionLease is a short-lived physical-namespace fence layered
// over the stable ownership marker. It prevents two cloned metadata databases
// carrying the same instance identity from deleting concurrently.
type NamespaceDeletionLease interface {
	AcquireNamespaceDeletionLease(context.Context, string, time.Duration) (string, error)
	VerifyNamespaceDeletionLease(context.Context, string, string) error
	ReleaseNamespaceDeletionLease(context.Context, string, string) error
}

// NamespaceDeletionLeaseRepository is implemented by a control-plane
// authority that persists the short-lived namespace deletion fence. The
// physical object-store marker remains the proof of namespace ownership;
// this repository only serializes deletion across metadata writers.
type NamespaceDeletionLeaseRepository interface {
	NamespaceDeletionLease
}
