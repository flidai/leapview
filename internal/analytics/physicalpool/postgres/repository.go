// Package postgres implements the clean-slate PostgreSQL physical-pool
// authority. PostgreSQL stores only immutable identity/admission evidence;
// DuckLake remains the authority for table and object membership.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed schema.sql
var schemaSQL string

// DBTX is the native pgx query surface. pgx pools, connections, and
// caller-owned transactions satisfy it.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is the transaction surface accepted by schema and atomic helpers.
type Tx = DBTX

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository persists stable physical-pool identities, immutable admissions,
// ownership claim evidence, and the database-side deletion fence.
type Repository struct{ db DBTX }

// New constructs a repository over a pgx pool, connection, or transaction.
func New(db DBTX) *Repository { return &Repository{db: db} }

// SchemaSQL returns the capability-owned forward schema.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes schema through a caller-owned migration transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("physical-pool PostgreSQL transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// CreatePhysicalPool persists one stable pool identity. Identical retries are
// idempotent; a changed identity or namespace collision fails closed.
func (r *Repository) CreatePhysicalPool(ctx context.Context, input physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	if r == nil || r.db == nil {
		return physicalpool.PhysicalPool{}, invalidRepo()
	}
	normalized, err := normalizePool(input)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	var out physicalpool.PhysicalPool
	err = r.withTx(ctx, func(tx DBTX) error { var e error; out, e = createPhysicalPoolTx(ctx, tx, normalized); return e })
	return out, err
}

// CreatePhysicalPoolTx composes pool creation with another capability's
// caller-owned pgx transaction. It never commits or rolls back tx.
func (r *Repository) CreatePhysicalPoolTx(ctx context.Context, tx Tx, input physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	if tx == nil {
		return physicalpool.PhysicalPool{}, invalidRepo()
	}
	normalized, err := normalizePool(input)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	return createPhysicalPoolTx(ctx, tx, normalized)
}

