package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCanonicalEventIDValidation(t *testing.T) {
	const canonical = "01900000-0000-7000-8000-000000000001"
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "canonical lowercase UUIDv7", value: canonical, valid: true},
		{name: "uppercase", value: "01900000-0000-7000-8000-00000000000A", valid: false},
		{name: "UUIDv4", value: "01900000-0000-4000-8000-000000000001", valid: false},
		{name: "non RFC variant", value: "01900000-0000-7000-0000-000000000001", valid: false},
		{name: "surrounding whitespace", value: " " + canonical, valid: false},
		{name: "malformed", value: "not-a-uuid", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateUUIDv7("event id", test.value)
			if test.valid && err != nil {
				t.Fatalf("validateUUIDv7(%q) = %v, want nil", test.value, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("validateUUIDv7(%q) unexpectedly succeeded", test.value)
			}
		})
	}
	t.Run("generated UUIDv7", func(t *testing.T) {
		for range 32 {
			value, err := uuidv7()
			if err != nil {
				t.Fatalf("uuidv7() error: %v", err)
			}
			if err := validateUUIDv7("event id", value); err != nil {
				t.Fatalf("generated event id %q is not canonical UUIDv7: %v", value, err)
			}
		}
	})
}

func TestPostgreSQL18EventLogRejectsNonUUIDv7DirectInsert(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	insert := `INSERT INTO event.event_log
		(event_id, scope_id, aggregate_type, aggregate_id, aggregate_version,
		 event_type, schema_version, payload)
		VALUES ($1::uuid, $2, 'direct', $3, 1, 'direct.insert', 1, '{}'::jsonb)`
	if _, err := db.Exec(ctx, insert, "01900000-0000-7000-8000-000000000010", "scope", "valid"); err != nil {
		t.Fatalf("canonical UUIDv7 direct insert: %v", err)
	}
	for _, test := range []struct {
		name    string
		eventID string
	}{
		{name: "UUIDv4", eventID: "01900000-0000-4000-8000-000000000011"},
		{name: "non RFC variant", eventID: "01900000-0000-7000-0000-000000000012"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec(ctx, insert, test.eventID, "scope", test.name); err == nil {
				t.Fatalf("non-UUIDv7 event id %q was accepted", test.eventID)
			}
		})
	}
}

func TestPostgreSQL18EventRetentionRoleBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-secret"})
	database := h.NewDatabase(t, "event_retention_roles")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if _, err := runtimeDB.Exec(t.Context(), `SELECT event.prune_event_log(clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime event retention unexpectedly succeeded")
	}

	maintenanceDB, err := pgxpool.New(t.Context(), database.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT event.prune_event_log(clock_timestamp(), 1)`); err != nil {
		t.Fatalf("maintenance event retention: %v", err)
	}
}

