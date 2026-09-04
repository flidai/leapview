// Package migrations contains the authored PostgreSQL control-plane schema.
//
// PostgreSQL migrations are intentionally separate from the historical SQLite
// migrations.  Capability repositories can apply the baseline in a caller-owned
// transaction; this package does not open a connection or select a database.
package migrations

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// BaselineRevision is the first clean-slate control-plane schema revision.
const BaselineRevision int64 = 1

// BaselineMigrationID is the immutable identifier recorded in
// platform.schema_revision.
const BaselineMigrationID = "001_control_plane"

//go:embed 001_control_plane.sql
var baselineSQL string

//go:embed 002_project_identity_ledger.sql
var identityLedgerSQL string

//go:embed 003_contract_publication_evidence.sql
var contractPublicationSQL string

//go:embed 004_identity_activation_transition_journal.sql
var activationTransitionJournalSQL string

//go:embed 005_identity_activation_transition_references.sql
var activationTransitionReferencesSQL string

//go:embed 006_identity_restore_transition.sql
var identityRestoreTransitionSQL string

//go:embed 007_access_authority_compatibility.sql
var accessAuthorityCompatibilitySQL string

//go:embed 008_contract_publication_integrity.sql
var contractPublicationIntegritySQL string

//go:embed 009_platform_bootstrap_authority.sql
var platformBootstrapAuthoritySQL string

// IdentityLedgerRevision introduces the PostgreSQL-only FAI-617 resource
// identity ledger.
const IdentityLedgerRevision int64 = 2

// IdentityLedgerMigrationID is the immutable revision-two identifier.
const IdentityLedgerMigrationID = "002_project_identity_ledger"

// ContractPublicationRevision adds FAI-622 immutable version evidence without
// introducing a new identity or publication-store authority.
const ContractPublicationRevision int64 = 3

const ContractPublicationMigrationID = "003_contract_publication_evidence"

// ActivationTransitionJournalRevision adds the PostgreSQL-owned FAI-617
// activation transition journal.
const ActivationTransitionJournalRevision int64 = 4

const ActivationTransitionJournalMigrationID = "004_identity_activation_transition_journal"

// ActivationTransitionReferencesRevision adds durable-reference evidence to
// the FAI-617 activation transition journal without mutating revision four.
const ActivationTransitionReferencesRevision int64 = 5

const ActivationTransitionReferencesMigrationID = "005_identity_activation_transition_references"

// IdentityRestoreTransitionRevision adds the explicit FAI-663 restore
// operation and its immutable approved authored-ID evidence.
const IdentityRestoreTransitionRevision int64 = 6

const IdentityRestoreTransitionMigrationID = "006_identity_restore_transition"

// AccessAuthorityCompatibilityRevision upgrades the minimal baseline access
// projection in place for the native access and MCP OAuth repositories.
const AccessAuthorityCompatibilityRevision int64 = 7

const AccessAuthorityCompatibilityMigrationID = "007_access_authority_compatibility"

// ContractPublicationIntegrityRevision binds publication identity fields to
// the exact canonical bytes stored by revision three.
const ContractPublicationIntegrityRevision int64 = 8

const ContractPublicationIntegrityMigrationID = "008_contract_publication_integrity"

// PlatformBootstrapAuthorityRevision adds immutable platform instance
// identity and environment binding without rewriting revisions 001-008.
const PlatformBootstrapAuthorityRevision int64 = 9

const PlatformBootstrapAuthorityMigrationID = "009_platform_bootstrap_authority"

// BaselineSQL returns the exact authored baseline migration.  Callers should
// execute it as a migration authority, inside a transaction where the driver
// supports transactional DDL.
func BaselineSQL() string { return baselineSQL }

// BaselineChecksum is the SHA-256 digest recorded with the schema revision.
func BaselineChecksum() string {
	sum := sha256.Sum256([]byte(baselineSQL))
	return hex.EncodeToString(sum[:])
}

// IdentityLedgerSQL returns the exact authored FAI-617 migration.
func IdentityLedgerSQL() string { return identityLedgerSQL }

// IdentityLedgerChecksum is the SHA-256 recorded for revision two.
func IdentityLedgerChecksum() string {
	sum := sha256.Sum256([]byte(identityLedgerSQL))
	return hex.EncodeToString(sum[:])
}

func ContractPublicationSQL() string { return contractPublicationSQL }

func ContractPublicationChecksum() string {
	sum := sha256.Sum256([]byte(contractPublicationSQL))
	return hex.EncodeToString(sum[:])
}

func ActivationTransitionJournalSQL() string { return activationTransitionJournalSQL }

func ActivationTransitionJournalChecksum() string {
	sum := sha256.Sum256([]byte(activationTransitionJournalSQL))
	return hex.EncodeToString(sum[:])
}

func ActivationTransitionReferencesSQL() string { return activationTransitionReferencesSQL }

func ActivationTransitionReferencesChecksum() string {
	sum := sha256.Sum256([]byte(activationTransitionReferencesSQL))
	return hex.EncodeToString(sum[:])
}

func IdentityRestoreTransitionSQL() string { return identityRestoreTransitionSQL }

