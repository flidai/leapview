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

//go:embed 002_project_identity_ledger_replacement.sql
var identityLedgerReplacementSQL string

//go:embed 013_identity_ledger_privileges.sql
var identityLedgerPrivilegesSQL string

//go:embed 014_contract_publication_correction.sql
var contractPublicationCorrectionSQL string

const ContractPublicationCorrectionRevision int64 = 14
const ContractPublicationCorrectionMigrationID = "014_contract_publication_correction"

func ContractPublicationCorrectionChecksum() string {
	return sqlChecksum(contractPublicationCorrectionSQL)
}

const IdentityLedgerReplacementMigrationID = "002_project_identity_ledger_replacement"
const IdentityLedgerPrivilegesRevision int64 = 13
const IdentityLedgerPrivilegesMigrationID = "013_identity_ledger_privileges"

func IdentityLedgerReplacementChecksum() string { return sqlChecksum(identityLedgerReplacementSQL) }
func IdentityLedgerPrivilegesChecksum() string  { return sqlChecksum(identityLedgerPrivilegesSQL) }
func sqlChecksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

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

//go:embed 010_access_control_authority.sql
var accessControlAuthoritySQL string

//go:embed 011_typed_attribute_registry.sql
var typedAttributeRegistrySQL string

//go:embed 012_semantic_attribute_control.sql
var semanticAttributeControlSQL string

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

// AccessControlAuthorityRevision adds mutable live role assignments and
// grants. Canonical roles and generation-scoped authorization snapshots remain
// owned by their existing immutable authorities.
const AccessControlAuthorityRevision int64 = 10

const AccessControlAuthorityMigrationID = "010_access_control_authority"

// TypedAttributeRegistryRevision adds the FAI-636 typed semantic-access
// registry without rewriting any earlier global control-plane revision.
const TypedAttributeRegistryRevision int64 = 11

const TypedAttributeRegistryMigrationID = "011_typed_attribute_registry"

// SemanticAttributeControlRevision adds durable semantic-access assignments
// and trusted claim mappings while preserving the immutable definition
// registry introduced by revision eleven.
const SemanticAttributeControlRevision int64 = 12

const SemanticAttributeControlMigrationID = "012_semantic_attribute_control"

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

func AccessControlAuthoritySQL() string { return accessControlAuthoritySQL }

func AccessControlAuthorityChecksum() string {
	sum := sha256.Sum256([]byte(accessControlAuthoritySQL))
	return hex.EncodeToString(sum[:])
}

func TypedAttributeRegistrySQL() string { return typedAttributeRegistrySQL }

func TypedAttributeRegistryChecksum() string {
	sum := sha256.Sum256([]byte(typedAttributeRegistrySQL))
	return hex.EncodeToString(sum[:])
}

func SemanticAttributeControlSQL() string { return semanticAttributeControlSQL }

