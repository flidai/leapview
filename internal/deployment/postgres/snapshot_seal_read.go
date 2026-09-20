package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

func sameSealIdentity(a SnapshotSeal, b SnapshotSeal) bool {
	return a.AttemptID == b.AttemptID && a.CandidateID == b.CandidateID && a.PhysicalPoolID == b.PhysicalPoolID && a.TenantDomain == b.TenantDomain && a.Region == b.Region && a.EncryptionDomain == b.EncryptionDomain && a.ObjectNamespace == b.ObjectNamespace && a.CatalogDatabase == b.CatalogDatabase && a.CatalogID == b.CatalogID && a.CatalogUUID == b.CatalogUUID && a.CatalogVersion == b.CatalogVersion && a.DuckLakeSnapshotID == b.DuckLakeSnapshotID && a.RelationNamespace == b.RelationNamespace && a.RelationManifestDigest == b.RelationManifestDigest && a.ClosureDigest == b.ClosureDigest && a.ObjectRoot == b.ObjectRoot && a.ObjectRootDigest == b.ObjectRootDigest && a.ArtifactRoot == b.ArtifactRoot && a.ArtifactRootDigest == b.ArtifactRootDigest && a.CompiledGraphDigest == b.CompiledGraphDigest && a.CompiledConfigDigest == b.CompiledConfigDigest && a.SecurityDomainFingerprint == b.SecurityDomainFingerprint && a.AuthorizationPolicyRevision == b.AuthorizationPolicyRevision && a.AuthorizationPolicyDigest == b.AuthorizationPolicyDigest && a.RequestDigest == b.RequestDigest && a.PlanDigest == b.PlanDigest && a.CompatibilityDigest == b.CompatibilityDigest && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.DuckDBVersion == b.DuckDBVersion && a.RuntimeVersion == b.RuntimeVersion && a.DuckLakeExtensionVersion == b.DuckLakeExtensionVersion && a.DuckLakeSpecVersion == b.DuckLakeSpecVersion && a.CatalogSchemaVersion == b.CatalogSchemaVersion && sameCanonical(a.QualificationEvidence, b.QualificationEvidence) && sameOptionalCanonical(a.ResolvedInputs, b.ResolvedInputs) && a.ResolvedInputsDigest == b.ResolvedInputsDigest
}

func sameCanonical(a, b []byte) bool {
	aa, err1 := canonicalObject(a, maxEvidence, true)
	bb, err2 := canonicalObject(b, maxEvidence, true)
	return err1 == nil && err2 == nil && bytes.Equal(aa, bb)
}

func sameOptionalCanonical(a, b []byte) bool {
	if len(a) == 0 || strings.TrimSpace(string(a)) == "{}" {
		a = []byte(`{}`)
	}
	if len(b) == 0 || strings.TrimSpace(string(b)) == "{}" {
		b = []byte(`{}`)
	}
	return sameCanonical(a, b)
}

func loadSeal(ctx context.Context, db DBTX, id string) (SnapshotSeal, error) {
	row, err := depdb.New(db).GetSnapshotSeal(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	}
	if err != nil {
		return SnapshotSeal{}, err
	}
	return SnapshotSeal{
		SealID: row.SealID, AttemptID: row.AttemptID, CandidateID: row.CandidateID,
		PhysicalPoolID: row.PhysicalPoolID, TenantDomain: row.TenantDomain, Region: row.Region,
		EncryptionDomain: row.EncryptionDomain, ObjectNamespace: row.ObjectNamespace,
		CatalogDatabase: row.CatalogDatabase, CatalogID: row.CatalogID, CatalogUUID: row.CatalogUuid,
		CatalogVersion: row.CatalogVersion, DuckLakeSnapshotID: row.DucklakeSnapshotID,
		RelationNamespace: row.RelationNamespace, RelationManifestDigest: row.RelationManifestDigest,
		ClosureDigest: row.ClosureDigest, ObjectRoot: row.ObjectRoot, ObjectRootDigest: row.ObjectRootDigest,
		ArtifactRoot: row.ArtifactRoot, ArtifactRootDigest: row.ArtifactRootDigest,
		CompiledGraphDigest: row.CompiledGraphDigest, CompiledConfigDigest: row.CompiledConfigDigest,
		SecurityDomainFingerprint: row.SecurityDomainFingerprint, AuthorizationPolicyRevision: row.AuthorizationPolicyRevision,
		AuthorizationPolicyDigest: row.AuthorizationPolicyDigest, RequestDigest: row.RequestDigest,
		PlanDigest: row.PlanDigest, CompatibilityDigest: row.CompatibilityDigest,
		ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest,
		DuckDBVersion: row.DuckdbVersion, RuntimeVersion: row.RuntimeVersion,
		DuckLakeExtensionVersion: row.DucklakeExtensionVersion, DuckLakeSpecVersion: row.DucklakeSpecVersion,
		CatalogSchemaVersion: row.CatalogSchemaVersion, QualificationEvidence: append([]byte(nil), row.QualificationEvidence...),
		CreatedAt: dbTime(row.CreatedAt), ResolvedInputs: append([]byte(nil), row.ResolvedInputs...),
		ResolvedInputsDigest: row.ResolvedInputsDigest, QualifiedAt: dbTime(row.QualifiedAt),
	}, nil
}

func (r *Repository) SnapshotSeal(ctx context.Context, id string) (SnapshotSeal, error) {
	db, err := requireDB(r)
	if err != nil {
		return SnapshotSeal{}, err
	}
	id, err = uuidID(id, "seal id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	return loadSeal(ctx, db, id)
}

// SnapshotSealTx is the transaction-aware immutable seal projection.
func (r *Repository) SnapshotSealTx(ctx context.Context, tx Tx, id string) (SnapshotSeal, error) {
	if tx == nil {
		return SnapshotSeal{}, ErrInvalid
	}
	id, err := uuidID(id, "seal id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	return loadSeal(ctx, tx, id)
}

func (r *Repository) LoadSnapshotSeal(ctx context.Context, id string) (SnapshotSeal, error) {
	return r.SnapshotSeal(ctx, id)
}
