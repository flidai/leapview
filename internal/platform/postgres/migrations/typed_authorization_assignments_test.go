package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestTypedAuthorizationAssignmentsMigrationIsForwardOnlyAndPairPinned(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "025_typed_authorization_assignments.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"authorization_snapshot",
		"authorization_role_binding",
		"authorization_grant",
		"authorization_policy_role_binding",
		"permission_profile",
		"permission_role",
		"permissions jsonb",
		"access.valid_permission_pairs",
		"NULLS NOT DISTINCT",
		"reject_authorization_identity_rewrite",
		"TG_TABLE_NAME = 'authorization_data_policy'",
		"typed authorization assignments migration is immutable",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("typed authorization assignment migration missing %q", required)
		}
	}
	down := migration[strings.Index(migration, "-- +goose Down"):]
	if strings.Contains(strings.ToUpper(down), "DROP COLUMN") || strings.Contains(strings.ToUpper(down), "DROP TABLE") {
		t.Error("typed authorization assignment migration must not destructively roll back evidence")
	}
}
