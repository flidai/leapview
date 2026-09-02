// Package sqlite persists the durable control-plane side of physical pools.
//
// DuckLake remains the authority for table and file membership. This adapter
// stores only canonical pool identity and append-only, non-secret admission
// evidence, and reconstructs the domain contract from those records on every
// load so a process restart cannot bypass admission verification.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

// Repository is the direct SQLite adapter for physical-pool identity and
// admission records. It intentionally has no manifest or credential store.
type Repository struct {
	db *sql.DB
}

// NewRepository constructs a repository over the migrated platform database.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// AdmissionContract is the complete, verified restart contract for one pool
// tuple. Every value is reconstructed from SQLite and passed through the
// physicalpool domain validators before it is returned.
type AdmissionContract struct {
	Pool      physicalpool.PhysicalPool
	Admission physicalpool.PoolAdmission
	Evidence  physicalpool.Evidence
}

func createPhysicalPoolTx(ctx context.Context, tx *sql.Tx, normalized physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	identityJSON, err := normalized.Identity.CanonicalJSON()
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	retentionJSON, err := canonicalRetentionJSON(normalized.Identity.RetentionPolicy)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO physical_pools (
		  id, identity_digest, storage_location, storage_namespace,
		  storage_implementation, object_naming_contract, region, tenant, encryption_domain,
		  isolation_boundary, encryption_key_ref, credential_reference,
		  retention_authority, retention_policy_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(normalized.ID), string(normalized.ID), normalized.Identity.StorageLocation,
		normalized.Identity.StorageNamespace, normalized.Identity.Compatibility.StorageImplementation,
		normalized.Identity.Compatibility.ObjectNamingContract, normalized.Identity.Region,
		normalized.Identity.Tenant, normalized.Identity.EncryptionDomain, normalized.Identity.IsolationBoundary,
		normalized.Identity.EncryptionKeyRef, normalized.Identity.CredentialReference,
		normalized.Identity.RetentionAuthority, retentionJSON)
	if err == nil {
		return normalized, nil
	}
	if !isConstraint(err) {
		return physicalpool.PhysicalPool{}, repositoryError(err)
	}
	// Resolve an idempotent retry while holding the caller's write
	// transaction. A namespace collision is checked explicitly because SQLite
	// constraint text is not a stable domain reason.
	stored, readErr := loadStoredPoolTx(ctx, tx, string(normalized.ID), normalized.Identity.Compatibility)
	if readErr == nil {
		storedJSON, jsonErr := stored.Identity.CanonicalJSON()
		if jsonErr == nil && storedJSON == identityJSON {
			return normalized, nil
		}
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	if !errors.Is(readErr, sql.ErrNoRows) {
		return physicalpool.PhysicalPool{}, readErr
	}
	var otherID string
	if namespaceErr := tx.QueryRowContext(ctx, `
		SELECT id FROM physical_pools
		WHERE storage_implementation = ? AND storage_location = ? AND storage_namespace = ?`,
		normalized.Identity.Compatibility.StorageImplementation, normalized.Identity.StorageLocation,
		normalized.Identity.StorageNamespace).Scan(&otherID); namespaceErr == nil && otherID != string(normalized.ID) {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	return physicalpool.PhysicalPool{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
}

// CreatePhysicalPool persists one stable pool identity. A retry with the same
// canonical identity is idempotent; a pool ID or deletable namespace collision
// with a different identity is rejected without exposing SQLite diagnostics.
func (r *Repository) CreatePhysicalPool(ctx context.Context, input physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	normalized, err := normalizePool(input)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	if r == nil || r.db == nil {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return physicalpool.PhysicalPool{}, repositoryError(err)
	}
	defer tx.Rollback()
	created, err := createPhysicalPoolTx(ctx, tx, normalized)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	if err := tx.Commit(); err != nil {
		return physicalpool.PhysicalPool{}, repositoryError(err)
	}
	return created, nil
}

// Create is a concise alias for CreatePhysicalPool.
func (r *Repository) Create(ctx context.Context, input physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	return r.CreatePhysicalPool(ctx, input)
}

// CreateAndAdmit is the explicit operator bootstrap path for a new physical
// pool. Pool identity creation and compatibility admission are intentionally
// separate durable records, but this helper makes the supported pre-release
// workflow one call: create the stable namespace, then append the exact
// conformance evidence. It is idempotent for identical identity/evidence and
// never synthesizes an admission or replaces an existing one.
func (r *Repository) CreateAndAdmit(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, error) {
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	if r == nil || r.db == nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, repositoryError(err)
	}
	defer tx.Rollback()
	created, err := createPhysicalPoolTx(ctx, tx, normalized)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	admission, err := admitTx(ctx, tx, created, evidence)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	admitted, err := created.ApplyAdmission(admission)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	if err := tx.Commit(); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, repositoryError(err)
	}
	return admitted, admission, nil
}

// MigrateAndAdmit is the explicit upgrade path for a stable pool identity
// that predates the append-only compatibility admission records. It requires
// the caller to supply fresh conformance evidence for the current runtime;
// migration never marks a pool admitted from configuration alone.
func (r *Repository) MigrateAndAdmit(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, error) {
	return r.CreateAndAdmit(ctx, pool, evidence)
}

// CreateAndAdmitWithOwnership acquires the conditional marker in the actual
// physical namespace before granting this metadata database deletion
// authority. Same-owner retries are idempotent; a marker owned by another
// instance fails closed even when its SQLite database is unavailable.
func (r *Repository) CreateAndAdmitWithOwnership(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence, ownerID string, marker physicalpool.NamespaceOwnership) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, error) {
	if marker == nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, physicalpool.ErrOwnershipConflict
	}
	if err := pool.Validate(); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	admission, err := pool.Admit(evidence)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	claim := physicalpool.OwnershipClaim{PoolID: pool.ID, CompatibilityDigest: admission.CompatibilityDigest, EvidenceDigest: admission.EvidenceDigest, OwnerID: ownerID}
	if err := marker.AcquireNamespaceOwnership(ctx, claim); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	return r.CreateAndAdmit(ctx, pool, evidence)
}

