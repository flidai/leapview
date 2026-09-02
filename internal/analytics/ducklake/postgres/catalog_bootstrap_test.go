package postgres

import (
	"strings"
	"testing"
)

func TestDeriveCatalogIdentityIsStableAndPoolScoped(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	first, err := DeriveCatalogIdentity("pool-a", DefaultDuckLakeDatabase, digest, "0.3")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := DeriveCatalogIdentity("pool-a", DefaultDuckLakeDatabase, digest, "0.3")
	if err != nil {
		t.Fatal(err)
	}
	other, err := DeriveCatalogIdentity("pool-b", DefaultDuckLakeDatabase, digest, "0.3")
	if err != nil {
		t.Fatal(err)
	}
	if first != replay || first.CatalogUUID == other.CatalogUUID || first.MetadataSchema == other.MetadataSchema {
		t.Fatalf("catalog identity is not stable and pool-scoped: first=%#v replay=%#v other=%#v", first, replay, other)
	}
}

func TestCatalogBootstrapSchemaIsMinimalAndImmutable(t *testing.T) {
	for _, required := range []string{"catalog_identity", "catalog_runtime_compatibility", "catalog_identity_immutable", "REVOKE ALL ON SCHEMA ducklake FROM PUBLIC"} {
		if !strings.Contains(SchemaSQL(), required) {
			t.Fatalf("schema missing %q", required)
		}
	}
	for _, deferred := range []string{"snapshot_retention", "catalog_migration", "generation_binding", "semantic_attribute"} {
		if strings.Contains(SchemaSQL(), deferred) {
			t.Fatalf("deferred capability %q leaked into bootstrap schema", deferred)
		}
	}
}

func TestValidateDatabaseIdentityRejectsRoleOrDatabaseAlias(t *testing.T) {
	good := DatabaseIdentity{Database: DefaultDuckLakeDatabase, User: DefaultDuckLakeCatalogMigratorRole, SessionUser: DefaultDuckLakeCatalogMigratorRole}
	if err := ValidateDatabaseIdentity(good, DefaultDuckLakeDatabase, DefaultDuckLakeCatalogMigratorRole); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []DatabaseIdentity{
		{Database: DefaultControlDatabase, User: good.User, SessionUser: good.SessionUser},
		{Database: good.Database, User: "leapview_ducklake_runtime", SessionUser: "leapview_ducklake_runtime"},
		{Database: good.Database, User: good.User, SessionUser: "postgres"},
	} {
		if err := ValidateDatabaseIdentity(bad, DefaultDuckLakeDatabase, DefaultDuckLakeCatalogMigratorRole); err == nil {
			t.Fatalf("accepted wrong authority %#v", bad)
		}
	}
}
