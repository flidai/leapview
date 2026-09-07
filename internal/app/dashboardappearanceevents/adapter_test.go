package dashboardappearanceevents

import (
	"context"
	"errors"
	"testing"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
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
	_, err := NewWithRepository(eventspostgres.New()).AppendEvent(context.Background(), &validationTx{}, appearancepostgres.EventInput{EventID: "not-a-uuid"})
	if !errors.Is(err, appearancepostgres.ErrConflict) {
		t.Fatalf("invalid event id error = %v, want appearance conflict", err)
	}
}

func TestAppendEventMapsCanonicalPlatformEvent(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "dashboard_appearance_events_adapter")
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
	color := "#fff"
	input := appearancepostgres.EventInput{EventID: "01900000-0000-7000-8000-000000000031", ProjectID: "project:events", DashboardID: "dashboard:events", ActorID: "actor", Revision: 1, Patch: dashboardappearance.Patch{Color: &color}}
	got, err := NewWithRepository(eventspostgres.New()).AppendEvent(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if got.EventID != input.EventID || got.ProjectID != input.ProjectID || got.DashboardID != input.DashboardID || got.ActorID != input.ActorID || got.Revision != input.Revision || got.AggregateVersion != 1 || got.Patch.Color == nil || *got.Patch.Color != color {
		_ = tx.Rollback(t.Context())
		t.Fatalf("mapped appearance event = %+v", got)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAppendEventFailsClosedWithoutAdapter(t *testing.T) {
	var adapter *Adapter
	_, err := adapter.AppendEvent(context.Background(), nil, appearancepostgres.EventInput{EventID: "01900000-0000-7000-8000-000000000001"})
	if err == nil {
		t.Fatal("nil adapter unexpectedly accepted")
	}
}