// Admit validates and appends one exact compatibility conformance result. An
// identical retry returns the existing immutable admission; a different
// evidence record is appended, including when it represents a runtime or
// extension upgrade joining the same stable pool.
func (r *Repository) Admit(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence, supplied ...physicalpool.PoolAdmission) (physicalpool.PoolAdmission, error) {
	if r == nil || r.db == nil {
		return physicalpool.PoolAdmission{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
	}
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return physicalpool.PoolAdmission{}, repositoryError(err)
	}
	defer tx.Rollback()
	admission, err := admitTx(ctx, tx, normalized, evidence, supplied...)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	if err := tx.Commit(); err != nil {
		return physicalpool.PoolAdmission{}, repositoryError(err)
	}
	return admission, nil
}

// admitTx appends or resolves one immutable admission while the caller owns
// the transaction. Keeping pool creation and admission in this same helper is
// what makes CreateAndAdmit atomic across a crash or failed evidence check.
func admitTx(ctx context.Context, tx *sql.Tx, normalized physicalpool.PhysicalPool, evidence physicalpool.Evidence, supplied ...physicalpool.PoolAdmission) (physicalpool.PoolAdmission, error) {
	if len(supplied) > 1 {
		return physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticInvalidField, "admission")
	}
	admission, err := normalized.Admit(evidence)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	if len(supplied) == 1 {
		if err := supplied[0].Validate(); err != nil {
			return physicalpool.PoolAdmission{}, err
		}
		if supplied[0] != admission {
			return physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "admission")
		}
	}
	compatibilityJSON, err := canonicalCompatibilityJSON(admission.Compatibility)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	evidenceJSON, normalizedEvidence, err := canonicalEvidenceJSON(evidence)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	// Use the normalized evidence digest as the durable key. NewEvidence has
	// sorted checks before hashing, so equivalent input order is idempotent.
	admission.EvidenceDigest = normalizedEvidence.Digest

	stored, err := loadStoredPoolTx(ctx, tx, string(normalized.ID), admission.Compatibility)
	if errors.Is(err, sql.ErrNoRows) {
		return physicalpool.PoolAdmission{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticMissingField, "physical_pool")
	}
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	if !sameCanonicalIdentity(stored.Identity, normalized.Identity) {
		return physicalpool.PoolAdmission{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}

	var existing compatibilityRow
	queryErr := tx.QueryRowContext(ctx, `
		SELECT compatibility_json, evidence_json, evidence_digest,
		       compatibility_digest, conformance_version
		FROM physical_pool_admissions WHERE pool_id = ? AND evidence_digest = ?`,
		string(normalized.ID), admission.EvidenceDigest).Scan(
		&existing.CompatibilityJSON, &existing.EvidenceJSON, &existing.EvidenceDigest,
		&existing.CompatibilityDigest, &existing.ConformanceVersion)
	if queryErr == nil {
		_, _, storedAdmission, parseErr := parseAdmissionRow(existing, normalized.ID)
		if parseErr != nil {
			return physicalpool.PoolAdmission{}, parseErr
		}
		if !sameAdmission(storedAdmission, admission, compatibilityJSON, evidenceJSON) {
			return physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_digest")
		}
		return storedAdmission, nil
	}
	if !errors.Is(queryErr, sql.ErrNoRows) {
		return physicalpool.PoolAdmission{}, repositoryError(queryErr)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO physical_pool_admissions (
		  pool_id, compatibility_json, evidence_json, evidence_digest,
		  compatibility_digest, conformance_version
		) VALUES (?, ?, ?, ?, ?, ?)`, string(admission.PoolID), compatibilityJSON,
		evidenceJSON, admission.EvidenceDigest, admission.CompatibilityDigest,
		admission.ConformanceVersion)
	if err != nil {
		if isConstraint(err) {
			// A concurrent retry can win the immutable insert. Resolve it through
			// the same strict row parser rather than trusting the constraint text.
			if retryErr := tx.QueryRowContext(ctx, `
				SELECT compatibility_json, evidence_json, evidence_digest,
				       compatibility_digest, conformance_version
				FROM physical_pool_admissions WHERE pool_id = ? AND evidence_digest = ?`,
				string(normalized.ID), admission.EvidenceDigest).Scan(
				&existing.CompatibilityJSON, &existing.EvidenceJSON, &existing.EvidenceDigest,
				&existing.CompatibilityDigest, &existing.ConformanceVersion); retryErr == nil {
				_, _, storedAdmission, parseErr := parseAdmissionRow(existing, normalized.ID)
				if parseErr != nil {
					return physicalpool.PoolAdmission{}, parseErr
				}
				if sameAdmission(storedAdmission, admission, compatibilityJSON, evidenceJSON) {
					return storedAdmission, nil
				}
				return physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_digest")
			}
		}
		return physicalpool.PoolAdmission{}, repositoryError(err)
	}
	return admission, nil
}

// AdmitPhysicalPool is an explicit alias for callers that prefer a domain
// qualified method name.
func (r *Repository) AdmitPhysicalPool(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence, supplied ...physicalpool.PoolAdmission) (physicalpool.PoolAdmission, error) {
	return r.Admit(ctx, pool, evidence, supplied...)
}

// LoadAdmissionContract reconstructs and verifies one immutable admission.
// With a tuple argument, the exact tuple's latest evidence record is loaded;
// without one, the latest append-only admission is selected. No admission is
// returned until pool ID, identity digest, retention policy, compatibility,
// evidence digest, and domain admission checks all agree.
func (r *Repository) LoadAdmissionContract(ctx context.Context, poolRef any, tuple ...physicalpool.Compatibility) (AdmissionContract, error) {
	if r == nil || r.db == nil {
		return AdmissionContract{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
	}
	if len(tuple) > 1 {
		return AdmissionContract{}, safe(physicalpool.ErrInvalidCompatibility, physicalpool.DiagnosticInvalidField, "compatibility")
	}
	poolID, expectedIdentity, err := resolvePoolReference(poolRef)
	if err != nil {
		return AdmissionContract{}, err
	}
	var requested *physicalpool.Compatibility
	if len(tuple) == 1 {
		if err := tuple[0].Validate(); err != nil {
			return AdmissionContract{}, err
		}
		requested = &tuple[0]
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AdmissionContract{}, repositoryError(err)
	}
	defer tx.Rollback()
	// A fallback tuple is needed only to validate the stable fields while
	// loading the pool row. The exact tuple is supplied by the admission row.
	fallback := physicalpool.Compatibility{}
	if requested != nil {
		fallback = *requested
	}
	stored, err := loadStoredPoolTx(ctx, tx, string(poolID), fallback)
	if errors.Is(err, sql.ErrNoRows) {
		return AdmissionContract{}, safe(physicalpool.ErrPoolNotAdmitted, physicalpool.DiagnosticMissingField, "physical_pool")
	}
	if err != nil {
		return AdmissionContract{}, err
	}

	var row compatibilityRow
	var query string
	var args []any
	if requested != nil {
		digest, digestErr := requested.Digest()
		if digestErr != nil {
			return AdmissionContract{}, digestErr
		}
		query = `SELECT compatibility_json, evidence_json, evidence_digest, compatibility_digest, conformance_version
			FROM physical_pool_admissions WHERE pool_id = ? AND compatibility_digest = ?
			ORDER BY admitted_at DESC, rowid DESC LIMIT 1`
		args = []any{string(poolID), digest}
	} else {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM physical_pool_admissions WHERE pool_id = ?`, string(poolID)).Scan(&count); err != nil {
			return AdmissionContract{}, repositoryError(err)
		}
		if count == 0 {
			return AdmissionContract{}, safe(physicalpool.ErrPoolNotAdmitted, physicalpool.DiagnosticMissingField, "admission")
		}
		if count > 1 {
			// A pool can retain multiple append-only tuples/evidence records. A
			// caller must identify which one it intends to attach; silently
			// selecting by timestamp would make restart behavior ambiguous.
			return AdmissionContract{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticInvalidField, "admission")
		}
		query = `SELECT compatibility_json, evidence_json, evidence_digest, compatibility_digest, conformance_version
			FROM physical_pool_admissions WHERE pool_id = ? LIMIT 1`
		args = []any{string(poolID)}
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(
		&row.CompatibilityJSON, &row.EvidenceJSON, &row.EvidenceDigest,
		&row.CompatibilityDigest, &row.ConformanceVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if requested != nil {
				return AdmissionContract{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
			}
			return AdmissionContract{}, safe(physicalpool.ErrPoolNotAdmitted, physicalpool.DiagnosticMissingField, "admission")
		}
		return AdmissionContract{}, repositoryError(err)
	}

	contract, err := verifyAdmissionContract(poolID, stored, row, expectedIdentity)
	if err != nil {
		return AdmissionContract{}, err
	}
	if requested != nil && !contract.Admission.Compatibility.Equal(*requested) {
		return AdmissionContract{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
	}
	if err := tx.Commit(); err != nil {
		return AdmissionContract{}, repositoryError(err)
	}
	return contract, nil
}

// LoadAdmissionContractByCompatibilityDigest resolves the immutable
// compatibility tuple named by durable delivery evidence, then delegates to
// the normal tuple-aware verifier. Callers must not fall back to the latest
// admission because a pool may retain multiple incompatible tuples.
func (r *Repository) LoadAdmissionContractByCompatibilityDigest(ctx context.Context, poolID physicalpool.PoolID, compatibilityDigest string) (AdmissionContract, error) {
	if strings.TrimSpace(compatibilityDigest) == "" {
		return AdmissionContract{}, safe(physicalpool.ErrInvalidCompatibility, physicalpool.DiagnosticMissingField, "compatibility_digest")
	}
	if r == nil || r.db == nil {
		return AdmissionContract{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
	}
	var encoded string
	if err := r.db.QueryRowContext(ctx, `SELECT compatibility_json FROM physical_pool_admissions WHERE pool_id = ? AND compatibility_digest = ? ORDER BY admitted_at DESC, rowid DESC LIMIT 1`, string(poolID), compatibilityDigest).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdmissionContract{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
		}
		return AdmissionContract{}, repositoryError(err)
	}
	var tuple physicalpool.Compatibility
	if err := json.Unmarshal([]byte(encoded), &tuple); err != nil {
		return AdmissionContract{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticInvalidField, "compatibility")
	}
	if digest, err := tuple.Digest(); err != nil || digest != compatibilityDigest {
		return AdmissionContract{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
	}
	return r.LoadAdmissionContract(ctx, poolID, tuple)
}

// Load is a concise alias for LoadAdmissionContract.
func (r *Repository) Load(ctx context.Context, poolRef any, tuple ...physicalpool.Compatibility) (AdmissionContract, error) {
	return r.LoadAdmissionContract(ctx, poolRef, tuple...)
}

func resolvePoolReference(value any) (physicalpool.PoolID, *physicalpool.PhysicalPool, error) {
	switch ref := value.(type) {
	case physicalpool.PoolID:
		if strings.TrimSpace(string(ref)) == "" {
			return "", nil, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticMissingField, "id")
		}
		return ref, nil, nil
	case string:
		if strings.TrimSpace(ref) == "" {
			return "", nil, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticMissingField, "id")
		}
		return physicalpool.PoolID(ref), nil, nil
	case physicalpool.PhysicalPool:
		if err := ref.Validate(); err != nil {
			return "", nil, err
		}
		copy := ref
		return ref.ID, &copy, nil
	case *physicalpool.PhysicalPool:
		if ref == nil {
			return "", nil, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticMissingField, "id")
		}
		if err := ref.Validate(); err != nil {
			return "", nil, err
		}
		copy := *ref
		return ref.ID, &copy, nil
	default:
		return "", nil, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "id")
	}
}

