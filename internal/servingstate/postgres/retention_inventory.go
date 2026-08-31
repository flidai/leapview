package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingdb "github.com/flidai/leapview/internal/servingstate/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RetentionInventory is a read-only projection of delivery retention roots
// and serving-state reader leases for one exact delivery target scope. It is
// intentionally an observation DTO: delivery remains the sole owner of root
// lifecycle and this API performs no mutations or retention decisions.
type RetentionInventory struct {
	TargetID     string                   `json:"targetId"`
	Environment  servingstate.Environment `json:"environment"`
	Roots        []RetentionRoot          `json:"roots"`
	ReaderLeases []RetentionReaderLease   `json:"readerLeases"`
}

// RetentionRoot is delivery-owned retention-root evidence. Snapshot is
// resolved through the immutable delivery snapshot seal and is nil when the
// root is explicitly unbound (for example a recovery root).
type RetentionRoot struct {
	RootID         string `json:"rootId"`
	TargetID       string `json:"targetId"`
	Environment    string `json:"environment"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	CandidateID    string `json:"candidateId,omitempty"`
	GenerationID   string `json:"generationId,omitempty"`
	SnapshotSealID string `json:"snapshotSealId,omitempty"`
	// Snapshot is nil when this root is explicitly unbound from a snapshot
	// seal. Snapshot identity is represented once so consumers cannot observe
	// conflicting flattened and nested values.
	Snapshot  *RetentionSnapshotIdentity `json:"snapshot,omitempty"`
	ExpiresAt time.Time                  `json:"expiresAt,omitempty"`
	CreatedAt time.Time                  `json:"createdAt"`
	RetiredAt time.Time                  `json:"retiredAt,omitempty"`
	ExpiredAt time.Time                  `json:"expiredAt,omitempty"`
}

// RetentionReaderLease is serving-state reader-lease evidence. Lease state is
// derived at read time: released rows remain visible as released, unreleased
// rows past expiry remain visible as expired, and all other rows are active.
// Snapshot comes from the delivery snapshot seal joined through the lease
// generation, not from an untrusted caller-provided value.
type RetentionReaderLease struct {
	LeaseID        string                    `json:"leaseId"`
	GenerationID   string                    `json:"generationId"`
	Environment    string                    `json:"environment"`
	SnapshotSealID string                    `json:"snapshotSealId"`
	OwnerID        string                    `json:"ownerId"`
	AcquiredAt     time.Time                 `json:"acquiredAt"`
	ExpiresAt      time.Time                 `json:"expiresAt"`
	ReleasedAt     time.Time                 `json:"releasedAt,omitempty"`
	State          string                    `json:"state"`
	Snapshot       RetentionSnapshotIdentity `json:"snapshot"`
}

// RetentionSnapshotIdentity is immutable snapshot-seal evidence shared by
// roots and reader leases.
type RetentionSnapshotIdentity struct {
	SealID          string `json:"sealId"`
	SnapshotID      int64  `json:"snapshotId"`
	PhysicalPoolID  string `json:"physicalPoolId"`
	CatalogID       string `json:"catalogId"`
	CatalogDatabase string `json:"catalogDatabase"`
	CatalogUUID     string `json:"catalogUuid"`
}

// RetentionInventory reads all retention roots and reader leases in the exact
// target/environment scope. Results are deterministically ordered by the
// explicit ORDER BY clauses in the sqlc queries (root kind/id, then lease id).
func (r *Repository) RetentionInventory(ctx context.Context, targetID, environment string) (RetentionInventory, error) {
	if err := validateRetentionInventoryTarget(targetID); err != nil {
		return RetentionInventory{}, err
	}
	if err := servingstate.ValidateEnvironment(servingstate.Environment(environment)); err != nil {
		return RetentionInventory{}, err
	}
	db, err := r.dbOrErr()
	if err != nil {
		return RetentionInventory{}, err
	}
	ctx = contextOrBackground(ctx)
	// A repository built over a caller-owned transaction must observe that
	// transaction's snapshot. Standalone pools use a short repeatable-read,
	// read-only transaction so root and lease rows cannot straddle a cutover.
	if tx, ok := db.(pgx.Tx); ok {
		return readRetentionInventory(ctx, tx, targetID, environment)
	}
	b, ok := db.(interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	})
	if !ok {
		return RetentionInventory{}, errors.New("retention inventory requires a PostgreSQL transaction-capable database")
	}
	tx, err := b.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RetentionInventory{}, err
	}
	defer tx.Rollback(context.Background())
	inventory, err := readRetentionInventory(ctx, tx, targetID, environment)
	if err != nil {
		return RetentionInventory{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RetentionInventory{}, err
	}
	return inventory, nil
}

func readRetentionInventory(ctx context.Context, db DBTX, targetID, environment string) (RetentionInventory, error) {
	rootRows, err := querySet(db).ListRetentionRoots(ctx, servingdb.ListRetentionRootsParams{TargetID: targetID, Environment: environment})
	if err != nil {
		return RetentionInventory{}, err
	}
	leaseRows, err := querySet(db).ListReaderLeaseInventory(ctx, servingdb.ListReaderLeaseInventoryParams{TargetID: targetID, Environment: environment})
	if err != nil {
		return RetentionInventory{}, err
	}
	inventory := RetentionInventory{
		TargetID: targetID, Environment: servingstate.Environment(environment),
		Roots:        make([]RetentionRoot, 0, len(rootRows)),
		ReaderLeases: make([]RetentionReaderLease, 0, len(leaseRows)),
	}
	for _, row := range rootRows {
		var snapshot *RetentionSnapshotIdentity
		if row.DucklakeSnapshotID != nil {
			snapshot = &RetentionSnapshotIdentity{SealID: row.SnapshotSealID, SnapshotID: *row.DucklakeSnapshotID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, CatalogDatabase: row.CatalogDatabase, CatalogUUID: row.CatalogUuid}
		}
		inventory.Roots = append(inventory.Roots, RetentionRoot{
			RootID: row.RRootID, TargetID: row.TargetID, Environment: row.Environment,
			Kind: row.RootKind, State: row.State, CandidateID: row.CandidateID,
			GenerationID: row.GenerationID, SnapshotSealID: row.SnapshotSealID,
			Snapshot:  snapshot,
			ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: requiredTimestamp(row.CreatedAt),
			RetiredAt: optionalTimestamp(row.RetiredAt), ExpiredAt: optionalTimestamp(row.ExpiredAt),
		})
	}
	for _, row := range leaseRows {
		snapshot := RetentionSnapshotIdentity{SealID: row.SnapshotSealID, SnapshotID: row.DucklakeSnapshotID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, CatalogDatabase: row.CatalogDatabase, CatalogUUID: row.CatalogUuid}
		inventory.ReaderLeases = append(inventory.ReaderLeases, RetentionReaderLease{
			LeaseID: row.LeaseID, GenerationID: row.LGenerationID, Environment: row.Environment,
			SnapshotSealID: row.SnapshotSealID,
			OwnerID:        row.OwnerID, AcquiredAt: requiredTimestamp(row.AcquiredAt),
			ExpiresAt: requiredTimestamp(row.ExpiresAt), ReleasedAt: optionalTimestamp(row.ReleasedAt),
			State:    row.LeaseState,
			Snapshot: snapshot,
		})
	}
	return inventory, nil
}

func validateRetentionInventoryTarget(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 255 {
		return errors.New("retention inventory target id is invalid")
	}
	return nil
}

func optionalTimestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func requiredTimestamp(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}
