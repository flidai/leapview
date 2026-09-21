package migrations

import (
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// This is the exact revision-023 migration applied to the live/audit lineage,
// copied from 938ff6b68726b72959eae5bdb2696954b890b528 for qualification only.
//
//go:embed testdata/023_dashboard_authoring_delete.sql
var dashboardDeleteRevision23 []byte

const dashboardDeleteRevision23SHA256 = "2c4fadac6aa63193883e655218f938f8e7f66f85c54a773e95fa9061e10efcf2"

func TestSchema24ConvergesBothRevision23Lineages(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "schema-24-migrator"})
	runtime := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "schema-24-runtime"})
	for _, name := range []string{"leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		harness.EnsureRole(t, postgrestest.Role{Name: name})
	}
	harness.GrantRole(t, owner, migrator)

	tests := []struct {
		name       string
		lineage    func(*testing.T) fstest.MapFS
		seed       func(*testing.T, *pgxpool.Pool)
		assertData func(*testing.T, *pgxpool.Pool)
	}{
		{
			name: "live dashboard-delete revision 023",
			lineage: func(t *testing.T) fstest.MapFS {
				got := fmt.Sprintf("%x", sha256.Sum256(dashboardDeleteRevision23))
				if got != dashboardDeleteRevision23SHA256 {
					t.Fatalf("dashboard-delete revision 023 digest = %s, want %s", got, dashboardDeleteRevision23SHA256)
				}
				migrations := migrationSetThrough(t, 22)
				migrations["023_dashboard_authoring_delete.sql"] = &fstest.MapFile{Data: dashboardDeleteRevision23}
				return migrations
			},
			seed: func(t *testing.T, admin *pgxpool.Pool) {
				if _, err := admin.Exec(t.Context(), `
					INSERT INTO project.project_identity(project_id,title) VALUES ('schema24-live-project','Schema 24 live project');
					INSERT INTO dashboard.authoring_delete_commands(
						project_id,dashboard_id,command_id,request_fingerprint,revision_id,revision_number,content_hash
					) VALUES (
						'schema24-live-project','deleted-dashboard','11111111-1111-4111-8111-111111111111',
						'delete-request','22222222-2222-4222-8222-222222222222',7,
						'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
					)`); err != nil {
					t.Fatal(err)
				}
			},
			assertData: func(t *testing.T, admin *pgxpool.Pool) {
				assertRowCount(t, admin, "dashboard.authoring_delete_commands", 1)
				assertRowCount(t, admin, "project.project_identity", 1)
				var revision int64
				if err := admin.QueryRow(t.Context(), `SELECT revision_number FROM dashboard.authoring_delete_commands WHERE dashboard_id='deleted-dashboard'`).Scan(&revision); err != nil || revision != 7 {
					t.Fatalf("preserved dashboard-delete revision = %d, error = %v; want 7", revision, err)
				}
			},
		},
		{
			name:    "current-main recovery-ledger revision 023",
			lineage: func(t *testing.T) fstest.MapFS { return migrationSetThrough(t, 23) },
			seed: func(t *testing.T, admin *pgxpool.Pool) {
				if _, err := admin.Exec(t.Context(), `
					INSERT INTO project.project_identity(project_id,title) VALUES ('schema24-main-project','Schema 24 main project');
					INSERT INTO refresh.recovery_qualification_schedule(
						schedule_revision_id,schedule_id,scenario,operation,policy_version,policy_sha256,
						target_scope,artifact_identity,cron,timezone,stale_after,next_run_at,valid_from,updated_at
					) VALUES (
						'revision-1','schedule-1','restore-proof','restore','policy-v1',repeat('b',64),
						'demo','sha256:'||repeat('c',64),'0 0 * * *','UTC',interval '1 day',
						'2030-01-02 00:00:00+00','2030-01-01 00:00:00+00','2030-01-01 00:00:00+00'
					);
					INSERT INTO refresh.recovery_qualification_occurrence(
						occurrence_id,request_digest,schedule_id,schedule_revision_id,scenario,operation,
						policy_version,policy_sha256,target_scope,artifact_identity,planned_at,expires_at,created_at
					) VALUES (
						'occurrence-1','sha256:'||repeat('d',64),'schedule-1','revision-1','restore-proof','restore',
						'policy-v1',repeat('b',64),'demo','sha256:'||repeat('c',64),
						'2030-01-02 00:00:00+00','2030-01-03 00:00:00+00','2030-01-01 00:00:00+00'
					)`); err != nil {
					t.Fatal(err)
				}
			},
			assertData: func(t *testing.T, admin *pgxpool.Pool) {
				assertRowCount(t, admin, "refresh.recovery_qualification_schedule", 1)
				assertRowCount(t, admin, "refresh.recovery_qualification_occurrence", 1)
				assertRowCount(t, admin, "project.project_identity", 1)
				var scheduleRevision string
				if err := admin.QueryRow(t.Context(), `SELECT schedule_revision_id FROM refresh.recovery_qualification_occurrence WHERE occurrence_id='occurrence-1'`).Scan(&scheduleRevision); err != nil || scheduleRevision != "revision-1" {
					t.Fatalf("preserved occurrence schedule revision = %q, error = %v; want revision-1", scheduleRevision, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			admin, migrationDB, runtimeDB := openSchema24Database(t, harness, owner, migrator, runtime)
			provider, err := newProvider(migrationDB, test.lineage(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Up(t.Context()); err != nil {
				t.Fatalf("apply revision-23 lineage: %v", err)
			}
			assertGooseRevision(t, migrationDB, 23)
			test.seed(t, admin)

			if err := ApplyGoose(t.Context(), migrationDB); err != nil {
				t.Fatalf("apply schema-24 convergence: %v", err)
			}
			assertGooseRevision(t, migrationDB, 24)
			if err := VerifyGoose(t.Context(), migrationDB); err != nil {
				t.Fatalf("current runtime rejected converged schema: %v", err)
			}
			assertRecoveryQualificationContract(t, admin)
			test.assertData(t, admin)
			var cursorCount int
			if err := runtimeDB.QueryRowContext(t.Context(), `SELECT count(*) FROM refresh.recovery_qualification_enqueue_cursor`).Scan(&cursorCount); err != nil || cursorCount != 1 {
				t.Fatalf("runtime recovery-ledger access count = %d, error = %v; want 1", cursorCount, err)
			}
		})
	}
}

func TestSchema24FreshDatabaseAndForwardOnlyDown(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "schema-24-fresh"})
	runtime := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "schema-24-fresh-runtime"})
	for _, name := range []string{"leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		harness.EnsureRole(t, postgrestest.Role{Name: name})
	}
	harness.GrantRole(t, owner, migrator)
	admin, migrationDB, _ := openSchema24Database(t, harness, owner, migrator, runtime)

	if err := ApplyGoose(t.Context(), migrationDB); err != nil {
		t.Fatalf("fresh migration to schema 24: %v", err)
	}
	assertGooseRevision(t, migrationDB, CurrentRevision)
	assertRecoveryQualificationContract(t, admin)
	if err := VerifyGoose(t.Context(), migrationDB); err != nil {
		t.Fatalf("current runtime rejected fresh schema: %v", err)
	}

	provider, err := NewProvider(migrationDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(t.Context()); err == nil || !strings.Contains(err.Error(), "schema 24 convergence is forward-only") {
		t.Fatalf("schema-24 down error = %v, want forward-only refusal", err)
	}
	assertGooseRevision(t, migrationDB, CurrentRevision)
}

