package deploymentworkflow

import (
	"context"
	"errors"
	"testing"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecordWorkflowFailsClosed(t *testing.T) {
	if err := New(nil).RecordWorkflow(context.Background(), nil, jobs.WorkflowIntent{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil authority error = %v, want deployment.ErrInvalid", err)
	}
	var adapter *Adapter
	if err := adapter.RecordWorkflow(context.Background(), nil, jobs.WorkflowIntent{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil adapter error = %v, want deployment.ErrInvalid", err)
	}
}

func TestRecordWorkflowUsesCallerTransactionAndReplay(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_workflow_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), jobspostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := New(jobspostgres.NewRepository(db))
	intent := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "delivery-publication-1", ResourceKind: "delivery_publication", ResourceID: "publication-1", EventType: "publication.created", Data: []byte(`{"status":"pending"}`)}}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordWorkflow(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `CREATE TEMP TABLE source_marker (id integer)`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM jobs.event`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back workflow rows = %d, want 0", count)
	}

	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordWorkflow(t.Context(), tx, intent); err != nil {
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
	if err := adapter.RecordWorkflow(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("exact replay = %v", err)
	}
	second := intent
	second.Event.Key = "delivery-publication-2"
	second.Event.EventType = "publication.updated"
	if err := adapter.RecordWorkflow(t.Context(), tx, second); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("append after exact replay = %v", err)
	}
	var maxEventID int64
	if err := tx.QueryRow(t.Context(), `SELECT count(*), max(event_id) FROM jobs.event`).Scan(&count, &maxEventID); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if count != 2 || maxEventID != 2 {
		_ = tx.Rollback(t.Context())
		t.Fatalf("event sequence after replay = count %d, max %d; want count 2, max 2", count, maxEventID)
	}
	changed := intent
	changed.Event.Data = []byte(`{"status":"changed"}`)
	if err := adapter.RecordWorkflow(t.Context(), tx, changed); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting replay error = %v, want deployment.ErrConflict", err)
	}
	_ = tx.Rollback(t.Context())
}
