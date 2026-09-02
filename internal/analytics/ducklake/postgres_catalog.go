package ducklake

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const PostgresCatalogInitialize = "initialize"

var catalogIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PostgresCatalogConfig is the bounded catalog creation contract. PostgreSQL
// credentials are installed separately through CredentialBootstrap and never
// appear in the DuckLake ATTACH statement.
type PostgresCatalogConfig struct {
	PhysicalPoolID string
	DuckLakeSecret string
	PostgresSecret string
	MetadataSchema string
	DataPath       string
	Mode           string
}

func (c PostgresCatalogConfig) Validate() error {
	if c.Mode != PostgresCatalogInitialize {
		return fmt.Errorf("unsupported PostgreSQL DuckLake catalog mode %q", c.Mode)
	}
	if strings.TrimSpace(c.PhysicalPoolID) == "" || c.PhysicalPoolID != strings.TrimSpace(c.PhysicalPoolID) {
		return errors.New("physical pool id is required and normalized")
	}
	if c.MetadataSchema != MetadataSchemaForPool(c.PhysicalPoolID) {
		return errors.New("metadata schema is not admitted for the physical pool")
	}
	for label, value := range map[string]string{
		"DuckLake secret":   c.DuckLakeSecret,
		"PostgreSQL secret": c.PostgresSecret,
		"metadata schema":   c.MetadataSchema,
	} {
		if !catalogIdentifierPattern.MatchString(value) {
			return fmt.Errorf("%s is not a safe SQL identifier", label)
		}
	}
	if strings.TrimSpace(c.DataPath) == "" {
		return errors.New("DATA_PATH is required when initializing a PostgreSQL DuckLake catalog")
	}
	return nil
}

// Statements returns the temporary DuckLake secret and one initialize-only
// attach. Automatic migration remains disabled.
func (c PostgresCatalogConfig) Statements() ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	secret := fmt.Sprintf(
		"CREATE OR REPLACE TEMPORARY SECRET %s (TYPE ducklake, METADATA_PATH '', METADATA_PARAMETERS MAP {'TYPE': 'postgres', 'SECRET': '%s'})",
		quoteCatalogIdentifier(c.DuckLakeSecret), sqlLiteral(c.PostgresSecret),
	)
	attach := fmt.Sprintf(
		"ATTACH IF NOT EXISTS 'ducklake:%s' AS %s (METADATA_SCHEMA '%s', AUTOMATIC_MIGRATION false, DATA_PATH '%s', DATA_INLINING_ROW_LIMIT 0, CREATE_IF_NOT_EXISTS true)",
		sqlLiteral(c.DuckLakeSecret), quoteCatalogIdentifier(catalogAlias),
		sqlLiteral(c.MetadataSchema), sqlLiteral(c.DataPath),
	)
	return []string{secret, attach}, nil
}

// MetadataSchemaForPool derives a stable SQL-safe namespace without exposing
// tenant or storage identifiers in the catalog database.
func MetadataSchemaForPool(poolID string) string {
	digest := sha256.Sum256([]byte(poolID))
	return "leapview_catalog_" + hex.EncodeToString(digest[:])[:32]
}

func quoteCatalogIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