func migrationSetThrough(t *testing.T, maximum int) fstest.MapFS {
	t.Helper()
	result := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		revision, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			t.Fatalf("invalid migration name %q", entry.Name())
		}
		if revision > maximum {
			continue
		}
		contents, err := fs.ReadFile(MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = &fstest.MapFile{Data: contents}
	}
	if len(result) != maximum {
		t.Fatalf("migration set through %d has %d files", maximum, len(result))
	}
	return result
}

func openSchema24Database(t *testing.T, harness *postgrestest.Harness, owner, migrator, runtime postgrestest.Role) (*pgxpool.Pool, *sql.DB, *sql.DB) {
	t.Helper()
	nameDigest := sha256.Sum256([]byte(t.Name()))
	database := harness.NewDatabase(t, fmt.Sprintf("schema24_%x", nameDigest[:8]))
	harness.GrantDatabase(t, database.Name, owner, "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, runtime, "CONNECT")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `ALTER DATABASE `+database.Name+` OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	runtimeDB, err := sql.Open("pgx", database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeDB.Close() })
	return admin, migrationDB, runtimeDB
}

func assertGooseRevision(t *testing.T, db *sql.DB, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRowContext(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Goose revision = %d, want %d", got, want)
	}
}

func assertRecoveryQualificationContract(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	for _, relation := range []string{
		"refresh.recovery_qualification_schedule",
		"refresh.recovery_qualification_enqueue_cursor",
		"refresh.recovery_qualification_occurrence",
		"refresh.recovery_qualification_attempt",
		"refresh.recovery_qualification_evidence_attempt",
	} {
		var exists bool
		if err := db.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, relation).Scan(&exists); err != nil || !exists {
			t.Fatalf("required relation %s exists = %t, error = %v", relation, exists, err)
		}
	}
	var functionExists, triggerExists bool
	if err := db.QueryRow(t.Context(), `
		SELECT to_regprocedure('refresh.retain_recovery_qualification_occurrences(timestamp with time zone,timestamp with time zone,integer)') IS NOT NULL,
		       EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='recovery_qualification_attempt_evidence_guard' AND NOT tgisinternal)`).Scan(&functionExists, &triggerExists); err != nil {
		t.Fatal(err)
	}
	if !functionExists || !triggerExists {
		t.Fatalf("recovery contract function=%t trigger=%t, want both present", functionExists, triggerExists)
	}
}

func assertRowCount(t *testing.T, db *pgxpool.Pool, relation string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM `+relation).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", relation, got, want)
	}
}