func IdentityRestoreTransitionChecksum() string {
	sum := sha256.Sum256([]byte(identityRestoreTransitionSQL))
	return hex.EncodeToString(sum[:])
}

func AccessAuthorityCompatibilitySQL() string { return accessAuthorityCompatibilitySQL }

func AccessAuthorityCompatibilityChecksum() string {
	sum := sha256.Sum256([]byte(accessAuthorityCompatibilitySQL))
	return hex.EncodeToString(sum[:])
}

func ContractPublicationIntegritySQL() string { return contractPublicationIntegritySQL }

func ContractPublicationIntegrityChecksum() string {
	sum := sha256.Sum256([]byte(contractPublicationIntegritySQL))
	return hex.EncodeToString(sum[:])
}

func PlatformBootstrapAuthoritySQL() string { return platformBootstrapAuthoritySQL }

func PlatformBootstrapAuthorityChecksum() string {
	sum := sha256.Sum256([]byte(platformBootstrapAuthoritySQL))
	return hex.EncodeToString(sum[:])
}

// Tx is the transaction boundary required by Apply.  pgx.Tx and pgxpool.Tx
// both satisfy it; keeping the boundary here avoids opening a second
// connection or introducing repository policy into the schema package.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// RevisionReader is the read-only surface used to verify that an ordinary
// runtime connection observes the exact migration ledger prepared by the
// separate migrator authority.
type RevisionReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type migration struct {
	revision int64
	id       string
	sql      string
	checksum string
}

func ordered() []migration {
	return []migration{
		{BaselineRevision, BaselineMigrationID, baselineSQL, BaselineChecksum()},
		{IdentityLedgerRevision, IdentityLedgerMigrationID, identityLedgerSQL, IdentityLedgerChecksum()},
		{ContractPublicationRevision, ContractPublicationMigrationID, contractPublicationSQL, ContractPublicationChecksum()},
		{ActivationTransitionJournalRevision, ActivationTransitionJournalMigrationID, activationTransitionJournalSQL, ActivationTransitionJournalChecksum()},
		{ActivationTransitionReferencesRevision, ActivationTransitionReferencesMigrationID, activationTransitionReferencesSQL, ActivationTransitionReferencesChecksum()},
		{IdentityRestoreTransitionRevision, IdentityRestoreTransitionMigrationID, identityRestoreTransitionSQL, IdentityRestoreTransitionChecksum()},
		{AccessAuthorityCompatibilityRevision, AccessAuthorityCompatibilityMigrationID, accessAuthorityCompatibilitySQL, AccessAuthorityCompatibilityChecksum()},
		{ContractPublicationIntegrityRevision, ContractPublicationIntegrityMigrationID, contractPublicationIntegritySQL, ContractPublicationIntegrityChecksum()},
		{PlatformBootstrapAuthorityRevision, PlatformBootstrapAuthorityMigrationID, platformBootstrapAuthoritySQL, PlatformBootstrapAuthorityChecksum()},
	}
}

// Apply executes every authored migration in revision order on a caller-owned
// transaction and records each exact SHA-256 in platform.schema_revision. The
// SQL contains no BEGIN/COMMIT so a failed migration can be rolled back by the
// caller. A pre-existing revision with a different checksum is rejected.
func Apply(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("postgres migration transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, migration := range ordered() {
		if err := applyOne(ctx, tx, migration.revision, migration.id, migration.sql, migration.checksum); err != nil {
			return err
		}
	}
	return nil
}

// Verify proves that a runtime connection sees every exact authored revision.
// Missing, renamed, or checksum-drifted rows fail closed.
func Verify(ctx context.Context, reader RevisionReader) error {
	if reader == nil {
		return errors.New("postgres migration revision reader is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, want := range ordered() {
		var revision int64
		var migrationID, checksum string
		if err := reader.QueryRow(ctx, `
			SELECT revision, migration_id, checksum
			FROM platform.schema_revision WHERE revision = $1`, want.revision).
			Scan(&revision, &migrationID, &checksum); err != nil {
			return fmt.Errorf("verify PostgreSQL schema revision %d: %w", want.revision, err)
		}
		if revision != want.revision || migrationID != want.id || checksum != want.checksum {
			return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", revision, migrationID, checksum)
		}
	}
	return nil
}

func applyOne(ctx context.Context, tx Tx, revision int64, migrationID, sql, checksum string) error {
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.schema_revision (revision, migration_id, checksum)
		VALUES ($1, $2, $3)
		ON CONFLICT (revision) DO NOTHING`, revision, migrationID, checksum); err != nil {
		return fmt.Errorf("record PostgreSQL schema revision: %w", err)
	}
	var recordedRevision int64
	var recordedMigrationID, recordedChecksum string
	if err := tx.QueryRow(ctx, `
		SELECT revision, migration_id, checksum
		FROM platform.schema_revision WHERE revision = $1`, revision).
		Scan(&recordedRevision, &recordedMigrationID, &recordedChecksum); err != nil {
		return fmt.Errorf("verify PostgreSQL schema revision: %w", err)
	}
	if recordedRevision != revision || recordedMigrationID != migrationID || recordedChecksum != checksum {
		return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recordedRevision, recordedMigrationID, recordedChecksum)
	}
	return nil
}
