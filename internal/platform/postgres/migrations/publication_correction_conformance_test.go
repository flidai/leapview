package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apptesting "github.com/flidai/leapview/internal/app/testing"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real historical prefixes: no edits to historical SQL or ledger checksums.
func TestContractPublicationCorrectionPostgreSQL18(t *testing.T) {
	h := postgrestest.Start(t, apptesting.PostgresConformanceRequired())
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.GrantRole(t, owner, migrator)

	cases := []struct {
		prefix     int
		corruption string
	}{
		{0, ""}, {7, ""}, {8, ""}, {12, ""}, {13, ""},
		{13, "digest"}, {13, "profile"}, {13, "kind"}, {13, "version"}, {13, "baseline"},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("revision-%d/%s", tc.prefix, tc.corruption), func(t *testing.T) {
			database := h.NewDatabase(t, fmt.Sprintf("correction_%d", i))
			h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
			db, err := pgxpool.New(t.Context(), database.AdminURL())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := t.Context()
			if tc.prefix > 0 {
				tx, err := db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				for _, m := range ordered()[:tc.prefix] {
					if err := applyOne(ctx, tx, m.revision, m.id, m.sql, m.checksum); err != nil {
						t.Fatal(err)
					}
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				canonical := []byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:correction","contract":{"version":"1.2.3"}},"profile":"leapview.contract/v1"}`)
				version, baseline := "1.2.3", "1.2.3"
				switch tc.corruption {
				case "profile":
					canonical = []byte(strings.ReplaceAll(string(canonical), "leapview.contract/v1", "wrong.profile/v1"))
				case "kind":
					canonical = []byte(strings.ReplaceAll(string(canonical), "Source", "Model"))
				case "version":
					version, baseline = "1.2.4", "1.2.4"
				case "baseline":
					baseline = "1.2.4"
				}
				if _, err := db.Exec(ctx, `INSERT INTO project.resource_identity(instance_id,authored_id,resource_kind,lifecycle_state,active_bundle_id)
     VALUES('correction','source:correction','source','active','bundle')`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(ctx, `INSERT INTO project.contract_publication(instance_id,authored_id,resource_kind,version,version_baseline,
      projection_profile,canonical_bytes,canonical_digest,validation_evidence_json)
     VALUES('correction','source:correction','source',$1,$2,'leapview.contract/v1',$3,
      CASE WHEN $4::boolean THEN 'sha256:' || repeat('0',64) ELSE 'sha256:' || encode(sha256($3::bytea),'hex') END,
      '{"version":"1","checks":[{"outcome":"passed"}]}')`, version, baseline, canonical, tc.corruption == "digest"); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func(query string) string {
				t.Helper()
				var value string
				if err := db.QueryRow(ctx, query).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			const rowsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(p))::text,'[]') FROM project.contract_publication p`
			const constraintsSQL = `SELECT jsonb_agg(jsonb_build_array(conname,pg_get_constraintdef(oid),convalidated) ORDER BY conname)::text
    FROM pg_constraint WHERE conrelid='project.contract_publication'::regclass AND conname <> 'contract_publication_evidence_binding_check'`
			const historySQL = `SELECT jsonb_agg(to_jsonb(r) ORDER BY revision)::text FROM platform.schema_revision r`
			var rowsBefore, constraintsBefore, historyBefore string
			if tc.prefix > 0 {
				rowsBefore, constraintsBefore, historyBefore = snapshot(rowsSQL), snapshot(constraintsSQL), snapshot(historySQL)
			}
			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE leapview_control_migrator`); err != nil {
				t.Fatal(err)
			}
			err = Apply(ctx, tx)
			if tc.corruption != "" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, "corrupt existing row") {
					t.Fatalf("corruption error = %v, want preflight 23514", err)
				}
				if err := tx.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				if snapshot(historySQL) != historyBefore || snapshot(rowsSQL) != rowsBefore || snapshot(constraintsSQL) != constraintsBefore {
					t.Fatal("failed correction changed historical state")
				}
				var installed bool
				if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='project.contract_publication'::regclass AND conname='contract_publication_evidence_binding_check')`).Scan(&installed); err != nil {
					t.Fatal(err)
				}
				if installed {
					t.Fatal("failed preflight partially installed correction")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := Verify(ctx, db); err != nil {
				t.Fatal(err)
			}
			if tc.prefix > 0 && snapshot(rowsSQL) != rowsBefore {
				t.Fatal("correction changed immutable publication evidence")
			}
			// Revision 008 adds two checks to revision 007; all later definitions must
			// remain byte-for-byte catalog-identical across the additive correction.
			if tc.prefix >= 8 && snapshot(constraintsSQL) != constraintsBefore {
				t.Fatal("correction changed historical constraints")
			}
			var validated bool
			if err := db.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid='project.contract_publication'::regclass AND conname='contract_publication_evidence_binding_check'`).Scan(&validated); err != nil {
				t.Fatal(err)
			}
			if !validated {
				t.Fatal("correction not validated")
			}
		})
	}
}
