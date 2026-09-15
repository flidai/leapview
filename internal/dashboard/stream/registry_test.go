package stream

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryExpiresOrphanCoordinatorCreatedByCommand(t *testing.T) {
	registry := NewRegistryWithTTL(10 * time.Millisecond)
	coordinator := registry.Ensure("client:page:instance", context.Background(), func(RefreshEvent) {})
	canceled := make(chan struct{})
	if _, err := coordinator.Begin(nil, func(ctx context.Context, _ RefreshPublisher) {
		<-ctx.Done()
		close(canceled)
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("orphan coordinator was not canceled at TTL")
	}
	if _, ok := registry.Get("client:page:instance"); ok {
		t.Fatal("expired orphan remains registered")
	}
}

func TestRegistryRefreshSemanticModelTargetsOnlyMatchingStreams(t *testing.T) {
	registry := NewRegistry()
	defer registry.Close()
	ctx := context.Background()
	for _, streamID := range []string{"sales-a", "sales-b", "support"} {
		registry.Ensure(streamID, ctx, func(RefreshEvent) {})
	}
	refreshed := map[string]int{}
	registry.Bind("sales-a", "sales", "prod", "orders", func() { refreshed["sales-a"]++ })
	registry.Bind("sales-b", "sales", "prod", "customers", func() { refreshed["sales-b"]++ })
	registry.Bind("support", "support", "prod", "orders", func() { refreshed["support"]++ })
	registry.Ensure("sales-dev", ctx, func(RefreshEvent) {})
	registry.Bind("sales-dev", "sales", "dev", "orders", func() { refreshed["sales-dev"]++ })

	streams := registry.RefreshSemanticModel("sales", "prod", "orders")
	if len(streams) != 1 || streams[0] != "sales-a" {
		t.Fatalf("refreshed streams = %#v, want sales-a", streams)
	}
	if refreshed["sales-a"] != 1 || refreshed["sales-b"] != 0 || refreshed["support"] != 0 || refreshed["sales-dev"] != 0 {
		t.Fatalf("refresh callbacks = %#v", refreshed)
	}
}

func TestRegistryRefreshSemanticModelRetainsPublicationScope(t *testing.T) {
	registry := NewRegistry()
	defer registry.Close()
	ctx := context.Background()
	for _, streamID := range []string{"public-a", "public-b", "private"} {
		registry.Ensure(streamID, ctx, func(RefreshEvent) {})
	}
	refreshed := map[string]int{}
	registry.BindForPublication("public-a", "sales", "prod", "orders", "publication-a", func() { refreshed["public-a"]++ })
	registry.BindForPublication("public-b", "sales", "prod", "orders", "publication-b", func() { refreshed["public-b"]++ })
	registry.Bind("private", "sales", "prod", "orders", func() { refreshed["private"]++ })

	targets := registry.RefreshSemanticModelTargets("sales", "prod", "orders")
	if len(targets) != 3 || targets[0].StreamID != "private" || targets[1].Publication != "publication-a" || targets[2].Publication != "publication-b" {
		t.Fatalf("refresh targets = %#v, want private plus publication-a/publication-b scopes", targets)
	}
	if refreshed["public-a"] != 1 || refreshed["public-b"] != 1 || refreshed["private"] != 1 {
		t.Fatalf("refresh callbacks = %#v", refreshed)
	}
}

func TestRegistryRejectsWhenCapacityContainsOnlyActiveStreams(t *testing.T) {
	registry := NewRegistryWithLimits(time.Minute, 2)
	defer registry.Close()
	ctx := context.Background()
	_, closeA := registry.Open("stream-a", ctx, func(RefreshEvent) {})
	defer closeA()
	_, closeB := registry.Open("stream-b", ctx, func(RefreshEvent) {})
	defer closeB()

	if _, err := registry.EnsureWithError("forged", ctx, func(RefreshEvent) {}); !errors.Is(err, ErrRegistryCapacity) {
		t.Fatalf("capacity error = %v, want ErrRegistryCapacity", err)
	}
	if _, _, err := registry.OpenWithError("another-live", ctx, func(RefreshEvent) {}); !errors.Is(err, ErrRegistryCapacity) {
		t.Fatalf("live capacity error = %v, want ErrRegistryCapacity", err)
	}
	if registry.Len() != 2 {
		t.Fatalf("registry length = %d, want bounded length 2", registry.Len())
	}
}

func TestRegistryEvictsOldestOrphanDeterministically(t *testing.T) {
	registry := NewRegistryWithLimits(time.Minute, 2)
	defer registry.Close()
	ctx := context.Background()
	first := registry.Ensure("first", ctx, func(RefreshEvent) {})
	registry.Ensure("second", ctx, func(RefreshEvent) {})
	if first == nil {
		t.Fatal("first orphan was not admitted")
	}
	if next := registry.Ensure("third", ctx, func(RefreshEvent) {}); next == nil {
		t.Fatal("third orphan was not admitted after deterministic eviction")
	}
	if _, ok := registry.Get("first"); ok {
		t.Fatal("oldest orphan remained after capacity eviction")
	}
	if _, ok := registry.Get("second"); !ok {
		t.Fatal("newer orphan was evicted instead of oldest")
	}
}

func TestRegistryOpenBelowCapacityRetainsExistingOrphan(t *testing.T) {
	registry := NewRegistryWithLimits(time.Minute, 3)
	defer registry.Close()
	ctx := context.Background()
	registry.Ensure("orphan", ctx, func(RefreshEvent) {})
	if _, _, err := registry.OpenWithError("live", ctx, func(RefreshEvent) {}); err != nil {
		t.Fatalf("open below capacity: %v", err)
	}
	if _, ok := registry.Get("orphan"); !ok {
		t.Fatal("open below capacity evicted an existing orphan")
	}
}
