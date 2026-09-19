package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueConstraintOnlyMatchesPostgresUniqueViolations(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505", ConstraintName: "authoring_dashboards_project_slug_key"}
	if !isUniqueConstraint(fmt.Errorf("create dashboard: %w", unique)) {
		t.Fatal("wrapped PostgreSQL unique violation was not recognized")
	}

	foreignKey := &pgconn.PgError{Code: "23503", ConstraintName: "authoring_dashboards_project_fk"}
	if isUniqueConstraint(foreignKey) {
		t.Fatal("PostgreSQL foreign-key violation was misclassified as an identity conflict")
	}
	if isUniqueConstraint(errors.New("unique constraint in untyped message")) {
		t.Fatal("untyped error text was misclassified as a PostgreSQL unique violation")
	}
}
