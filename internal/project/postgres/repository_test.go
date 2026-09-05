package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func identityTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "project_identity_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsureIdentityExactReplayAndConflict(t *testing.T) {
	db := identityTestDB(t)
	r := New(db)
	id := projectgraph.ResourceID("project:sales")
	first, err := r.Ensure(t.Context(), EnsureInput{ID: id, Title: "Sales", Description: "Revenue"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Ensure(t.Context(), EnsureInput{ID: id, Title: "Sales", Description: "Revenue"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !first.CreatedAt.Equal(second.CreatedAt) || !first.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("replay changed identity: first=%#v second=%#v", first, second)
	}
	if _, err := r.Ensure(t.Context(), EnsureInput{ID: id, Title: "Other", Description: "Revenue"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("title conflict = %v, want ErrConflict", err)
	}
	if _, err := r.Ensure(t.Context(), EnsureInput{ID: id, Title: "Sales", Description: "Other"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("description conflict = %v, want ErrConflict", err)
	}
	if err := r.EnsureIdentity(t.Context(), id); err != nil {
		t.Fatalf("metadata-blind ensure changed authored row: %v", err)
	}
	got, err := r.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Sales" || got.Description != "Revenue" {
		t.Fatalf("authored metadata overwritten: %#v", got)
	}
}

func TestEnsureIdentityConcurrentReplayAndConflict(t *testing.T) {
	db := identityTestDB(t)
	r := New(db)
	id := projectgraph.ResourceID("project:concurrent")
	const workers = 12
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Ensure(context.Background(), EnsureInput{ID: id, Title: "Concurrent", Description: "same"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact replay: %v", err)
		}
	}
	if _, err := r.Ensure(context.Background(), EnsureInput{ID: id, Title: "different", Description: "same"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent hard conflict = %v, want ErrConflict", err)
	}
}

func TestEnsureIdentityValidationAndCallerTransaction(t *testing.T) {
	db := identityTestDB(t)
	r := New(db)
	for _, in := range []EnsureInput{
		{ID: "", Title: "x"},
		{ID: "bad id", Title: "x"},
		{ID: "project:whitespace-title", Title: " Sales "},
		{ID: "project:too-title", Title: strings.Repeat("x", maxTitleBytes+1)},
		{ID: "project:too-description", Description: strings.Repeat("x", maxDescriptionBytes+1)},
	} {
		if _, err := r.Ensure(t.Context(), in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Ensure(%#v) = %v, want ErrInvalid", in, err)
		}
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.EnsureTx(t.Context(), tx, EnsureInput{ID: "project:rollback", Title: "Rollback"})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("timestamps were not DB-owned")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByID(t.Context(), "project:rollback"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("caller rollback left identity: %v", err)
	}
}

func TestIdentitySchemaLeastPrivilege(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	db := h.NewDatabase(t, "project_identity_privilege_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var runtimeUsage, readonlyUsage, publicUsage bool
	if err := admin.QueryRow(t.Context(), `
		SELECT has_schema_privilege('leapview_control_runtime','project','USAGE'),
		       has_schema_privilege('leapview_control_readonly','project','USAGE'),
		       has_schema_privilege('public','project','USAGE')`).
		Scan(&runtimeUsage, &readonlyUsage, &publicUsage); err != nil {
		t.Fatal(err)
	}
	if !runtimeUsage || !readonlyUsage || publicUsage {
		t.Fatalf("schema privileges runtime=%v readonly=%v public=%v", runtimeUsage, readonlyUsage, publicUsage)
	}
	var runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert bool
	if err := admin.QueryRow(t.Context(), `
		SELECT has_table_privilege('leapview_control_runtime','project.project_identity','INSERT'),
		       has_table_privilege('leapview_control_runtime','project.project_identity','UPDATE'),
		       has_table_privilege('leapview_control_runtime','project.project_identity','DELETE'),
		       has_table_privilege('leapview_control_readonly','project.project_identity','SELECT'),
		       has_table_privilege('leapview_control_readonly','project.project_identity','INSERT')`).
		Scan(&runtimeInsert, &runtimeUpdate, &runtimeDelete, &readonlySelect, &readonlyInsert); err != nil {
		t.Fatal(err)
	}
	if !runtimeInsert || runtimeUpdate || runtimeDelete || !readonlySelect || readonlyInsert {
		t.Fatalf("table privileges runtime=(insert=%v update=%v delete=%v) readonly=(select=%v insert=%v)", runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert)
	}
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if _, err := New(runtimeDB).Ensure(t.Context(), EnsureInput{ID: "project:runtime", Title: "Runtime"}); err != nil {
		t.Fatalf("runtime ensure = %v", err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE project.project_identity SET title='mutated' WHERE project_id='project:runtime'`); err == nil {
		t.Fatal("runtime UPDATE unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM project.project_identity WHERE project_id='project:runtime'`); err == nil {
		t.Fatal("runtime DELETE unexpectedly succeeded")
	}
	readonlyDB, err := pgxpool.New(t.Context(), db.URL(readonlyRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyDB.Close)
	if _, err := New(readonlyDB).ByID(t.Context(), "project:runtime"); err != nil {
		t.Fatalf("readonly SELECT = %v", err)
	}
	if _, err := readonlyDB.Exec(t.Context(), `INSERT INTO project.project_identity(project_id,title) VALUES ('project:readonly','Readonly')`); err == nil {
		t.Fatal("readonly INSERT unexpectedly succeeded")
	}
}

func TestIdentitySchemaInvariants(t *testing.T) {
	db := identityTestDB(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `INSERT INTO project.project_identity(project_id,title) VALUES ('project:immutable','Immutable')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE project.project_identity SET title='mutated' WHERE project_id='project:immutable'`); err == nil {
		t.Fatal("identity UPDATE bypassed immutable trigger")
	}
	if _, err := db.Exec(ctx, `DELETE FROM project.project_identity WHERE project_id='project:immutable'`); err == nil {
		t.Fatal("identity DELETE bypassed immutable trigger")
	}
	if _, err := db.Exec(ctx, `INSERT INTO project.project_identity(project_id,title) VALUES ('project:bounded', $1)`, strings.Repeat("x", maxTitleBytes+1)); err == nil {
		t.Fatal("oversized title accepted")
	}
	var created, updated time.Time
	if err := db.QueryRow(ctx, `SELECT created_at,updated_at FROM project.project_identity WHERE project_id='project:immutable'`).Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if created.IsZero() || updated.IsZero() || updated.Before(created) {
		t.Fatalf("invalid DB timestamps: created=%v updated=%v", created, updated)
	}
}
