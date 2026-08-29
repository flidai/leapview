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
	created, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "concurrent-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "principal", GroupIDs: []string{}, ResourceKind: "refresh", ResourceID: "run-1", EstimatedMemoryBytes: 1, Payload: []byte(`{"v":1}`)})
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
	if err := repo.Complete(ctx, winner.ID, winner.Fence()); err != nil {
		t.Fatalf("complete winner: %v", err)
	}
	if err := repo.Complete(ctx, winner.ID, winner.Fence()); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("second completion = %v, want conflict", err)
	}

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
		appended, err := repo.AppendEvent(ctx, "refresh", "workflow-1", "refresh.observed", []byte(`{"status":"observed"}`))
		if err != nil {
			t.Fatal(err)
		}
		if appended.ID != 2 {
			t.Fatalf("event after replay id=%d, want 2", appended.ID)
		}
	})

	t.Run("bounded reclaim and stale fence", func(t *testing.T) {
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "reclaim-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "reclaim-principal", GroupIDs: []string{}, ResourceKind: "refresh", ResourceID: "reclaim-1", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		first, ok, err := repo.ClaimByID(ctx, job.ID, "background", "worker-a", time.Minute)
		if err != nil || !ok {
			t.Fatalf("first claim = %#v, %v, %v", first, ok, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs.job SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, job.ID); err != nil {
			t.Fatal(err)
		}
		second, ok, err := repo.ClaimByID(ctx, job.ID, "background", "worker-a", time.Minute)
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
		job, err := repo.Enqueue(ctx, jobs.EnqueueInput{ID: "ceiling-job", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "ceiling-principal", GroupIDs: []string{}, ResourceKind: "refresh", ResourceID: "ceiling-1", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		for attempt := int64(1); attempt <= MaxAttempts; attempt++ {
			claimed, ok, claimErr := repo.ClaimByID(ctx, job.ID, "background", "ceiling-worker", time.Minute)
			if claimErr != nil || !ok || claimed.Attempts != int(attempt) {
				t.Fatalf("claim %d = %#v, %v, %v", attempt, claimed, ok, claimErr)
			}
			if _, err := pool.Exec(ctx, `UPDATE jobs.job SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, job.ID); err != nil {
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
