package ducklake

import (
	"strings"
	"testing"
)

func TestPostgresCatalogInitializeStatementsExcludeCredentialsAndMigration(t *testing.T) {
	poolID := "pool-a"
	config := PostgresCatalogConfig{
		PhysicalPoolID: poolID, DuckLakeSecret: "leapview_lake", PostgresSecret: "leapview_pg",
		MetadataSchema: MetadataSchemaForPool(poolID), DataPath: "s3://bucket/prefix", Mode: PostgresCatalogInitialize,
	}
	statements, err := config.Statements()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(statements, "\n")
	for _, required := range []string{"METADATA_SCHEMA", "AUTOMATIC_MIGRATION false", "CREATE_IF_NOT_EXISTS true", "DATA_INLINING_ROW_LIMIT 0"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("statements missing %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"postgres://", "PASSWORD", "AUTOMATIC_MIGRATION true"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("statements expose forbidden %q: %s", forbidden, joined)
		}
	}
}

func TestPostgresCatalogInitializeRejectsUnsafeOrMismatchedIdentity(t *testing.T) {
	valid := PostgresCatalogConfig{
		PhysicalPoolID: "pool-a", DuckLakeSecret: "leapview_lake", PostgresSecret: "leapview_pg",
		MetadataSchema: MetadataSchemaForPool("pool-a"), DataPath: "/data", Mode: PostgresCatalogInitialize,
	}
	for name, mutate := range map[string]func(*PostgresCatalogConfig){
		"unsafe secret": func(c *PostgresCatalogConfig) { c.PostgresSecret = "bad-secret" },
		"wrong schema":  func(c *PostgresCatalogConfig) { c.MetadataSchema = MetadataSchemaForPool("pool-b") },
		"migration":     func(c *PostgresCatalogConfig) { c.Mode = "migrate" },
		"missing data":  func(c *PostgresCatalogConfig) { c.DataPath = "" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := config.Statements(); err == nil {
				t.Fatal("invalid PostgreSQL catalog config accepted")
			}
		})
	}
}
