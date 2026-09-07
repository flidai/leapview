package releaseevents

import (
	"context"
	"errors"
	"testing"

	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/release/postgres"
)

func TestNewWithRepositoryPreservesEventRepositoryIdentity(t *testing.T) {
	events := eventspostgres.New()
	adapter := NewWithRepository(events)
	if !adapter.Matches(events) {
		t.Fatal("adapter did not retain the supplied platform event repository")
	}
	if adapter.Matches(eventspostgres.New()) {
		t.Fatal("adapter accepted a distinct platform event repository")
	}
	var nilAdapter *Adapter
	if nilAdapter.Matches(events) {
		t.Fatal("nil adapter matched a platform event repository")
	}
}

func TestAppendEventRejectsNonCanonicalExplicitEventID(t *testing.T) {
	_, err := NewWithRepository(eventspostgres.New()).AppendEvent(context.Background(), nil, postgres.EventInput{
		EventID: "not-a-uuid", ScopeID: "scope", AggregateType: "release",
		AggregateID: "release-1", EventType: "release.created", SchemaVersion: 1,
		Payload: []byte(`{}`),
	})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Fatalf("invalid event id error = %v, want release.ErrInvalid", err)
	}
}

func TestAppendEventRejectsWhitespaceExplicitEventID(t *testing.T) {
	_, err := NewWithRepository(eventspostgres.New()).AppendEvent(context.Background(), nil, postgres.EventInput{EventID: " 00000000-0000-7000-8000-000000000001 "})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Fatalf("whitespace event id error = %v, want release.ErrInvalid", err)
	}
}