func TestPostgreSQL18EventRollbackAndVersionAllocation(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE source_mutation (id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO source_mutation VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AppendEvent(ctx, tx, EventInput{ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back event count = %d, want 0", count)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	event, err := r.AppendEvent(ctx, tx, EventInput{ScopeID: "scope", AggregateType: "order", AggregateID: "1", EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if event.AggregateVersion != 1 {
		t.Fatalf("first aggregate version = %d, want 1", event.AggregateVersion)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var databaseTime time.Time
	if err := db.QueryRow(ctx, `
		INSERT INTO event.event_log
		 (event_id,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,occurred_at,payload)
		VALUES ('01900000-0000-7000-8000-000000000099','guard','direct','1',1,'forged-time',1,
		        '1970-01-01 00:00:00+00'::timestamptz,'{}'::jsonb)
		RETURNING occurred_at`).Scan(&databaseTime); err != nil {
		t.Fatal(err)
	}
	if databaseTime.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("direct insert retained caller occurrence time: %s", databaseTime)
	}
}

func TestPostgreSQL18ConcurrentSameEventIdentityIsIdempotent(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	input := EventInput{EventID: "01900000-0000-7000-8000-000000000001", ScopeID: "scope", AggregateType: "order", AggregateID: "same", EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tx, err := db.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			if _, err := r.AppendEvent(ctx, tx, input); err != nil {
				_ = tx.Rollback(ctx)
				results <- err
				return
			}
			results <- tx.Commit(ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, versions int
	if err := db.QueryRow(ctx, `SELECT count(*), count(DISTINCT aggregate_version) FROM event.event_log WHERE event_id = '01900000-0000-7000-8000-000000000001'`).Scan(&count, &versions); err != nil {
		t.Fatal(err)
	}
	if count != 1 || versions != 1 {
		t.Fatalf("same-identity event rows=%d versions=%d, want 1/1", count, versions)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input.Payload = []byte(`{"ok":false}`)
	if _, err := r.AppendEvent(ctx, tx, input); err == nil {
		t.Fatal("conflicting same-identity event was accepted")
	} else {
		var conflict *EventConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("conflict error = %v, want EventConflictError", err)
		}
	}
	_ = tx.Rollback(ctx)
}

func TestPostgreSQL18EventPayloadUsesStoredJSONBCanonicalBytes(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	eventID := "01900000-0000-7000-8000-0000000000aa"
	initialInput := EventInput{
		EventID: eventID, ScopeID: "scope", AggregateType: "payload", AggregateID: "canonical",
		EventType: "created", SchemaVersion: 1,
		Payload: []byte(`{"amount":1e3,"nested":{"large":9007199254740993,"value":2}}`),
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := r.AppendEvent(ctx, tx, initialInput)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var storedPayload string
	if err := db.QueryRow(ctx, `SELECT payload::text FROM event.event_log WHERE event_id = $1::uuid`, eventID).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	if string(initial.Payload) != storedPayload {
		t.Fatalf("initial payload = %q, stored payload = %q", initial.Payload, storedPayload)
	}
	if !strings.Contains(storedPayload, "9007199254740993") {
		t.Fatalf("stored payload lost large integer: %q", storedPayload)
	}

	// PostgreSQL jsonb equality treats these numeric spellings as identical,
	// while the stored text remains the exact canonical representation returned
	// by the database on the initial append.
	retryInput := initialInput
	retryInput.Payload = []byte(`{"nested":{"value":2,"large":9007199254740993},"amount":1000}`)
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.AppendEvent(ctx, tx, retryInput)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("numeric-equivalent retry: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if string(replay.Payload) != storedPayload {
		t.Fatalf("replay payload = %q, stored payload = %q", replay.Payload, storedPayload)
	}

	read, err := r.GetEvent(ctx, db, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Payload) != storedPayload {
		t.Fatalf("read payload = %q, stored payload = %q", read.Payload, storedPayload)
	}
}

func TestPostgreSQL18ConcurrentEnrollmentProducerHistoricalEvent(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{ConsumerKey: "sink", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"order"}})
	if err != nil {
		t.Fatal(err)
	}
	producerTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, appendErr := r.AppendEvent(ctx, producerTx, EventInput{ScopeID: "scope", AggregateType: "order", AggregateID: "historical", EventType: "changed", SchemaVersion: 1, Payload: []byte(`{"historical":true}`)})
		if appendErr == nil {
			appendErr = producerTx.Commit(ctx)
		} else {
			_ = producerTx.Rollback(ctx)
		}
		result <- appendErr
	}()
	if err := enrollTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	backfillTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backfill, err := r.Backfill(ctx, backfillTx, consumer.ConsumerID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := backfillTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !backfill.Done {
		t.Fatal("empty historical backfill did not enable consumer")
	}
	var deliveries int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_delivery WHERE consumer_id = $1::uuid`, consumer.ConsumerID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("delivery count = %d, want 1", deliveries)
	}
}

func TestPostgreSQL18RetirementFenceClosesProducerFanoutRace(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{ConsumerKey: "retiring-sink", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"retirement"}})
	if err != nil {
		_ = enrollTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := r.Backfill(ctx, enrollTx, consumer.ConsumerID, 100); err != nil {
		_ = enrollTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enrollTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Hold the registry fence before the producer starts.  AppendEvent writes
	// its event row, then waits on this key-share boundary; retirement commits
	// first and the producer must therefore observe lifecycle=retired and skip
	// fan-out for the post-retirement event.
	retireTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var registry bool
	if err := retireTx.QueryRow(ctx, `
		SELECT registry_id FROM event.event_fanout_registry
		WHERE registry_id = true FOR UPDATE`).Scan(&registry); err != nil {
		_ = retireTx.Rollback(ctx)
		t.Fatal(err)
	}
	if !registry {
		_ = retireTx.Rollback(ctx)
		t.Fatal("event fan-out registry row is invalid")
	}
	producerTx, err := db.Begin(ctx)
	if err != nil {
		_ = retireTx.Rollback(ctx)
		t.Fatal(err)
	}
	producerDone := make(chan error, 1)
	go func() {
		_, appendErr := r.AppendEvent(ctx, producerTx, EventInput{
			ScopeID: "scope", AggregateType: "retirement", AggregateID: "one",
			EventType: "retirement.changed", SchemaVersion: 1, Payload: []byte(`{"after":true}`),
		})
		if appendErr == nil {
			appendErr = producerTx.Commit(ctx)
		} else {
			_ = producerTx.Rollback(ctx)
		}
		producerDone <- appendErr
	}()
	select {
	case appendErr := <-producerDone:
		_ = retireTx.Rollback(ctx)
		t.Fatalf("producer crossed retirement fence before retirement: %v", appendErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := r.RetireConsumer(ctx, retireTx, RetireOptions{ConsumerID: consumer.ConsumerID}); err != nil {
		_ = retireTx.Rollback(ctx)
		_ = producerTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := retireTx.Commit(ctx); err != nil {
		_ = producerTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := <-producerDone; err != nil {
		t.Fatal(err)
	}
	var deliveries int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM event.event_delivery WHERE consumer_id = $1::uuid`, consumer.ConsumerID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("post-retirement producer created %d deliveries, want 0", deliveries)
	}
}

func TestPostgreSQL18DuplicateSafeBackfillAndPoisonRetention(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	for i := 0; i < 3; i++ {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.AppendEvent(ctx, tx, EventInput{ScopeID: "scope", AggregateType: "batch", AggregateID: "1", EventType: "item", SchemaVersion: 1, Payload: []byte(`{"item":1}`)}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := New().EnrollConsumer(ctx, tx, ConsumerInput{ConsumerKey: "sink", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"batch"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Roll back a partial batch; the next transaction restarts from the prior
	// frontier and ON CONFLICT keeps retries duplicate-safe.
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backfill(ctx, tx, consumer.ConsumerID, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		tx, err = db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		batch, err := r.Backfill(ctx, tx, consumer.ConsumerID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if batch.Done {
			break
		}
	}
	var deliveries int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_delivery WHERE consumer_id = $1::uuid`, consumer.ConsumerID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 3 {
		t.Fatalf("duplicate-safe delivery count = %d, want 3", deliveries)
	}
	claimTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := r.Claim(ctx, claimTx, ClaimOptions{ConsumerID: consumer.ConsumerID, WorkerID: "worker", Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %#v, %v", claimed, err)
	}
	if err := r.Complete(ctx, claimTx, consumer.ConsumerID, claimed[0].EventID, "worker", claimed[0].ClaimGeneration, DeliveryDeadLetter, []byte(`{"error":"poison"}`)); err != nil {
		t.Fatal(err)
	}
	if err := claimTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	pruneTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := r.Prune(ctx, pruneTx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("poison prune removed %d events, want 0", removed)
	}
	if err := pruneTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	retireTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RetireConsumer(ctx, retireTx, RetireOptions{ConsumerID: consumer.ConsumerID, Waive: true, Evidence: []byte(`{"ticket":"poison-resolved"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := retireTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	pruneTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed, err = r.Prune(ctx, pruneTx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("waived prune removed %d events, want 3", removed)
	}
	if err := pruneTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL18ClaimFencingRetryPauseAndRetire(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	appendEvent := func() string {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		e, err := r.AppendEvent(ctx, tx, EventInput{ScopeID: "scope", AggregateType: "claim", AggregateID: "1", EventType: "item", SchemaVersion: 1, Payload: []byte(`{"item":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return e.EventID
	}
	eventID := appendEvent()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, tx, ConsumerInput{ConsumerKey: "sink", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"claim"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backfill(ctx, tx, consumer.ConsumerID, 100); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	readTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := r.ListDeliveries(ctx, readTx, consumer.ConsumerID, 10)
	if err != nil {
		_ = readTx.Rollback(ctx)
		t.Fatal(err)
	}
	_ = readTx.Rollback(ctx)
	if len(pending) != 1 || pending[0].Status != "pending" || pending[0].ClaimedBy != "" {
		t.Fatalf("pending deliveries = %#v", pending)
	}
	claimTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Claim(ctx, claimTx, ClaimOptions{ConsumerID: consumer.ConsumerID, WorkerID: "reused-worker", Limit: 1, Lease: time.Minute})
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if err := claimTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The runtime role has table UPDATE privileges for delivery processing, but
	// the persistence edge must still fence stale workers that attempt to
	// rewind or reuse a claim generation directly.
	guardTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `
		UPDATE event.event_delivery
		SET claim_generation = claim_generation - 1
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID); err == nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal("delivery claim-generation rewind unexpectedly succeeded")
	}
	_ = guardTx.Rollback(ctx)
	guardTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `
		UPDATE event.event_delivery
		SET claim_generation = claim_generation + 2
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID); err == nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal("delivery claim-generation skip unexpectedly succeeded")
	}
	_ = guardTx.Rollback(ctx)
	guardTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `
		UPDATE event.event_delivery
		SET claimed_by = 'stale-owner'
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID); err == nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal("delivery owner reuse unexpectedly succeeded")
	}
	_ = guardTx.Rollback(ctx)
	guardTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `
		UPDATE event.event_delivery
		SET consumer_id = '00000000-0000-0000-0000-000000000099'::uuid
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID); err == nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal("delivery consumer identity mutation unexpectedly succeeded")
	}
	_ = guardTx.Rollback(ctx)
	guardTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `
		UPDATE event.event_delivery
		SET event_id = '00000000-0000-0000-0000-000000000098'::uuid
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID); err == nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal("delivery event identity mutation unexpectedly succeeded")
	}
	_ = guardTx.Rollback(ctx)
	if _, err := db.Exec(ctx, `UPDATE event.event_delivery SET claimed_until = clock_timestamp() - interval '1 second' WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID); err != nil {
		t.Fatal(err)
	}
	claimTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Claim(ctx, claimTx, ClaimOptions{ConsumerID: consumer.ConsumerID, WorkerID: "reused-worker", Limit: 1, Lease: time.Minute})
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
	if second[0].ClaimGeneration <= first[0].ClaimGeneration {
		t.Fatalf("claim generation did not advance: first=%d second=%d", first[0].ClaimGeneration, second[0].ClaimGeneration)
	}
	if err := r.Complete(ctx, claimTx, consumer.ConsumerID, eventID, "reused-worker", first[0].ClaimGeneration, DeliverySucceeded, []byte(`{"stale":true}`)); err == nil {
		t.Fatal("stale claim generation completed successor claim")
	}
	if err := r.Retry(ctx, claimTx, RetryOptions{ConsumerID: consumer.ConsumerID, EventID: eventID, WorkerID: "reused-worker", ClaimGeneration: second[0].ClaimGeneration, MaxAttempts: 10, Evidence: []byte(`{"retry":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := claimTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PauseConsumer(ctx, tx, consumer.ConsumerID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	appendEvent()
	claimTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Claim(ctx, claimTx, ClaimOptions{ConsumerID: consumer.ConsumerID, WorkerID: "paused-worker", Limit: 1, Lease: time.Minute}); err == nil {
		t.Fatal("paused consumer claimed a delivery")
	}
	_ = claimTx.Rollback(ctx)
	resumeTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ResumeConsumer(ctx, resumeTx, consumer.ConsumerID); err != nil {
		t.Fatal(err)
	}
	if err := resumeTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claimTx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := r.Claim(ctx, claimTx, ClaimOptions{ConsumerID: consumer.ConsumerID, WorkerID: "resumed-worker", Limit: 10, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 2 {
		t.Fatalf("resumed claims = %#v, want two deliveries", resumed)
	}
	var poison Delivery
	for _, delivery := range resumed {
		if delivery.EventID == eventID {
			poison = delivery
		}
	}
	if poison.EventID == "" {
		t.Fatalf("original delivery missing from resumed claims: %#v", resumed)
	}
	if err := r.Retry(ctx, claimTx, RetryOptions{
		ConsumerID: consumer.ConsumerID, EventID: poison.EventID, WorkerID: "resumed-worker",
		ClaimGeneration: poison.ClaimGeneration, MaxAttempts: poison.Attempts, Evidence: []byte(`{"exhausted":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := claimTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var poisonStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM event.event_delivery WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID).Scan(&poisonStatus); err != nil {
		t.Fatal(err)
	}
	if poisonStatus != "dead_letter" {
		t.Fatalf("exhausted delivery status = %q, want dead_letter", poisonStatus)
	}
	// An explicit replay races retention in a separate transaction.  The
	// maintenance function must serialize on the delivery table lock and then
	// observe the pending state, leaving the event available for replay.
	replayTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replay(ctx, replayTx, ReplayOptions{ConsumerID: consumer.ConsumerID, EventID: eventID, Evidence: []byte(`{"reason":"operator-replay"}`)}); err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatal(err)
	}
	pruneTx, err := db.Begin(ctx)
	if err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatal(err)
	}
	type pruneResult struct {
		removed int64
		err     error
	}
	pruneDone := make(chan pruneResult, 1)
	go func() {
		removed, pruneErr := r.Prune(ctx, pruneTx, time.Now().Add(time.Hour))
		pruneDone <- pruneResult{removed: removed, err: pruneErr}
	}()
	select {
	case result := <-pruneDone:
		_ = replayTx.Rollback(ctx)
		_ = pruneTx.Rollback(ctx)
		t.Fatalf("prune completed before replay commit: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if err := replayTx.Commit(ctx); err != nil {
		_ = pruneTx.Rollback(ctx)
		t.Fatal(err)
	}
	result := <-pruneDone
	if result.err != nil {
		_ = pruneTx.Rollback(ctx)
		t.Fatal(result.err)
	}
	if result.removed != 0 {
		_ = pruneTx.Rollback(ctx)
		t.Fatalf("replayed event was pruned: removed=%d", result.removed)
	}
	if err := pruneTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var replayStatus string
	if err := db.QueryRow(ctx, `
		SELECT status FROM event.event_delivery
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID).Scan(&replayStatus); err != nil {
		t.Fatal(err)
	}
	if replayStatus != "pending" {
		t.Fatalf("replayed delivery status = %q, want pending after prune race", replayStatus)
	}
	var liveClaim Delivery
	for _, delivery := range resumed {
		if delivery.EventID != eventID {
			liveClaim = delivery
		}
	}
	if _, err := db.Exec(ctx, `UPDATE event.event_delivery SET claimed_until = clock_timestamp() - interval '1 second' WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, liveClaim.EventID); err != nil {
		t.Fatal(err)
	}
	expiredTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(ctx, expiredTx, consumer.ConsumerID, liveClaim.EventID, "resumed-worker", liveClaim.ClaimGeneration, DeliverySucceeded, []byte(`{"late":true}`)); err == nil {
		t.Fatal("expired claim completed")
	}
	_ = expiredTx.Rollback(ctx)
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RetireConsumer(ctx, tx, RetireOptions{ConsumerID: consumer.ConsumerID}); err == nil {
		t.Fatal("retirement without waiver ignored unresolved deliveries")
	}
	_ = tx.Rollback(ctx)
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RetireConsumer(ctx, tx, RetireOptions{ConsumerID: consumer.ConsumerID, Waive: true, Evidence: []byte(`{"approved_by":"operator"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	if err := db.QueryRow(ctx, `SELECT lifecycle FROM event.event_consumer WHERE consumer_id = $1::uuid`, consumer.ConsumerID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "retired" {
		t.Fatalf("consumer lifecycle = %q, want retired", lifecycle)
	}
}

func TestPostgreSQL18ReplayEvidenceAndRetireFence(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	const eventID = "01900000-0000-7000-8000-000000000201"

	appendTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AppendEvent(ctx, appendTx, EventInput{
		EventID: eventID, ScopeID: "scope", AggregateType: "replay-fence", AggregateID: "1",
		EventType: "item", SchemaVersion: 1, Payload: []byte(`{"ok":true}`),
	}); err != nil {
		_ = appendTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := appendTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{
		ConsumerKey: "replay-fence", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"replay-fence"},
	})
	if err != nil {
		_ = enrollTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enrollTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	backfillTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backfill, err := r.Backfill(ctx, backfillTx, consumer.ConsumerID, 10)
	if err != nil {
		_ = backfillTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := backfillTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !backfill.Done {
		t.Fatal("backfill did not enable consumer")
	}

	// Move the delivery to a terminal state so Replay has an eligible row.
	claimTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := r.Claim(ctx, claimTx, ClaimOptions{ConsumerID: consumer.ConsumerID, WorkerID: "replay-fence-worker", Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		_ = claimTx.Rollback(ctx)
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if err := r.Complete(ctx, claimTx, consumer.ConsumerID, eventID, "replay-fence-worker", claimed[0].ClaimGeneration, DeliverySucceeded, []byte(`{"initial":true}`)); err != nil {
		_ = claimTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := claimTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	invalidReplayTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replay(ctx, invalidReplayTx, ReplayOptions{ConsumerID: consumer.ConsumerID, EventID: eventID, Evidence: []byte(`{}`)}); err == nil {
		_ = invalidReplayTx.Rollback(ctx)
		t.Fatal("replay accepted empty operator evidence")
	}
	if err := invalidReplayTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	replayTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayEvidence := []byte(`{"reason":"operator-request","ticket":"FAI-593"}`)
	if err := r.Replay(ctx, replayTx, ReplayOptions{ConsumerID: consumer.ConsumerID, EventID: eventID, Evidence: replayEvidence}); err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatal(err)
	}
	var status, evidence string
	if err := replayTx.QueryRow(ctx, `SELECT status, evidence::text FROM event.event_delivery WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, eventID).Scan(&status, &evidence); err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatal(err)
	}
	if status != "pending" {
		_ = replayTx.Rollback(ctx)
		t.Fatalf("replay status in transaction = %q, want pending", status)
	}
	var gotEvidence map[string]any
	if err := json.Unmarshal([]byte(evidence), &gotEvidence); err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatalf("replay evidence is invalid JSON: %v", err)
	}
	if gotEvidence["reason"] != "operator-request" || gotEvidence["ticket"] != "FAI-593" {
		_ = replayTx.Rollback(ctx)
		t.Fatalf("replay evidence = %#v", gotEvidence)
	}

	// Replay holds the registry lock until commit. Retirement must wait for
	// that lock, then observe the pending row and refuse to retire it.
	retireTx, err := db.Begin(ctx)
	if err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatal(err)
	}
	retireDone := make(chan error, 1)
	go func() {
		retireDone <- r.RetireConsumer(ctx, retireTx, RetireOptions{ConsumerID: consumer.ConsumerID})
	}()
	select {
	case gotErr := <-retireDone:
		_ = replayTx.Rollback(ctx)
		_ = retireTx.Rollback(ctx)
		t.Fatalf("retirement completed before replay commit: %v", gotErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := replayTx.Commit(ctx); err != nil {
		_ = retireTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := <-retireDone; err == nil {
		_ = retireTx.Rollback(ctx)
		t.Fatal("retirement succeeded despite replayed pending delivery")
	}
	if err := retireTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var lifecycle, deliveryStatus string
	if err := db.QueryRow(ctx, `SELECT c.lifecycle, d.status, d.evidence::text FROM event.event_consumer c JOIN event.event_delivery d USING (consumer_id) WHERE c.consumer_id = $1::uuid AND d.event_id = $2::uuid`, consumer.ConsumerID, eventID).Scan(&lifecycle, &deliveryStatus, &evidence); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "enabled" {
		t.Fatalf("consumer lifecycle after replay/retire race = %q, want enabled", lifecycle)
	}
	if deliveryStatus != "pending" {
		t.Fatalf("delivery status after replay/retire race = %q, want pending", deliveryStatus)
	}
}

func TestPostgreSQL18CommittedClaimSurvivesConnectionLossAndIsLeaseReclaimed(t *testing.T) {
	db := eventTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := New()

	appendTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	event, err := r.AppendEvent(ctx, appendTx, EventInput{
		ScopeID: "scope", AggregateType: "connection-loss", AggregateID: "1",
		EventType: "item", SchemaVersion: 1, Payload: []byte(`{"ok":true}`),
	})
	if err != nil {
		_ = appendTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := appendTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{ConsumerKey: "connection-loss", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"connection-loss"}})
	if err != nil {
		_ = enrollTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enrollTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	backfillTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backfill, err := r.Backfill(ctx, backfillTx, consumer.ConsumerID, 10)
	if err != nil {
		_ = backfillTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := backfillTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !backfill.Done {
		t.Fatal("backfill did not enable consumer")
	}

	// Keep each worker on an independent pool/connection. Hijacking the
	// claimant connection and closing the underlying pgx connection models a
	// process loss while leaving the committed claim visible to other workers.
	claimantPool := eventTestPool(t, db)
	successorPool := eventTestPool(t, db)
	stalePool := eventTestPool(t, db)
	claimantConn, err := claimantPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimantPID := claimantConn.Conn().PgConn().PID()
	claimTx, err := claimantConn.Begin(ctx)
	if err != nil {
		claimantConn.Release()
		t.Fatal(err)
	}
	first, err := r.Claim(ctx, claimTx, ClaimOptions{
		ConsumerID: consumer.ConsumerID, WorkerID: "worker-lost", Limit: 1, Lease: 100 * time.Millisecond,
	})
	if err != nil || len(first) != 1 {
		_ = claimTx.Rollback(ctx)
		claimantConn.Release()
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if err := claimTx.Commit(ctx); err != nil {
		claimantConn.Release()
		t.Fatal(err)
	}
	firstClaim := first[0]
	if firstClaim.EventID != event.EventID {
		claimantConn.Release()
		t.Fatalf("first claim event = %q, want %q", firstClaim.EventID, event.EventID)
	}
	// Hijack transfers ownership of the physical connection, so closing it is
	// an actual connection loss rather than a pool release/reuse.
	lostConn := claimantConn.Hijack()
	claimantCloseCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	if err := lostConn.Close(claimantCloseCtx); err != nil {
		closeCancel()
		t.Fatal(err)
	}
	closeCancel()
	var claimantBackendAlive bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity WHERE pid = $1)`, claimantPID).Scan(&claimantBackendAlive); err != nil {
		t.Fatal(err)
	}
	if claimantBackendAlive {
		t.Fatalf("claimant backend %d remains active after connection loss", claimantPID)
	}

	var status string
	var claimedBy string
	if err := db.QueryRow(ctx, `
		SELECT status, claimed_by FROM event.event_delivery
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, event.EventID).Scan(&status, &claimedBy); err != nil {
		t.Fatal(err)
	}
	if status != "claimed" || claimedBy != "worker-lost" {
		t.Fatalf("delivery after claimant loss = status %q owner %q, want claimed/worker-lost", status, claimedBy)
	}

	// Poll the database clock, rather than sleeping a fixed duration, so the
	// successor claim starts as soon as the lease is observably expired while
	// remaining bounded if the real PostgreSQL lane is unhealthy.
	expiryCtx, expiryCancel := context.WithTimeout(ctx, 2*time.Second)
	defer expiryCancel()
	for {
		var expired bool
		if err := db.QueryRow(expiryCtx, `
			SELECT claimed_until < clock_timestamp()
			FROM event.event_delivery
			WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, event.EventID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-expiryCtx.Done():
			t.Fatalf("delivery lease did not expire: %v", expiryCtx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}

	successorConn, err := successorPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	successorTx, err := successorConn.Begin(ctx)
	if err != nil {
		successorConn.Release()
		t.Fatal(err)
	}
	second, err := r.Claim(ctx, successorTx, ClaimOptions{
		ConsumerID: consumer.ConsumerID, WorkerID: "worker-successor", Limit: 1, Lease: time.Second,
	})
	if err != nil || len(second) != 1 {
		_ = successorTx.Rollback(ctx)
		successorConn.Release()
		t.Fatalf("successor claim = %#v, %v", second, err)
	}
	if second[0].ClaimGeneration <= firstClaim.ClaimGeneration {
		_ = successorTx.Rollback(ctx)
		successorConn.Release()
		t.Fatalf("claim generation did not advance: first=%d second=%d", firstClaim.ClaimGeneration, second[0].ClaimGeneration)
	}
	if err := successorTx.Commit(ctx); err != nil {
		successorConn.Release()
		t.Fatal(err)
	}
	successorConn.Release()

	staleConn, err := stalePool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staleTx, err := staleConn.Begin(ctx)
	if err != nil {
		staleConn.Release()
		t.Fatal(err)
	}
	staleErr := r.Complete(ctx, staleTx, consumer.ConsumerID, event.EventID, "worker-lost", firstClaim.ClaimGeneration, DeliverySucceeded, []byte(`{"stale":true}`))
	if !errors.Is(staleErr, ErrDeliveryClaimLost) {
		_ = staleTx.Rollback(ctx)
		staleConn.Release()
		t.Fatalf("stale completion error = %v, want ErrDeliveryClaimLost", staleErr)
	}
	_ = staleTx.Rollback(ctx)
	staleConn.Release()

	var currentGeneration int64
	if err := db.QueryRow(ctx, `
		SELECT status, claimed_by, claim_generation FROM event.event_delivery
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumer.ConsumerID, event.EventID).Scan(&status, &claimedBy, &currentGeneration); err != nil {
		t.Fatal(err)
	}
	if status != "claimed" || claimedBy != "worker-successor" {
		t.Fatalf("delivery after stale completion = status %q owner %q, want claimed/worker-successor", status, claimedBy)
	}
	if currentGeneration != second[0].ClaimGeneration {
		t.Fatalf("delivery generation after stale completion = %d, want %d", currentGeneration, second[0].ClaimGeneration)
	}
}

func TestCanonicalConsumerAggregateTypes(t *testing.T) {
	got, err := canonicalAggregateTypes([]string{"z-order", "order", "z-order"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"order", "z-order"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical aggregate types = %#v, want %#v", got, want)
	}
	for _, values := range [][]string{nil, {}, {" "}, {strings.Repeat("x", 256)}} {
		if _, err := canonicalAggregateTypes(values); err == nil {
			t.Fatalf("canonical aggregate types %#v unexpectedly succeeded", values)
		}
	}
	tooMany := make([]string, MaxConsumerAggregateTypes+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("x", i+1)
	}
	if _, err := canonicalAggregateTypes(tooMany); err == nil {
		t.Fatalf("aggregate filter with %d values unexpectedly succeeded", len(tooMany))
	}
}

func TestPostgreSQL18ConsumerByIDReturnsCanonicalEnrollment(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	const consumerID = "01900000-0000-7000-8000-000000000141"
	const frontierID = "01900000-0000-7000-8000-000000000142"

	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{
		ConsumerID:     consumerID,
		ConsumerKey:    "consumer-by-id",
		ReplayFrom:     time.Date(2025, 2, 3, 4, 5, 6, 123456000, time.FixedZone("replay-offset", -7*60*60)),
		Metadata:       []byte(`{"z":1,"a":"value"}`),
		AggregateTypes: []string{"z-order", "order", "z-order"},
	}); err != nil {
		_ = enrollTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enrollTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// ConsumerByID is a read-side lookup, not a registry-fenced mutation. It
	// must work in a caller-owned stronger-isolation transaction as well.
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE event.event_consumer
		SET lifecycle = 'paused',
			replay_from = '2025-02-03 04:05:06.123456-07'::timestamptz,
			frontier_event_id = $2::uuid,
			frontier_occurred_at = '2025-02-04 05:06:07.654321+09'::timestamptz,
			metadata = '{"z":1,"a":"value"}'::jsonb,
			created_at = '2025-01-01 00:00:00.111111-05'::timestamptz,
			updated_at = '2025-02-05 00:00:00.222222+11'::timestamptz
		WHERE consumer_id = $1::uuid`, consumerID, frontierID); err != nil {
		t.Fatal(err)
	}

	got, err := r.ConsumerByID(ctx, tx, consumerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsumerID != consumerID || got.ConsumerKey != "consumer-by-id" || got.Lifecycle != "paused" {
		t.Fatalf("identity/lifecycle = %#v, want id %q key %q lifecycle paused", got, consumerID, "consumer-by-id")
	}
	if got.FrontierEventID != frontierID {
		t.Fatalf("frontier event id = %q, want %q", got.FrontierEventID, frontierID)
	}
	wantReplay := time.Date(2025, 2, 3, 11, 5, 6, 123456000, time.UTC)
	wantFrontier := time.Date(2025, 2, 3, 20, 6, 7, 654321000, time.UTC)
	wantCreated := time.Date(2025, 1, 1, 5, 0, 0, 111111000, time.UTC)
	wantUpdated := time.Date(2025, 2, 4, 13, 0, 0, 222222000, time.UTC)
	for name, value := range map[string]struct {
		got, want time.Time
	}{
		"replay_from":          {got.ReplayFrom, wantReplay},
		"frontier_occurred_at": {got.FrontierOccurred, wantFrontier},
		"created_at":           {got.CreatedAt, wantCreated},
		"updated_at":           {got.UpdatedAt, wantUpdated},
	} {
		if !value.got.Equal(value.want) || value.got.Location() != time.UTC {
			t.Errorf("%s = %v (%s), want %v (UTC)", name, value.got, value.got.Location(), value.want)
		}
	}
	if want := []string{"order", "z-order"}; !reflect.DeepEqual(got.AggregateTypes, want) {
		t.Fatalf("aggregate types = %#v, want %#v", got.AggregateTypes, want)
	}
	var metadata map[string]any
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatalf("metadata is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(metadata, map[string]any{"a": "value", "z": float64(1)}) {
		t.Fatalf("metadata = %#v, want canonical object", metadata)
	}

	// The aggregate slice is a copy owned by the returned value, and a second
	// lookup must not observe caller mutation.
	got.AggregateTypes[0] = "caller-mutated"
	again, err := r.ConsumerByID(ctx, tx, consumerID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.AggregateTypes, []string{"order", "z-order"}) {
		t.Fatalf("aggregate types after caller mutation = %#v", again.AggregateTypes)
	}

	// ConsumerByID neither begins nor commits the caller's transaction.
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("caller transaction is no longer usable: %v", err)
	}
	if one != 1 {
		t.Fatalf("transaction probe = %d, want 1", one)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL18ConsumerByIDMissingAndMalformedID(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()

	t.Run("missing preserves pgx ErrNoRows", func(t *testing.T) {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = r.ConsumerByID(ctx, tx, "01900000-0000-7000-8000-000000000149")
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("missing consumer error = %v, want pgx.ErrNoRows classification", err)
		}
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
			t.Fatalf("caller transaction is no longer usable: %v", err)
		}
	})

	t.Run("malformed rejects before query and preserves transaction", func(t *testing.T) {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := r.ConsumerByID(ctx, tx, "not-a-uuid"); err == nil {
			t.Fatal("malformed consumer id unexpectedly succeeded")
		}
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
			t.Fatalf("caller transaction is no longer usable: %v", err)
		}
	})
}

func TestPostgreSQL18ConsumerByIDFailsClosedOnEmptyAggregateFilter(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	const consumerID = "01900000-0000-7000-8000-000000000151"
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, tx, ConsumerInput{ConsumerID: consumerID, ConsumerKey: "empty-filter", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"orders"}})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM event.event_consumer_aggregate WHERE consumer_id = $1::uuid`, consumer.ConsumerID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := r.ConsumerByID(ctx, tx, consumer.ConsumerID); err == nil || !strings.Contains(err.Error(), "aggregate filter") {
		_ = tx.Rollback(ctx)
		t.Fatalf("empty aggregate filter error = %v, want validation error", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL18RegistryFenceRejectsStrongerIsolationWithoutWrites(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	const eventID = "01900000-0000-7000-8000-000000000131"

	// AppendEvent must reject before its identity lock, aggregate allocation, or
	// event insert can leave a durable row behind.
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.AppendEvent(ctx, tx, EventInput{
		EventID: eventID, ScopeID: "scope", AggregateType: "orders", AggregateID: "1",
		EventType: "created", SchemaVersion: 1, Payload: []byte(`{"ok":true}`),
	})
	if !errors.Is(err, ErrUnsupportedTransactionIsolation) {
		_ = tx.Rollback(ctx)
		t.Fatalf("repeatable-read append error = %v, want ErrUnsupportedTransactionIsolation", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_log WHERE event_id = $1::uuid`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("repeatable-read append left %d event rows", count)
	}

	// Enrollment must reject before creating either its consumer row or its
	// aggregate filter rows.
	tx, err = db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, tx, ConsumerInput{
		ConsumerKey: "strong-isolation", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"orders"},
	})
	if !errors.Is(err, ErrUnsupportedTransactionIsolation) {
		_ = tx.Rollback(ctx)
		t.Fatalf("serializable enrollment error = %v, want ErrUnsupportedTransactionIsolation", err)
	}
	if consumer.ConsumerID != "" {
		t.Fatalf("rejected enrollment returned consumer %q", consumer.ConsumerID)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_consumer WHERE consumer_key = 'strong-isolation'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("serializable enrollment left %d consumer rows", count)
	}

	// Establish a valid backfilling consumer, then verify Backfill's stronger
	// isolation guard leaves lifecycle, retention, and delivery state untouched.
	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backfillConsumer, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{
		ConsumerKey: "backfill-strong-isolation", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"orders"},
	})
	if err != nil {
		_ = enrollTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enrollTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	backfillTx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Backfill(ctx, backfillTx, backfillConsumer.ConsumerID, 100)
	if !errors.Is(err, ErrUnsupportedTransactionIsolation) {
		_ = backfillTx.Rollback(ctx)
		t.Fatalf("repeatable-read backfill error = %v, want ErrUnsupportedTransactionIsolation", err)
	}
	if err := backfillTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	if err := db.QueryRow(ctx, `SELECT lifecycle FROM event.event_consumer WHERE consumer_id = $1::uuid`, backfillConsumer.ConsumerID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "backfilling" {
		t.Fatalf("rejected backfill lifecycle = %q, want backfilling", lifecycle)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_consumer_aggregate WHERE consumer_id = $1::uuid`, backfillConsumer.ConsumerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backfill consumer filter count = %d, want 1", count)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_delivery WHERE consumer_id = $1::uuid`, backfillConsumer.ConsumerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected backfill left %d deliveries", count)
	}

	retireTx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	err = r.RetireConsumer(ctx, retireTx, RetireOptions{ConsumerID: backfillConsumer.ConsumerID})
	if !errors.Is(err, ErrUnsupportedTransactionIsolation) {
		_ = retireTx.Rollback(ctx)
		t.Fatalf("serializable retirement error = %v, want ErrUnsupportedTransactionIsolation", err)
	}
	if err := retireTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT lifecycle FROM event.event_consumer WHERE consumer_id = $1::uuid`, backfillConsumer.ConsumerID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "backfilling" {
		t.Fatalf("rejected retirement lifecycle = %q, want backfilling", lifecycle)
	}
}

func TestPostgreSQL18FilteredFanoutAndBackfill(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	appendEvent := func(eventID, aggregateType string) {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.AppendEvent(ctx, tx, EventInput{
			EventID: eventID, ScopeID: "scope", AggregateType: aggregateType, AggregateID: eventID,
			EventType: "changed", SchemaVersion: 1, Payload: []byte(`{"ok":true}`),
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("01900000-0000-7000-8000-000000000101", "orders")
	appendEvent("01900000-0000-7000-8000-000000000102", "users")

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, tx, ConsumerInput{
		ConsumerKey: "filtered", ReplayFrom: time.Unix(0, 0),
		AggregateTypes: []string{"orders", "orders"},
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if want := []string{"orders"}; !reflect.DeepEqual(consumer.AggregateTypes, want) {
		_ = tx.Rollback(ctx)
		t.Fatalf("consumer aggregate types = %#v, want %#v", consumer.AggregateTypes, want)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backfill, err := r.Backfill(ctx, tx, consumer.ConsumerID, 100)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if !backfill.Done || backfill.Scanned != 1 || backfill.Inserted != 1 {
		t.Fatalf("filtered backfill = %#v, want one admitted event", backfill)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	appendEvent("01900000-0000-7000-8000-000000000103", "users")
	appendEvent("01900000-0000-7000-8000-000000000104", "orders")
	var deliveries int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM event.event_delivery WHERE consumer_id = $1::uuid`, consumer.ConsumerID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 2 {
		t.Fatalf("filtered delivery count = %d, want 2", deliveries)
	}
	var users int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM event.event_delivery d
		JOIN event.event_log e ON e.event_id = d.event_id
		WHERE d.consumer_id = $1::uuid AND e.aggregate_type = 'users'`, consumer.ConsumerID).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Fatalf("filtered users delivery count = %d, want 0", users)
	}
}

func TestPostgreSQL18FilteredConsumerDoesNotPinUnadmittedRetention(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	eventID := "01900000-0000-7000-8000-000000000111"
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AppendEvent(ctx, tx, EventInput{
		EventID: eventID, ScopeID: "scope", AggregateType: "ignored", AggregateID: "1",
		EventType: "changed", SchemaVersion: 1, Payload: []byte(`{"ignored":true}`),
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, tx, ConsumerInput{
		ConsumerKey: "retention-filter", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"admitted"},
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backfill, err := r.Backfill(ctx, tx, consumer.ConsumerID, 100)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if !backfill.Done || backfill.Scanned != 0 || backfill.Inserted != 0 {
		t.Fatalf("filtered ignored backfill = %#v, want empty completion", backfill)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := r.Prune(ctx, tx, time.Now().Add(time.Hour))
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("filtered retention prune removed %d events, want 1", removed)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQL18ConsumerEnrollmentConflictLeavesAllowlistUnchanged(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	consumerID := "01900000-0000-7000-8000-000000000121"
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.EnrollConsumer(ctx, tx, ConsumerInput{
		ConsumerID: consumerID, ConsumerKey: "enrollment-conflict", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"orders"},
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.EnrollConsumer(ctx, tx, ConsumerInput{
		ConsumerID: consumerID, ConsumerKey: "enrollment-conflict-retry", ReplayFrom: time.Unix(0, 0), AggregateTypes: []string{"users"},
	})
	if err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("duplicate consumer enrollment unexpectedly succeeded")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var aggregateTypes []string
	rows, err := db.Query(ctx, `SELECT aggregate_type FROM event.event_consumer_aggregate WHERE consumer_id = $1::uuid ORDER BY aggregate_type`, consumerID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var aggregateType string
		if err := rows.Scan(&aggregateType); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		aggregateTypes = append(aggregateTypes, aggregateType)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if want := []string{"orders"}; !reflect.DeepEqual(aggregateTypes, want) {
		t.Fatalf("allowlist after enrollment conflict = %#v, want %#v", aggregateTypes, want)
	}
}

func eventTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
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

func eventTestPool(t *testing.T, base *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	config := base.Config().Copy()
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
