package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queryEventDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "queryaudit_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := ApplySchema(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func queryEventInput(id string) queryaudit.EventInput {
	return queryaudit.EventInput{EventID: id, ProjectID: projectgraph.ResourceID("project:test"), PrincipalID: "principal", Surface: "api", Operation: "query", QueryKind: "rows", ModelID: "sales", Target: "orders", Status: "success", SQL: "select * from orders", QueryJSON: `{"target":"orders"}`}
}

func TestRepositoryExactReplayConflictAndBounds(t *testing.T) {
	db := queryEventDB(t)
	r := New(db)
	ctx := t.Context()
	input := queryEventInput("01900000-0000-7000-8000-000000000001")
	if err := r.RecordQueryEvent(ctx, input); err != nil {
		t.Fatal(err)
	}
	// A repository bound to a caller transaction participates in its atomic
	// boundary; rollback must remove the event completely.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rollbackInput := queryEventInput("01900000-0000-7000-8000-000000000005")
	if err := New(tx).RecordQueryEvent(ctx, rollbackInput); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var rolledBack int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit.query_event WHERE event_id=$1`, rollbackInput.EventID).Scan(&rolledBack); err != nil {
		t.Fatal(err)
	}
	if rolledBack != 0 {
		t.Fatalf("rolled-back event count = %d, want 0", rolledBack)
	}
	var created time.Time
	if err := db.QueryRow(ctx, `SELECT created_at FROM audit.query_event WHERE event_id=$1`, input.EventID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("database occurrence time appears caller-controlled: %s", created)
	}
	if err := r.RecordQueryEvent(ctx, input); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changed := input
	changed.SQL = "select different"
	if err := r.RecordQueryEvent(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay = %v, want conflict", err)
	}
	collision := queryEventInput("01900000-0000-7000-8000-000000000006")
	collision.RetryIdentity = "retry-collision"
	if err := r.RecordQueryEvent(ctx, collision); err != nil {
		t.Fatal(err)
	}
	collisionOtherID := queryEventInput("01900000-0000-7000-8000-000000000007")
	collisionOtherID.RetryIdentity = collision.RetryIdentity
	if err := r.RecordQueryEvent(ctx, collisionOtherID); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry identity collision = %v, want conflict", err)
	}
	collisionOtherRetry := collision
	collisionOtherRetry.RetryIdentity = "retry-different"
	if err := r.RecordQueryEvent(ctx, collisionOtherRetry); !errors.Is(err, ErrConflict) {
		t.Fatalf("event identity collision = %v, want conflict", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit.query_event`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("event count = %d, want 2", count)
	}
	got, err := r.GetQueryEvent(ctx, input.EventID)
	if err != nil || got.CreatedAt == "" || got.QueryJSON != `{"target":"orders"}` {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if err := r.RecordQueryEvent(ctx, queryEventInput("")); err == nil {
		t.Fatal("missing durable identity accepted")
	}
	secret := queryEventInput("01900000-0000-7000-8000-000000000002")
	secret.SQL = `select * from t where password='hunter2'`
	secret.Error = `Authorization: Bearer bearer-secret`
	secret.QueryJSON = `{"token":"secret-value","ok":true}`
	if err := r.RecordQueryEvent(ctx, secret); err != nil {
		t.Fatal(err)
	}
	row, err := r.GetQueryEvent(ctx, secret.EventID)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"hunter2", "bearer-secret", "secret-value"} {
		if strings.Contains(row.SQL+row.Error+row.QueryJSON, leaked) {
			t.Fatalf("secret %q leaked: %#v", leaked, row)
		}
	}
	tooLarge := queryEventInput("01900000-0000-7000-8000-000000000003")
	tooLarge.SQL = strings.Repeat("x", MaxSQLBytes+1)
	if err := r.RecordQueryEvent(ctx, tooLarge); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized SQL = %v, want invalid", err)
	}
	badJSON := queryEventInput("01900000-0000-7000-8000-000000000004")
	badJSON.QueryJSON = `{"a":1,"a":2}`
	if err := r.RecordQueryEvent(ctx, badJSON); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate JSON = %v, want invalid", err)
	}
}

