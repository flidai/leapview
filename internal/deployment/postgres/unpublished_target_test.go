package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWithUnpublishedTargetRunsCallbackAndRollsBackOnDenial(t *testing.T) {
	db := deliveryTestDB(t)
	repository := New(db)
	ctx := t.Context()
	const ephemeralTargetID, ephemeralProjectID = "target_first_grant_ephemeral", "project_first_grant_ephemeral"
	ephemeralCalled := false
	if err := repository.WithUnpublishedTarget(ctx, ephemeralTargetID, ephemeralProjectID, "prod", func(context.Context) error {
		ephemeralCalled = true
		return nil
	}); err != nil {
		t.Fatalf("unpublished target callback before first plan: %v", err)
	}
	if !ephemeralCalled {
		t.Fatal("callback did not run for an absent, exactly scoped target")
	}
	if _, err := repository.Target(ctx, ephemeralTargetID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("temporary target survived fence rollback: %v", err)
	}

	const targetID, projectID, environment = "target_first_grant", "project_first_grant", "prod"
	if _, err := repository.CreateTarget(ctx, TargetInput{TargetID: targetID, ProjectID: projectID, Environment: environment}); err != nil {
		t.Fatal(err)
	}

	if err := repository.WithUnpublishedTarget(ctx, targetID, projectID, environment, func(callbackCtx context.Context) error {
		if callbackCtx != ctx {
			t.Fatal("callback context was not forwarded")
		}
		return nil
	}); err != nil {
		t.Fatalf("unpublished target callback: %v", err)
	}

	denial := errors.New("access policy denied first grant")
	if err := repository.WithUnpublishedTarget(ctx, targetID, projectID, environment, func(context.Context) error {
		return denial
	}); !errors.Is(err, denial) {
		t.Fatalf("callback error = %v, want %v", err, denial)
	}

	// The callback's denial must not leave the delivery row locked.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var lockedTarget string
	if err := tx.QueryRow(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE NOWAIT`, targetID).Scan(&lockedTarget); err != nil {
		t.Fatalf("target remained locked after callback denial: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	called := false
	if err := repository.WithUnpublishedTarget(ctx, targetID, "other_project", environment, func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched target scope error = %v, want ErrConflict", err)
	}
	if called {
		t.Fatal("callback ran for a mismatched target scope")
	}
	if err := repository.WithUnpublishedTarget(ctx, targetID, projectID, environment, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil callback error = %v, want ErrInvalid", err)
	}
	if err := repository.WithUnpublishedTarget(nil, targetID, projectID, environment, func(context.Context) error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil context error = %v, want ErrInvalid", err)
	}
}

func TestWithUnpublishedTargetSerializesWithActivationTargetLock(t *testing.T) {
	db := deliveryTestDB(t)
	repository := New(db)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const targetID, projectID = "target_first_grant_lock", "project_first_grant_lock"
	if _, err := repository.CreateTarget(ctx, TargetInput{TargetID: targetID, ProjectID: projectID, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCallback()
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- repository.WithUnpublishedTarget(ctx, targetID, projectID, "prod", func(context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatalf("fenced callback did not start: %v", ctx.Err())
	}

	// The weaker mutation lock remains compatible with the key-share lock used
	// by candidate and generation foreign-key checks.
	fkCheck, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var referencedTarget string
	if err := fkCheck.QueryRow(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR KEY SHARE NOWAIT`, targetID).Scan(&referencedTarget); err != nil {
		_ = fkCheck.Rollback(ctx)
		t.Fatalf("target foreign-key key-share lock was blocked: %v", err)
	}
	if err := fkCheck.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	contender, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Rollback(ctx)
	lockAttempt := make(chan error, 1)
	go func() {
		var lockedTarget string
		lockAttempt <- contender.QueryRow(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE NOWAIT`, targetID).Scan(&lockedTarget)
	}()
	select {
	case err := <-lockAttempt:
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("activation-style target lock error = %v, want lock_not_available (55P03)", err)
		}
	case <-ctx.Done():
		t.Fatalf("activation-style lock attempt stalled: %v", ctx.Err())
	}
	if err := contender.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	releaseCallback()
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatalf("fenced callback: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("fenced callback did not release target lock: %v", ctx.Err())
	}

	contender, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Rollback(ctx)
	if err := contender.QueryRow(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE NOWAIT`, targetID).Scan(new(string)); err != nil {
		t.Fatalf("target lock remained held after callback returned: %v", err)
	}
}

func TestWithUnpublishedTargetRejectsAlreadyActiveTarget(t *testing.T) {
	db := deliveryTestDB(t)
	lineage := &testActivationLineage{}
	repository := NewWithOptions(db, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: lineage})
	activation, ids := prepareLostAckActivation(t, repository)
	lineage.expected = ActivationLineageInput{TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: ids.generation, CompiledGraphDigest: testDigest('b')}
	seedPhysicalRetentionFixture(t, db, ids.seal)
	if _, err := repository.Activate(t.Context(), activation); err != nil {
		t.Fatalf("activate target: %v", err)
	}

	called := false
	err := repository.WithUnpublishedTarget(t.Context(), ids.target, "project_lost_ack", "prod", func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("already-active target error = %v, want ErrAlreadyActive", err)
	}
	if called {
		t.Fatal("callback ran for an active target")
	}
}
