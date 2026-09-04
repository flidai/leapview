package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/publication"
)

func TestPublicationStreamRegistryClosesStaleGenerationAndPublicID(t *testing.T) {
	registry := publication.NewMemoryStreamRegistry()
	ctx, unregister, err := registry.Register(context.Background(), "publication", "stream", publication.StreamVersion{
		PublicID: "public-old", ServingStateID: "state-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	registry.Reconcile(context.Background(), map[string]publication.StreamVersion{
		"publication": {PublicID: "public-new", ServingStateID: "state-new"},
	})
	select {
	case <-ctx.Done():
	default:
		t.Fatal("stale publication stream remained active")
	}
}

func TestPublicationStreamRegistryKeepsCurrentGeneration(t *testing.T) {
	registry := publication.NewMemoryStreamRegistry()
	version := publication.StreamVersion{PublicID: "public", ServingStateID: "state"}
	ctx, unregister, err := registry.Register(context.Background(), "publication", "stream", version)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	registry.Reconcile(context.Background(), map[string]publication.StreamVersion{"publication": version})
	select {
	case <-ctx.Done():
		t.Fatal("current publication stream was closed")
	default:
	}
}