func TestRepositoryConcurrentExactReplay(t *testing.T) {
	db := queryEventDB(t)
	r := New(db)
	input := queryEventInput("01900000-0000-7000-8000-000000000010")
	ctx := t.Context()
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.RecordQueryEvent(ctx, input) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit.query_event WHERE event_id = $1`, input.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent count = %d, want 1", count)
	}
}

func TestRepositoryPaginationFilteringAndAppendOnly(t *testing.T) {
	db := queryEventDB(t)
	r := New(db)
	ctx := t.Context()
	// One multi-row statement gives the database-owned trigger one
	// statement_timestamp for all rows, exercising the (created_at,event_id)
	// tie-breaker rather than relying on client timestamps.
	if _, err := db.Exec(ctx, `INSERT INTO audit.query_event (event_id,retry_identity,project_id,principal_id,target) VALUES
		('01900000-0000-7000-8000-000000000011','equal-11','project:test','principal','equal'),
		('01900000-0000-7000-8000-000000000012','equal-12','project:test','principal','equal'),
		('01900000-0000-7000-8000-000000000013','equal-13','project:test','principal','equal')`); err != nil {
		t.Fatal(err)
	}
	equal, err := r.ListQueryEvents(ctx, queryaudit.Filter{Target: "equal", Limit: 2})
	if err != nil || len(equal) != 2 {
		t.Fatalf("equal timestamp page = %d, %v", len(equal), err)
	}
	equalToken := base64.RawURLEncoding.EncodeToString([]byte(equal[1].CreatedAt + "\x00" + equal[1].ID))
	equalNext, err := r.ListQueryEvents(ctx, queryaudit.Filter{Target: "equal", PageToken: equalToken, Limit: 2})
	if err != nil || len(equalNext) != 1 || equalNext[0].ID == equal[0].ID || equalNext[0].ID == equal[1].ID {
		t.Fatalf("equal timestamp next page = %#v, %v", equalNext, err)
	}
	if _, err := r.ListQueryEvents(ctx, queryaudit.Filter{PageToken: "bad"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed token = %v, want invalid", err)
	}
	if _, err := r.ListQueryEvents(ctx, queryaudit.Filter{PageToken: equalToken, CursorID: equal[1].ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed token cursor = %v, want invalid", err)
	}
	if _, err := r.ListQueryEvents(ctx, queryaudit.Filter{CursorID: equal[1].ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor without time = %v, want invalid", err)
	}
	for i := 0; i < 5; i++ {
		input := queryEventInput(fmt.Sprintf("01900000-0000-7000-8000-%012d", i+20))
		input.Surface = map[bool]string{true: "api", false: "agent"}[i%2 == 0]
		input.Status = map[bool]string{true: "success", false: "error"}[i%2 == 0]
		input.Target = fmt.Sprintf("target-%d", i)
		if err := r.RecordQueryEvent(ctx, input); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	first, err := r.ListQueryEvents(ctx, queryaudit.Filter{Limit: 2})
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = %d, %v", len(first), err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(first[1].CreatedAt + "\x00" + first[1].ID))
	next, err := r.ListQueryEvents(ctx, queryaudit.Filter{PageToken: token, Limit: 10})
	if err != nil || len(next) != 6 {
		t.Fatalf("next page = %d, %v", len(next), err)
	}
	filtered, err := r.ListQueryEvents(ctx, queryaudit.Filter{Surfaces: []string{"api"}, Status: "success", Search: "target-", Limit: 10})
	if err != nil || len(filtered) != 3 {
		t.Fatalf("filtered = %d, %v", len(filtered), err)
	}
	if _, err := db.Exec(ctx, `UPDATE audit.query_event SET status='error' WHERE event_id=$1`, first[0].ID); err == nil {
		t.Fatal("query event UPDATE accepted")
	}
	if _, err := db.Exec(ctx, `DELETE FROM audit.query_event WHERE event_id=$1`, first[0].ID); err == nil {
		t.Fatal("query event DELETE accepted")
	}
}

func TestRepositoryLeastPrivilegeRoles(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	db := h.NewDatabase(t, "queryaudit_roles")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if err := ApplySchema(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	var publicSchema, publicTable bool
	if err := admin.QueryRow(t.Context(), `SELECT has_schema_privilege('public','audit','USAGE'), has_table_privilege('public','audit.query_event','SELECT')`).Scan(&publicSchema, &publicTable); err != nil {
		t.Fatal(err)
	}
	if publicSchema || publicTable {
		t.Fatalf("PUBLIC query-audit privileges schema=%v table=%v", publicSchema, publicTable)
	}
	runtimePool, err := pgxpool.New(t.Context(), db.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimePool.Close)
	readonlyPool, err := pgxpool.New(t.Context(), db.URL(readonly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyPool.Close)
	if err := New(runtimePool).RecordQueryEvent(t.Context(), queryEventInput("01900000-0000-7000-8000-000000000099")); err != nil {
		t.Fatalf("runtime insert: %v", err)
	}
	var count int
	if err := readonlyPool.QueryRow(t.Context(), `SELECT count(*) FROM audit.query_event`).Scan(&count); err != nil {
		t.Fatalf("readonly select: %v", err)
	}
	if _, err := runtimePool.Exec(t.Context(), `UPDATE audit.query_event SET status='error'`); err == nil {
		t.Fatal("runtime UPDATE accepted")
	}
	if _, err := readonlyPool.Exec(t.Context(), `INSERT INTO audit.query_event (event_id,retry_identity,project_id,principal_id) VALUES ($1,'x','project:test','p')`, uuid.New()); err == nil {
		t.Fatal("readonly INSERT accepted")
	}
}

func TestRepositoryDeterministicRetryIdentity(t *testing.T) {
	db := queryEventDB(t)
	r := New(db)
	input := queryEventInput("")
	input.RetryIdentity = "request:deterministic-1"
	if err := r.RecordQueryEvent(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordQueryEvent(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetQueryEvent(context.Background(), "not-a-uuid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid get = %v", err)
	}
}