// LoadAdmissionByEvidence loads a specific append-only evidence record. It is
// useful when multiple checks have admitted the same compatibility tuple.
func (r *Repository) LoadAdmissionByEvidence(ctx context.Context, poolID physicalpool.PoolID, evidenceDigest string) (AdmissionContract, error) {
	if strings.TrimSpace(evidenceDigest) == "" {
		return AdmissionContract{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticMissingField, "evidence_digest")
	}
	if r == nil || r.db == nil {
		return AdmissionContract{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AdmissionContract{}, repositoryError(err)
	}
	defer tx.Rollback()
	stored, err := loadStoredPoolTx(ctx, tx, string(poolID), physicalpool.Compatibility{})
	if errors.Is(err, sql.ErrNoRows) {
		return AdmissionContract{}, safe(physicalpool.ErrPoolNotAdmitted, physicalpool.DiagnosticMissingField, "physical_pool")
	}
	if err != nil {
		return AdmissionContract{}, err
	}
	var row compatibilityRow
	if err := tx.QueryRowContext(ctx, `
		SELECT compatibility_json, evidence_json, evidence_digest, compatibility_digest, conformance_version
		FROM physical_pool_admissions WHERE pool_id = ? AND evidence_digest = ? LIMIT 1`,
		string(poolID), evidenceDigest).Scan(
		&row.CompatibilityJSON, &row.EvidenceJSON, &row.EvidenceDigest,
		&row.CompatibilityDigest, &row.ConformanceVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdmissionContract{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticMissingField, "evidence_digest")
		}
		return AdmissionContract{}, repositoryError(err)
	}
	contract, err := verifyAdmissionContract(poolID, stored, row, nil)
	if err != nil {
		return AdmissionContract{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdmissionContract{}, repositoryError(err)
	}
	return contract, nil
}

type compatibilityRow struct {
	CompatibilityJSON   string
	EvidenceJSON        string
	EvidenceDigest      string
	CompatibilityDigest string
	ConformanceVersion  string
}

func normalizePool(input physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	if err := input.Validate(); err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	normalized, err := physicalpool.NewPhysicalPool(input.Identity)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	if normalized.ID != input.ID {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "id")
	}
	normalized.Compatibility = input.Compatibility
	if normalized.Compatibility == (physicalpool.Compatibility{}) {
		normalized.Compatibility = normalized.Identity.Compatibility
	}
	normalized.Admitted = input.Admitted
	normalized.AdmissionDigest = input.AdmissionDigest
	return normalized, nil
}

