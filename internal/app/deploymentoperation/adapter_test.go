package deploymentoperation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
	if _, err := New(nil).BeginAttemptTx(context.Background(), nil, deploymentmodule.NativeOperationBeginAttemptInput{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil authority begin attempt error = %v, want deployment.ErrInvalid", err)
	}
	if _, err := New(nil).RenewLeaseTx(context.Background(), nil, deploymentmodule.NativeOperationLease{}, time.Second); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil authority renew error = %v, want deployment.ErrInvalid", err)
	}
	if err := New(nil).FailTx(context.Background(), nil, deploymentmodule.NativeOperationLease{}, []byte(`{"ok":true}`)); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil authority fail error = %v, want deployment.ErrInvalid", err)
	}
	if err := New(nil).MarkIndeterminateTx(context.Background(), nil, deploymentmodule.NativeOperationLease{}, []byte(`{"ok":true}`)); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil authority indeterminate error = %v, want deployment.ErrInvalid", err)
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

func TestOperationAdapterBuildAttemptLifecycle(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_operation_build_attempt")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := New(operationpostgres.New(db))
	input := deploymentmodule.NativeOperationAcquireInput{Scope: "target", OperationType: "delivery.plan.build", IdempotencyKey: "build-1", RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OwnerID: "builder"}
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000501"

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := adapter.AcquireTx(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	bound, err := adapter.BeginAttemptTx(t.Context(), tx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: acquired.Lease, AttemptID: attemptID, AttemptIdentity: "native-build-1"})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if bound.AttemptID != attemptID || bound.AttemptIdentity != "native-build-1" || bound.Lease.AttemptID != attemptID || bound.Lease.AttemptIdentity != "native-build-1" || bound.Lease.OperationID != acquired.Operation.OperationID || bound.Lease.FencingGeneration != acquired.Lease.FencingGeneration {
		_ = tx.Rollback(t.Context())
		t.Fatalf("begin attempt projection = %+v", bound)
	}
	renewed, err := adapter.RenewLeaseTx(t.Context(), tx, bound.Lease, time.Minute)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if !renewed.LeaseExpiresAt.After(bound.Lease.LeaseExpiresAt) || renewed.AttemptID != attemptID || renewed.AttemptIdentity != "native-build-1" {
		_ = tx.Rollback(t.Context())
		t.Fatalf("renewed lease projection = %+v", renewed)
	}
	if err := adapter.FailTx(t.Context(), tx, renewed, []byte(` { "failure": "gate" } `)); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	looked, found, err := adapter.Lookup(t.Context(), input)
	if err != nil || !found {
		t.Fatalf("failed operation lookup = %+v found=%v err=%v", looked, found, err)
	}
	if looked.State != deploymentmodule.NativeOperationStateFailed || looked.AttemptID != attemptID || looked.AttemptIdentity != "native-build-1" || string(looked.Outcome) != `{"failure":"gate"}` {
		t.Fatalf("failed operation projection = %+v", looked)
	}

	replayTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replayInput := input
	replayInput.OwnerID = "retry-builder"
	replay, err := adapter.AcquireTx(t.Context(), replayTx, replayInput)
	if err != nil || replay.Status != deploymentmodule.NativeOperationReplay || replay.Operation.State != deploymentmodule.NativeOperationStateFailed || replay.Operation.AttemptID != attemptID || string(replay.Operation.Outcome) != `{"failure":"gate"}` {
		_ = replayTx.Rollback(t.Context())
		t.Fatalf("failed replay projection = %+v err=%v", replay, err)
	}
	_ = replayTx.Rollback(t.Context())

	validationTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RenewLeaseTx(t.Context(), validationTx, bound.Lease, 0); !errors.Is(err, deploymentmodule.ErrNativeOperationInvalid) {
		t.Fatalf("zero renewal duration error = %v, want native operation invalid", err)
	}
	if _, err := adapter.BeginAttemptTx(t.Context(), validationTx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: bound.Lease, AttemptID: "0198f2c0-7c7a-8f00-8a11-000000000501", AttemptIdentity: "bad"}); !errors.Is(err, deploymentmodule.ErrNativeOperationInvalid) {
		t.Fatalf("non-v7 attempt error = %v, want native operation invalid", err)
	}
	if err := adapter.FailTx(t.Context(), validationTx, bound.Lease, nil); !errors.Is(err, deploymentmodule.ErrNativeOperationInvalid) {
		t.Fatalf("empty failure outcome error = %v, want native operation invalid", err)
	}
	_ = validationTx.Rollback(t.Context())
}

