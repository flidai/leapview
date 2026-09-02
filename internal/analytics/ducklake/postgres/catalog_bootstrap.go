// Package postgres owns the minimal PostgreSQL control-plane identity ledger
// needed to bootstrap a PostgreSQL-backed DuckLake catalog. Serving,
// migration, retention, and deployment lifecycle authorities are deliberately
// outside this reconciliation slice.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	DefaultControlDatabase             = "leapview_control"
	DefaultDuckLakeDatabase            = "leapview_ducklake"
	DefaultDuckLakeCatalogMigratorRole = "leapview_ducklake_migrator"
	catalogIdentitySeedPrefix          = "leapview/ducklake/catalog/v1\x00"
)

var (
	ErrInvalid                 = errors.New("invalid DuckLake PostgreSQL identity")
	ErrConflict                = errors.New("DuckLake PostgreSQL identity conflict")
	ErrWrongDatabaseCredential = errors.New("DuckLake credential is connected to the wrong database or role")
)

//go:embed schema.sql
var schemaSQL string

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type DatabaseIdentity struct {
	Database    string
	User        string
	SessionUser string
}

type CatalogIdentity struct {
	PhysicalPoolID       string
	CatalogDatabase      string
	CatalogID            string
	CatalogUUID          string
	MetadataSchema       string
	CompatibilityDigest  string
	CatalogSchemaVersion string
	CreatedAt            time.Time
}

type RuntimeCompatibility struct {
	DuckDBRuntime        string
	DuckLakeExtension    string
	CatalogFormat        string
	CompatibilityDigest  string
	CatalogSchemaVersion string
}

type CatalogRegistrationEvidence struct {
	CatalogDatabase      string
	CatalogSchemaVersion string
}

func SchemaSQL() string { return schemaSQL }

func ReadDatabaseIdentity(ctx context.Context, db DBTX) (DatabaseIdentity, error) {
	if db == nil {
		return DatabaseIdentity{}, ErrInvalid
	}
	row, err := dbgen.New(db).ReadDatabaseIdentity(ctx)
	if err != nil {
		return DatabaseIdentity{}, err
	}
	return DatabaseIdentity{Database: row.DatabaseName, User: row.UserName, SessionUser: row.SessionUserName}, nil
}

func ValidateDatabaseIdentity(identity DatabaseIdentity, expectedDatabase, expectedRole string) error {
	expectedDatabase, expectedRole = strings.TrimSpace(expectedDatabase), strings.TrimSpace(expectedRole)
	if expectedDatabase == "" || expectedRole == "" || identity.Database != expectedDatabase || identity.User != expectedRole || identity.SessionUser != expectedRole {
		return fmt.Errorf("%w: got database=%q user=%q session_user=%q, expected database=%q role=%q", ErrWrongDatabaseCredential, identity.Database, identity.User, identity.SessionUser, expectedDatabase, expectedRole)
	}
	return nil
}

func EnsureCatalogMetadataSchema(ctx context.Context, db DBTX, metadataSchema string) error {
	if db == nil || !validSchema(metadataSchema) {
		return ErrInvalid
	}
	// sqlc-exception:schema-ddl -- the deterministic schema identifier is
	// validated before interpolation; PostgreSQL identifiers cannot be bound.
	if _, err := db.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoteIdentifier(metadataSchema)); err != nil {
		return fmt.Errorf("create DuckLake metadata schema: %w", err)
	}
	var currentUser, owner string
	// sqlc-exception:system-catalog -- sqlc's standalone schema parser does not
	// model pg_catalog tables; the schema name remains a bound parameter.
	if err := db.QueryRow(ctx, `SELECT current_user::text, r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname=$1`, metadataSchema).Scan(&currentUser, &owner); err != nil {
		return fmt.Errorf("verify DuckLake metadata schema owner: %w", err)
	}
	if strings.TrimSpace(currentUser) == "" || currentUser != owner {
		return fmt.Errorf("%w: DuckLake metadata schema owner is not the catalog migrator", ErrWrongDatabaseCredential)
	}
	return nil
}

func ProvisionCatalogPrivileges(ctx context.Context, db DBTX, metadataSchema, runtimeRole, maintenanceRole string) error {
	if db == nil || !validSchema(metadataSchema) || !validSchema(runtimeRole) || !validSchema(maintenanceRole) || runtimeRole == maintenanceRole {
		return ErrInvalid
	}
	schema := quoteIdentifier(metadataSchema)
	for _, roleName := range []string{runtimeRole, maintenanceRole} {
		role := quoteIdentifier(roleName)
		for _, statement := range []string{
			fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role),
			fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s", schema, role),
			fmt.Sprintf("GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %s TO %s", schema, role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", schema, role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s", schema, role),
			fmt.Sprintf("REVOKE CREATE ON SCHEMA %s FROM %s", schema, role),
		} {
			// sqlc-exception:schema-ddl -- validated dynamic schema and role identifiers.
			if _, err := db.Exec(ctx, statement); err != nil {
				return fmt.Errorf("provision DuckLake catalog privileges: %w", err)
			}
		}
	}
	return nil
}

