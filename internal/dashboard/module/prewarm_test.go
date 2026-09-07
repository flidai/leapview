package module

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

func prewarmTestConfig() PrewarmConfig {
	return PrewarmConfig{PublicationIDs: []string{"one", "two"}, MaxPublications: 2, MaxTargets: 4, ExecutionDeadline: time.Second, Concurrency: 1}
}
func prewarmTestPublication(id, generation string) publication.Publication {
	return publication.Publication{ID: id, PublicID: "public-" + id, Name: id, ProjectID: "project_1", Dashboard: "published", DefaultPage: "overview", Configured: true, ServingStateID: generation, Revision: 1}
}

func TestPrewarmConfig(t *testing.T) {
	if err := (PrewarmConfig{}).validate(); err != nil {
		t.Fatal(err)
	}
	if c := newPrewarmCoordinator(PrewarmConfig{}, nil, nil); c != nil {
		t.Fatal("empty selection enabled warming")
	}
	for _, field := range []string{"valid", "duplicate", "empty", "space", "publications", "targets", "deadline", "concurrency"} {
		t.Run(field, func(t *testing.T) {
			c := prewarmTestConfig()
			switch field {
			case "duplicate":
				c.PublicationIDs[1] = "one"
			case "empty":
				c.PublicationIDs[0] = ""
			case "space":
				c.PublicationIDs[0] = " one"
			case "publications":
				c.MaxPublications = 1
			case "targets":
				c.MaxTargets = 0
			case "deadline":
				c.ExecutionDeadline = 0
			case "concurrency":
				c.Concurrency = 2
			}
			if err := c.validate(); (err == nil) != (field == "valid") {
				t.Fatalf("validation = %v", err)
			}
		})
	}
}

func TestPrewarmSelectionDeduplicationAndDelayedReadiness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		c := newPrewarmCoordinator(prewarmTestConfig(), func(context.Context, publication.Publication) prewarmResult {
			calls++
			return prewarmResult{"completed", "executed"}
		}, nil)
		done := make(chan struct{})
		go func() { c.run(ctx); close(done) }()
		row := prewarmTestPublication("one", "a")
		c.reconcile([]publication.Publication{row, prewarmTestPublication("unselected", "a")}, "b")
		synctest.Wait()
		if calls != 0 {
			t.Fatal("incompatible generation executed")
		}
		row.Configured = false
		c.reconcile([]publication.Publication{row}, "a")
		synctest.Wait()
		if calls != 0 {
			t.Fatal("inactive publication executed")
		}
		row.Configured = true
		c.reconcile([]publication.Publication{row}, "a")
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("delayed readiness calls=%d", calls)
		}
		for range 20 {
			c.reconcile([]publication.Publication{row}, "a")
			synctest.Wait()
		}
		if calls != 1 || len(c.items) != 2 {
			t.Fatalf("duplicate execution or unbounded state: calls=%d slots=%d", calls, len(c.items))
		}
		row.ServingStateID = "b"
		c.reconcile([]publication.Publication{row}, "b")
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("successor calls=%d", calls)
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("worker did not drain")
		}
	})
}

func TestPrewarmReplacementSerialExecutionAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		active, maxActive := 0, 0
		started := []string{}
		c := newPrewarmCoordinator(prewarmTestConfig(), func(ctx context.Context, row publication.Publication) prewarmResult {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			started = append(started, row.ID+row.ServingStateID)
			mu.Unlock()
			<-ctx.Done()
			mu.Lock()
			active--
			mu.Unlock()
			return prewarmResult{"canceled", "canceled"}
		}, nil)
		done := make(chan struct{})
		go func() { c.run(ctx); close(done) }()
		c.reconcile([]publication.Publication{prewarmTestPublication("one", "a"), prewarmTestPublication("two", "a")}, "a")
		synctest.Wait()
		if len(started) != 1 {
			t.Fatalf("serial admission started=%v", started)
		}
		c.reconcile([]publication.Publication{prewarmTestPublication("one", "b"), prewarmTestPublication("two", "b")}, "b")
		synctest.Wait()
		if len(started) != 2 || started[1] != "oneb" || maxActive != 1 {
			t.Fatalf("replacement started=%v maximum=%d", started, maxActive)
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("shutdown did not drain owner")
		}
		if active != 0 {
			t.Fatal("owner leaked after shutdown")
		}
		c.reconcile([]publication.Publication{prewarmTestPublication("one", "c")}, "c")
		synctest.Wait()
		if len(started) != 2 {
			t.Fatal("closed coordinator accepted work")
		}
	})
}

func TestPrewarmDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		finished := make(chan prewarmResult, 1)
		c := newPrewarmCoordinator(prewarmTestConfig(), func(ctx context.Context, _ publication.Publication) prewarmResult {
			<-ctx.Done()
			return prewarmResult{"failed", "execution"}
		}, func(outcome, reason string) {
			if outcome != "attempted" {
				finished <- prewarmResult{outcome, reason}
			}
		})
		go c.run(ctx)
		c.reconcile([]publication.Publication{prewarmTestPublication("one", "a")}, "a")
		select {
		case result := <-finished:
			if result != (prewarmResult{"canceled", "deadline"}) {
				t.Fatalf("deadline result=%v", result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("deadline did not reach owner")
		}
	})
}

type prewarmLifecycleTelemetry struct {
	testDashboardTelemetry
	mu       sync.Mutex
	outcomes []prewarmResult
}

func (o *prewarmLifecycleTelemetry) DashboardPrewarmObserved(outcome, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, prewarmResult{outcome, reason})
}
func (o *prewarmLifecycleTelemetry) attempts() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, outcome := range o.outcomes {
		if outcome.outcome == "attempted" {
			count++
		}
	}
	return count
}

// The monitor and warmup worker read concurrently with readiness changes.
type prewarmPublicationRepository struct {
	*publicationRepositoryStub
	mu sync.Mutex
}

func (r *prewarmPublicationRepository) ListAll(ctx context.Context) ([]publication.Publication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.publicationRepositoryStub.ListAll(ctx)
}

func (r *prewarmPublicationRepository) GetByPublicID(ctx context.Context, id string) (publication.Publication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.publicationRepositoryStub.GetByPublicID(ctx, id)
}

func (r *prewarmPublicationRepository) markReady() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.row.Configured = true
}

type prewarmBlockedRepository struct {
	*publicationRepositoryStub
	entered chan struct{}
}

func (r *prewarmBlockedRepository) ListAll(ctx context.Context) ([]publication.Publication, error) {
	close(r.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPrewarmMonitorShutdownDuringReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repository := &prewarmBlockedRepository{publicationRepositoryStub: &publicationRepositoryStub{}, entered: make(chan struct{})}
		m := &Module{prewarmConfig: prewarmTestConfig(), publications: repository, publicationService: publication.NewService(repository, nil), publicationAuditConfigured: true, streams: publication.NewMemoryStreamRegistry()}
		if err := m.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer m.Stop(context.Background())
		synctest.Wait()
		select {
		case <-repository.entered:
		default:
			t.Fatal("startup reconciliation did not reach publication reader")
		}
		stopped := make(chan error, 1)
		go func() { stopped <- m.Stop(context.Background()) }()
		synctest.Wait()
		select {
		case err := <-stopped:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("shutdown did not cancel reconciliation and drain worker")
		}
		if !m.prewarm.closed {
			t.Fatal("shutdown left coordinator open")
		}
	})
}

func TestPrewarmPublicationMonitorStartupAndReadiness(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			compiled := moduleCompiledRevision(t, "project_1", "published", "a")
			provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "a"}
			metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled}})
			row := prewarmTestPublication("one", "a")
			row.Configured = !delayed
			repository := &prewarmPublicationRepository{publicationRepositoryStub: &publicationRepositoryStub{row: row}}
			observer := &prewarmLifecycleTelemetry{}
			m := &Module{prewarmConfig: prewarmTestConfig(), publications: repository, publicationService: publication.NewService(repository, nil), publicationAuditConfigured: true, streams: publication.NewMemoryStreamRegistry(), dashboardTelemetry: observer, snapshot: func(context.Context) (string, error) { return "a", nil }}
			m.handler.Metrics = metrics
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer m.Stop(context.Background())
			synctest.Wait()
			if delayed {
				if observer.attempts() != 0 {
					t.Fatal("inactive publication warmed at startup")
				}
				repository.markReady()
				<-time.After(publicationMonitorInterval)
				synctest.Wait()
			}
			if observer.attempts() != 1 {
				t.Fatalf("monitor attempts=%d, want 1", observer.attempts())
			}
			for range 3 {
				<-time.After(publicationMonitorInterval)
				synctest.Wait()
			}
			if observer.attempts() != 1 {
				t.Fatal("repeated ticks repeated work")
			}
			if err := m.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
