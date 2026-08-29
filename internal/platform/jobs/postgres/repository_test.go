package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestSchemaContainsNativeJobCoordinationTables(t *testing.T) {
	if len(SchemaSQL()) == 0 {
		t.Fatal("schema SQL is empty")
	}
}

func TestPostgreSQL18JobsLeastPrivilegeRoles(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-secret"})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Login: true, Password: "readonly-secret"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "jobs_roles")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "SET ROLE leapview_control_migrator"); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		conn.Release()
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	var runtimeDelete bool
	if err := runtimeDB.QueryRow(t.Context(), `SELECT has_table_privilege(current_user, 'jobs.job', 'DELETE')`).Scan(&runtimeDelete); err != nil {
		t.Fatal(err)
	}
	if runtimeDelete {
		t.Fatal("runtime role has direct jobs DELETE privilege")
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM jobs.job`); err == nil {
		t.Fatal("runtime direct jobs DELETE unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `SELECT jobs.prune(clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime job retention unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `
		INSERT INTO jobs.event_sequence(resource_kind, resource_id, next_event_id)
		VALUES ('refresh', 'append-only-proof', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `
		INSERT INTO jobs.event(resource_kind, resource_id, event_id, event_type, data)
		VALUES ('refresh', 'append-only-proof', 1, 'refresh.created', '{"ok":true}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `
		UPDATE jobs.event SET data='{"ok":false}'::jsonb
		WHERE resource_kind='refresh' AND resource_id='append-only-proof' AND event_id=1`); err == nil {
		t.Fatal("runtime mutated append-only job event")
	}

	maintenanceDB, err := pgxpool.New(t.Context(), database.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT jobs.prune(clock_timestamp(), 1)`); err != nil {
		t.Fatalf("maintenance job retention: %v", err)
	}

	readonlyDB, err := pgxpool.New(t.Context(), database.URL(readonly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyDB.Close)
	var readonlyPayload bool
	if err := readonlyDB.QueryRow(t.Context(), `SELECT has_table_privilege(current_user, 'jobs.job', 'SELECT')`).Scan(&readonlyPayload); err != nil {
		t.Fatal(err)
	}
	if readonlyPayload {
		t.Fatal("readonly role can select payload-bearing jobs table")
	}
	var observations int
	if err := readonlyDB.QueryRow(t.Context(), `SELECT count(*) FROM jobs.job_observability`).Scan(&observations); err != nil {
		t.Fatalf("readonly observability view: %v", err)
	}
}

func TestPostgreSQL18ConcurrentWorkerClaimConformance(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if !postgresConformanceRequired() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcpostgres.Run(ctx, "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8",
		tcpostgres.WithDatabase("jobs_conformance"), tcpostgres.WithUsername("jobs"), tcpostgres.WithPassword("jobs-secret"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)))
	if err != nil {
		if postgresConformanceRequired() {
			t.Fatalf("required PostgreSQL 18 jobs conformance container: %v", err)
		}
		t.Skipf("PostgreSQL conformance container unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, SchemaSQL()); err != nil {
		t.Fatalf("apply jobs schema: %v", err)
	}
	if _, err := pool.Exec(ctx, SchemaSQL()); err != nil {
		t.Fatalf("reapply jobs schema: %v", err)
	}
	repo := NewRepository(pool)
	created, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "concurrent-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "principal", GroupIDs: []string{}, PartitionKey: "test:concurrent", ResourceKind: "refresh", ResourceID: "run-1", EstimatedMemoryBytes: 1, Payload: []byte(`{"v":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wg sync.WaitGroup
	claimed := make(chan jobs.Job, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job, ok, claimErr := repo.ClaimByID(ctx, created.ID, "background", "worker-"+string(rune('a'+i)), time.Minute)
			if claimErr != nil {
				errs <- claimErr
				return
			}
			if ok {
				claimed <- job
			}
		}(i)
	}
	wg.Wait()
	close(claimed)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var winner jobs.Job
	claimCount := 0
	for job := range claimed {
		claimCount++
		winner = job
	}
	if claimCount != 1 || winner.ID == "" || winner.Attempts != 1 || winner.LeaseGeneration != 1 {
		t.Fatalf("claim winner = %#v", winner)
	}
	t.Run("canonical enqueue replay", func(t *testing.T) {
		input := jobs.EnqueueInput{ID: "canonical-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "canonical-principal", PartitionKey: "test:canonical", ResourceKind: "refresh", ResourceID: "canonical", EstimatedMemoryBytes: 1, Payload: []byte(`{"b":2,"a":1}`)}
		first, err := repo.Enqueue(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		input.Payload = []byte(` { "a": 1, "b": 2 } `)
		second, err := repo.Enqueue(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID != second.ID || !jsonEquivalent(second.Payload, []byte(`{"a":1,"b":2}`)) {
			t.Fatalf("canonical replay first=%#v second=%#v", first, second)
		}
		if err := repo.Cancel(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
	})
	if err := repo.Complete(ctx, winner.ID, winner.Fence()); err != nil {
		t.Fatalf("complete winner: %v", err)
	}
	if err := repo.Complete(ctx, winner.ID, winner.Fence()); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("second completion = %v, want conflict", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs.job SET error = '{"tampered":true}'::jsonb WHERE id = $1`, winner.ID); err == nil {
		t.Fatal("terminal job accepted a direct mutation")
	}
	if removed, err := repo.Prune(ctx, time.Now().UTC().Add(time.Second), 10); err != nil || removed < 1 {
		t.Fatalf("bounded terminal prune removed=%d err=%v", removed, err)
	}
	if _, err := repo.Get(ctx, winner.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("pruned terminal job lookup = %v", err)
	}
	t.Run("direct sequence and evidence bounds", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `INSERT INTO jobs.event_sequence (resource_kind, resource_id, next_event_id) VALUES ('guard', 'bad-start', 2)`); err == nil {
			t.Fatal("event sequence accepted a non-one starting value")
		}
		if _, err := pool.Exec(ctx, `INSERT INTO jobs.event_sequence (resource_kind, resource_id, next_event_id) VALUES ('guard', 'good', 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs.event_sequence SET next_event_id = next_event_id + 2 WHERE resource_kind = 'guard' AND resource_id = 'good'`); err == nil {
			t.Fatal("event sequence accepted a non-unit advance")
		}
		if _, err := pool.Exec(ctx, `INSERT INTO jobs.event (resource_kind, resource_id, event_id, event_type, data) VALUES ('guard', 'bad-event', 1, 'guard.test', 'null'::jsonb)`); err == nil {
			t.Fatal("event accepted a non-object payload")
		}
		if _, err := pool.Exec(ctx, `INSERT INTO jobs.event (resource_kind, resource_id, event_id, event_type, data, created_at) VALUES (' guard ', 'bad-identity', 1, 'guard.test', '{}'::jsonb, clock_timestamp() - interval '1 day')`); err == nil {
			t.Fatal("event accepted a noncanonical identity")
		}
		claimInput := jobs.EnqueueInput{ID: "guard-lease-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassControl, PrincipalID: "guard-lease-principal", PartitionKey: "test:guard", ResourceKind: "refresh", ResourceID: "guard-lease", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}
		if _, err := repo.Enqueue(ctx, claimInput); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO jobs.attempt (job_id, attempt_number, fencing_generation, owner, lease_expires_at) VALUES ($1, 1, 1, 'forged', clock_timestamp() + interval '25 hours')`, claimInput.ID); err == nil {
			t.Fatal("attempt accepted a lease beyond the bounded window")
		}
	})

	t.Run("ordered concurrent events", func(t *testing.T) {
		const count = 20
		var wg sync.WaitGroup
		errs := make(chan error, count)
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := repo.AppendEvent(ctx, "refresh", "events-1", "refresh.progress", []byte(`{"status":"running"}`))
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		events, err := repo.ListEvents(ctx, "refresh", "events-1", 0, count)
		if err != nil || len(events) != count {
			t.Fatalf("events = %d, err=%v", len(events), err)
		}
		for i, event := range events {
			if event.ID != int64(i+1) {
				t.Fatalf("event %d id=%d", i, event.ID)
			}
		}
	})

	t.Run("workflow replay is idempotent", func(t *testing.T) {
		intent := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "refresh.ready", ResourceKind: "refresh", ResourceID: "workflow-1", EventType: "refresh.ready", Data: []byte(`{"status":"ready"}`)}}
		if err := repo.CommitWorkflow(ctx, intent); err != nil {
			t.Fatal(err)
		}
		if err := repo.CommitWorkflow(ctx, intent); err != nil {
			t.Fatal(err)
		}
		changed := intent
		changed.Event.Data = []byte(`{"status":"different"}`)
		if err := repo.CommitWorkflow(ctx, changed); !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("workflow replay with changed payload = %v, want conflict", err)
		}
		large := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "refresh.large", ResourceKind: "refresh", ResourceID: "workflow-1", EventType: "refresh.large", Data: []byte(`{"value":9007199254740993}`)}}
		if err := repo.CommitWorkflow(ctx, large); err != nil {
			t.Fatal(err)
		}
		largeChanged := large
		largeChanged.Event.Data = []byte(`{"value":9007199254740992}`)
		if err := repo.CommitWorkflow(ctx, largeChanged); !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("large-number workflow replay = %v, want conflict", err)
		}
		appended, err := repo.AppendEvent(ctx, "refresh", "workflow-1", "refresh.observed", []byte(`{"status":"observed"}`))
		if err != nil {
			t.Fatal(err)
		}
		if appended.ID != 3 {
			t.Fatalf("event after replay id=%d, want 3", appended.ID)
		}
		const concurrent = 8
		workflowErrs := make(chan error, concurrent)
		var workflowWG sync.WaitGroup
		for i := 0; i < concurrent; i++ {
			workflowWG.Add(1)
			go func() {
				defer workflowWG.Done()
				workflowErrs <- repo.CommitWorkflow(ctx, intent)
			}()
		}
		workflowWG.Wait()
		close(workflowErrs)
		for workflowErr := range workflowErrs {
			if workflowErr != nil {
				t.Fatal(workflowErr)
			}
		}
		events, err := repo.ListEvents(ctx, "refresh", "workflow-1", 0, 10)
		if err != nil || len(events) != 3 || events[0].ID != 1 || events[1].ID != 2 || events[2].ID != 3 {
			t.Fatalf("concurrent workflow events = %#v, err=%v", events, err)
		}
	})

	t.Run("bounded reclaim and stale fence", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "reclaim-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "reclaim-principal", GroupIDs: []string{}, PartitionKey: "test:reclaim", ResourceKind: "refresh", ResourceID: "reclaim-1", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		first, ok, err := repo.ClaimByID(ctx, job.ID, "background", "worker-a", 50*time.Millisecond)
		if err != nil || !ok {
			t.Fatalf("first claim = %#v, %v, %v", first, ok, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs.job SET lease_expires_at = lease_expires_at - interval '1 second' WHERE id = $1`, job.ID); err == nil {
			t.Fatal("heartbeat shortened a live lease")
		}
		if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.1)`); err != nil {
			t.Fatal(err)
		}
		second, ok, err := repo.ClaimByID(ctx, job.ID, "background", "worker-a", 50*time.Millisecond)
		if err != nil || !ok || second.Attempts != 2 || second.LeaseGeneration != 2 {
			t.Fatalf("reclaim = %#v, %v, %v", second, ok, err)
		}
		if err := repo.Complete(ctx, first.ID, first.Fence()); !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("stale completion = %v", err)
		}
		if err := repo.Complete(ctx, second.ID, jobs.Fence{Owner: second.LeaseOwner}); err == nil {
			t.Fatal("completion accepted a zero fencing generation")
		}
		if err := repo.Retry(ctx, second.ID, second.Fence(), MaxRetryDelay+time.Second, []byte(`{"retry":true}`)); err == nil {
			t.Fatal("retry accepted an unbounded delay")
		}
		if err := repo.Complete(ctx, second.ID, second.Fence()); err != nil {
			t.Fatal(err)
		}
		var firstOutcome, secondOutcome string
		if err := pool.QueryRow(ctx, `SELECT outcome FROM jobs.attempt WHERE job_id = $1 AND attempt_number = 1`, job.ID).Scan(&firstOutcome); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT outcome FROM jobs.attempt WHERE job_id = $1 AND attempt_number = 2`, job.ID).Scan(&secondOutcome); err != nil {
			t.Fatal(err)
		}
		if firstOutcome != "expired" || secondOutcome != "succeeded" {
			t.Fatalf("attempt outcomes = %q, %q", firstOutcome, secondOutcome)
		}
	})

	t.Run("attempt ceiling", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "ceiling-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "ceiling-principal", GroupIDs: []string{}, PartitionKey: "test:ceiling", ResourceKind: "refresh", ResourceID: "ceiling-1", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		for attempt := int64(1); attempt <= MaxAttempts; attempt++ {
			claimed, ok, claimErr := repo.ClaimByID(ctx, job.ID, "background", "ceiling-worker", 20*time.Millisecond)
			if claimErr != nil || !ok || claimed.Attempts != int(attempt) {
				t.Fatalf("claim %d = %#v, %v, %v", attempt, claimed, ok, claimErr)
			}
			if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.05)`); err != nil {
				t.Fatal(err)
			}
		}
		candidates, err := repo.Candidates(ctx, "background", 16)
		if err != nil || len(candidates) != 1 || candidates[0].ID != job.ID {
			t.Fatalf("expired ceiling candidates = %#v, err=%v", candidates, err)
		}
		if _, ok, err := repo.ClaimByID(ctx, candidates[0].ID, "background", "ceiling-worker", time.Minute); err != nil || ok {
			t.Fatalf("claim past ceiling = ok:%v err:%v", ok, err)
		}
		stored, err := repo.Get(ctx, job.ID)
		if err != nil || stored.Status != jobs.StatusFailed {
			t.Fatalf("exhausted job = %#v, err=%v", stored, err)
		}
		var latestOutcome, latestError string
		var latestRetryAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT outcome,error::text,retry_at FROM jobs.attempt WHERE job_id=$1 ORDER BY attempt_number DESC LIMIT 1`, job.ID).Scan(&latestOutcome, &latestError, &latestRetryAt); err != nil {
			t.Fatal(err)
		}
		if latestOutcome != "failed" || !jsonEquivalent([]byte(latestError), []byte(`{"code":"MAX_ATTEMPTS_EXCEEDED"}`)) || latestRetryAt != nil {
			t.Fatalf("ceiling latest attempt outcome=%q error=%q retry_at=%v", latestOutcome, latestError, latestRetryAt)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReconcileTerminalTx(ctx, tx, job.ID, jobs.StatusFailed); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("exact ceiling reconciliation replay: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expired claim with missing exact attempt fails closed", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "reclaim-missing-attempt", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "reclaim-missing-principal", PartitionKey: "refresh:reclaim-missing:prod", ResourceKind: "refresh", ResourceID: "reclaim-missing-attempt", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "reclaim-missing-worker", 20*time.Millisecond)
		if err != nil || !ok {
			t.Fatalf("missing-attempt claim: ok=%v err=%v", ok, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM jobs.attempt WHERE job_id=$1 AND attempt_number=$2 AND fencing_generation=$3`, claimed.ID, claimed.Attempts, claimed.LeaseGeneration); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.05)`); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "reclaim-missing-worker-2", time.Minute); err != nil || ok {
			t.Fatalf("missing-attempt reclaim: ok=%v err=%v, want closed", ok, err)
		}
	})
	t.Run("principal fairness is partitioned", func(t *testing.T) {
		inputs := []jobs.EnqueueInput{
			{ID: "partition-a-1", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "shared-principal", PartitionKey: "refresh:project-a:prod", ResourceKind: "refresh", ResourceID: "a-1", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)},
			{ID: "partition-a-2", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "shared-principal", PartitionKey: "refresh:project-a:prod", ResourceKind: "refresh", ResourceID: "a-2", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)},
			{ID: "partition-b-1", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "shared-principal", PartitionKey: "refresh:project-b:prod", ResourceKind: "refresh", ResourceID: "b-1", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)},
		}
		for _, input := range inputs {
			if _, err := repo.Enqueue(ctx, input); err != nil {
				t.Fatal(err)
			}
		}
		candidates, err := repo.CandidatesByResourceKind(ctx, jobpolicy.WorkloadClassBackground, "refresh", 16)
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]bool)
		for _, candidate := range candidates {
			seen[candidate.ID] = true
		}
		if !seen["partition-a-1"] || !seen["partition-b-1"] || seen["partition-a-2"] {
			t.Fatalf("partitioned fairness candidates = %#v", candidates)
		}
		if _, ok, err := repo.ClaimByID(ctx, "partition-a-2", jobpolicy.WorkloadClassBackground, "partition-worker", time.Minute); err != nil || ok {
			t.Fatalf("direct claim bypassed partition head: ok=%v err=%v", ok, err)
		}
		headA, ok, err := repo.ClaimByID(ctx, "partition-a-1", jobpolicy.WorkloadClassBackground, "partition-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("partition A head claim: ok=%v err=%v", ok, err)
		}
		headB, ok, err := repo.ClaimByID(ctx, "partition-b-1", jobpolicy.WorkloadClassBackground, "partition-worker-b", time.Minute)
		if err != nil || !ok {
			t.Fatalf("partition B progress claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Complete(ctx, headB.ID, headB.Fence()); err != nil {
			t.Fatal(err)
		}
		if err := repo.Complete(ctx, headA.ID, headA.Fence()); err != nil {
			t.Fatal(err)
		}
		successorA, ok, err := repo.ClaimByID(ctx, "partition-a-2", jobpolicy.WorkloadClassBackground, "partition-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("partition A successor claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Complete(ctx, successorA.ID, successorA.Fence()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("supersede validates queued and running authorities", func(t *testing.T) {
		queued, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "supersede-queued", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "supersede-principal", PartitionKey: "refresh:supersede:prod", ResourceKind: "refresh", ResourceID: "supersede-queued", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		running, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "supersede-running", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "supersede-principal", PartitionKey: "refresh:supersede:running", ResourceKind: "refresh", ResourceID: "supersede-running", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		_, ok, err := repo.ClaimByID(ctx, running.ID, jobpolicy.WorkloadClassBackground, "supersede-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("running claim: ok=%v err=%v", ok, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.SupersedeTx(ctx, tx, []string{running.ID, queued.ID}); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{queued.ID, running.ID} {
			stored, getErr := repo.Get(ctx, id)
			if getErr != nil || stored.Status != jobs.StatusCancelled {
				t.Fatalf("superseded job %s = %#v err=%v", id, stored, getErr)
			}
		}
		var outcome string
		if err := pool.QueryRow(ctx, `SELECT outcome FROM jobs.attempt WHERE job_id=$1`, running.ID).Scan(&outcome); err != nil || outcome != "cancelled" {
			t.Fatalf("superseded running attempt outcome=%q err=%v", outcome, err)
		}
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.SupersedeTx(ctx, tx, []string{"missing-supersede-job"}); !errors.Is(err, jobs.ErrConflict) {
			_ = tx.Rollback(ctx)
			t.Fatalf("missing supersede error=%v, want conflict", err)
		}
		_ = tx.Rollback(ctx)
	})
	t.Run("terminal recovery closes retrying attempt exactly", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "reconcile-retrying", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "reconcile-principal", PartitionKey: "refresh:reconcile:prod", ResourceKind: "refresh", ResourceID: "reconcile-retrying", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "reconcile-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("reconcile claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Retry(ctx, claimed.ID, claimed.Fence(), time.Second, []byte(`{"retry":true}`)); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReconcileTerminalTx(ctx, tx, claimed.ID, jobs.StatusSucceeded); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.Get(ctx, claimed.ID)
		if err != nil || stored.Status != jobs.StatusSucceeded || stored.ErrorJSON != "{}" {
			t.Fatalf("reconciled job = %#v err=%v", stored, err)
		}
		var outcome, errorJSON string
		if err := pool.QueryRow(ctx, `SELECT outcome,error::text FROM jobs.attempt WHERE job_id=$1`, claimed.ID).Scan(&outcome, &errorJSON); err != nil || outcome != "succeeded" || errorJSON != "{}" {
			t.Fatalf("reconciled attempt outcome=%q error=%q err=%v", outcome, errorJSON, err)
		}
		conflictTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReconcileTerminalTx(ctx, conflictTx, claimed.ID, jobs.StatusFailed); !errors.Is(err, jobs.ErrConflict) {
			_ = conflictTx.Rollback(ctx)
			t.Fatalf("reconcile mismatched terminal status = %v, want conflict", err)
		}
		_ = conflictTx.Rollback(ctx)
	})
	t.Run("queued retry cancellation closes attempt and replays conflict", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "cancel-retrying", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "cancel-principal", PartitionKey: "refresh:cancel:prod", ResourceKind: "refresh", ResourceID: "cancel-retrying", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "cancel-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("cancel claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Retry(ctx, claimed.ID, claimed.Fence(), time.Second, []byte(`{"retry":true}`)); err != nil {
			t.Fatal(err)
		}
		if err := repo.Cancel(ctx, claimed.ID); err != nil {
			t.Fatalf("cancel retrying job: %v", err)
		}
		stored, err := repo.Get(ctx, claimed.ID)
		if err != nil || stored.Status != jobs.StatusCancelled || !jsonEquivalent([]byte(stored.ErrorJSON), []byte(`{"code":"JOB_CANCELLED"}`)) {
			t.Fatalf("cancelled retrying job = %#v err=%v", stored, err)
		}
		var outcome, errorJSON string
		var retryAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT outcome,error::text,retry_at FROM jobs.attempt WHERE job_id=$1`, claimed.ID).Scan(&outcome, &errorJSON, &retryAt); err != nil || outcome != "cancelled" || !jsonEquivalent([]byte(errorJSON), []byte(`{"code":"JOB_CANCELLED"}`)) || retryAt != nil {
			t.Fatalf("cancelled retrying attempt outcome=%q error=%q retry_at=%v err=%v", outcome, errorJSON, retryAt, err)
		}
		if err := repo.Cancel(ctx, claimed.ID); !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("cancel replay = %v, want conflict", err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		txJob, err := repo.EnqueueTx(ctx, tx, jobs.EnqueueInput{ID: "cancel-retrying-tx", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "cancel-principal", PartitionKey: "refresh:cancel:tx", ResourceKind: "refresh", ResourceID: "cancel-retrying-tx", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		// Claim/retry outside this transaction gives CancelTx the same queued
		// retry evidence a caller-owned domain transaction would observe.
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		txClaimed, ok, err := repo.ClaimByID(ctx, txJob.ID, jobpolicy.WorkloadClassBackground, "cancel-worker-tx", time.Minute)
		if err != nil || !ok {
			t.Fatalf("cancel tx claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Retry(ctx, txClaimed.ID, txClaimed.Fence(), time.Second, []byte(`{"retry":true}`)); err != nil {
			t.Fatal(err)
		}
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CancelTx(ctx, tx, txClaimed.ID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("cancel tx retrying job: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		afterRetry, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "cancel-claimed-after-retry", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "cancel-principal", PartitionKey: "refresh:cancel:claimed", ResourceKind: "refresh", ResourceID: "cancel-claimed-after-retry", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		firstClaim, ok, err := repo.ClaimByID(ctx, afterRetry.ID, jobpolicy.WorkloadClassBackground, "cancel-claimed-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("cancel claimed first claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Retry(ctx, firstClaim.ID, firstClaim.Fence(), 0, []byte(`{"retry":true}`)); err != nil {
			t.Fatal(err)
		}
		secondClaim, ok, err := repo.ClaimByID(ctx, afterRetry.ID, jobpolicy.WorkloadClassBackground, "cancel-claimed-worker-2", time.Minute)
		if err != nil || !ok {
			t.Fatalf("cancel claimed retry claim: ok=%v err=%v", ok, err)
		}
		if err := repo.CancelClaimed(ctx, secondClaim.ID, secondClaim.Fence()); err != nil {
			t.Fatalf("cancel claimed after retry: %v", err)
		}
		claimedAfterRetry, err := repo.Get(ctx, afterRetry.ID)
		if err != nil || claimedAfterRetry.Status != jobs.StatusCancelled || !jsonEquivalent([]byte(claimedAfterRetry.ErrorJSON), []byte(`{"code":"JOB_CANCELLED"}`)) {
			t.Fatalf("cancel claimed after retry job=%#v err=%v", claimedAfterRetry, err)
		}
	})
	t.Run("retrying attempt cannot be revived or rewritten", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "retrying-attempt-guard", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "retrying-guard-principal", PartitionKey: "refresh:retrying-guard:prod", ResourceKind: "refresh", ResourceID: "retrying-attempt-guard", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "retrying-guard-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("retrying guard claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Retry(ctx, claimed.ID, claimed.Fence(), time.Second, []byte(`{"retry":true}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs.attempt SET outcome='running',retry_at=NULL WHERE job_id=$1 AND attempt_number=$2`, claimed.ID, claimed.Attempts); err == nil {
			t.Fatal("retrying attempt was revived to running")
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs.attempt SET error='{"tampered":true}'::jsonb WHERE job_id=$1 AND attempt_number=$2`, claimed.ID, claimed.Attempts); err == nil {
			t.Fatal("retrying attempt evidence was rewritten")
		}
		if err := repo.Cancel(ctx, claimed.ID); err != nil {
			t.Fatalf("retrying guard cleanup: %v", err)
		}
	})
	t.Run("terminal reconciliation rejects mismatched attempt evidence", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "reconcile-mismatched-evidence", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "reconcile-mismatch-principal", PartitionKey: "refresh:reconcile-mismatch:prod", ResourceKind: "refresh", ResourceID: "reconcile-mismatched-evidence", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "reconcile-mismatch-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("mismatch claim: ok=%v err=%v", ok, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs.attempt SET outcome='succeeded',finished_at=clock_timestamp(),error='{"forged":true}'::jsonb WHERE job_id=$1 AND attempt_number=$2 AND fencing_generation=$3`, claimed.ID, claimed.Attempts, claimed.LeaseGeneration); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReconcileTerminalTx(ctx, tx, claimed.ID, jobs.StatusSucceeded); !errors.Is(err, jobs.ErrConflict) {
			_ = tx.Rollback(ctx)
			t.Fatalf("mismatched attempt evidence reconcile=%v, want conflict", err)
		}
		_ = tx.Rollback(ctx)
	})
	t.Run("terminal reconciliation rejects mismatched attempt owner", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "reconcile-mismatched-owner", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "reconcile-owner-principal", PartitionKey: "refresh:reconcile-owner:prod", ResourceKind: "refresh", ResourceID: "reconcile-mismatched-owner", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "reconcile-owner-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("owner mismatch claim: ok=%v err=%v", ok, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM jobs.attempt WHERE job_id=$1 AND attempt_number=$2`, claimed.ID, claimed.Attempts); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO jobs.attempt(job_id,attempt_number,fencing_generation,owner,lease_expires_at) VALUES($1,$2,$3,'forged-owner',clock_timestamp()+interval '1 minute')`, claimed.ID, claimed.Attempts, claimed.LeaseGeneration); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReconcileTerminalTx(ctx, tx, claimed.ID, jobs.StatusSucceeded); !errors.Is(err, jobs.ErrConflict) {
			_ = tx.Rollback(ctx)
			t.Fatalf("mismatched attempt owner reconcile=%v, want conflict", err)
		}
		_ = tx.Rollback(ctx)
	})
	t.Run("poison quarantine closes queued retry attempt and replays", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "quarantine-retrying", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "quarantine-principal", PartitionKey: "refresh:quarantine:prod", ResourceKind: "refresh", ResourceID: "quarantine-retrying", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := repo.ClaimByID(ctx, job.ID, jobpolicy.WorkloadClassBackground, "quarantine-worker", time.Minute)
		if err != nil || !ok {
			t.Fatalf("quarantine claim: ok=%v err=%v", ok, err)
		}
		if err := repo.Retry(ctx, claimed.ID, claimed.Fence(), 0, []byte(`{"retry":true}`)); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := repo.QuarantineQueuedTx(ctx, tx, claimed.ID, []byte(`{"code":"REFRESH_POISON_PAYLOAD"}`))
		if err != nil || !changed {
			_ = tx.Rollback(ctx)
			t.Fatalf("quarantine retrying changed=%v err=%v", changed, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.Get(ctx, claimed.ID)
		if err != nil || stored.Status != jobs.StatusCancelled {
			t.Fatalf("quarantine job=%#v err=%v", stored, err)
		}
		var outcome string
		if err := pool.QueryRow(ctx, `SELECT outcome FROM jobs.attempt WHERE job_id=$1`, claimed.ID).Scan(&outcome); err != nil || outcome != "cancelled" {
			t.Fatalf("quarantine attempt outcome=%q err=%v", outcome, err)
		}
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := repo.QuarantineQueuedTx(ctx, tx, claimed.ID, []byte(`{"code":"REFRESH_POISON_PAYLOAD"}`))
		if err != nil || !replayed {
			_ = tx.Rollback(ctx)
			t.Fatalf("quarantine replay changed=%v err=%v", replayed, err)
		}
		_ = tx.Rollback(ctx)
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if replayed, err := repo.QuarantineQueuedTx(ctx, tx, claimed.ID, []byte(`{"code":"REFRESH_POISON_PAYLOAD","detail":"drift"}`)); err != nil || replayed {
			_ = tx.Rollback(ctx)
			t.Fatalf("tampered poison replay changed=%v err=%v", replayed, err)
		}
		_ = tx.Rollback(ctx)
	})
}

func postgresConformanceRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED"))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}