func loadStoredPoolTx(ctx context.Context, tx *sql.Tx, id string, fallback physicalpool.Compatibility) (physicalpool.PhysicalPool, error) {
	var row poolRow
	err := tx.QueryRowContext(ctx, `
		SELECT id, identity_digest, storage_location, storage_namespace,
		       storage_implementation, object_naming_contract, region, tenant, encryption_domain,
		       isolation_boundary, encryption_key_ref, credential_reference,
		       retention_authority, retention_policy_json
		FROM physical_pools WHERE id = ?`, id).Scan(
		&row.ID, &row.IdentityDigest, &row.StorageLocation, &row.StorageNamespace,
		&row.StorageImplementation, &row.ObjectNamingContract, &row.Region, &row.Tenant, &row.EncryptionDomain,
		&row.IsolationBoundary, &row.EncryptionKeyRef, &row.CredentialReference,
		&row.RetentionAuthority, &row.RetentionPolicyJSON)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	return row.pool(fallback)
}

type poolRow struct {
	ID                    string
	IdentityDigest        string
	StorageLocation       string
	StorageNamespace      string
	StorageImplementation string
	ObjectNamingContract  string
	Region                string
	Tenant                string
	EncryptionDomain      string
	IsolationBoundary     string
	EncryptionKeyRef      string
	CredentialReference   string
	RetentionAuthority    string
	RetentionPolicyJSON   string
}

