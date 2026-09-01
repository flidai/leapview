package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDeriveCatalogIdentityIsDeterministicAndPoolScoped(t *testing.T) {
	poolID := "sha256:" + strings.Repeat("a", 64)
	compatibilityDigest := "sha256:" + strings.Repeat("b", 64)
	first, err := DeriveCatalogIdentity(poolID, "leapview_ducklake", compatibilityDigest, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := DeriveCatalogIdentity(poolID, "leapview_ducklake", compatibilityDigest, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !sameCatalog(first, replay) {
		t.Fatalf("derived identity replay differs: %#v != %#v", first, replay)
	}
	if first.CatalogID != "ducklake:"+poolID || first.MetadataSchema != ducklake.MetadataSchemaForPool(poolID) {
		t.Fatalf("derived pool identity = %#v", first)
	}
	other, err := DeriveCatalogIdentity("sha256:"+strings.Repeat("c", 64), "leapview_ducklake", compatibilityDigest, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	if other.CatalogID == first.CatalogID || other.CatalogUUID == first.CatalogUUID || other.MetadataSchema == first.MetadataSchema {
		t.Fatalf("catalog identity is not pool-scoped: first=%#v other=%#v", first, other)
	}
}

func TestDeriveCatalogIdentityRejectsIncompleteAuthorityEvidence(t *testing.T) {
	poolID := "sha256:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	for name, values := range map[string][3]string{
		"database":      {"", digest, "1.0"},
		"compatibility": {"leapview_ducklake", "", "1.0"},
		"version":       {"leapview_ducklake", digest, ""},
	} {
		if _, err := DeriveCatalogIdentity(poolID, values[0], values[1], values[2]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s error = %v, want ErrInvalid", name, err)
		}
	}
}

type catalogEvidenceDB struct {
	database string
	version  string
	query    string
}

func (*catalogEvidenceDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unused")
}
func (*catalogEvidenceDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (d *catalogEvidenceDB) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "current_database()") {
		return catalogEvidenceRow{values: []any{d.database, DefaultDuckLakeCatalogMigratorRole, DefaultDuckLakeCatalogMigratorRole}}
	}
	d.query = query
	return catalogEvidenceRow{values: []any{d.version, int64(1)}}
}

type catalogEvidenceRow struct{ values []any }

func (r catalogEvidenceRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan")
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		default:
			return errors.New("unexpected scan destination")
		}
	}
	return nil
}

func TestReadCatalogRegistrationEvidenceUsesGlobalDuckLakeVersion(t *testing.T) {
	db := &catalogEvidenceDB{database: "leapview_ducklake", version: "1.0"}
	schema := ducklake.MetadataSchemaForPool("pool-1")
	evidence, err := ReadCatalogRegistrationEvidence(t.Context(), db, schema)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CatalogDatabase != db.database || evidence.CatalogSchemaVersion != db.version {
		t.Fatalf("catalog evidence = %#v", evidence)
	}
	wantQuery := `SELECT value,count(*) OVER () FROM "` + schema + `".ducklake_metadata WHERE key='version' AND scope IS NULL AND scope_id IS NULL LIMIT 1`
	if db.query != wantQuery {
		t.Fatalf("catalog evidence query = %q, want %q", db.query, wantQuery)
	}
}
