package postgres

import (
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestSemanticAttributeSearchOwnerCursorPaginatesCompleteFilteredResult(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, AttributeRegistryMigrationSQL()); err != nil {
		t.Fatalf("apply attribute registry migration: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `
		INSERT INTO access.principal (id, principal_type, status)
		VALUES ($1::uuid, 'user', 'active')`, auditActorID); err != nil {
		t.Fatal(err)
	}
	_, err := db.admin.Exec(ctx, `
		INSERT INTO access.semantic_attribute_definition
			(definition_id, name, value_type, value_shape, profile, owner_kind, owner_id)
		SELECT ('00000000-0000-0000-0000-' || lpad(to_hex(i + 1), 12, '0'))::uuid,
			'pagination_attribute_' || lpad(i::text, 3, '0'), 'String', 'scalar',
			'leapview.semantic-access/v1',
			CASE WHEN i < 45 THEN 'instance' ELSE 'principal' END,
			CASE WHEN i < 45 THEN NULL ELSE $1::uuid END
		FROM generate_series(0, 249) AS series(i)`, auditActorID)
	if err != nil {
		t.Fatalf("insert pagination definitions: %v", err)
	}
	repo, err := NewAccess(db.admin, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}

	const pageSize = 50
	var names []string
	filter := access.SemanticAttributeSearch{OwnerKind: access.SemanticAttributeOwnerPrincipal, Limit: pageSize + 1}
	for {
		rows, searchErr := repo.SearchSemanticAttributes(ctx, filter)
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		if len(rows) == 0 {
			break
		}
		page := rows
		if len(page) > pageSize {
			page = page[:pageSize]
		}
		for _, row := range page {
			names = append(names, row.Name)
		}
		if len(rows) <= pageSize {
			break
		}
		last := page[len(page)-1]
		filter.AfterName, filter.AfterDefinitionID = last.Name, last.ID
	}

	if len(names) != 205 {
		t.Fatalf("filtered definitions = %d, want 205", len(names))
	}
	for i, name := range names {
		want := fmt.Sprintf("pagination_attribute_%03d", 45+i)
		if name != want {
			t.Fatalf("filtered definition %d = %q, want %q", i, name, want)
		}
	}
}