func createPhysicalPoolTx(ctx context.Context, tx DBTX, normalized physicalpool.PhysicalPool) (physicalpool.PhysicalPool, error) {
	retention, err := canonicalRetentionJSON(normalized.Identity.RetentionPolicy)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	identity, err := normalized.Identity.CanonicalJSON()
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO physical_pool.physical_pools
 (id,identity_digest,storage_location,storage_namespace,storage_implementation,object_naming_contract,region,tenant,isolation_boundary,encryption_key_ref,credential_reference,retention_authority,orphan_grace_period_seconds,reader_grace_period_seconds,build_grace_period_seconds,retention_policy)
 VALUES ($1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb) ON CONFLICT DO NOTHING`, string(normalized.ID), normalized.Identity.StorageLocation, normalized.Identity.StorageNamespace,
		normalized.Identity.Compatibility.StorageImplementation, normalized.Identity.Compatibility.ObjectNamingContract, normalized.Identity.Region, normalized.Identity.Tenant,
		normalized.Identity.IsolationBoundary, normalized.Identity.EncryptionKeyRef, normalized.Identity.CredentialReference, normalized.Identity.RetentionAuthority,
		normalized.Identity.RetentionPolicy.OrphanGracePeriodSeconds, normalized.Identity.RetentionPolicy.ReaderGracePeriodSeconds, normalized.Identity.RetentionPolicy.BuildGracePeriodSeconds, retention)
	if err == nil && result.RowsAffected() == 1 {
		return normalized, nil
	}
	if err != nil {
		return physicalpool.PhysicalPool{}, repositoryError(err)
	}
	stored, loadErr := loadStoredPoolTx(ctx, tx, normalized.ID, normalized.Identity.Compatibility)
	if loadErr == nil {
		storedIdentity, _ := stored.Identity.CanonicalJSON()
		if storedIdentity == identity {
			return normalized, nil
		}
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	if !errors.Is(loadErr, pgx.ErrNoRows) {
		return physicalpool.PhysicalPool{}, loadErr
	}
	// Resolve namespace uniqueness separately from the pool-id uniqueness so
	// callers never receive a provider constraint diagnostic.
	var other string
	if qErr := tx.QueryRow(ctx, `SELECT id FROM physical_pool.physical_pools WHERE storage_implementation=$1 AND storage_location=$2 AND storage_namespace=$3`, normalized.Identity.Compatibility.StorageImplementation, normalized.Identity.StorageLocation, normalized.Identity.StorageNamespace).Scan(&other); qErr == nil && other != string(normalized.ID) {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	return physicalpool.PhysicalPool{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
}

// CreateAndAdmit atomically persists a new identity and exact conformance
// evidence. Failed evidence leaves no pool row behind.
func (r *Repository) CreateAndAdmit(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, error) {
	if r == nil || r.db == nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, invalidRepo()
	}
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	var out physicalpool.PhysicalPool
	var admission physicalpool.PoolAdmission
	err = r.withTx(ctx, func(tx DBTX) error {
		var e error
		out, e = createPhysicalPoolTx(ctx, tx, normalized)
		if e != nil {
			return e
		}
		admission, e = admitTx(ctx, tx, out, evidence)
		if e != nil {
			return e
		}
		out, e = out.ApplyAdmission(admission)
		return e
	})
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	return out, admission, nil
}

func (r *Repository) CreateAndAdmitTx(ctx context.Context, tx Tx, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, error) {
	if tx == nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, invalidRepo()
	}
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	created, err := createPhysicalPoolTx(ctx, tx, normalized)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	a, err := admitTx(ctx, tx, created, evidence)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	admitted, err := created.ApplyAdmission(a)
	return admitted, a, err
}

// CreateAndAdmitWithOwnership first acquires the conditional marker in the
// actual physical namespace. The PostgreSQL claim row is audit evidence only;
// it never substitutes for marker verification.
func (r *Repository) CreateAndAdmitWithOwnership(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence, ownerID string, marker physicalpool.NamespaceOwnership) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, error) {
	if r == nil || r.db == nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, invalidRepo()
	}
	if marker == nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, physicalpool.ErrOwnershipConflict
	}
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	a, err := normalized.Admit(evidence)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	claim := physicalpool.OwnershipClaim{PoolID: pool.ID, CompatibilityDigest: a.CompatibilityDigest, EvidenceDigest: a.EvidenceDigest, OwnerID: ownerID}
	if err := claim.Validate(); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	if err := marker.AcquireNamespaceOwnership(ctx, claim); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	var out physicalpool.PhysicalPool
	var admission physicalpool.PoolAdmission
	err = r.withTx(ctx, func(tx DBTX) error {
		var e error
		out, e = createPhysicalPoolTx(ctx, tx, normalized)
		if e != nil {
			return e
		}
		admission, e = admitTx(ctx, tx, out, evidence)
		if e != nil {
			return e
		}
		if e = recordOwnershipClaimTx(ctx, tx, claim); e != nil {
			return e
		}
		out, e = out.ApplyAdmission(admission)
		return e
	})
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, err
	}
	return out, admission, nil
}

func recordOwnershipClaimTx(ctx context.Context, tx DBTX, claim physicalpool.OwnershipClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO physical_pool.namespace_ownership_claims(pool_id,compatibility_digest,evidence_digest,owner_id) VALUES($1,$2,$3,$4) ON CONFLICT(pool_id,evidence_digest) DO NOTHING`, string(claim.PoolID), claim.CompatibilityDigest, claim.EvidenceDigest, claim.OwnerID)
	if err != nil {
		return repositoryError(err)
	}
	var owner, compatibility string
	if err := tx.QueryRow(ctx, `SELECT owner_id,compatibility_digest FROM physical_pool.namespace_ownership_claims WHERE pool_id=$1 AND evidence_digest=$2`, string(claim.PoolID), claim.EvidenceDigest).Scan(&owner, &compatibility); err != nil {
		return repositoryError(err)
	}
	if owner != claim.OwnerID || compatibility != claim.CompatibilityDigest {
		return physicalpool.ErrOwnershipConflict
	}
	return nil
}

func (r *Repository) Admit(ctx context.Context, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PoolAdmission, error) {
	if r == nil || r.db == nil {
		return physicalpool.PoolAdmission{}, invalidRepo()
	}
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	var out physicalpool.PoolAdmission
	err = r.withTx(ctx, func(tx DBTX) error {
		var e error
		out, e = admitTx(ctx, tx, normalized, evidence)
		return e
	})
	return out, err
}

func (r *Repository) AdmitTx(ctx context.Context, tx Tx, pool physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PoolAdmission, error) {
	if tx == nil {
		return physicalpool.PoolAdmission{}, invalidRepo()
	}
	normalized, err := normalizePool(pool)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	return admitTx(ctx, tx, normalized, evidence)
}

