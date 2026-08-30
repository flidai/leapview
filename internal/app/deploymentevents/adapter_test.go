package deploymentevents

import (
	"context"
	"errors"
	"testing"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppendDeliveryEventFailsClosed(t *testing.T) {
	var adapter *Adapter
	if _, err := adapter.AppendDeliveryEvent(context.Background(), nil, deploymentmodule.NativeDeliveryEventInput{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil adapter error = %v, want deployment.ErrInvalid", err)
	}
	if _, err := New().AppendDeliveryEvent(context.Background(), nil, deploymentmodule.NativeDeliveryEventInput{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil transaction error = %v, want deployment.ErrInvalid", err)
	}
	valid := deploymentmodule.NativeDeliveryEventInput{EventID: "01900000-0000-7000-8000-000000000101", CorrelationID: "01900000-0000-7000-8000-000000000102"}
	for name, mutate := range map[string]func(*deploymentmodule.NativeDeliveryEventInput){
		"event is not UUIDv7": func(in *deploymentmodule.NativeDeliveryEventInput) {
			in.EventID = "01900000-0000-4000-8000-000000000101"
		},
		"correlation is empty": func(in *deploymentmodule.NativeDeliveryEventInput) { in.CorrelationID = "" },
		"correlation is not UUIDv7": func(in *deploymentmodule.NativeDeliveryEventInput) {
			in.CorrelationID = "01900000-0000-4000-8000-000000000102"
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if _, err := New().AppendDeliveryEvent(context.Background(), nil, input); !errors.Is(err, deploymentpostgres.ErrInvalid) {
				t.Fatalf("invalid identity error = %v, want deployment.ErrInvalid", err)
			}
		})
	}
}

func TestAppendDeliveryEventUsesCallerTransactionAndReplay(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_event_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := New()
	input := deploymentmodule.NativeDeliveryEventInput{
		EventID: "01900000-0000-7000-8000-000000000101", ScopeID: "target",
		AggregateType: "delivery_publication", AggregateID: "publication-1",
		EventType: "publication_created", SchemaVersion: 1,
		CorrelationID: "01900000-0000-7000-8000-000000000102", Payload: []byte(`{"generation_id":"generation-1"}`),
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := adapter.AppendDeliveryEvent(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if stored.EventID != input.EventID || stored.AggregateVersion != 1 || stored.ScopeID != input.ScopeID || stored.AggregateType != input.AggregateType || stored.AggregateID != input.AggregateID || stored.EventType != input.EventType || stored.SchemaVersion != input.SchemaVersion || stored.CorrelationID != input.CorrelationID {
		t.Fatalf("stored event projection = %+v", stored)
	}
	if _, err := tx.Exec(t.Context(), `CREATE TEMP TABLE source_marker (id integer)`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back event rows = %d, want 0", count)
	}

	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.AppendDeliveryEvent(t.Context(), tx, input); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := adapter.AppendDeliveryEvent(t.Context(), tx, input)
	if err != nil || replay.AggregateVersion != 1 || replay.EventID != input.EventID {
		_ = tx.Rollback(t.Context())
		t.Fatalf("exact replay = %+v, %v", replay, err)
	}
	changed := input
	changed.Payload = []byte(`{"generation_id":"different"}`)
	if _, err := adapter.AppendDeliveryEvent(t.Context(), tx, changed); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting replay error = %v, want deployment.ErrConflict", err)
	}
	_ = tx.Rollback(t.Context())
}