func (r poolRow) pool(compatibility physicalpool.Compatibility) (physicalpool.PhysicalPool, error) {
	if compatibility == (physicalpool.Compatibility{}) {
		// There is no exact tuple to use until an admission row is selected. The
		// stable fields are still validated by a placeholder that is replaced by
		// the admission's tuple before identity verification.
		compatibility = physicalpool.Compatibility{
			StorageImplementation: r.StorageImplementation,
			ObjectNamingContract:  r.ObjectNamingContract,
		}
	}
	if compatibility.StorageImplementation != r.StorageImplementation || compatibility.ObjectNamingContract != r.ObjectNamingContract {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
	}
	var retention physicalpool.RetentionPolicy
	if err := decodeCanonicalJSON(r.RetentionPolicyJSON, &retention); err != nil {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "retention_policy")
	}
	canonicalRetention, err := canonicalRetentionJSON(retention)
	if err != nil || canonicalRetention != r.RetentionPolicyJSON {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "retention_policy")
	}
	identity := physicalpool.PoolIdentity{
		StorageLocation: r.StorageLocation, StorageNamespace: r.StorageNamespace,
		Region: r.Region, Tenant: r.Tenant, EncryptionDomain: r.EncryptionDomain, IsolationBoundary: r.IsolationBoundary,
		EncryptionKeyRef: r.EncryptionKeyRef, CredentialReference: r.CredentialReference,
		RetentionAuthority: r.RetentionAuthority, RetentionPolicy: retention,
		Compatibility: compatibility,
	}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	if pool.Identity.StorageLocation != r.StorageLocation || string(pool.ID) != r.ID || r.IdentityDigest != r.ID {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "id")
	}
	return pool, nil
}