func admitTx(ctx context.Context, tx DBTX, normalized physicalpool.PhysicalPool, evidence physicalpool.Evidence) (physicalpool.PoolAdmission, error) {
	a, err := normalized.Admit(evidence)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	compatJSON, err := a.Compatibility.CanonicalJSON()
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	evidenceJSON, normalizedEvidence, err := canonicalEvidenceJSON(evidence)
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	a.EvidenceDigest = normalizedEvidence.Digest
	stored, err := loadStoredPoolTx(ctx, tx, normalized.ID, a.Compatibility)
	if errors.Is(err, pgx.ErrNoRows) {
		return physicalpool.PoolAdmission{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticMissingField, "physical_pool")
	}
	if err != nil {
		return physicalpool.PoolAdmission{}, err
	}
	if !sameCanonicalIdentity(stored.Identity, normalized.Identity) {
		return physicalpool.PoolAdmission{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	var row compatibilityRow
	qErr := tx.QueryRow(ctx, `SELECT compatibility_json,duckdb_runtime,ducklake_extension,catalog_format,storage_implementation,object_naming_contract,evidence_json,evidence_digest,compatibility_digest,conformance_version FROM physical_pool.physical_pool_admissions WHERE pool_id=$1 AND evidence_digest=$2`, string(normalized.ID), a.EvidenceDigest).Scan(&row.CompatibilityJSON, &row.DuckDBRuntime, &row.DuckLakeExtension, &row.CatalogFormat, &row.StorageImplementation, &row.ObjectNamingContract, &row.EvidenceJSON, &row.EvidenceDigest, &row.CompatibilityDigest, &row.ConformanceVersion)
	if qErr == nil {
		_, _, existing, pErr := parseAdmissionRow(row, normalized.ID)
		if pErr != nil {
			return physicalpool.PoolAdmission{}, pErr
		}
		if !sameAdmission(existing, a) {
			return physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_digest")
		}
		return existing, nil
	}
	if !errors.Is(qErr, pgx.ErrNoRows) {
		return physicalpool.PoolAdmission{}, repositoryError(qErr)
	}
	result, err := tx.Exec(ctx, `INSERT INTO physical_pool.physical_pool_admissions(pool_id,compatibility_json,duckdb_runtime,ducklake_extension,catalog_format,storage_implementation,object_naming_contract,evidence_json,evidence_digest,compatibility_digest,conformance_version) VALUES($1,$2::jsonb,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11) ON CONFLICT DO NOTHING`, string(a.PoolID), compatJSON, a.Compatibility.DuckDBRuntime, a.Compatibility.DuckLakeExtension, a.Compatibility.CatalogFormat, a.Compatibility.StorageImplementation, a.Compatibility.ObjectNamingContract, evidenceJSON, a.EvidenceDigest, a.CompatibilityDigest, a.ConformanceVersion)
	if err == nil && result.RowsAffected() == 1 {
		return a, nil
	}
	if err != nil {
		return physicalpool.PoolAdmission{}, repositoryError(err)
	}
	// A concurrent immutable insert may have won. Resolve it identically.
	if retryErr := tx.QueryRow(ctx, `SELECT compatibility_json,duckdb_runtime,ducklake_extension,catalog_format,storage_implementation,object_naming_contract,evidence_json,evidence_digest,compatibility_digest,conformance_version FROM physical_pool.physical_pool_admissions WHERE pool_id=$1 AND evidence_digest=$2`, string(normalized.ID), a.EvidenceDigest).Scan(&row.CompatibilityJSON, &row.DuckDBRuntime, &row.DuckLakeExtension, &row.CatalogFormat, &row.StorageImplementation, &row.ObjectNamingContract, &row.EvidenceJSON, &row.EvidenceDigest, &row.CompatibilityDigest, &row.ConformanceVersion); retryErr == nil {
		_, _, existing, pErr := parseAdmissionRow(row, normalized.ID)
		if pErr != nil {
			return physicalpool.PoolAdmission{}, pErr
		}
		if sameAdmission(existing, a) {
			return existing, nil
		}
	}
	return physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_digest")
}

// LoadAdmissionContract reconstructs and verifies one exact immutable tuple.
// Callers must provide the tuple; selecting a latest admission is forbidden.
func (r *Repository) LoadAdmissionContract(ctx context.Context, poolID physicalpool.PoolID, tuple physicalpool.Compatibility) (physicalpool.AdmissionContract, error) {
	if r == nil || r.db == nil {
		return physicalpool.AdmissionContract{}, invalidRepo()
	}
	if strings.TrimSpace(string(poolID)) == "" {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticMissingField, "id")
	}
	if err := tuple.Validate(); err != nil {
		return physicalpool.AdmissionContract{}, err
	}
	requested := &tuple
	var out physicalpool.AdmissionContract
	err := r.readTx(ctx, func(tx DBTX) error {
		stored, e := loadStoredPoolTx(ctx, tx, poolID, *requested)
		if errors.Is(e, pgx.ErrNoRows) {
			return safe(physicalpool.ErrPoolNotAdmitted, physicalpool.DiagnosticMissingField, "physical_pool")
		}
		if e != nil {
			return e
		}
		var row compatibilityRow
		digest, dErr := requested.Digest()
		if dErr != nil {
			return dErr
		}
		e = tx.QueryRow(ctx, `SELECT compatibility_json,duckdb_runtime,ducklake_extension,catalog_format,storage_implementation,object_naming_contract,evidence_json,evidence_digest,compatibility_digest,conformance_version FROM physical_pool.physical_pool_admissions WHERE pool_id=$1 AND compatibility_digest=$2 ORDER BY admitted_at DESC,evidence_digest DESC LIMIT 1`, string(poolID), digest).Scan(&row.CompatibilityJSON, &row.DuckDBRuntime, &row.DuckLakeExtension, &row.CatalogFormat, &row.StorageImplementation, &row.ObjectNamingContract, &row.EvidenceJSON, &row.EvidenceDigest, &row.CompatibilityDigest, &row.ConformanceVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
		}
		if e != nil {
			return repositoryError(e)
		}
		out, e = verifyAdmissionContract(poolID, stored, row, nil)
		if e != nil {
			return e
		}
		if !out.Admission.Compatibility.Equal(*requested) {
			return safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
		}
		return nil
	})
	return out, err
}

func (r *Repository) LoadAdmissionContractByCompatibilityDigest(ctx context.Context, poolID physicalpool.PoolID, digest string) (physicalpool.AdmissionContract, error) {
	if strings.TrimSpace(digest) == "" {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrInvalidCompatibility, physicalpool.DiagnosticMissingField, "compatibility_digest")
	}
	if r == nil || r.db == nil {
		return physicalpool.AdmissionContract{}, invalidRepo()
	}
	var encoded []byte
	if err := r.db.QueryRow(ctx, `SELECT compatibility_json FROM physical_pool.physical_pool_admissions WHERE pool_id=$1 AND compatibility_digest=$2 ORDER BY admitted_at DESC,evidence_digest DESC LIMIT 1`, string(poolID), digest).Scan(&encoded); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return physicalpool.AdmissionContract{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
		}
		return physicalpool.AdmissionContract{}, repositoryError(err)
	}
	var tuple physicalpool.Compatibility
	if err := strictjson.Decode(encoded, &tuple); err != nil {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticInvalidField, "compatibility")
	}
	got, err := tuple.Digest()
	if err != nil || got != digest {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
	}
	return r.LoadAdmissionContract(ctx, poolID, tuple)
}

