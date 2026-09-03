package postgres

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestListPrincipalSemanticAttributeGroupsExcludesRevokedGroups(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate PostgreSQL access package source")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(sourcePath), "queries", "semantic_attribute_control.sql"))
	if err != nil {
		t.Fatalf("read semantic attribute control queries: %v", err)
	}

	source := string(body)
	start := strings.Index(source, "-- name: ListPrincipalSemanticAttributeGroups :many")
	if start < 0 {
		t.Fatal("ListPrincipalSemanticAttributeGroups query is missing")
	}
	query := source[start:]
	if next := strings.Index(query, "\n-- name:"); next >= 0 {
		query = query[:next]
	}
	for _, predicate := range []string{
		"JOIN access.access_group g ON g.id = pg.group_id",
		"pg.revoked_at IS NULL",
		"g.revoked_at IS NULL",
	} {
		if !strings.Contains(query, predicate) {
			t.Errorf("ListPrincipalSemanticAttributeGroups query lacks %q:\n%s", predicate, query)
		}
	}
}
