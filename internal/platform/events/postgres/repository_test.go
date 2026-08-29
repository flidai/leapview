package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const postgres18EventImage = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

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

func TestPostgreSQL18ConcurrentEnrollmentProducerHistoricalEvent(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	enrollTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := r.EnrollConsumer(ctx, enrollTx, ConsumerInput{ConsumerKey: "sink", ReplayFrom: time.Unix(0, 0)})
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
	consumer, err := New().EnrollConsumer(ctx, tx, ConsumerInput{ConsumerKey: "sink", ReplayFrom: time.Unix(0, 0)})
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
	consumer, err := r.EnrollConsumer(ctx, tx, ConsumerInput{ConsumerKey: "sink", ReplayFrom: time.Unix(0, 0)})
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

func eventTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if !eventConformanceRequired() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcpostgres.Run(ctx, postgres18EventImage,
		tcpostgres.WithDatabase("leapview_control"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("leapview-event-secret"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		testcontainers.WithLogger(log.TestLogger(t)))
	if err != nil {
		if eventConformanceRequired() {
			t.Fatalf("required PostgreSQL 18 event container: %v", err)
		}
		t.Skipf("PostgreSQL 18 event container unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, SchemaSQL()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func eventConformanceRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED"))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}
