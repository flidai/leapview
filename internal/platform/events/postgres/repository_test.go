package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppendEventValidatesCanonicalIdentityAndPayloadBeforeSQL(t *testing.T) {
	db := eventTestDB(t)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	tests := []struct {
		name  string
		input EventInput
		want  string
	}{
		{name: "invalid event id", input: EventInput{EventID: "not-a-uuid", ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`{}`)}, want: "UUIDv7"},
		{name: "invalid payload", input: EventInput{EventID: "01900000-0000-7000-8000-000000000001", ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`[]`)}, want: "cannot unmarshal array"},
		{name: "invalid schema version", input: EventInput{EventID: "01900000-0000-7000-8000-000000000001", ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 0, Payload: []byte(`{}`)}, want: "positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New().AppendEvent(context.Background(), tx, tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("AppendEvent() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAppendEventRejectsNilCallerContextOrTransaction(t *testing.T) {
	input := EventInput{ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`{}`)}
	if _, err := New().AppendEvent(context.Background(), nil, input); err == nil {
		t.Fatal("nil transaction unexpectedly accepted")
	}
	if _, err := New().AppendEvent(nil, nil, input); err == nil || !strings.Contains(err.Error(), "event context is nil") {
		t.Fatalf("nil context error = %v", err)
	}
}

func TestValidateUUIDv7(t *testing.T) {
	if err := validateUUIDv7("event id", "01900000-0000-7000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"01900000-0000-4000-8000-000000000001",
		"01900000-0000-7000-c000-000000000001",
		"01900000-0000-7000-8000-00000000000A",
	} {
		if err := validateUUIDv7("event id", id); err == nil {
			t.Fatalf("validateUUIDv7(%q) unexpectedly succeeded", id)
		}
	}
}

func TestPostgreSQL18EventLogRejectsNonUUIDv7DirectInsert(t *testing.T) {
	db := eventTestDB(t)
	insert := `INSERT INTO event.event_log
		(event_id, scope_id, aggregate_type, aggregate_id, aggregate_version,
		 event_type, schema_version, payload)
		VALUES ($1::uuid, 'scope', 'direct', $2, 1, 'direct.insert', 1, '{}'::jsonb)`
	if _, err := db.Exec(t.Context(), insert, "01900000-0000-7000-8000-000000000010", "valid"); err != nil {
		t.Fatalf("canonical UUIDv7 direct insert: %v", err)
	}
	for _, eventID := range []string{
		"01900000-0000-4000-8000-000000000011",
		"01900000-0000-7000-0000-000000000012",
	} {
		if _, err := db.Exec(t.Context(), insert, eventID, eventID); err == nil {
			t.Fatalf("non-UUIDv7 event id %q was accepted", eventID)
		}
	}
}

func TestPostgreSQL18EventLogIsImmutableAndOwnsOccurredAt(t *testing.T) {
	db := eventTestDB(t)
	const eventID = "01900000-0000-7000-8000-000000000020"
	suppliedOccurredAt := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var before, occurredAt, after time.Time
	if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&before); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.QueryRow(t.Context(), `
		INSERT INTO event.event_log
		    (event_id, scope_id, aggregate_type, aggregate_id, aggregate_version,
		     event_type, schema_version, occurred_at, payload)
		VALUES ($1::uuid, 'scope', 'trigger', 'authority', 1,
		        'trigger.test', 1, $2::timestamptz, '{}'::jsonb)
		RETURNING occurred_at`, eventID, suppliedOccurredAt).Scan(&occurredAt); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&after); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if occurredAt.Before(before) || occurredAt.After(after) {
		t.Fatalf("occurred_at = %s, want database clock between %s and %s", occurredAt, before, after)
	}
	if occurredAt.Equal(suppliedOccurredAt) {
		t.Fatalf("supplied occurred_at %s was persisted unchanged", suppliedOccurredAt)
	}

	if _, err := db.Exec(t.Context(), `
		UPDATE event.event_log
		SET payload = '{"tampered":true}'::jsonb
		WHERE event_id = $1::uuid`, eventID); err == nil || !strings.Contains(err.Error(), "durable event log is immutable") {
		t.Fatalf("owner/admin event UPDATE error = %v, want immutable-trigger rejection", err)
	}
}

func TestPostgreSQL18EventRollbackAndVersionAllocation(t *testing.T) {
	db := eventTestDB(t)
	repository := New()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AppendEvent(t.Context(), tx, EventInput{ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back event count = %d, want 0", count)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	event, err := repository.AppendEvent(t.Context(), tx, EventInput{ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if event.AggregateVersion != 1 {
		t.Fatalf("first aggregate version = %d, want 1", event.AggregateVersion)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL18ConcurrentSameEventIdentityIsIdempotent(t *testing.T) {
	db := eventTestDB(t)
	repository := New()
	input := EventInput{EventID: "01900000-0000-7000-8000-000000000001", ScopeID: "scope", AggregateType: "order", AggregateID: "same", EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			tx, err := db.Begin(t.Context())
			if err == nil {
				_, err = repository.AppendEvent(t.Context(), tx, input)
			}
			if err == nil {
				err = tx.Commit(t.Context())
			} else if tx != nil {
				_ = tx.Rollback(t.Context())
			}
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id = $1::uuid`, input.EventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("same-identity event rows = %d, want 1", count)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input.Payload = []byte(`{"ok":false}`)
	_, err = repository.AppendEvent(t.Context(), tx, input)
	_ = tx.Rollback(t.Context())
	var conflict *EventConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict error = %v, want EventConflictError", err)
	}
}

func TestPostgreSQL18EventPayloadRetryUsesJSONBSemantics(t *testing.T) {
	db := eventTestDB(t)
	repository := New()
	input := EventInput{
		EventID: "01900000-0000-7000-8000-0000000000aa", ScopeID: "scope",
		AggregateType: "payload", AggregateID: "canonical", EventType: "created", SchemaVersion: 1,
		Payload: []byte(`{"amount":1e3,"nested":{"large":9007199254740993,"value":2}}`),
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repository.AppendEvent(t.Context(), tx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(t.Context(), `SELECT payload::text FROM event.event_log WHERE event_id = $1::uuid`, input.EventID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(initial.Payload) != stored || !strings.Contains(stored, "9007199254740993") {
		t.Fatalf("stored payload = %q, initial = %q", stored, initial.Payload)
	}
	input.Payload = []byte(`{"nested":{"value":2,"large":9007199254740993},"amount":1000}`)
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repository.AppendEvent(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("numeric-equivalent retry: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if string(replay.Payload) != stored {
		t.Fatalf("replay payload = %q, stored = %q", replay.Payload, stored)
	}
}

func TestPostgreSQL18EventRetentionRoleBoundary(t *testing.T) {
	harness := postgrestest.Start(t)
	runtimeRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	maintenanceRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-secret"})
	database := harness.NewDatabase(t, "event_retention_roles")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if _, err := runtimeDB.Exec(t.Context(), `SELECT event.prune_event_log(clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime event retention unexpectedly succeeded")
	}
	maintenanceDB, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT event.prune_event_log(clock_timestamp(), 1)`); err != nil {
		t.Fatalf("maintenance event retention: %v", err)
	}
}

func eventTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), SchemaSQL()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}
