package releaseevents

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/release/postgres"
)

func TestAppendEventRejectsNonCanonicalExplicitEventID(t *testing.T) {
	_, err := New().AppendEvent(context.Background(), nil, postgres.EventInput{
		EventID: "not-a-uuid", ScopeID: "scope", AggregateType: "release",
		AggregateID: "release-1", EventType: "release.created", SchemaVersion: 1,
		Payload: []byte(`{}`),
	})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Fatalf("invalid event id error = %v, want release.ErrInvalid", err)
	}
}

func TestAppendEventRejectsWhitespaceExplicitEventID(t *testing.T) {
	_, err := New().AppendEvent(context.Background(), nil, postgres.EventInput{EventID: " 00000000-0000-7000-8000-000000000001 "})
	if !errors.Is(err, postgres.ErrInvalid) {
		t.Fatalf("whitespace event id error = %v, want release.ErrInvalid", err)
	}
}