func sameCanonicalIdentity(left, right physicalpool.PoolIdentity) bool {
	l, errL := left.CanonicalJSON()
	r, errR := right.CanonicalJSON()
	return errL == nil && errR == nil && l == r
}

func verifyAdmissionContract(poolID physicalpool.PoolID, stored physicalpool.PhysicalPool, row compatibilityRow, expectedIdentity *physicalpool.PhysicalPool) (AdmissionContract, error) {
	compatibility, evidence, admission, err := parseAdmissionRow(row, poolID)
	if err != nil {
		return AdmissionContract{}, err
	}
	// The pool row contains only stable compatibility fields. Rebuild it with
	// the exact admitted tuple, then recompute the content-addressed identity.
	identity := stored.Identity
	identity.Compatibility = compatibility
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		return AdmissionContract{}, err
	}
	if pool.ID != poolID || string(poolID) != string(stored.ID) {
		return AdmissionContract{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	if expectedIdentity != nil && !sameCanonicalIdentity(expectedIdentity.Identity, pool.Identity) {
		return AdmissionContract{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	if admission.PoolID != pool.ID {
		return AdmissionContract{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	admitted, err := pool.ApplyAdmission(admission)
	if err != nil {
		return AdmissionContract{}, err
	}
	if err := physicalpool.VerifyAdmission(admitted, compatibility, admission, evidence); err != nil {
		return AdmissionContract{}, err
	}
	return AdmissionContract{Pool: admitted, Admission: admission, Evidence: evidence}, nil
}

func parseAdmissionRow(row compatibilityRow, poolID physicalpool.PoolID) (physicalpool.Compatibility, physicalpool.Evidence, physicalpool.PoolAdmission, error) {
	var compatibility physicalpool.Compatibility
	if err := decodeCanonicalJSON(row.CompatibilityJSON, &compatibility); err != nil {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "compatibility_json")
	}
	canonicalCompatibility, err := canonicalCompatibilityJSON(compatibility)
	if err != nil || canonicalCompatibility != row.CompatibilityJSON {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "compatibility_json")
	}
	var evidence physicalpool.Evidence
	if err := decodeCanonicalJSON(row.EvidenceJSON, &evidence); err != nil {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_json")
	}
	canonicalEvidence, normalizedEvidence, err := canonicalEvidenceJSON(evidence)
	if err != nil || canonicalEvidence != row.EvidenceJSON || normalizedEvidence.Digest != row.EvidenceDigest {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_digest")
	}
	if !normalizedEvidence.Compatibility.Equal(compatibility) || normalizedEvidence.ConformanceVersion != row.ConformanceVersion {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "conformance_version")
	}
	compatibilityDigest, err := compatibility.Digest()
	if err != nil || compatibilityDigest != row.CompatibilityDigest {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility_digest")
	}
	admission := physicalpool.PoolAdmission{
		PoolID: poolID, Compatibility: compatibility,
		CompatibilityDigest: row.CompatibilityDigest, EvidenceDigest: row.EvidenceDigest,
		ConformanceVersion: row.ConformanceVersion,
	}
	if err := admission.Validate(); err != nil {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, err
	}
	return compatibility, normalizedEvidence, admission, nil
}

func sameAdmission(left, right physicalpool.PoolAdmission, compatibilityJSON, evidenceJSON string) bool {
	leftCompatibility, leftErr := canonicalCompatibilityJSON(left.Compatibility)
	rightCompatibility, rightErr := canonicalCompatibilityJSON(right.Compatibility)
	if leftErr != nil || rightErr != nil || leftCompatibility != compatibilityJSON || rightCompatibility != compatibilityJSON || evidenceJSON == "" {
		return false
	}
	return left.PoolID == right.PoolID && left.CompatibilityDigest == right.CompatibilityDigest && left.EvidenceDigest == right.EvidenceDigest && left.ConformanceVersion == right.ConformanceVersion
}

func canonicalCompatibilityJSON(value physicalpool.Compatibility) (string, error) {
	return value.CanonicalJSON()
}

func canonicalEvidenceJSON(value physicalpool.Evidence) (string, physicalpool.Evidence, error) {
	normalized, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: value.Compatibility, ConformanceVersion: value.ConformanceVersion, Checks: value.Checks,
	})
	if err != nil {
		return "", physicalpool.Evidence{}, err
	}
	payload := struct {
		Compatibility      physicalpool.Compatibility   `json:"compatibility"`
		ConformanceVersion string                       `json:"conformance_version"`
		Checks             []physicalpool.EvidenceCheck `json:"checks"`
		Digest             string                       `json:"digest"`
	}{normalized.Compatibility, normalized.ConformanceVersion, normalized.Checks, normalized.Digest}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", physicalpool.Evidence{}, fmt.Errorf("marshal admission evidence: %w", err)
	}
	return string(encoded), normalized, nil
}

func canonicalRetentionJSON(value physicalpool.RetentionPolicy) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal retention policy: %w", err)
	}
	return string(encoded), nil
}

func decodeCanonicalJSON(raw string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func safe(cause error, code physicalpool.DiagnosticCode, field string) error {
	return &physicalpool.DiagnosticsError{Cause: cause, Diagnostics: []physicalpool.Diagnostic{{Code: code, Field: field}}}
}

func isConstraint(err error) bool {
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "constraint") || strings.Contains(value, "unique") || strings.Contains(value, "foreign key")
}

func repositoryError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("physical pool repository: %w", err)
}
