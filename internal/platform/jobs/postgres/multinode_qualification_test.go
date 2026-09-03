package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgreSQL18MultiNodeJobQualification exercises two independently
// configured pools. A separate repository per pool mirrors the two serving
// nodes and proves the durable job row, rather than an in-process mutex, is
// the coordination authority.
func TestPostgreSQL18MultiNodeJobQualification(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "jobs_multinode_qualification")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	poolA, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolA.Close)
	poolB, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolB.Close)
	if poolA == poolB {
		t.Fatal("multi-node qualification accidentally reused one PostgreSQL pool")
	}
	if _, err := poolA.Exec(ctx, SchemaSQL()); err != nil {
		t.Fatalf("apply jobs schema: %v", err)
	}

	repoA := NewRepository(poolA)
	repoB := NewRepository(poolB)
	if repoA == repoB {
		t.Fatal("multi-node qualification accidentally reused one jobs repository")
	}

	t.Run("simultaneous claims have one durable winner", func(t *testing.T) {
		created, err := repoA.Enqueue(ctx, jobs.EnqueueInput{
			ID: "multinode-claim", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground,
			PrincipalID: "multinode-principal", PartitionKey: "refresh:multinode:prod",
			ResourceKind: "refresh", ResourceID: "multinode-claim", EstimatedMemoryBytes: 1, Payload: []byte(`{"v":1}`),
		})
		if err != nil {
			t.Fatal(err)
		}

		type claimResult struct {
			job jobs.Job
			ok  bool
			err error
		}
		start := make(chan struct{})
		ready := make(chan struct{}, 2)
		results := make(chan claimResult, 2)
		var wg sync.WaitGroup
		for i, repo := range []*Repository{repoA, repoB} {
			owner := []string{"node-a", "node-b"}[i]
			wg.Add(1)
			go func(repo *Repository, owner string) {
				defer wg.Done()
				ready <- struct{}{}
				<-start
				claimed, ok, claimErr := repo.ClaimByID(ctx, created.ID, jobpolicy.WorkloadClassBackground, owner, time.Minute)
				results <- claimResult{job: claimed, ok: ok, err: claimErr}
			}(repo, owner)
		}
		<-ready
		<-ready
		close(start)
		wg.Wait()
		close(results)

		var winner jobs.Job
		winners := 0
		for result := range results {
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.ok {
				winners++
				winner = result.job
			}
		}
		if winners != 1 || winner.Attempts != 1 || winner.LeaseGeneration != 1 {
			t.Fatalf("simultaneous claim winners=%d winner=%#v", winners, winner)
		}
		if err := repoA.Complete(ctx, created.ID, winner.Fence()); err != nil {
			t.Fatalf("complete claim winner: %v", err)
		}
	})

	t.Run("takeover fences reused owner identity", func(t *testing.T) {
		created, err := repoA.Enqueue(ctx, jobs.EnqueueInput{
			ID: "multinode-takeover", Kind: "refresh.run", WorkloadClass: jobpolicy.WorkloadClassBackground,
			PrincipalID: "multinode-takeover-principal", PartitionKey: "refresh:multinode:takeover",
			ResourceKind: "refresh", ResourceID: "multinode-takeover", EstimatedMemoryBytes: 1, Payload: []byte(`{"v":1}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		const reusedOwner = "node-reused"
		first, ok, err := repoA.ClaimByID(ctx, created.ID, jobpolicy.WorkloadClassBackground, reusedOwner, 25*time.Millisecond)
		if err != nil || !ok {
			t.Fatalf("first claim = %#v, ok=%v, err=%v", first, ok, err)
		}
		if _, err := poolA.Exec(ctx, `SELECT pg_sleep(0.1)`); err != nil {
			t.Fatal(err)
		}
		second, ok, err := repoB.ClaimByID(ctx, created.ID, jobpolicy.WorkloadClassBackground, reusedOwner, time.Minute)
		if err != nil || !ok {
			t.Fatalf("takeover claim = %#v, ok=%v, err=%v", second, ok, err)
		}
		if second.Attempts != 2 || second.LeaseGeneration <= first.LeaseGeneration {
			t.Fatalf("takeover did not advance fencing: first=%#v second=%#v", first, second)
		}

		if err := repoA.Renew(ctx, created.ID, first.Fence(), time.Minute); !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("stale heartbeat = %v, want conflict", err)
		}
		if err := repoA.Complete(ctx, created.ID, first.Fence()); !errors.Is(err, jobs.ErrConflict) {
			t.Fatalf("stale terminal completion = %v, want conflict", err)
		}
		if err := repoB.Complete(ctx, created.ID, second.Fence()); err != nil {
			t.Fatalf("takeover completion: %v", err)
		}

		var firstOutcome, secondOutcome string
		if err := poolA.QueryRow(ctx, `SELECT outcome FROM jobs.attempt WHERE job_id=$1 AND attempt_number=1`, created.ID).Scan(&firstOutcome); err != nil {
			t.Fatal(err)
		}
		if err := poolB.QueryRow(ctx, `SELECT outcome FROM jobs.attempt WHERE job_id=$1 AND attempt_number=2`, created.ID).Scan(&secondOutcome); err != nil {
			t.Fatal(err)
		}
		if firstOutcome != "expired" || secondOutcome != "succeeded" {
			t.Fatalf("attempt outcomes = %q, %q", firstOutcome, secondOutcome)
		}
	})
}
