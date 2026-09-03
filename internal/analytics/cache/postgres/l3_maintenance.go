package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	cachedb "github.com/flidai/leapview/internal/analytics/cache/postgres/internal/db"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) AcquireL3ObjectFence(ctx context.Context, in AcquireL3ObjectFenceInput) (L3ObjectFence, error) {
	if r == nil {
		return L3ObjectFence{}, ErrInvalid
	}
	return acquireL3ObjectFence(ctx, r.db, in)
}

func (m *Maintenance) AcquireL3ObjectFence(ctx context.Context, in AcquireL3ObjectFenceInput) (L3ObjectFence, error) {
	return acquireL3ObjectFence(ctx, mDB(m), in)
}

func acquireL3ObjectFence(ctx context.Context, db DBTX, in AcquireL3ObjectFenceInput) (L3ObjectFence, error) {
	if db == nil || platformdigest.ValidateSHA256Identity(in.StorageSecurityDomain) != nil ||
		!literal(in.ObjectKey, 2048) || !literal(in.OwnerID, 255) || in.Lease <= 0 || in.Lease > maxLeaseDuration {
		return L3ObjectFence{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := uuid.NewV7()
	if err != nil {
		return L3ObjectFence{}, err
	}
	row, err := cachedb.New(db).AcquireL3ObjectFence(ctx, cachedb.AcquireL3ObjectFenceParams{
		LeaseID: dbUUID(id), StorageSecurityDomain: in.StorageSecurityDomain, ObjectKey: in.ObjectKey,
		OwnerID: in.OwnerID, LeaseMicroseconds: in.Lease.Microseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return L3ObjectFence{}, ErrBusy
	}
	if err != nil {
		return L3ObjectFence{}, err
	}
	if !row.LeaseID.Valid || !row.ExpiresAt.Valid || !row.AcquiredAt.Valid {
		return L3ObjectFence{}, ErrBusy
	}
	return L3ObjectFence{LeaseID: row.LeaseID.Bytes, StorageSecurityDomain: row.StorageSecurityDomain,
		ObjectKey: row.ObjectKey, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch,
		ExpiresAt: row.ExpiresAt.Time.UTC(), AcquiredAt: row.AcquiredAt.Time.UTC()}, nil
}

func (r *Repository) RenewL3ObjectFence(ctx context.Context, fence L3ObjectFence, duration time.Duration) error {
	if r == nil {
		return ErrInvalid
	}
	return renewL3ObjectFence(ctx, r.db, fence, duration)
}

func (m *Maintenance) RenewL3ObjectFence(ctx context.Context, fence L3ObjectFence, duration time.Duration) error {
	return renewL3ObjectFence(ctx, mDB(m), fence, duration)
}

func renewL3ObjectFence(ctx context.Context, db DBTX, fence L3ObjectFence, duration time.Duration) error {
	if db == nil || validL3ObjectFence(fence) != nil || duration <= 0 || duration > maxLeaseDuration {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := cachedb.New(db).RenewL3ObjectFence(ctx, cachedb.RenewL3ObjectFenceParams{
		LeaseID: dbUUID(fence.LeaseID), StorageSecurityDomain: fence.StorageSecurityDomain,
		ObjectKey: fence.ObjectKey, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch,
		LeaseMicroseconds: duration.Microseconds(),
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}

func (r *Repository) ReleaseL3ObjectFence(ctx context.Context, fence L3ObjectFence) error {
	if r == nil {
		return ErrInvalid
	}
	return releaseL3ObjectFence(ctx, r.db, fence)
}

func (m *Maintenance) ReleaseL3ObjectFence(ctx context.Context, fence L3ObjectFence) error {
	return releaseL3ObjectFence(ctx, mDB(m), fence)
}

func releaseL3ObjectFence(ctx context.Context, db DBTX, fence L3ObjectFence) error {
	if db == nil || validL3ObjectFence(fence) != nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := cachedb.New(db).ReleaseL3ObjectFence(ctx, cachedb.ReleaseL3ObjectFenceParams{
		LeaseID: dbUUID(fence.LeaseID), StorageSecurityDomain: fence.StorageSecurityDomain,
		ObjectKey: fence.ObjectKey, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch,
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}

func validL3ObjectFence(fence L3ObjectFence) error {
	if fence.LeaseID == uuid.Nil || platformdigest.ValidateSHA256Identity(fence.StorageSecurityDomain) != nil ||
		!literal(fence.ObjectKey, 2048) || !literal(fence.OwnerID, 255) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	return nil
}

func (m *Maintenance) AcquireL3GCLease(ctx context.Context, storageDomain, ownerID string, ttl time.Duration) (L3GCLease, error) {
	if m == nil || m.db == nil || platformdigest.ValidateSHA256Identity(storageDomain) != nil ||
		!literal(ownerID, 255) || ttl <= 0 || ttl > maxLeaseDuration {
		return L3GCLease{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := uuid.NewV7()
	if err != nil {
		return L3GCLease{}, err
	}
	row, err := cachedb.New(m.db).AcquireL3GCLease(ctx, cachedb.AcquireL3GCLeaseParams{
		LeaseID: dbUUID(id), StorageSecurityDomain: storageDomain, OwnerID: ownerID, LeaseMicroseconds: ttl.Microseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return L3GCLease{}, ErrBusy
	}
	if err != nil {
		return L3GCLease{}, err
	}
	if !row.LeaseID.Valid || !row.ExpiresAt.Valid || !row.AcquiredAt.Valid || row.Cycle <= 0 {
		return L3GCLease{}, ErrBusy
	}
	return L3GCLease{LeaseID: row.LeaseID.Bytes, StorageSecurityDomain: row.StorageSecurityDomain,
		OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, CursorObjectKey: row.CursorObjectKey,
		Cycle: row.Cycle, ExpiresAt: row.ExpiresAt.Time.UTC(), AcquiredAt: row.AcquiredAt.Time.UTC()}, nil
}

func (m *Maintenance) RenewL3GCLease(ctx context.Context, lease L3GCLease, ttl time.Duration) error {
	if m == nil || m.db == nil || validL3GCLease(lease) != nil || ttl <= 0 || ttl > maxLeaseDuration {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := cachedb.New(m.db).RenewL3GCLease(ctx, cachedb.RenewL3GCLeaseParams{
		LeaseID: dbUUID(lease.LeaseID), StorageSecurityDomain: lease.StorageSecurityDomain,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch, LeaseMicroseconds: ttl.Microseconds(),
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}

func (m *Maintenance) ReleaseL3GCLease(ctx context.Context, lease L3GCLease) error {
	if m == nil || m.db == nil || validL3GCLease(lease) != nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := cachedb.New(m.db).ReleaseL3GCLease(ctx, cachedb.ReleaseL3GCLeaseParams{
		LeaseID: dbUUID(lease.LeaseID), StorageSecurityDomain: lease.StorageSecurityDomain,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}

func (m *Maintenance) AdvanceL3GCCursor(ctx context.Context, lease L3GCLease, next string, complete bool) error {
	if m == nil || m.db == nil || validL3GCLease(lease) != nil ||
		(complete && next != "") || (!complete && !literal(next, 2048)) {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := cachedb.New(m.db).AdvanceL3GCCursor(ctx, cachedb.AdvanceL3GCCursorParams{
		LeaseID: dbUUID(lease.LeaseID), StorageSecurityDomain: lease.StorageSecurityDomain,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch, NextObjectKey: next, Complete: complete,
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}

func validL3GCLease(lease L3GCLease) error {
	if lease.LeaseID == uuid.Nil || platformdigest.ValidateSHA256Identity(lease.StorageSecurityDomain) != nil ||
		!literal(lease.OwnerID, 255) || lease.FencingEpoch <= 0 || lease.Cycle <= 0 ||
		(lease.CursorObjectKey != "" && !literal(lease.CursorObjectKey, 2048)) {
		return ErrInvalid
	}
	return nil
}

// PrepareL3ObjectGC tombstones every terminal manifest for one exact object
// and returns true only when no live manifest or retention root remains in the
// physical-pool security domain. It is deliberately maintenance-only.
func (m *Maintenance) PrepareL3ObjectGC(ctx context.Context, fence L3ObjectFence) (bool, error) {
	if m == nil || m.db == nil || validL3ObjectFence(fence) != nil {
		return false, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	eligible, err := cachedb.New(m.db).PrepareL3ObjectGC(ctx, cachedb.PrepareL3ObjectGCParams{
		LeaseID: dbUUID(fence.LeaseID), StorageSecurityDomain: fence.StorageSecurityDomain,
		ObjectKey: fence.ObjectKey, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch,
	})
	if err != nil {
		if strings.Contains(err.Error(), "stale object fence") {
			return false, ErrStaleFence
		}
		return false, err
	}
	return eligible, nil
}