func (r *Repository) LoadAdmissionByEvidence(ctx context.Context, poolID physicalpool.PoolID, digest string) (physicalpool.AdmissionContract, error) {
	if strings.TrimSpace(digest) == "" {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticMissingField, "evidence_digest")
	}
	if r == nil || r.db == nil {
		return physicalpool.AdmissionContract{}, invalidRepo()
	}
	var out physicalpool.AdmissionContract
	err := r.readTx(ctx, func(tx DBTX) error {
		var row compatibilityRow
		e := tx.QueryRow(ctx, `SELECT compatibility_json,duckdb_runtime,ducklake_extension,catalog_format,storage_implementation,object_naming_contract,evidence_json,evidence_digest,compatibility_digest,conformance_version FROM physical_pool.physical_pool_admissions WHERE pool_id=$1 AND evidence_digest=$2 LIMIT 1`, string(poolID), digest).Scan(&row.CompatibilityJSON, &row.DuckDBRuntime, &row.DuckLakeExtension, &row.CatalogFormat, &row.StorageImplementation, &row.ObjectNamingContract, &row.EvidenceJSON, &row.EvidenceDigest, &row.CompatibilityDigest, &row.ConformanceVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticMissingField, "evidence_digest")
		}
		if e != nil {
			return repositoryError(e)
		}
		compatibility, _, _, parseErr := parseAdmissionRow(row, poolID)
		if parseErr != nil {
			return parseErr
		}
		stored, e := loadStoredPoolTx(ctx, tx, poolID, compatibility)
		if errors.Is(e, pgx.ErrNoRows) {
			return safe(physicalpool.ErrPoolNotAdmitted, physicalpool.DiagnosticMissingField, "physical_pool")
		}
		if e != nil {
			return e
		}
		out, e = verifyAdmissionContract(poolID, stored, row, nil)
		return e
	})
	return out, err
}

