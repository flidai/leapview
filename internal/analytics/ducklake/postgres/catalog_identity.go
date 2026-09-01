package postgres

import (
	"context"
	"fmt"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	"github.com/google/uuid"
)

const catalogIdentitySeedPrefix = "leapview/ducklake/catalog/v1\x00"

// CatalogRegistrationEvidence is read from the owner-capable DuckLake
// PostgreSQL connection after DuckLake has initialized its metadata tables.
// CatalogSchemaVersion is DuckLake's global metadata format version, not the
// mutable schema_version of an individual snapshot.
type CatalogRegistrationEvidence struct {
	CatalogDatabase      string
	CatalogSchemaVersion string
}

// BootstrapCatalog registers the first catalog identity and runtime tuple in
// one caller-owned control transaction. It is intentionally separate from
// fenced catalog upgrades: bootstrap can only insert a missing exact row or
// replay the existing one; it cannot change either identity.
func BootstrapCatalog(ctx context.Context, tx DBTX, identity CatalogIdentity, compatibility RuntimeCompatibility) (CatalogIdentity, CatalogRuntimeCompatibility, error) {
	if tx == nil || validateCatalog(identity) != nil || compatibility.validate() != nil ||
		identity.CompatibilityDigest != compatibility.CompatibilityDigest ||
		identity.CatalogSchemaVersion != compatibility.CatalogSchemaVersion {
		return CatalogIdentity{}, CatalogRuntimeCompatibility{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	catalog, err := RegisterCatalog(ctx, tx, identity)
	if err != nil {
		return CatalogIdentity{}, CatalogRuntimeCompatibility{}, err
	}
	initial := CatalogRuntimeCompatibility{
		PhysicalPoolID:       identity.PhysicalPoolID,
		CatalogID:            identity.CatalogID,
		RuntimeCompatibility: compatibility,
	}
	if err := querygen(tx).InsertInitialCatalogRuntimeCompatibility(ctx, dbgen.InsertInitialCatalogRuntimeCompatibilityParams{
		PhysicalPoolID:       initial.PhysicalPoolID,
		CatalogID:            initial.CatalogID,
		DuckdbRuntime:        initial.DuckDBRuntime,
		DucklakeExtension:    initial.DuckLakeExtension,
		CatalogFormat:        initial.CatalogFormat,
		CompatibilityDigest:  initial.CompatibilityDigest,
		CatalogSchemaVersion: initial.CatalogSchemaVersion,
	}); err != nil {
		return CatalogIdentity{}, CatalogRuntimeCompatibility{}, err
	}
	registered, err := LoadCatalogRuntimeCompatibility(ctx, tx, identity.PhysicalPoolID)
	if err != nil {
		return CatalogIdentity{}, CatalogRuntimeCompatibility{}, err
	}
	if registered.PhysicalPoolID != initial.PhysicalPoolID || registered.CatalogID != initial.CatalogID || !sameRuntimeCompatibility(registered.RuntimeCompatibility, initial.RuntimeCompatibility) {
		return CatalogIdentity{}, CatalogRuntimeCompatibility{}, fmt.Errorf("%w: physical pool %q runtime compatibility", ErrConflict, identity.PhysicalPoolID)
	}
	return catalog, registered, nil
}

// DeriveCatalogIdentity constructs the application-owned stable identity for
// the one DuckLake catalog bound to a physical pool. The UUID is RFC 9562
// version 5 and therefore repeats exactly across bootstrap retries.
func DeriveCatalogIdentity(physicalPoolID, catalogDatabase, compatibilityDigest, catalogSchemaVersion string) (CatalogIdentity, error) {
	catalogID := "ducklake:" + physicalPoolID
	identity := CatalogIdentity{
		PhysicalPoolID:       physicalPoolID,
		CatalogDatabase:      catalogDatabase,
		CatalogID:            catalogID,
		CatalogUUID:          uuid.NewSHA1(uuid.NameSpaceURL, []byte(catalogIdentitySeedPrefix+physicalPoolID)).String(),
		MetadataSchema:       ducklake.MetadataSchemaForPool(physicalPoolID),
		CompatibilityDigest:  compatibilityDigest,
		CatalogSchemaVersion: catalogSchemaVersion,
	}
	if err := validateCatalog(identity); err != nil {
		return CatalogIdentity{}, err
	}
	return identity, nil
}

// ReadCatalogRegistrationEvidence reads the two identity fields whose
// authority is the initialized DuckLake PostgreSQL catalog itself. The
// metadata schema is a validated deterministic identifier before it is
// interpolated into the query.
func ReadCatalogRegistrationEvidence(ctx context.Context, db DBTX, metadataSchema string) (CatalogRegistrationEvidence, error) {
	if db == nil || !validSchema(metadataSchema) {
		return CatalogRegistrationEvidence{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	databaseIdentity, err := ReadDatabaseIdentity(ctx, db)
	if err != nil {
		return CatalogRegistrationEvidence{}, err
	}
	query := `SELECT value,count(*) OVER () FROM ` + quoteSQLIdentifier(metadataSchema) + `.ducklake_metadata WHERE key='version' AND scope IS NULL AND scope_id IS NULL LIMIT 1`
	var catalogSchemaVersion string
	var matches int64
	// sqlc-exception:dynamic-identifier -- deterministic validated per-pool schema.
	if err := db.QueryRow(ctx, query).Scan(&catalogSchemaVersion, &matches); err != nil {
		return CatalogRegistrationEvidence{}, fmt.Errorf("read DuckLake catalog format version: %w", err)
	}
	evidence := CatalogRegistrationEvidence{
		CatalogDatabase:      databaseIdentity.Database,
		CatalogSchemaVersion: catalogSchemaVersion,
	}
	if matches != 1 || !validCatalogDatabase(evidence.CatalogDatabase) || !validID(evidence.CatalogSchemaVersion) {
		return CatalogRegistrationEvidence{}, ErrInvalid
	}
	return evidence, nil
}
