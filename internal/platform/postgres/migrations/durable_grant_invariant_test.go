package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestResourceShareOnwardDelegationMigrationIsForwardOnly(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "030_resource_share_no_onward_delegation.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"ALTER TABLE access.resource_share_grant",
		"resource_share_grant_no_onward_delegation",
		"CHECK (allow_onward_delegation = false)",
		"resource share onward-delegation invariant is immutable",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("resource-share invariant migration missing %q", required)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(migration), "RESET ROLE;") {
		t.Error("resource-share invariant migration must restore the migrator role")
	}
	down := migration[strings.Index(migration, "-- +goose Down"):]
	if strings.Contains(strings.ToUpper(down), "DROP CONSTRAINT") {
		t.Error("resource-share invariant migration Down must not remove the security constraint")
	}
}