type compatibilityRow struct {
	CompatibilityJSON     []byte
	DuckDBRuntime         string
	DuckLakeExtension     string
	CatalogFormat         string
	StorageImplementation string
	ObjectNamingContract  string
	EvidenceJSON          []byte
	EvidenceDigest        string
	CompatibilityDigest   string
	ConformanceVersion    string
}

func loadStoredPoolTx(ctx context.Context, tx DBTX, id physicalpool.PoolID, fallback physicalpool.Compatibility) (physicalpool.PhysicalPool, error) {
	var row poolRow
	err := tx.QueryRow(ctx, `SELECT id,identity_digest,storage_location,storage_namespace,storage_implementation,object_naming_contract,region,tenant,isolation_boundary,encryption_key_ref,credential_reference,retention_authority,orphan_grace_period_seconds,reader_grace_period_seconds,build_grace_period_seconds,retention_policy FROM physical_pool.physical_pools WHERE id=$1`, string(id)).Scan(&row.ID, &row.IdentityDigest, &row.StorageLocation, &row.StorageNamespace, &row.StorageImplementation, &row.ObjectNamingContract, &row.Region, &row.Tenant, &row.IsolationBoundary, &row.EncryptionKeyRef, &row.CredentialReference, &row.RetentionAuthority, &row.OrphanGracePeriodSeconds, &row.ReaderGracePeriodSeconds, &row.BuildGracePeriodSeconds, &row.RetentionPolicy)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	return row.pool(fallback)
}

type poolRow struct {
	ID, IdentityDigest, StorageLocation, StorageNamespace, StorageImplementation, ObjectNamingContract, Region, Tenant, IsolationBoundary, EncryptionKeyRef, CredentialReference, RetentionAuthority string
	OrphanGracePeriodSeconds, ReaderGracePeriodSeconds, BuildGracePeriodSeconds                                                                                                                      int64
	RetentionPolicy                                                                                                                                                                                  []byte
}

func (r poolRow) pool(compatibility physicalpool.Compatibility) (physicalpool.PhysicalPool, error) {
	if compatibility == (physicalpool.Compatibility{}) {
		compatibility = physicalpool.Compatibility{StorageImplementation: r.StorageImplementation, ObjectNamingContract: r.ObjectNamingContract}
	}
	if compatibility.StorageImplementation != r.StorageImplementation || compatibility.ObjectNamingContract != r.ObjectNamingContract {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility")
	}
	retention := physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: r.OrphanGracePeriodSeconds, ReaderGracePeriodSeconds: r.ReaderGracePeriodSeconds, BuildGracePeriodSeconds: r.BuildGracePeriodSeconds}
	var encodedRetention physicalpool.RetentionPolicy
	if err := strictjson.Decode(r.RetentionPolicy, &encodedRetention); err != nil {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "retention_policy")
	}
	if encodedRetention != retention {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "retention_policy")
	}
	canon, err := canonicalRetentionJSON(retention)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	var parsed map[string]any
	_ = json.Unmarshal(r.RetentionPolicy, &parsed)
	var canonMap map[string]any
	_ = json.Unmarshal([]byte(canon), &canonMap)
	if !jsonEqual(parsed, canonMap) {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "retention_policy")
	}
	identity := physicalpool.PoolIdentity{StorageLocation: r.StorageLocation, StorageNamespace: r.StorageNamespace, Region: r.Region, Tenant: r.Tenant, IsolationBoundary: r.IsolationBoundary, EncryptionKeyRef: r.EncryptionKeyRef, CredentialReference: r.CredentialReference, RetentionAuthority: r.RetentionAuthority, RetentionPolicy: retention, Compatibility: compatibility}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		return physicalpool.PhysicalPool{}, err
	}
	if pool.Identity.StorageLocation != r.StorageLocation || string(pool.ID) != r.ID || r.IdentityDigest != r.ID {
		return physicalpool.PhysicalPool{}, safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "id")
	}
	return pool, nil
}

