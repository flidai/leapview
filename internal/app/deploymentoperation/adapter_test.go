package deploymentoperation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOperationAdapterFailsClosed(t *testing.T) {
	input := deploymentmodule.NativeOperationAcquireInput{Scope: "target", OperationType: "deployment.create", IdempotencyKey: "key", RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OwnerID: "operator"}
	var adapter *Adapter
	if _, err := adapter.AcquireTx(context.Background(), nil, input); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil adapter error = %v, want deployment.ErrInvalid", err)
	}
	if _, err := New(nil).AcquireTx(context.Background(), nil, input); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil authority error = %v, want deployment.ErrInvalid", err)
	}
}

func TestOperationAdapterUsesCallerTransactionAndReplay(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_operation_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := New(operationpostgres.New(db))
	input := deploymentmodule.NativeOperationAcquireInput{Scope: "target", OperationType: "deployment.create", IdempotencyKey: "operation-1", RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OwnerID: "operator"}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := adapter.AcquireTx(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	operationUUID, parseErr := uuid.Parse(acquired.Operation.OperationID)
	if parseErr != nil || operationUUID.String() != acquired.Operation.OperationID || operationUUID.Version() != 7 {
		t.Fatalf("operation ID = %q, want canonical UUIDv7: %v", acquired.Operation.OperationID, parseErr)
	}
	if acquired.Status != deploymentmodule.NativeOperationAcquired || acquired.Operation.Scope != input.Scope || acquired.Operation.OperationType != input.OperationType || acquired.Operation.IdempotencyKey != input.IdempotencyKey || acquired.Operation.RequestDigest != input.RequestDigest || acquired.Operation.OwnerID != input.OwnerID || acquired.Lease.Scope != input.Scope || acquired.Lease.IdempotencyKey != input.IdempotencyKey || acquired.Lease.OperationID != acquired.Operation.OperationID || acquired.Lease.OwnerID != input.OwnerID || acquired.Lease.FencingGeneration <= 0 || acquired.Lease.LeaseExpiresAt.IsZero() {
		t.Fatalf("acquired projection = %+v", acquired)
	}
	outcome := json.RawMessage(`{"publicationId":"01900000-0000-7000-8000-000000000301"}`)
	if err := adapter.CompleteTx(t.Context(), tx, acquired.Lease, outcome); err != nil {
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
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back operation rows = %d, want 0", count)
	}

	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	acquired, err = adapter.AcquireTx(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := adapter.CompleteTx(t.Context(), tx, acquired.Lease, outcome); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	lookedUp, found, err := adapter.Lookup(t.Context(), input)
	if err != nil || !found || lookedUp.OperationID != acquired.Operation.OperationID || string(lookedUp.Outcome) != string(outcome) {
		t.Fatalf("operation lookup = %+v found=%v err=%v", lookedUp, found, err)
	}
	missing := input
	missing.IdempotencyKey = "missing"
	if _, found, err := adapter.Lookup(t.Context(), missing); err != nil || found {
		t.Fatalf("missing lookup found=%v err=%v", found, err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := adapter.AcquireTx(t.Context(), tx, input)
	if err != nil || replay.Status != deploymentmodule.NativeOperationReplay || replay.Operation.OperationID != acquired.Operation.OperationID || string(replay.Operation.Outcome) != string(outcome) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("replay projection = %+v, %v", replay, err)
	}
	changed := input
	changed.RequestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := adapter.AcquireTx(t.Context(), tx, changed); !errors.Is(err, deploymentmodule.ErrNativeOperationConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting replay error = %v, want native operation conflict", err)
	}
	_ = tx.Rollback(t.Context())
}
