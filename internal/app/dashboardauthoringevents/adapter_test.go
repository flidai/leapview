package dashboardauthoringevents

import (
	"context"
	"errors"
	"testing"

	authoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type validationTx struct{ pgx.Tx }

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

func TestAppendEventRejectsInvalidIdentityBeforeStorage(t *testing.T) {
	_, err := New().AppendEvent(context.Background(), &validationTx{}, authoringpostgres.EventInput{EventID: "not-a-uuid"})
	if !errors.Is(err, authoring.ErrConflict) {
		t.Fatalf("invalid event id error = %v, want authoring conflict", err)
	}
}

func TestCanonicalCorrelationIDOmitsOpaqueAuditIdentity(t *testing.T) {
	if got := eventspostgres.CanonicalCorrelationID("corr-client-1"); got != "" {
		t.Fatalf("opaque correlation projected to event = %q, want empty", got)
	}
	if got := eventspostgres.CanonicalCorrelationID("01900000-0000-7000-8000-000000000012"); got != "01900000-0000-7000-8000-000000000012" {
		t.Fatalf("canonical UUID correlation projected as %q", got)
	}
}

func TestAppendEventMapsCanonicalPlatformEvent(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "dashboard_authoring_events_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := authoringpostgres.EventInput{EventID: "01900000-0000-7000-8000-000000000011", ProjectID: "project:events", DashboardID: "dashboard:events", ActorID: "actor", CorrelationID: "01900000-0000-7000-8000-000000000012", Revision: 2, Type: "dashboard_authoring.draft_created", Payload: []byte(`{"revision":2}`)}
	got, err := New().AppendEvent(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if got.EventID != input.EventID || got.ProjectID != input.ProjectID || got.DashboardID != input.DashboardID || got.ActorID != input.ActorID || got.Revision != input.Revision || got.Type != input.Type || got.AggregateVersion != 1 {
		_ = tx.Rollback(t.Context())
		t.Fatalf("mapped authoring event = %+v", got)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAppendEventFailsClosedWithoutAdapter(t *testing.T) {
	var adapter *Adapter
	_, err := adapter.AppendEvent(context.Background(), nil, authoringpostgres.EventInput{EventID: "01900000-0000-7000-8000-000000000001"})
	if err == nil {
		t.Fatal("nil adapter unexpectedly accepted")
	}
}
