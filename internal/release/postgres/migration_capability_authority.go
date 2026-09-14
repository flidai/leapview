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
	ErrMigrationCapabilityOwnerEvidence  = errors.New("migration capability owner evidence is invalid")
)

// MigrationCapabilityAuthority is the only production publication and runtime
// resolution boundary. It binds publication to independently trusted owner
// keys; Repository deliberately exposes no direct Capability write method.
type MigrationCapabilityAuthority struct {
	repository *Repository
	registry   migrationcapability.OwnerRegistry
}

func NewMigrationCapabilityAuthority(repository *Repository, registry migrationcapability.OwnerRegistry) (*MigrationCapabilityAuthority, error) {
	if repository == nil || repository.db == nil {
		return nil, ErrMigrationCapabilityInvalid
	}
	frozenRegistry, err := registry.Frozen()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMigrationCapabilityOwnerEvidence, err)
	}
	return &MigrationCapabilityAuthority{repository: repository, registry: frozenRegistry}, nil
}

// Publish appends one authenticated subsystem-owner capability. Exact replays
// converge on the existing canonical record; no update path exists.
func (a *MigrationCapabilityAuthority) Publish(ctx context.Context, evidence migrationcapability.OwnerEvidence) (migrationcapability.Capability, error) {
	if a == nil || a.repository == nil || a.repository.db == nil {
		return migrationcapability.Capability{}, ErrMigrationCapabilityInvalid
	}
	b, ok := a.repository.db.(beginner)
	if !ok {
		return migrationcapability.Capability{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var published migrationcapability.Capability
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		published, err = publishMigrationCapability(ctx, tx, evidence, a.registry)
		return err
	})
	return published, err
}

func publishMigrationCapability(ctx context.Context, tx pgx.Tx, evidence migrationcapability.OwnerEvidence, registry migrationcapability.OwnerRegistry) (migrationcapability.Capability, error) {
	canonical, err := registry.Verify(evidence)
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityOwnerEvidence, err)
	}
	document, err := canonical.CanonicalJSON()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	digest, err := canonical.Digest()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityInvalid, err)
	}
	evidenceDocument, err := evidence.CanonicalJSON()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityOwnerEvidence, err)
	}
	evidenceDigest, err := evidence.Digest()
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityOwnerEvidence, err)
	}
	q := releasedb.New(tx)
	if err := verifyCapabilityArtifactLocked(ctx, q, canonical.ArtifactAdmissionDigest); err != nil {
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
		OwnerEvidenceVersion:    evidence.Version,
		OwnerEvidenceDigest:     evidenceDigest,
		OwnerEvidenceBytes:      evidenceDocument,
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
	stored, err := readMigrationCapabilityRow(row, registry)
	if err != nil {
		return migrationcapability.Capability{}, err
	}
	if !bytes.Equal(row.CapabilityBytes, document) || !bytes.Equal(row.OwnerEvidenceBytes, evidenceDocument) {
		return migrationcapability.Capability{}, ErrMigrationCapabilityConflict
	}
	return stored, nil
}

// ResolveMigrationCapability implements migrationcapability.Authority. It
// performs exact content-addressed lookup and revalidates both canonical bytes
// and the referenced OCI admission on every read.
func (a *MigrationCapabilityAuthority) ResolveMigrationCapability(ctx context.Context, artifactAdmissionDigest, targetIdentityDigest string, subsystem migrationcapability.Subsystem) (migrationcapability.Capability, error) {
	if a == nil || a.repository == nil || a.repository.db == nil {
		return migrationcapability.Capability{}, ErrMigrationCapabilityInvalid
	}
	if platformdigest.ValidateSHA256Identity(artifactAdmissionDigest) != nil || platformdigest.ValidateSHA256Identity(targetIdentityDigest) != nil || !validMigrationCapabilitySubsystem(subsystem) {
		return migrationcapability.Capability{}, ErrMigrationCapabilityInvalid
	}
	q := releasedb.New(a.repository.db)
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
	capability, err := readMigrationCapabilityRow(row, a.registry)
	if err != nil {
		return migrationcapability.Capability{}, err
	}
	if err := verifyCapabilityArtifact(ctx, q, artifactAdmissionDigest); err != nil {
		return migrationcapability.Capability{}, err
	}
	return capability, nil
}

func readMigrationCapabilityRow(row releasedb.ReleaseMigrationCapability, registry migrationcapability.OwnerRegistry) (migrationcapability.Capability, error) {
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
	evidence, err := migrationcapability.ParseOwnerEvidenceCanonical(row.OwnerEvidenceBytes)
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityOwnerEvidence, err)
	}
	evidenceDigest, err := evidence.Digest()
	if err != nil || row.OwnerEvidenceVersion != evidence.Version || row.OwnerEvidenceDigest != evidenceDigest {
		return migrationcapability.Capability{}, ErrMigrationCapabilityDigestMismatch
	}
	verified, err := registry.Verify(evidence)
	if err != nil {
		return migrationcapability.Capability{}, fmt.Errorf("%w: %v", ErrMigrationCapabilityOwnerEvidence, err)
	}
	verifiedDocument, err := verified.CanonicalJSON()
	if err != nil || !bytes.Equal(verifiedDocument, row.CapabilityBytes) {
		return migrationcapability.Capability{}, ErrMigrationCapabilityDigestMismatch
	}
	return capability, nil
}

func verifyCapabilityArtifactLocked(ctx context.Context, q *releasedb.Queries, admissionDigest string) error {
	reference, err := q.LockOCIArtifactAdmissionByDigest(ctx, admissionDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrMigrationCapabilityArtifact, ErrArtifactAdmissionNotFound)
	}
	if err != nil {
		return err
	}
	return verifyCapabilityArtifactReference(ctx, q, reference, admissionDigest)
}

func verifyCapabilityArtifact(ctx context.Context, q *releasedb.Queries, admissionDigest string) error {
	reference, err := q.GetOCIArtifactReferenceByAdmissionDigest(ctx, admissionDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrMigrationCapabilityArtifact, ErrArtifactAdmissionNotFound)
	}
	if err != nil {
		return err
	}
	return verifyCapabilityArtifactReference(ctx, q, reference, admissionDigest)
}

func verifyCapabilityArtifactReference(ctx context.Context, q *releasedb.Queries, reference, admissionDigest string) error {
	row, err := q.GetOCIArtifactAdmission(ctx, reference)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMigrationCapabilityArtifact, err)
	}
	_, storedDigest, err := readArtifactAdmissionRow(row)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMigrationCapabilityArtifact, err)
	}
	if storedDigest != admissionDigest {
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

var _ migrationcapability.Authority = (*MigrationCapabilityAuthority)(nil)