func verifyAdmissionContract(poolID physicalpool.PoolID, stored physicalpool.PhysicalPool, row compatibilityRow, expected *physicalpool.PhysicalPool) (physicalpool.AdmissionContract, error) {
	compatibility, evidence, admission, err := parseAdmissionRow(row, poolID)
	if err != nil {
		return physicalpool.AdmissionContract{}, err
	}
	identity := stored.Identity
	identity.Compatibility = compatibility
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		return physicalpool.AdmissionContract{}, err
	}
	if pool.ID != poolID || stored.ID != poolID {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	if expected != nil && !sameCanonicalIdentity(expected.Identity, pool.Identity) {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	if admission.PoolID != pool.ID {
		return physicalpool.AdmissionContract{}, safe(physicalpool.ErrPoolMismatch, physicalpool.DiagnosticPoolMismatch, "physical_pool_id")
	}
	admitted, err := pool.ApplyAdmission(admission)
	if err != nil {
		return physicalpool.AdmissionContract{}, err
	}
	if err := physicalpool.VerifyAdmission(admitted, compatibility, admission, evidence); err != nil {
		return physicalpool.AdmissionContract{}, err
	}
	return physicalpool.AdmissionContract{Pool: admitted, Admission: admission, Evidence: evidence}, nil
}

func parseAdmissionRow(row compatibilityRow, poolID physicalpool.PoolID) (physicalpool.Compatibility, physicalpool.Evidence, physicalpool.PoolAdmission, error) {
	var compatibility physicalpool.Compatibility
	if err := strictjson.Decode(row.CompatibilityJSON, &compatibility); err != nil {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "compatibility_json")
	}
	var evidence physicalpool.Evidence
	if err := strictjson.Decode(row.EvidenceJSON, &evidence); err != nil {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_json")
	}
	if compatibility.DuckDBRuntime != row.DuckDBRuntime || compatibility.DuckLakeExtension != row.DuckLakeExtension || compatibility.CatalogFormat != row.CatalogFormat || compatibility.StorageImplementation != row.StorageImplementation || compatibility.ObjectNamingContract != row.ObjectNamingContract {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "compatibility_json")
	}
	canonical, normalized, err := canonicalEvidenceJSON(evidence)
	_ = canonical
	if err != nil || normalized.Digest != row.EvidenceDigest {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "evidence_digest")
	}
	if !normalized.Compatibility.Equal(compatibility) || normalized.ConformanceVersion != row.ConformanceVersion {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrEvidenceInvalid, physicalpool.DiagnosticEvidenceMismatch, "conformance_version")
	}
	compatDigest, err := compatibility.Digest()
	if err != nil || compatDigest != row.CompatibilityDigest {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, safe(physicalpool.ErrCompatibilityMismatch, physicalpool.DiagnosticTupleMismatch, "compatibility_digest")
	}
	a := physicalpool.PoolAdmission{PoolID: poolID, Compatibility: compatibility, CompatibilityDigest: row.CompatibilityDigest, EvidenceDigest: row.EvidenceDigest, ConformanceVersion: row.ConformanceVersion}
	if err := a.Validate(); err != nil {
		return physicalpool.Compatibility{}, physicalpool.Evidence{}, physicalpool.PoolAdmission{}, err
	}
	return compatibility, normalized, a, nil
}

func sameAdmission(left, right physicalpool.PoolAdmission) bool { return left == right }
func sameCanonicalIdentity(left, right physicalpool.PoolIdentity) bool {
	l, e1 := left.CanonicalJSON()
	r, e2 := right.CanonicalJSON()
	return e1 == nil && e2 == nil && l == r
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
func canonicalEvidenceJSON(value physicalpool.Evidence) (string, physicalpool.Evidence, error) {
	normalized, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: value.Compatibility, ConformanceVersion: value.ConformanceVersion, Checks: value.Checks})
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
		return "", physicalpool.Evidence{}, err
	}
	return string(encoded), normalized, nil
}
func canonicalRetentionJSON(value physicalpool.RetentionPolicy) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(value)
	return string(b), err
}
func jsonEqual(a, b map[string]any) bool {
	return reflect.DeepEqual(a, b)
}