func TestOperationAdapterIndeterminateProjectionAndEvidence(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_operation_indeterminate")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := New(operationpostgres.New(db))
	input := deploymentmodule.NativeOperationAcquireInput{Scope: "target", OperationType: "delivery.plan.build", IdempotencyKey: "build-indeterminate", RequestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", OwnerID: "builder"}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := adapter.AcquireTx(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	bound, err := adapter.BeginAttemptTx(t.Context(), tx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: acquired.Lease, AttemptID: "0198f2c0-7c7a-7f00-8a11-000000000502", AttemptIdentity: "external-writer-2"})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := adapter.MarkIndeterminateTx(t.Context(), tx, bound.Lease, []byte(` { "writer": "unknown" } `)); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	looked, found, err := adapter.Lookup(t.Context(), input)
	if err != nil || !found || looked.State != deploymentmodule.NativeOperationStateIndeterminate || looked.AttemptID != bound.AttemptID || looked.AttemptIdentity != bound.AttemptIdentity || string(looked.AttemptEvidence) != `{"writer":"unknown"}` {
		t.Fatalf("indeterminate projection = %+v found=%v err=%v", looked, found, err)
	}
	validationTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.MarkIndeterminateTx(t.Context(), validationTx, deploymentmodule.NativeOperationLease{Scope: bound.Lease.Scope, IdempotencyKey: bound.Lease.IdempotencyKey, OperationID: bound.Lease.OperationID, OwnerID: bound.Lease.OwnerID, FencingGeneration: bound.Lease.FencingGeneration, LeaseExpiresAt: bound.Lease.LeaseExpiresAt}, []byte(`{"writer":"unknown"}`)); !errors.Is(err, deploymentmodule.ErrNativeOperationInvalid) {
		t.Fatalf("missing attempt identity error = %v, want native operation invalid", err)
	}
	if err := adapter.MarkIndeterminateTx(t.Context(), validationTx, bound.Lease, []byte(`{}`)); !errors.Is(err, deploymentmodule.ErrNativeOperationInvalid) {
		t.Fatalf("empty indeterminate evidence error = %v, want native operation invalid", err)
	}
	_ = validationTx.Rollback(t.Context())
}

func TestValidateAcquireResultRejectsOperationLeaseAttemptDrift(t *testing.T) {
	now := time.Now().UTC()
	input := deploymentmodule.NativeOperationAcquireInput{
		Scope: "target", OperationType: "delivery.plan.build", IdempotencyKey: "attempt-drift",
		RequestDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", OwnerID: "builder",
	}
	result := operationpostgres.AcquireResult{
		Status: operationpostgres.StatusAcquired,
		Operation: operationpostgres.Operation{
			Scope: input.Scope, OperationType: input.OperationType, IdempotencyKey: input.IdempotencyKey,
			RequestDigest: input.RequestDigest, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000510",
			State: operationpostgres.StatePending, OwnerID: input.OwnerID, FencingGeneration: 1,
			LeaseExpiresAt: now.Add(time.Minute), AttemptID: "0198f2c0-7c7a-7f00-8a11-000000000511", AttemptIdentity: "attempt-one",
		},
		Lease: operationpostgres.Lease{
			Scope: input.Scope, IdempotencyKey: input.IdempotencyKey, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000510",
			OwnerID: input.OwnerID, FencingGeneration: 1, LeaseExpiresAt: now.Add(time.Minute),
			AttemptID: "0198f2c0-7c7a-7f00-8a11-000000000512", AttemptIdentity: "attempt-two",
		},
	}
	if err := validateAcquireResult(result, deploymentmodule.NativeOperationAcquired, input); !errors.Is(err, deploymentmodule.ErrNativeOperationConflict) {
		t.Fatalf("attempt drift error = %v, want native operation conflict", err)
	}
}
