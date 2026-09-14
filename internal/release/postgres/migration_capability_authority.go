package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrMigrationCapabilityInvalid        = errors.New("invalid migration capability")
	ErrMigrationCapabilityNotFound       = errors.New("migration capability not found")
	ErrMigrationCapabilityConflict       = errors.New("migration capability conflict")
	ErrMigrationCapabilityDigestMismatch = errors.New("migration capability digest mismatch")
	ErrMigrationCapabilityArtifact       = errors.New("migration capability artifact admission is invalid")
)

// PublishMigrationCapability appends one maintenance-owned capability. Exact
// replays converge on the existing canonical record; no update path exists.
func (r *Repository) PublishMigrationCapability(ctx context.Context, capability migrationcapability.Capability) (migrationcapability.Capability, error) {
	if r == nil || r.db == nil {
		return migrationcapability.Capability{}, ErrMigrationCapabilityInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return migrationcapability.Capability{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var published migrationcapability.Capability
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		published, err = publishMigrationCapability(ctx, tx, capability)
		return err
	})
	return published, err
}

func publishMigrationCapability(ctx context.Context, tx pgx.Tx, capability migrationcapability.Capability) (migrationcapability.Capability, error) {
	document, err := capability.CanonicalJSON()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	canonical, err := migrationcapability.ParseCanonical(document)
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	digest, err := canonical.Digest()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	q := releasedb.New(tx)
	if err := verifyCapabilityArtifact(ctx, tx, q, canonical.ArtifactAdmissionDigest); err != nil {
		return migrationcapability.Capability{}, err
	}
	inserted, err := q.InsertMigrationCapability(ctx, releasedb.InsertMigrationCapabilityParams{
		ArtifactAdmissionDigest: canonical.ArtifactAdmissionDigest,
		TargetIdentityDigest:    canonical.TargetIdentityDigest,
		Subsystem:               string(canonical.Subsystem),
		OwnerIdentity:           canonical.Owner.Identity,
		OwnerContractVersion:    canonical.Owner.ContractVersion,
		CapabilityVersion:       canonical.Version,
		CapabilityDigest:        digest,
		CapabilityBytes:         document,
	})
	if err != nil {
		return migrationcapability.Capability{}, mapMigrationCapabilityDatabaseError(err)
	}
	row, err := q.GetMigrationCapability(ctx, releasedb.GetMigrationCapabilityParams{
		ArtifactAdmissionDigest: canonical.ArtifactAdmissionDigest,
		TargetIdentityDigest:    canonical.TargetIdentityDigest,
		Subsystem:               string(canonical.Subsystem),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if inserted == 0 {
			return migrationcapability.Capability{}, ErrMigrationCapabilityConflict
		}
		return migrationcapability.Capability{}, ErrMigrationCapabilityNotFound
	}
	if err != nil {
		return migrationcapability.Capability{}, err
	}
	stored, err := readMigrationCapabilityRow(row)
	if err != nil {
		return migrationcapability.Capability{}, err
	}
	if !bytes.Equal(row.CapabilityBytes, document) {
		return migrationcapability.Capability{}, ErrMigrationCapabilityConflict
	}
	return stored, nil
}

// ResolveMigrationCapability implements migrationcapability.Authority. It
// performs exact content-addressed lookup and revalidates both canonical bytes
// and the referenced OCI admission on every read.
func (r *Repository) ResolveMigrationCapability(ctx context.Context, artifactAdmissionDigest, targetIdentityDigest string, subsystem migrationcapability.Subsystem) (migrationcapability.Capability, error) {
	if r == nil || r.db == nil {
		return migrationcapability.Capability{}, ErrMigrationCapabilityInvalid
	}
	if platformdigest.ValidateSHA256Identity(artifactAdmissionDigest) != nil || platformdigest.ValidateSHA256Identity(targetIdentityDigest) != nil || !validMigrationCapabilitySubsystem(subsystem) {
		return migrationcapability.Capability{}, ErrMigrationCapabilityInvalid
	}
	q := releasedb.New(r.db)
	row, err := q.GetMigrationCapability(ctx, releasedb.GetMigrationCapabilityParams{
		ArtifactAdmissionDigest: artifactAdmissionDigest,
		TargetIdentityDigest:    targetIdentityDigest,
		Subsystem:               string(subsystem),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return migrationcapability.Capability{}, ErrMigrationCapabilityNotFound
	}
	if err != nil {
		return migrationcapability.Capability{}, err
	}
	capability, err := readMigrationCapabilityRow(row)
	if err != nil {
		return migrationcapability.Capability{}, err
	}
	if err := verifyCapabilityArtifact(ctx, r.db, q, artifactAdmissionDigest); err != nil {
		return migrationcapability.Capability{}, err
	}
	return capability, nil
}

func readMigrationCapabilityRow(row releasedb.ReleaseMigrationCapability) (migrationcapability.Capability, error) {
	capability, err := migrationcapability.ParseCanonical(row.CapabilityBytes)
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	digest, err := capability.Digest()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	if row.ArtifactAdmissionDigest != capability.ArtifactAdmissionDigest || row.TargetIdentityDigest != capability.TargetIdentityDigest ||
		row.Subsystem != string(capability.Subsystem) || row.OwnerIdentity != capability.Owner.Identity ||
		row.OwnerContractVersion != capability.Owner.ContractVersion || row.CapabilityVersion != capability.Version ||
		row.CapabilityDigest != digest {
		return migrationcapability.Capability{}, ErrMigrationCapabilityDigestMismatch
	}
	return capability, nil
}

func verifyCapabilityArtifact(ctx context.Context, db DBTX, q *releasedb.Queries, admissionDigest string) error {
	reference, err := q.GetOCIArtifactReferenceByAdmissionDigest(ctx, admissionDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrMigrationCapabilityArtifact, ErrArtifactAdmissionNotFound)
	}
	if err != nil {
		return err
	}
	identity, err := New(db).ResolveArtifact(ctx, reference)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMigrationCapabilityArtifact, err)
	}
	if identity.ArtifactAdmissionDigest != admissionDigest {
		return fmt.Errorf("%w: admission digest mismatch", ErrMigrationCapabilityArtifact)
	}
	return nil
}

func validMigrationCapabilitySubsystem(value migrationcapability.Subsystem) bool {
	switch value {
	case migrationcapability.SubsystemGoose, migrationcapability.SubsystemRiverJobs,
		migrationcapability.SubsystemDuckLake, migrationcapability.SubsystemPhysicalPool:
		return true
	default:
		return false
	}
}

func mapMigrationCapabilityDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrMigrationCapabilityConflict
		case "23503":
			return ErrMigrationCapabilityArtifact
		case "23514", "22001":
			return ErrMigrationCapabilityInvalid
		}
	}
	return err
}

var _ migrationcapability.Authority = (*Repository)(nil)