func SemanticAttributeControlChecksum() string {
	sum := sha256.Sum256([]byte(semanticAttributeControlSQL))
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
		{IdentityLedgerRevision, IdentityLedgerReplacementMigrationID, identityLedgerReplacementSQL, IdentityLedgerReplacementChecksum()},
		{ContractPublicationRevision, ContractPublicationMigrationID, contractPublicationSQL, ContractPublicationChecksum()},
		{ActivationTransitionJournalRevision, ActivationTransitionJournalMigrationID, activationTransitionJournalSQL, ActivationTransitionJournalChecksum()},
		{ActivationTransitionReferencesRevision, ActivationTransitionReferencesMigrationID, activationTransitionReferencesSQL, ActivationTransitionReferencesChecksum()},
		{IdentityRestoreTransitionRevision, IdentityRestoreTransitionMigrationID, identityRestoreTransitionSQL, IdentityRestoreTransitionChecksum()},
		{AccessAuthorityCompatibilityRevision, AccessAuthorityCompatibilityMigrationID, accessAuthorityCompatibilitySQL, AccessAuthorityCompatibilityChecksum()},
		{ContractPublicationIntegrityRevision, ContractPublicationIntegrityMigrationID, contractPublicationIntegritySQL, ContractPublicationIntegrityChecksum()},
		{PlatformBootstrapAuthorityRevision, PlatformBootstrapAuthorityMigrationID, platformBootstrapAuthoritySQL, PlatformBootstrapAuthorityChecksum()},
		{AccessControlAuthorityRevision, AccessControlAuthorityMigrationID, accessControlAuthoritySQL, AccessControlAuthorityChecksum()},
		{TypedAttributeRegistryRevision, TypedAttributeRegistryMigrationID, typedAttributeRegistrySQL, TypedAttributeRegistryChecksum()},
		{SemanticAttributeControlRevision, SemanticAttributeControlMigrationID, semanticAttributeControlSQL, SemanticAttributeControlChecksum()},
		{IdentityLedgerPrivilegesRevision, IdentityLedgerPrivilegesMigrationID, identityLedgerPrivilegesSQL, IdentityLedgerPrivilegesChecksum()},
		{ContractPublicationCorrectionRevision, ContractPublicationCorrectionMigrationID, contractPublicationCorrectionSQL, ContractPublicationCorrectionChecksum()},
	}
}

// Apply serializes, validates and plans the complete history before executing
// pending SQL. The caller MUST supply a READ COMMITTED transaction and roll it
// back on any error. The advisory lock is held until that transaction ends.
func Apply(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("postgres migration transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	declaration, err := identitySupersession()
	if err != nil {
		return err
	}
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return fmt.Errorf("inspect migration isolation: %w", err)
	}
	if isolation != "read committed" {
		return errors.New("PostgreSQL migrations require READ COMMITTED isolation")
	}
	if _, err := tx.Exec(ctx, migrationLockSQL); err != nil {
		return fmt.Errorf("acquire PostgreSQL migration lock: %w", err)
	}
	records, err := inspectHistory(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateHistory(records, declaration); err != nil {
		return err
	}
	for _, migration := range ordered()[len(records):] {
		if err := applyOne(ctx, tx, migration.revision, migration.id, migration.sql, migration.checksum); err != nil {
			return err
		}
	}
	return Verify(ctx, tx)
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
	declaration, err := identitySupersession()
	if err != nil {
		return err
	}
	records, err := inspectHistory(ctx, reader)
	if err != nil {
		return err
	}
	if err := validateHistory(records, declaration); err != nil {
		return err
	}
	if len(records) != len(ordered()) {
		return errors.New("PostgreSQL migration history is incomplete")
	}
	return nil
}

func applyOne(ctx context.Context, tx Tx, revision int64, migrationID, sql, checksum string) error {
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("apply PostgreSQL migration %d (%s): %w", revision, migrationID, err)
	}
	// Cast parameters/results to stable built-in types: bootstrap rollback can
	// invalidate domain OIDs retained by a pooled connection's prepared plans.
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.schema_revision (revision, migration_id, checksum)
		VALUES ($1::bigint, $2::text, $3::text)`, revision, migrationID, checksum); err != nil {
		return fmt.Errorf("record PostgreSQL schema revision: %w", err)
	}
	var recordedRevision int64
	var recordedMigrationID, recordedChecksum string
	if err := tx.QueryRow(ctx, `
		SELECT revision, migration_id, checksum::text
		FROM platform.schema_revision WHERE revision = $1`, revision).
		Scan(&recordedRevision, &recordedMigrationID, &recordedChecksum); err != nil {
		return fmt.Errorf("verify PostgreSQL schema revision: %w", err)
	}
	if recordedRevision != revision || recordedMigrationID != migrationID || recordedChecksum != checksum {
		return fmt.Errorf("PostgreSQL schema revision mismatch: got revision=%d migration=%q checksum=%q", recordedRevision, recordedMigrationID, recordedChecksum)
	}
	return nil
}
