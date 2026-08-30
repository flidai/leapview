package dashboardpublicationevents

import (
	"context"
	"errors"
	"testing"

	publication "github.com/flidai/leapview/internal/dashboard/publication"
	publicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type validationTx struct{ pgx.Tx }

func TestAppendEventRejectsInvalidIdentityBeforeStorage(t *testing.T) {
	_, err := New().AppendEvent(context.Background(), &validationTx{}, publicationpostgres.EventInput{EventID: "not-a-uuid"})
	if !errors.Is(err, publication.ErrConflict) {
		t.Fatalf("invalid event id error = %v, want publication conflict", err)
	}
}

func TestAppendEventMapsCanonicalPlatformEvent(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "dashboard_publication_events_adapter")
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
	input := publicationpostgres.EventInput{EventID: "01900000-0000-7000-8000-000000000021", ProjectID: "project:events", PublicationID: "publication-events", ActorID: "actor", CorrelationID: "01900000-0000-7000-8000-000000000022", Type: "dashboard_publication.configured", ServingStateID: "generation-events", Revision: 1, Payload: []byte(`{"name":"website"}`)}
	got, err := New().AppendEvent(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if got.EventID != input.EventID || got.ProjectID != input.ProjectID || got.PublicationID != input.PublicationID || got.ActorID != input.ActorID || got.CorrelationID != input.CorrelationID || got.Type != input.Type || got.ServingStateID != input.ServingStateID || got.Revision != input.Revision || got.AggregateVersion != 1 {
		_ = tx.Rollback(t.Context())
		t.Fatalf("mapped publication event = %+v", got)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAppendEventFailsClosedWithoutAdapter(t *testing.T) {
	var adapter *Adapter
	_, err := adapter.AppendEvent(context.Background(), nil, publicationpostgres.EventInput{EventID: "01900000-0000-7000-8000-000000000001"})
	if err == nil {
		t.Fatal("nil adapter unexpectedly accepted")
	}
}