func ReadCatalogRegistrationEvidence(ctx context.Context, db DBTX, metadataSchema string) (CatalogRegistrationEvidence, error) {
	if db == nil || !validSchema(metadataSchema) {
		return CatalogRegistrationEvidence{}, ErrInvalid
	}
	database, err := ReadDatabaseIdentity(ctx, db)
	if err != nil {
		return CatalogRegistrationEvidence{}, err
	}
	query := `SELECT value, count(*) OVER () FROM ` + quoteIdentifier(metadataSchema) + `.ducklake_metadata WHERE key='version' AND scope IS NULL AND scope_id IS NULL LIMIT 1`
	var version string
	var matches int64
	// sqlc-exception:dynamic-identifier -- deterministic validated per-pool schema.
	if err := db.QueryRow(ctx, query).Scan(&version, &matches); err != nil {
		return CatalogRegistrationEvidence{}, fmt.Errorf("read DuckLake catalog format version: %w", err)
	}
	if matches != 1 || !validID(database.Database, 255) || !validID(version, 128) {
		return CatalogRegistrationEvidence{}, ErrInvalid
	}
	return CatalogRegistrationEvidence{CatalogDatabase: database.Database, CatalogSchemaVersion: version}, nil
}

func DeriveCatalogIdentity(poolID, database, compatibilityDigest, schemaVersion string) (CatalogIdentity, error) {
	identity := CatalogIdentity{
		PhysicalPoolID: poolID, CatalogDatabase: database, CatalogID: "ducklake:" + poolID,
		CatalogUUID:    uuid.NewSHA1(uuid.NameSpaceURL, []byte(catalogIdentitySeedPrefix+poolID)).String(),
		MetadataSchema: ducklake.MetadataSchemaForPool(poolID), CompatibilityDigest: compatibilityDigest,
		CatalogSchemaVersion: schemaVersion,
	}
	if err := validateCatalog(identity); err != nil {
		return CatalogIdentity{}, err
	}
	return identity, nil
}

func BootstrapCatalog(ctx context.Context, tx DBTX, identity CatalogIdentity, compatibility RuntimeCompatibility) error {
	if tx == nil || validateCatalog(identity) != nil || validateCompatibility(compatibility) != nil ||
		identity.CompatibilityDigest != compatibility.CompatibilityDigest || identity.CatalogSchemaVersion != compatibility.CatalogSchemaVersion {
		return ErrInvalid
	}
	q := dbgen.New(tx)
	if err := q.InsertCatalogIdentity(ctx, dbgen.InsertCatalogIdentityParams{
		PhysicalPoolID: identity.PhysicalPoolID, CatalogDatabase: identity.CatalogDatabase,
		CatalogID: identity.CatalogID, CatalogUuid: identity.CatalogUUID,
		MetadataSchema: identity.MetadataSchema, CompatibilityDigest: identity.CompatibilityDigest,
		CatalogSchemaVersion: identity.CatalogSchemaVersion,
	}); err != nil {
		return err
	}
	stored, err := q.GetCatalogIdentity(ctx, identity.PhysicalPoolID)
	if err != nil {
		return err
	}
	if stored.CatalogDatabase != identity.CatalogDatabase || stored.CatalogID != identity.CatalogID || stored.CatalogUuid != identity.CatalogUUID || stored.MetadataSchema != identity.MetadataSchema || stored.CompatibilityDigest != identity.CompatibilityDigest || stored.CatalogSchemaVersion != identity.CatalogSchemaVersion {
		return fmt.Errorf("%w: physical pool %q catalog identity", ErrConflict, identity.PhysicalPoolID)
	}
	if err := q.InsertInitialCatalogRuntimeCompatibility(ctx, dbgen.InsertInitialCatalogRuntimeCompatibilityParams{
		PhysicalPoolID: identity.PhysicalPoolID, CatalogID: identity.CatalogID,
		DuckdbRuntime: compatibility.DuckDBRuntime, DucklakeExtension: compatibility.DuckLakeExtension,
		CatalogFormat: compatibility.CatalogFormat, CompatibilityDigest: compatibility.CompatibilityDigest,
		CatalogSchemaVersion: compatibility.CatalogSchemaVersion,
	}); err != nil {
		return err
	}
	runtime, err := q.GetCatalogRuntimeCompatibility(ctx, identity.PhysicalPoolID)
	if err != nil {
		return err
	}
	if runtime.CatalogID != identity.CatalogID || runtime.DuckdbRuntime != compatibility.DuckDBRuntime || runtime.DucklakeExtension != compatibility.DuckLakeExtension || runtime.CatalogFormat != compatibility.CatalogFormat || runtime.CompatibilityDigest != compatibility.CompatibilityDigest || runtime.CatalogSchemaVersion != compatibility.CatalogSchemaVersion {
		return fmt.Errorf("%w: physical pool %q runtime compatibility", ErrConflict, identity.PhysicalPoolID)
	}
	return nil
}

func validateCatalog(identity CatalogIdentity) error {
	if !validID(identity.PhysicalPoolID, 255) || !validID(identity.CatalogDatabase, 255) || !validID(identity.CatalogID, 255) || !validSchema(identity.MetadataSchema) || !validID(identity.CatalogSchemaVersion, 128) || !validDigest(identity.CompatibilityDigest) {
		return ErrInvalid
	}
	u, err := uuid.Parse(identity.CatalogUUID)
	if err != nil || u.String() != identity.CatalogUUID {
		return ErrInvalid
	}
	return nil
}

func validateCompatibility(c RuntimeCompatibility) error {
	if !validID(c.DuckDBRuntime, 255) || !validID(c.DuckLakeExtension, 255) || !validID(c.CatalogFormat, 255) || !validDigest(c.CompatibilityDigest) || !validID(c.CatalogSchemaVersion, 128) {
		return ErrInvalid
	}
	return nil
}

func validID(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && !strings.ContainsRune(value, '\x00')
}

func validSchema(value string) bool {
	if !validID(value, 128) {
		return false
	}
	for i, r := range value {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
