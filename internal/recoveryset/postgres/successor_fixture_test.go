package postgres_test

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset/successor"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// These tests validate durable recovery evidence, not physical restoration.
// This validates recovery evidence binding. It does not prove successful physical disaster recovery.
func TestSuccessorMaintenanceMigrationPath(t *testing.T) {
	pool, admin, _ := successorDatabase(t)
	var currentRole string
	if err := pool.QueryRow(t.Context(), `SELECT current_user`).Scan(&currentRole); err != nil {
		t.Fatal(err)
	}
	if currentRole != "leapview_control_maintenance" {
		t.Fatalf("unexpected role %s", currentRole)
	}
	for _, table := range []string{"successor_evidence_v2", "successor_evidence_locator_v2", "successor_manifest_binding", "recovery_set_v3", "recovery_set_v3_root"} {
		for _, role := range []string{"leapview_control_maintenance", "leapview_control_runtime", "leapview_control_readonly", "leapview_control_backup", "successor_unrelated"} {
			for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "TRIGGER"} {
				var allowed bool
				if err := admin.QueryRow(t.Context(), `SELECT has_table_privilege($1, $2, $3)`, role, "recovery."+table, privilege).Scan(&allowed); err != nil {
					t.Fatal(err)
				}
				want := role == "leapview_control_maintenance" && (privilege == "SELECT" || privilege == "INSERT")
				// Preserve the canonical backup/read-only roles' read capability;
				// withholding SELECT would break normal whole-database backups.
				want = want || privilege == "SELECT" && (role == "leapview_control_backup" || role == "leapview_control_readonly")
				if allowed != want {
					t.Errorf("%s %s %s=%t want %t", role, table, privilege, allowed, want)
				}
			}
		}
	}
}

func successorDatabase(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, string) {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "disposable-migrator", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "disposable-maintenance", Login: true})
	for _, name := range []string{"leapview_control_runtime", "leapview_control_readonly", "leapview_control_backup", "successor_unrelated"} {
		h.EnsureRole(t, postgrestest.Role{Name: name})
	}
	h.GrantRole(t, owner, migrator)
	d := h.NewDatabase(t, "successor_qualification")
	h.GrantDatabase(t, d.Name, owner, "CREATE")
	h.GrantDatabase(t, d.Name, migrator, "CONNECT", "CREATE")
	h.GrantDatabase(t, d.Name, maintenance, "CONNECT")
	admin, err := pgxpool.New(t.Context(), d.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `ALTER DATABASE successor_qualification OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	migrationDB, err := sql.Open("pgx", d.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDB.Close()
	migrationPool, err := pgxpool.New(t.Context(), d.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()
	if err := postgresbaseline.ApplyWithMigrationFence(t.Context(), migrationPool, migrationDB); err != nil {
		t.Fatal(err)
	}
	if err := postgresbaseline.Apply(t.Context(), migrationDB); err != nil {
		t.Fatalf("migration retry: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), d.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, admin, d.URL(maintenance)
}

func successorGolden(t *testing.T, name string) (successor.RecoverySet3, successor.Evidence, map[string][]byte) {
	t.Helper()
	docs := make(map[string][]byte)
	for _, part := range []string{"set", "manifest", "anchor", "profiles", "receipt", "authority"} {
		raw, err := os.ReadFile(filepath.Join("..", "successor", "testdata", "frozen", name+"-"+part+".json"))
		if err != nil {
			t.Fatal(err)
		}
		docs[part] = bytes.TrimSuffix(raw, []byte("\n"))
	}
	set, err := successor.ParseRecoverySet3(docs["set"])
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := successor.ParseManagedManifest2(docs["manifest"])
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := successor.ParseSourceAnchor(docs["anchor"])
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := successor.ParseProviderProfileSet(docs["profiles"])
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := successor.ParseReceipt(docs["receipt"])
	if err != nil {
		t.Fatal(err)
	}
	authority, err := successor.ParseAuthorityRegistry(docs["authority"])
	if err != nil {
		t.Fatal(err)
	}
	// Golden source/authority/clock are independently selected fixture inputs.
	// Negative submissions never update these trusted expectations.
	e := successor.Evidence{Manifest: manifest, Anchor: anchor, Profiles: profiles, Receipt: receipt, Authorities: authority,
		ExpectedScope:    successor.ExpectedScope{SetID: set.ID, SourceFrontierAnchorDigest: set.SourceFrontierAnchorDigest, ManagedClosureDigest: manifest.ManagedClosureDigest, EmptyScopeVerified: name == "empty"},
		VerificationTime: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)}
	if err := set.ValidateEvidence(e); err != nil {
		t.Fatal(err)
	}
	return set, e, docs
}
