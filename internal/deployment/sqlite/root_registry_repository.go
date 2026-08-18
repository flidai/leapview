package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// DeliveryRoot is one complete catalog root. The registry deliberately keeps
// catalog object identity only; DuckLake remains authoritative for the files
// reachable from the catalog.
type DeliveryRoot = deployment.DeliveryRoot

// RootRecord is the input form for explicit quarantine/retention roots.
type RootRecord = DeliveryRoot

type RootSet = deployment.RootSet

func validateRoot(root DeliveryRoot) error {
	if err := deployment.ValidateDeliveryID(root.PhysicalPoolID); err != nil {
		return err
	}
	if err := deployment.ValidateDeliveryID(root.SourceID); err != nil {
		return fmt.Errorf("root source: %w", err)
	}
	switch root.Kind {
	case "candidate", "build", "published", "rollback", "retained", "quarantined", "lease":
	default:
		return fmt.Errorf("%w: unsupported root kind %q", deployment.ErrDeliveryInvalid, root.Kind)
	}
	if err := deployment.ValidateDeliveryDigest(root.CatalogDigest); err != nil {
		return err
	}
	if root.ObjectKey == "" || root.ObjectKey != stringsTrim(root.ObjectKey) || root.ObjectKey[0] == '/' || root.ObjectKey == "." || root.ObjectKey == ".." || stringsContainsUnsafeRootKey(root.ObjectKey) {
		return fmt.Errorf("%w: root object key must be canonical", deployment.ErrDeliveryInvalid)
	}
	if root.CreatedAt.IsZero() || root.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("%w: root creation time must be UTC", deployment.ErrDeliveryInvalid)
	}
	if !root.ExpiresAt.IsZero() && root.ExpiresAt.Location() != time.UTC {
		return fmt.Errorf("%w: root expiry time must be UTC", deployment.ErrDeliveryInvalid)
	}
	if !root.ExpiresAt.IsZero() && !root.ExpiresAt.After(root.CreatedAt) {
		return fmt.Errorf("%w: root expiry must be after creation", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func stringsContainsUnsafeRootKey(s string) bool {
	if (len(s) >= 2 && s[:2] == "./") || (len(s) >= 3 && s[:3] == "../") {
		return true
	}
	for _, bad := range []string{"//", "/./", "/../", "/.", "/..", "://"} {
		if stringsIndex(s, bad) >= 0 {
			return true
		}
	}
	return false
}

func stringsIndex(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func sqliteTimeAfter(value, now string) bool {
	v, err := parseDeliveryTime(value)
	if err != nil {
		return false
	}
	n, err := parseDeliveryTime(now)
	return err == nil && v.After(n)
}

// stringsTrim is kept local to this file so this adapter has no dependency on
// the normalization helpers used by the deployment contracts.
func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func (r *Repository) RegisterRoot(ctx context.Context, root DeliveryRoot) (DeliveryRoot, error) {
	if root.Status == "" {
		root.Status = "active"
	}
	if err := validateRoot(root); err != nil {
		return DeliveryRoot{}, err
	}
	if root.Status != "active" {
		return DeliveryRoot{}, fmt.Errorf("%w: new root must be active", deployment.ErrDeliveryInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryRoot{}, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, root.PhysicalPoolID); err != nil {
		return DeliveryRoot{}, err
	}
	now := deliveryTime(root.CreatedAt)
	// A GC lease owns the same fence and therefore excludes root creation.
	fence, err := deploydb.New(tx).GetDeliveryPoolFence(ctx, root.PhysicalPoolID)
	gcLease, gcExpiry := fence.GcLeaseID, ""
	if fence.GcExpiresAt.Valid {
		gcExpiry = fence.GcExpiresAt.String
	}
	if err != nil {
		return DeliveryRoot{}, err
	}
	if gcLease != "" && sqliteTimeAfter(gcExpiry, now) {
		return DeliveryRoot{}, fmt.Errorf("%w: GC lease excludes root creation", deployment.ErrDeliveryConflict)
	}
	res, err := deploydb.New(tx).CreateDeliveryRootRegistry(ctx, deploydb.CreateDeliveryRootRegistryParams{PhysicalPoolID: root.PhysicalPoolID, RootKind: root.Kind, SourceID: root.SourceID, CandidateID: presentString(root.CandidateID), GenerationID: presentString(root.GenerationID), LeaseID: presentString(root.LeaseID), CatalogDigest: root.CatalogDigest, ObjectKey: root.ObjectKey, CreatedAt: now, ExpiresAt: nullableRootTime(root.ExpiresAt)})
	if err != nil {
		return DeliveryRoot{}, fmt.Errorf("%w: register root: %v", deployment.ErrDeliveryConflict, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		existing, readErr := rootByKeyTx(ctx, tx, root.PhysicalPoolID, root.Kind, root.SourceID)
		if readErr != nil {
			return DeliveryRoot{}, readErr
		}
		if existing.Status == "active" && existing.CatalogDigest == root.CatalogDigest && existing.ObjectKey == root.ObjectKey && existing.CandidateID == root.CandidateID && existing.GenerationID == root.GenerationID && existing.LeaseID == root.LeaseID {
			if err := tx.Commit(); err != nil {
				return DeliveryRoot{}, err
			}
			return existing, nil
		}
		return DeliveryRoot{}, fmt.Errorf("%w: root identity is retired or differs", deployment.ErrDeliveryConflict)
	}
	if err := tx.Commit(); err != nil {
		return DeliveryRoot{}, err
	}
	return root, nil
}

func (r *Repository) CreateRoot(ctx context.Context, root DeliveryRoot) (DeliveryRoot, error) {
	return r.RegisterRoot(ctx, root)
}

func nullableRootTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: deliveryTime(t), Valid: true}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RetireRoot atomically removes one explicit root. A query lease which won
// the fence first prevents retirement; a retired root cannot be leased later.
func (r *Repository) RetireRoot(ctx context.Context, poolID, kind, sourceID string, now time.Time) (DeliveryRoot, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return DeliveryRoot{}, fmt.Errorf("%w: retirement time must be UTC", deployment.ErrDeliveryInvalid)
	}
	if err := deployment.ValidateDeliveryID(poolID); err != nil {
		return DeliveryRoot{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryRoot{}, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, poolID); err != nil {
		return DeliveryRoot{}, err
	}
	fence, err := deploydb.New(tx).GetDeliveryPoolFence(ctx, poolID)
	gcLease, gcExpiry := fence.GcLeaseID, ""
	if fence.GcExpiresAt.Valid {
		gcExpiry = fence.GcExpiresAt.String
	}
	if err != nil {
		return DeliveryRoot{}, err
	}
	if gcLease != "" && sqliteTimeAfter(gcExpiry, deliveryTime(now)) {
		return DeliveryRoot{}, fmt.Errorf("%w: GC lease excludes retirement", deployment.ErrDeliveryConflict)
	}
	root, err := rootByKeyTx(ctx, tx, poolID, kind, sourceID)
	if err != nil {
		return DeliveryRoot{}, err
	}
	activeLeases, err := deploydb.New(tx).CountActiveQueryLeasesForCatalog(ctx, deploydb.CountActiveQueryLeasesForCatalogParams{PhysicalPoolID: poolID, CatalogDigest: root.CatalogDigest, Julianday: deliveryTime(now)})
	if err != nil {
		return DeliveryRoot{}, err
	}
	if activeLeases != 0 {
		return DeliveryRoot{}, fmt.Errorf("%w: query lease protects root", deployment.ErrDeliveryConflict)
	}
	retired := deliveryTime(now)
	res, err := deploydb.New(tx).RetireDeliveryRootRegistry(ctx, deploydb.RetireDeliveryRootRegistryParams{RetiredAt: presentString(retired), PhysicalPoolID: poolID, RootKind: kind, SourceID: sourceID})
	if err != nil {
		return DeliveryRoot{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return DeliveryRoot{}, fmt.Errorf("%w: root retirement CAS failed", deployment.ErrDeliveryConflict)
	}
	if err := tx.Commit(); err != nil {
		return DeliveryRoot{}, err
	}
	root.Status = "retired"
	return root, nil
}

func rootByKeyTx(ctx context.Context, q deploydb.DBTX, poolID, kind, sourceID string) (DeliveryRoot, error) {
	row, err := deploydb.New(q).GetDeliveryRootRegistry(ctx, deploydb.GetDeliveryRootRegistryParams{PhysicalPoolID: poolID, RootKind: kind, SourceID: sourceID})
	if err != nil {
		return DeliveryRoot{}, err
	}
	root := DeliveryRoot{PhysicalPoolID: row.PhysicalPoolID, Kind: row.RootKind, SourceID: row.SourceID, CandidateID: row.CandidateID.String, GenerationID: row.GenerationID.String, LeaseID: row.LeaseID.String, CatalogDigest: row.CatalogDigest, ObjectKey: row.ObjectKey, Status: row.Status}
	root.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return DeliveryRoot{}, err
	}
	root.ExpiresAt, err = parseNullableDeliveryTime(row.ExpiresAt)
	return root, err
}

func (r *Repository) RootByKey(ctx context.Context, poolID, kind, sourceID string) (DeliveryRoot, error) {
	return rootByKeyTx(ctx, r.db, poolID, kind, sourceID)
}

// AcquireQueryLeaseAgainstRoot serializes root resolution, retirement, and
// lease insertion through one SQLite transaction. It intentionally performs
// the exact candidate/generation binding check instead of accepting a digest
// supplied by a caller without checking the lifecycle row.
func (r *Repository) AcquireQueryLeaseAgainstRoot(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, PoolFence, error) {
	lease, fence, err := r.acquireQueryLeaseAgainstRootOnce(ctx, input)
	if err == nil || !sqliteBusyError(err) {
		return lease, fence, err
	}
	// Re-read after rolling back the failed transaction. A concurrent
	// retirement may already have changed the candidate/generation; regardless
	// of which fence won, this attempt lost and must report a typed conflict.
	if input.CandidateID != "" {
		_, _ = deliveryCandidateByIDTx(ctx, r.db, input.CandidateID)
	} else if input.GenerationID != "" {
		_, _ = deliveryGenerationByIDTx(ctx, r.db, input.GenerationID)
	}
	return deployment.DeliveryQueryLease{}, PoolFence{}, fencingBusyConflict(err)
}

func (r *Repository) acquireQueryLeaseAgainstRootOnce(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, PoolFence, error) {
	lease, err := deployment.NewDeliveryQueryLease(input)
	if err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	if err := configureFencingTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	defer func() {
		restoreFencingTx(ctx, tx)
		_ = tx.Rollback()
	}()
	if err := r.ensurePoolFenceTx(ctx, tx, lease.PhysicalPoolID); err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	now := deliveryTime(lease.CreatedAt)
	// A GC lease wins the fence and rejects new roots. Expiry is reconciled in
	// this same transaction, so restart cannot accidentally open a stale root.
	fence, err := deploydb.New(tx).GetDeliveryPoolFence(ctx, lease.PhysicalPoolID)
	gcLease, gcExpiry := fence.GcLeaseID, ""
	if fence.GcExpiresAt.Valid {
		gcExpiry = fence.GcExpiresAt.String
	}
	if err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	if gcLease != "" && !sqliteTimeAfter(gcExpiry, now) {
		if err := deploydb.New(tx).ExpireDeliveryGCLeasesForPool(ctx, deploydb.ExpireDeliveryGCLeasesForPoolParams{ReleasedAt: presentString(now), PhysicalPoolID: lease.PhysicalPoolID, Julianday: now}); err != nil {
			return deployment.DeliveryQueryLease{}, PoolFence{}, err
		}
		if err := deploydb.New(tx).ClearExpiredDeliveryGCLeaseFence(ctx, deploydb.ClearExpiredDeliveryGCLeaseFenceParams{UpdatedAt: now, PhysicalPoolID: lease.PhysicalPoolID, Julianday: now}); err != nil {
			return deployment.DeliveryQueryLease{}, PoolFence{}, err
		}
		gcLease = ""
	}
	if gcLease != "" && sqliteTimeAfter(gcExpiry, now) {
		return deployment.DeliveryQueryLease{}, PoolFence{}, fmt.Errorf("%w: GC lease excludes query root", deployment.ErrDeliveryConflict)
	}
	if lease.CandidateID != "" {
		binding, err := deploydb.New(tx).GetCandidateCatalogBinding(ctx, lease.CandidateID)
		digest, pool, status := binding.CatalogDigest, binding.PhysicalPoolID, binding.Status
		if err != nil {
			return deployment.DeliveryQueryLease{}, PoolFence{}, err
		}
		if digest != lease.CatalogDigest || pool != lease.PhysicalPoolID || status != string(deployment.DeliveryCandidateReady) {
			return deployment.DeliveryQueryLease{}, PoolFence{}, fmt.Errorf("%w: candidate is not an exact query root", deployment.ErrDeliveryConflict)
		}
	} else {
		binding, err := deploydb.New(tx).GetGenerationCatalogBinding(ctx, lease.GenerationID)
		digest, pool, status := binding.CatalogDigest, binding.PhysicalPoolID, binding.Status
		if err != nil {
			return deployment.DeliveryQueryLease{}, PoolFence{}, err
		}
		if digest != lease.CatalogDigest || pool != lease.PhysicalPoolID || (status != string(deployment.DeliveryGenerationPrepared) && status != string(deployment.DeliveryGenerationActive)) {
			return deployment.DeliveryQueryLease{}, PoolFence{}, fmt.Errorf("%w: generation is not an exact query root", deployment.ErrDeliveryConflict)
		}
	}
	quarantined, err := deploydb.New(tx).CountQuarantinedDeliveryRoots(ctx, deploydb.CountQuarantinedDeliveryRootsParams{PhysicalPoolID: lease.PhysicalPoolID, CatalogDigest: lease.CatalogDigest})
	if err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	if quarantined != 0 {
		return deployment.DeliveryQueryLease{}, PoolFence{}, fmt.Errorf("%w: catalog is quarantined", deployment.ErrDeliveryConflict)
	}
	err = deploydb.New(tx).CreateDeliveryQueryLease(ctx, deploydb.CreateDeliveryQueryLeaseParams{ID: lease.ID, HolderID: lease.HolderID, NULLIF: lease.CandidateID, NULLIF_2: lease.GenerationID, CatalogDigest: lease.CatalogDigest, PhysicalPoolID: lease.PhysicalPoolID, ExpiresAt: deliveryTime(lease.ExpiresAt), CreatedAt: now})
	if err != nil {
		if existing, readErr := deliveryQueryLeaseByIDTx(ctx, tx, lease.ID); readErr == nil && sameQueryLeaseIdentity(existing, lease) {
			if eventErr := appendQueryLeaseEventTx(ctx, tx, existing, "lease_acquired", deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:"+existing.ID)), "accepted", existing.CreatedAt); eventErr != nil {
				return deployment.DeliveryQueryLease{}, PoolFence{}, eventErr
			}
			restoreFencingTx(ctx, tx)
			if err := tx.Commit(); err != nil {
				return deployment.DeliveryQueryLease{}, PoolFence{}, err
			}
			f, ferr := r.PoolFence(ctx, lease.PhysicalPoolID)
			return existing, f, ferr
		}
		return deployment.DeliveryQueryLease{}, PoolFence{}, fmt.Errorf("%w: query lease identity conflict", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, lease, "lease_acquired", deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:"+lease.ID)), "accepted", lease.CreatedAt); err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	fence, err = deploydb.New(tx).GetDeliveryPoolFence(ctx, lease.PhysicalPoolID)
	if err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	f := PoolFence{PhysicalPoolID: fence.PhysicalPoolID, WriterEpoch: fence.WriterEpoch, GCEpoch: fence.GcLeaseEpoch, RootRevision: fence.RootRevision, GCLeaseID: fence.GcLeaseID, GCHolderID: fence.GcHolderID}
	f.GCExpires, err = parseNullableDeliveryTime(fence.GcExpiresAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	restoreFencingTx(ctx, tx)
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, PoolFence{}, err
	}
	return lease, f, nil
}

func (r *Repository) AcquireFencedQueryLease(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, PoolFence, error) {
	return r.AcquireQueryLeaseAgainstRoot(ctx, input)
}

func (r *Repository) EnumerateRoots(ctx context.Context, poolID string, now time.Time) (RootSet, error) {
	return r.EnumerateRootsWithGrace(ctx, poolID, now, 0)
}

// EnumerateRootsWithGrace includes active and recently expired query leases
// until now minus readerGrace. Expiry marks the lease as no longer extendable,
// but a reader that already pinned the catalog may still be draining; GC must
// retain its complete closure for that bounded window. Explicitly released
// leases are never roots.
func (r *Repository) EnumerateRootsWithGrace(ctx context.Context, poolID string, now time.Time, readerGrace time.Duration) (RootSet, error) {
	if err := deployment.ValidateDeliveryID(poolID); err != nil {
		return RootSet{}, err
	}
	if now.IsZero() || now.Location() != time.UTC {
		return RootSet{}, fmt.Errorf("%w: root enumeration time must be UTC", deployment.ErrDeliveryInvalid)
	}
	if readerGrace < 0 {
		return RootSet{}, fmt.Errorf("%w: reader grace must not be negative", deployment.ErrDeliveryInvalid)
	}
	leaseCutoff := now.Add(-readerGrace)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RootSet{}, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, poolID); err != nil {
		return RootSet{}, err
	}
	revision, err := deploydb.New(tx).GetDeliveryRootRevision(ctx, poolID)
	if err != nil {
		return RootSet{}, err
	}
	roots, err := enumerateRootsTx(ctx, tx, poolID, now, leaseCutoff)
	if err != nil {
		return RootSet{}, err
	}
	stableRevision, err := deploydb.New(tx).GetDeliveryRootRevision(ctx, poolID)
	if err != nil {
		return RootSet{}, err
	}
	if stableRevision != revision {
		return RootSet{}, fmt.Errorf("%w: root revision changed during enumeration", deployment.ErrDeliveryStale)
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Kind == roots[j].Kind {
			return roots[i].SourceID < roots[j].SourceID
		}
		return roots[i].Kind < roots[j].Kind
	})
	if err := tx.Commit(); err != nil {
		return RootSet{}, err
	}
	return RootSet{PhysicalPoolID: poolID, Revision: revision, Roots: roots}, nil
}

// enumerateRootsTx resolves the complete durable root projection against the
// caller's transaction snapshot. Keeping this helper transaction-scoped lets
// mutation paths compare the exact root set and revision before promoting a
// read snapshot to a writer.
func enumerateRootsTx(ctx context.Context, tx *sql.Tx, poolID string, now time.Time, leaseCutoff time.Time) ([]DeliveryRoot, error) {
	rows, err := deploydb.New(tx).EnumerateDeliveryRootsExpanded(ctx, deploydb.EnumerateDeliveryRootsExpandedParams{
		PhysicalPoolID: poolID, PhysicalPoolID_2: poolID, PhysicalPoolID_3: poolID, PhysicalPoolID_4: poolID,
		Julianday: deliveryTime(now), PhysicalPoolID_5: poolID, PhysicalPoolID_6: poolID,
		Julianday_2: deliveryTime(now), PhysicalPoolID_7: poolID, Julianday_3: deliveryTime(now),
		PhysicalPoolID_8: poolID, Julianday_4: deliveryTime(leaseCutoff), Julianday_5: deliveryTime(now),
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var roots []DeliveryRoot
	for _, row := range rows {
		root := DeliveryRoot{PhysicalPoolID: row.PhysicalPoolID, Kind: row.RootKind, SourceID: row.SourceID, CandidateID: row.CandidateID, GenerationID: row.GenerationID, LeaseID: row.LeaseID, CatalogDigest: row.CatalogDigest, ObjectKey: row.ObjectKey, Status: row.Status}
		root.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return nil, err
		}
		var expires sql.NullString
		switch value := row.ExpiresAt.(type) {
		case string:
			expires = sql.NullString{String: value, Valid: value != ""}
		case []byte:
			expires = sql.NullString{String: string(value), Valid: len(value) != 0}
		}
		root.ExpiresAt, err = parseNullableDeliveryTime(expires)
		if err != nil {
			return nil, err
		}
		key := root.Kind + "\x00" + root.SourceID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	return roots, nil
}

// DeliveryRootCompatibilityDigest resolves the admitted compatibility tuple
// for one exact durable root through generated SQLC queries. Repair tooling
// uses this binding before constructing the target-owned read-only inspector;
// callers cannot substitute a pool contract from an arbitrary object key.
func (r *Repository) DeliveryRootCompatibilityDigest(ctx context.Context, root deployment.DeliveryRoot) (string, error) {
	if r == nil || r.db == nil || root.PhysicalPoolID == "" || root.Kind == "" || root.SourceID == "" {
		return "", fmt.Errorf("%w: root compatibility identity is incomplete", deployment.ErrDeliveryInvalid)
	}
	q := deploydb.New(r.db)
	var digest, poolID string
	var err error
	switch root.Kind {
	case "build":
		var row deploydb.GetDeliveryCatalogSealRow
		row, err = q.GetDeliveryCatalogSeal(ctx, root.SourceID)
		digest, poolID = row.CompatibilityDigest, row.PhysicalPoolID
	case "candidate":
		var row deploydb.GetDeliveryCandidateRow
		row, err = q.GetDeliveryCandidate(ctx, root.SourceID)
		digest, poolID = row.CompatibilityDigest, row.PhysicalPoolID
	case "published", "rollback":
		var row deploydb.GetDeliveryGenerationRow
		row, err = q.GetDeliveryGeneration(ctx, root.GenerationID)
		digest, poolID = row.CompatibilityDigest, row.PhysicalPoolID
	case "lease", "retained", "quarantined":
		if root.CandidateID != "" {
			var row deploydb.GetDeliveryCandidateRow
			row, err = q.GetDeliveryCandidate(ctx, root.CandidateID)
			digest, poolID = row.CompatibilityDigest, row.PhysicalPoolID
		} else if root.GenerationID != "" {
			var row deploydb.GetDeliveryGenerationRow
			row, err = q.GetDeliveryGeneration(ctx, root.GenerationID)
			digest, poolID = row.CompatibilityDigest, row.PhysicalPoolID
		} else {
			return "", fmt.Errorf("%w: root has no candidate or generation binding", deployment.ErrDeliveryInvalid)
		}
	default:
		return "", fmt.Errorf("%w: unsupported root kind %q", deployment.ErrDeliveryInvalid, root.Kind)
	}
	if err != nil {
		return "", err
	}
	if poolID != root.PhysicalPoolID || digest == "" {
		return "", fmt.Errorf("%w: root physical-pool compatibility binding changed", deployment.ErrDeliveryConflict)
	}
	return digest, nil
}

func (r *Repository) RootSet(ctx context.Context, poolID string, now time.Time) (RootSet, error) {
	return r.EnumerateRoots(ctx, poolID, now)
}

func (r *Repository) EnumerateRootSet(ctx context.Context, poolID string, now time.Time) (RootSet, error) {
	return r.EnumerateRoots(ctx, poolID, now)
}

func (r *Repository) RetireCatalogRoot(ctx context.Context, poolID, kind, sourceID string, now time.Time) (DeliveryRoot, error) {
	return r.RetireRoot(ctx, poolID, kind, sourceID, now)
}
