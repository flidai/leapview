package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

type recordedLifecycle struct {
	name     string
	events   *[]string
	startErr error
}

func (l recordedLifecycle) Start(context.Context) error {
	*l.events = append(*l.events, "start:"+l.name)
	return l.startErr
}

func (l recordedLifecycle) Stop(context.Context) error {
	*l.events = append(*l.events, "stop:"+l.name)
	return nil
}

func TestApplicationStopsStartedComponentsWhenStartupFails(t *testing.T) {
	events := []string{}
	application := newApplication(http.NotFoundHandler(), []Lifecycle{
		recordedLifecycle{name: "one", events: &events},
		recordedLifecycle{name: "two", events: &events, startErr: errors.New("boom")},
	}, func(context.Context) error { events = append(events, "cleanup"); return nil })
	if err := application.Start(context.Background()); err == nil {
		t.Fatal("Start() accepted a component startup failure")
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:one", "start:two", "stop:one", "cleanup"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

type fatalLifecycle struct {
	recordedLifecycle
	fatal chan error
}

func (l fatalLifecycle) Fatal() <-chan error { return l.fatal }

func TestApplicationForwardsCapabilityFatalErrors(t *testing.T) {
	events := []string{}
	fatal := make(chan error, 1)
	application := newApplication(http.NotFoundHandler(), []Lifecycle{fatalLifecycle{
		recordedLifecycle: recordedLifecycle{name: "analytics", events: &events}, fatal: fatal,
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	want := errors.New("analytical failure")
	fatal <- want
	select {
	case got := <-application.Fatal():
		if !errors.Is(got, want) {
			t.Fatalf("Fatal() = %v, want %v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("fatal error was not forwarded")
	}
}

func TestApplicationShutdownIsReverseOrderedAndIdempotent(t *testing.T) {
	events := []string{}
	application := newApplication(http.NotFoundHandler(), []Lifecycle{
		recordedLifecycle{name: "one", events: &events},
		recordedLifecycle{name: "two", events: &events},
	},
		func(context.Context) error { events = append(events, "cleanup:one"); return nil },
		func(context.Context) error { events = append(events, "cleanup:two"); return nil },
	)
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:one", "start:two", "stop:two", "stop:one", "cleanup:two", "cleanup:one"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestApplicationShutdownDuringStartupPreventsLaterComponents(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	stopCtxErr := make(chan error, 1)
	cleanupCtxErr := make(chan error, 1)
	var mu sync.Mutex
	events := []string{}
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	application := newApplication(http.NotFoundHandler(), []Lifecycle{
		recordedLifecycle{name: "one", events: &events, startErr: nil},
		LifecycleFunc{
			start: func(context.Context) error {
				record("start:two")
				close(started)
				<-release
				return nil
			},
			stop: func(ctx context.Context) error { record("stop:two"); stopCtxErr <- ctx.Err(); return nil },
		},
		LifecycleFunc{start: func(context.Context) error { record("start:three"); return nil }, stop: func(ctx context.Context) error { record("stop:three"); stopCtxErr <- ctx.Err(); return nil }},
	}, func(ctx context.Context) error { record("cleanup"); cleanupCtxErr <- ctx.Err(); return nil })
	startDone := make(chan error, 1)
	go func() { startDone <- application.Start(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("component two did not start")
	}
	shutdownDone := make(chan error, 1)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShutdown()
	go func() { shutdownDone <- application.Shutdown(shutdownCtx) }()
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not honor its context")
	}
	close(release)
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() after concurrent shutdown = %v, want canceled", err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-stopCtxErr; err != nil {
		t.Fatalf("cleanup stop context = %v, want nil", err)
	}
	if err := <-cleanupCtxErr; err != nil {
		t.Fatalf("cleanup context = %v, want nil", err)
	}
	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	want := []string{"start:one", "start:two", "stop:two", "stop:one", "cleanup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestApplicationShutdownReturnsTerminalErrorsAndRepeatsThem(t *testing.T) {
	stopErr := errors.New("stop failed")
	cleanupErr := errors.New("cleanup failed")
	application := newApplication(http.NotFoundHandler(), []Lifecycle{
		LifecycleFunc{start: func(context.Context) error { return nil }, stop: func(context.Context) error { return stopErr }},
	}, func(context.Context) error { return cleanupErr })
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, stopErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Shutdown() error = %v, want stop and cleanup errors", err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, stopErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("repeated Shutdown() error = %v, want same terminal errors", err)
	}
}

func TestApplicationShutdownDuringStartupReturnsTerminalErrors(t *testing.T) {
	stopErr := errors.New("stop failed")
	cleanupErr := errors.New("cleanup failed")
	started := make(chan struct{})
	release := make(chan struct{})
	application := newApplication(http.NotFoundHandler(), []Lifecycle{
		LifecycleFunc{
			start: func(context.Context) error { close(started); <-release; return nil },
			stop:  func(context.Context) error { return stopErr },
		},
	}, func(context.Context) error { return cleanupErr })
	startDone := make(chan error, 1)
	go func() { startDone <- application.Start(context.Background()) }()
	<-started
	shutdownDone := make(chan error, 1)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	go func() { shutdownDone <- application.Shutdown(shutdownCtx) }()
	if err := <-shutdownDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initial Shutdown() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := <-startDone; !errors.Is(err, stopErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Start() error = %v, want stop and cleanup errors", err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, stopErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Shutdown() error = %v, want terminal errors", err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, stopErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("repeated Shutdown() error = %v, want same terminal errors", err)
	}
}

func TestApplicationCanceledStartUsesBoundedCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopChecked := make(chan error, 1)
	cleanupChecked := make(chan error, 1)
	application := newApplication(http.NotFoundHandler(), []Lifecycle{
		LifecycleFunc{
			start: func(ctx context.Context) error { close(started); <-ctx.Done(); return nil },
			stop: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					stopChecked <- errors.New("stop context has no deadline")
					return nil
				}
				stopChecked <- ctx.Err()
				return nil
			},
		},
	}, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			cleanupChecked <- errors.New("cleanup context has no deadline")
			return nil
		}
		cleanupChecked <- ctx.Err()
		return nil
	})
	startDone := make(chan error, 1)
	go func() { startDone <- application.Start(ctx) }()
	<-started
	cancel()
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want canceled", err)
	}
	if err := <-stopChecked; err != nil {
		t.Fatalf("stop context error = %v, want live bounded context", err)
	}
	if err := <-cleanupChecked; err != nil {
		t.Fatalf("cleanup context error = %v, want live bounded context", err)
	}
}

type LifecycleFunc struct {
	start func(context.Context) error
	stop  func(context.Context) error
}

func (l LifecycleFunc) Start(ctx context.Context) error { return l.start(ctx) }
func (l LifecycleFunc) Stop(ctx context.Context) error  { return l.stop(ctx) }

func TestAssembleRuntimeRejectsCapabilityBuildFailure(t *testing.T) {
	store := testStore(t)
	options := testStoreOptions(store, assemblyConfig{

		DeploymentConfig: deploymentmodule.Config{
			Database: store.SQLDB(),
		},
	})

	_, err := assembleRuntimeChecked(context.Background(), fakeMetrics{}, options)
	if err == nil {
		t.Fatal("assembleRuntimeChecked accepted an incomplete deployment capability")
	}
}
