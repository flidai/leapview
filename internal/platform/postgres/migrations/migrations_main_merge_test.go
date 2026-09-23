package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestDashboardAuthoringDeleteMigrationIsForwardOnlyAndDraftScoped(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "028_dashboard_authoring_delete.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"dashboard.authoring_delete_commands",
		"dashboard.authoring_delete_dashboard",
		"only private draft dashboards can be deleted",
		"authoring delete compare-and-swap conflict",
		"destructive down is forbidden",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("dashboard delete migration missing %q", required)
		}
	}
	down := migration[strings.Index(migration, "-- +goose Down"):]
	if strings.Contains(strings.ToUpper(down), "DROP TABLE") {
		t.Error("dashboard delete migration Down must refuse instead of deleting evidence")
	}
	if !strings.HasSuffix(strings.TrimSpace(migration), "RESET ROLE;") {
		t.Error("dashboard delete migration must restore the migrator role")
	}
}

func TestDevelopmentSessionMigrationIsOwnerScopedAndImmutable(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "026_development_session.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, required := range []string{
		"project.development_session", "owner_id", "checkout_id", "worktree_id",
		"attempted_artifact_digest", "last_valid_candidate_id", "last_valid_preview_url",
		"UNIQUE (owner_id, checkout_id, worktree_id, project_id, target_id, environment)",
		"development_session_mutation_guard", "revision must increase monotonically",
		"GRANT SELECT, INSERT, UPDATE ON project.development_session TO leapview_control_runtime",
		"development session authority migration is immutable",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("development session migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(migration[strings.Index(migration, "-- +goose Down"):]), "DROP TABLE") {
		t.Error("development session migration Down must refuse instead of deleting evidence")
	}
	up := migration[:strings.Index(migration, "-- +goose Down")]
	if !strings.HasSuffix(strings.TrimSpace(up), "RESET ROLE;") {
		t.Error("development session migration Up must restore the Goose migrator role before version recording")
	}
	if !strings.HasSuffix(strings.TrimSpace(migration), "RESET ROLE;") {
		t.Error("development session migration must restore the migrator role")
	}
}
