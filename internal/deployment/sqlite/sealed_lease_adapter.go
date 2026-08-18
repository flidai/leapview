package sqlite

// This adapter connects the serving reader's narrow lease capability to the
// durable delivery_query_leases table. It resolves the requested candidate or
// generation and its verified seal in one control-plane read sequence before
// asking Repository to perform the lease CAS.

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/deployment"
)

type SealedCatalogLeaseAdapter struct {
	Repository *Repository
	Now        func() time.Time
}

var _ catalogartifact.LeaseRepository = SealedCatalogLeaseAdapter{}

func (a SealedCatalogLeaseAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a SealedCatalogLeaseAdapter) AcquireQueryLease(ctx context.Context, input catalogartifact.LeaseInput) (catalogartifact.QueryLease, error) {
	if a.Repository == nil {
		return catalogartifact.QueryLease{}, fmt.Errorf("sealed catalog lease repository is required")
	}
	if input.CreatedAt.IsZero() || input.ExpiresAt.IsZero() || input.CreatedAt.Location() != time.UTC || input.ExpiresAt.Location() != time.UTC || !input.ExpiresAt.After(input.CreatedAt) {
		return catalogartifact.QueryLease{}, fmt.Errorf("sealed catalog lease times are invalid")
	}
	if (input.CandidateID == "") == (input.GenerationID == "") {
		return catalogartifact.QueryLease{}, fmt.Errorf("sealed catalog lease must name one candidate or generation")
	}
	var candidate deployment.DeliveryCandidate
	if input.CandidateID != "" {
		var err error
		candidate, err = a.Repository.DeliveryCandidateByID(ctx, input.CandidateID)
		if err != nil {
			return catalogartifact.QueryLease{}, err
		}
	} else {
		generation, err := a.Repository.DeliveryGenerationByID(ctx, input.GenerationID)
		if err != nil {
			return catalogartifact.QueryLease{}, err
		}
		candidate, err = a.Repository.DeliveryCandidateByID(ctx, generation.CandidateID)
		if err != nil {
			return catalogartifact.QueryLease{}, err
		}
		if generation.CatalogDigest != input.CatalogDigest || generation.CatalogObjectKey != input.ObjectKey || generation.PhysicalPoolID != input.PhysicalPoolID {
			return catalogartifact.QueryLease{}, fmt.Errorf("%w: generation is not bound to exact catalog", deployment.ErrDeliveryConflict)
		}
	}
	if candidate.SealID != input.SealID || candidate.CatalogDigest != input.CatalogDigest || candidate.CatalogObjectKey != input.ObjectKey || candidate.PhysicalPoolID != input.PhysicalPoolID || candidate.QualificationDigest != input.QualificationDigest {
		return catalogartifact.QueryLease{}, fmt.Errorf("%w: candidate is not bound to exact catalog seal", deployment.ErrDeliveryConflict)
	}
	seal, err := a.Repository.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if err != nil {
		return catalogartifact.QueryLease{}, err
	}
	if seal.Status != deployment.CatalogSealVerified || seal.ID != input.SealID || seal.CatalogDigest != input.CatalogDigest || seal.ObjectKey != input.ObjectKey || seal.ObjectKey != catalogseal.CanonicalObjectKey(input.CatalogDigest) || seal.ObjectSize != input.ObjectSize || seal.PhysicalPoolID != input.PhysicalPoolID || seal.ClosureDigest != input.ClosureDigest || seal.QualificationDigest != input.QualificationDigest {
		return catalogartifact.QueryLease{}, fmt.Errorf("%w: control-plane seal evidence does not match reader", deployment.ErrDeliveryConflict)
	}
	lease, fence, err := a.Repository.AcquireQueryLeaseAgainstRoot(ctx, deployment.DeliveryQueryLease{ID: input.ID, HolderID: input.HolderID, CandidateID: input.CandidateID, GenerationID: input.GenerationID, CatalogDigest: input.CatalogDigest, PhysicalPoolID: input.PhysicalPoolID, CreatedAt: input.CreatedAt, ExpiresAt: input.ExpiresAt})
	if err != nil {
		return catalogartifact.QueryLease{}, err
	}
	// The fence is evidence that the root CAS ran against this physical pool.
	// Do not reject a successful/idempotent acquire merely because a GC lease
	// wins immediately after the transaction commits; the query lease itself
	// is the durable protection for this reader.
	if lease.ID != input.ID || lease.HolderID != input.HolderID || lease.CandidateID != input.CandidateID || lease.GenerationID != input.GenerationID || lease.CatalogDigest != input.CatalogDigest || lease.PhysicalPoolID != input.PhysicalPoolID || !lease.CreatedAt.Equal(input.CreatedAt) || !lease.ExpiresAt.Equal(input.ExpiresAt) || lease.Status != deployment.DeliveryLeaseActive || fence.PhysicalPoolID != input.PhysicalPoolID {
		return catalogartifact.QueryLease{}, fmt.Errorf("%w: fenced lease identity is not exact", deployment.ErrDeliveryConflict)
	}
	return catalogartifact.QueryLease{ID: lease.ID}, nil
}

func (a SealedCatalogLeaseAdapter) ReleaseQueryLease(ctx context.Context, id string) error {
	if a.Repository == nil {
		return fmt.Errorf("sealed catalog lease repository is required")
	}
	_, err := a.Repository.ReleaseQueryLease(ctx, id, a.now())
	return err
}

// RenewQueryLease extends one exact active durable root. The repository CAS
// rejects released, expired, or otherwise replaced leases.
func (a SealedCatalogLeaseAdapter) RenewQueryLease(ctx context.Context, id string, expiresAt time.Time) error {
	if a.Repository == nil {
		return fmt.Errorf("sealed catalog lease repository is required")
	}
	now := a.now()
	if expiresAt.Location() != time.UTC || !expiresAt.After(now) {
		return fmt.Errorf("sealed catalog lease expiry is invalid")
	}
	_, err := a.Repository.HeartbeatQueryLease(ctx, id, now, expiresAt)
	return err
}