// AcquireNamespaceDeletionLease serializes destructive namespace deletion
// using PostgreSQL's clock. Expired rows can be taken over atomically.
func (r *Repository) AcquireNamespaceDeletionLease(ctx context.Context, ownerID string, ttl time.Duration) (string, error) {
	if r == nil || r.db == nil || strings.TrimSpace(ownerID) == "" || ttl <= 0 || ttl > 24*time.Hour {
		return "", physicalpool.ErrDeletionLeaseConflict
	}
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	var token string
	err = r.withTx(ctx, func(tx DBTX) error {
		e := tx.QueryRow(ctx, `INSERT INTO physical_pool.namespace_deletion_leases(singleton,owner_id,token,expires_at) VALUES(true,$1,$2::uuid,clock_timestamp()+($3::double precision * interval '1 second')) ON CONFLICT(singleton) DO UPDATE SET owner_id=EXCLUDED.owner_id,token=EXCLUDED.token,expires_at=EXCLUDED.expires_at,acquired_at=clock_timestamp() WHERE physical_pool.namespace_deletion_leases.expires_at<=clock_timestamp() RETURNING token`, ownerID, u.String(), ttl.Seconds()).Scan(&token)
		if errors.Is(e, pgx.ErrNoRows) {
			return physicalpool.ErrDeletionLeaseConflict
		}
		if e != nil {
			return repositoryError(e)
		}
		return nil
	})
	return token, err
}

func (r *Repository) VerifyNamespaceDeletionLease(ctx context.Context, ownerID, token string) error {
	if r == nil || r.db == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(token) == "" {
		return physicalpool.ErrDeletionLeaseConflict
	}
	var valid bool
	err := r.db.QueryRow(ctx, `SELECT owner_id=$1 AND token=$2::uuid AND expires_at>clock_timestamp() FROM physical_pool.namespace_deletion_leases WHERE singleton=true`, ownerID, token).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || err != nil || !valid {
		return physicalpool.ErrDeletionLeaseConflict
	}
	return nil
}
func (r *Repository) ReleaseNamespaceDeletionLease(ctx context.Context, ownerID, token string) error {
	if r == nil || r.db == nil || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(token) == "" {
		return nil
	}
	return r.withTx(ctx, func(tx DBTX) error {
		// Lock the singleton row while checking the owner. The DELETE repeats
		// the expiry predicate so a lease expiring between these statements is
		// never acknowledged as released.
		var owner, held string
		var active bool
		err := tx.QueryRow(ctx, `SELECT owner_id,token::text,expires_at>clock_timestamp() FROM physical_pool.namespace_deletion_leases WHERE singleton=true FOR UPDATE`).Scan(&owner, &held, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return repositoryError(err)
		}
		if owner != ownerID || held != token || !active {
			return physicalpool.ErrDeletionLeaseConflict
		}
		var deleted bool
		err = tx.QueryRow(ctx, `DELETE FROM physical_pool.namespace_deletion_leases WHERE singleton=true AND owner_id=$1 AND token=$2::uuid AND expires_at>clock_timestamp() RETURNING true`, ownerID, token).Scan(&deleted)
		if errors.Is(err, pgx.ErrNoRows) || !deleted {
			return physicalpool.ErrDeletionLeaseConflict
		}
		if err != nil {
			return repositoryError(err)
		}
		return nil
	})
}

func (r *Repository) withTx(ctx context.Context, fn func(DBTX) error) error {
	if b, ok := r.db.(beginner); ok {
		tx, err := b.Begin(ctx)
		if err != nil {
			return repositoryError(err)
		}
		defer tx.Rollback(ctx)
		if err := fn(tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return repositoryError(err)
		}
		return nil
	}
	return fn(r.db)
}
func (r *Repository) readTx(ctx context.Context, fn func(DBTX) error) error { return fn(r.db) }
func invalidRepo() error {
	return safe(physicalpool.ErrInvalidPool, physicalpool.DiagnosticInvalidField, "repository")
}
func safe(cause error, code physicalpool.DiagnosticCode, field string) error {
	return &physicalpool.DiagnosticsError{Cause: cause, Diagnostics: []physicalpool.Diagnostic{{Code: code, Field: field}}}
}
func repositoryError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("physical pool PostgreSQL repository: %w", err)
}

var _ physicalpool.PoolAdmissionRepository = (*Repository)(nil)
var _ physicalpool.NamespaceDeletionLeaseRepository = (*Repository)(nil)
