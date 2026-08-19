package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCleanupStackClosesEveryConstructionStageInReverseOnce(t *testing.T) {
	stack := &cleanupStack{}
	events := []string{}
	for _, name := range []string{"sqlite", "analytics", "workload", "runtime-host"} {
		name := name
		stack.Push(name, func(context.Context) error {
			events = append(events, name)
			return nil
		})
	}
	if err := stack.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"runtime-host", "workload", "analytics", "sqlite"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup order = %v, want %v", events, want)
	}
}

func TestCleanupStackContinuesAfterCancellationAndErrors(t *testing.T) {
	stack := &cleanupStack{}
	events := []string{}
	stack.Push("sqlite", func(ctx context.Context) error {
		events = append(events, "sqlite")
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("cleanup context error = %v, want canceled", ctx.Err())
		}
		return nil
	})
	wantErr := errors.New("cache close failed")
	stack.Push("analytics", func(context.Context) error {
		events = append(events, "analytics")
		return wantErr
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := stack.Close(ctx)
	if !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error = %v, want %v", err, wantErr)
	}
	if want := []string{"analytics", "sqlite"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
}

type orderedWorkload struct{ events *[]string }

func (w orderedWorkload) Close() { *w.events = append(*w.events, "workload") }
func (w orderedWorkload) Drain(context.Context) error {
	w.Close()
	return nil
}

type orderedDrainingWorkload struct{ events *[]string }

func (w orderedDrainingWorkload) Close() { *w.events = append(*w.events, "workload:close") }
func (w orderedDrainingWorkload) Drain(context.Context) error {
	*w.events = append(*w.events, "workload:drain")
	return nil
}

func TestRuntimeLifecycleStopsWorkersBeforeAdmission(t *testing.T) {
	events := []string{}
	workers := recordedLifecycle{name: "workers", events: &events}
	lifecycle := newRuntimeLifecycle(workers, nil, orderedWorkload{events: &events})
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start:workers", "stop:workers", "workload"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestRuntimeLifecycleDrainsAdmissionAfterWorkers(t *testing.T) {
	events := []string{}
	workers := recordedLifecycle{name: "workers", events: &events}
	lifecycle := newRuntimeLifecycle(workers, nil, orderedDrainingWorkload{events: &events})
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start:workers", "stop:workers", "workload:drain"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}
