package postgres

import (
	"context"
	"errors"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errActivationSavepointAuditAfterAppend = errors.New("injected failure after activation audit append")

type activationSavepointFailingAudit struct {
	delegate ActivationAuditPort
}

func (audit activationSavepointFailingAudit) AppendActivationAudit(ctx context.Context, tx Tx, input ActivationAuditInput) (AuditEvent, error) {
	if _, err := audit.delegate.AppendActivationAudit(ctx, tx, input); err != nil {
		return AuditEvent{}, err
	}
	return AuditEvent{}, errActivationSavepointAuditAfterAppend
}

func (audit activationSavepointFailingAudit) GetActivationAudit(ctx context.Context, tx Tx, input ActivationAuditInput) (AuditEvent, error) {
	return audit.delegate.GetActivationAudit(ctx, tx, input)
}

func activationSavepointFixture(t *testing.T, audit ActivationAuditPort) (*pgxpool.Pool, *Repository, ActivationInput, lostAckActivationIDs) {
	t.Helper()
	db := deliveryTestDB(t)
	if _, err := db.Exec(t.Context(), `CREATE TABLE activation_savepoint_probe (marker_id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	lineage := &testActivationLineage{}
	repository := NewWithOptions(db, Options{
		ActivationAdmission: func(ctx context.Context, tx Tx, publication DeliveryPublication) error {
			if publication.State != "pending" {
				t.Errorf("admission publication state = %q, want pending", publication.State)
			}
			_, err := tx.Exec(ctx, `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('credential-publication')`)
			return err
		},
		ActivationAudit: audit,
		Lineage:         lineage,
	})
	input, ids := prepareLostAckActivation(t, repository)
	lineage.expected = ActivationLineageInput{
		TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: ids.generation,
		CompiledGraphDigest: testDigest('b'),
	}
	seedPhysicalRetentionFixture(t, db, ids.seal)
	return db, repository, input, ids
}

func assertActivationSavepointUnchanged(t *testing.T, db DBTX, repository *Repository, ids lostAckActivationIDs) {
	t.Helper()
	target, err := repository.Target(t.Context(), ids.target)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 1 || target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
		t.Fatalf("target changed after rolled-back activation: %#v", target)
	}
	publication, err := repository.Publication(t.Context(), ids.publication)
	if err != nil {
		t.Fatal(err)
	}
	if publication.State != "pending" || publication.ResultTargetRevision != 0 {
		t.Fatalf("publication changed after rolled-back activation: %#v", publication)
	}
	var roots, events, audits, credentialWrites, liveSnapshots int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_retention_root WHERE target_id=$1 AND generation_id=$2::uuid AND root_kind='generation'`, ids.target, ids.generation).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, ids.publication).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, ids.publication).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM activation_savepoint_probe WHERE marker_id='credential-publication'`).Scan(&credentialWrites); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state='live'`, ids.seal.PhysicalPoolID, ids.seal.CatalogID, ids.seal.DuckLakeSnapshotID).Scan(&liveSnapshots); err != nil {
		t.Fatal(err)
	}
	if roots != 0 || events != 0 || audits != 0 || credentialWrites != 0 || liveSnapshots != 1 {
		t.Fatalf("partial activation survived rollback: generation roots=%d events=%d audits=%d credential writes=%d live snapshots=%d", roots, events, audits, credentialWrites, liveSnapshots)
	}
}

func TestActivationSavepointLateFailureDoesNotSurviveCallerCommit(t *testing.T) {
	for _, path := range []string{"ActivateTx", "ActivateTxWithPreCommitHook"} {
		t.Run(path, func(t *testing.T) {
			db, repository, input, ids := activationSavepointFixture(t, activationSavepointFailingAudit{
				delegate: testActivationAudit{audit: accesspostgres.New()},
			})
			tx, err := db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('unrelated-outer-write')`); err != nil {
				_ = tx.Rollback(context.Background())
				t.Fatal(err)
			}
			var activationErr error
			if path == "ActivateTx" {
				_, activationErr = repository.ActivateTx(t.Context(), tx, input)
			} else {
				_, activationErr = repository.ActivateTxWithPreCommitHook(t.Context(), tx, input, func(context.Context, Tx, DeliveryPublication) error { return nil })
			}
			if !errors.Is(activationErr, errActivationSavepointAuditAfterAppend) {
				_ = tx.Rollback(context.Background())
				t.Fatalf("activation error = %v, want post-append injected error", activationErr)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatalf("caller commit after activation error: %v", err)
			}
			var unrelated, credentialWrites int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM activation_savepoint_probe WHERE marker_id='unrelated-outer-write'`).Scan(&unrelated); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM activation_savepoint_probe WHERE marker_id='credential-publication'`).Scan(&credentialWrites); err != nil {
				t.Fatal(err)
			}
			if unrelated != 1 || credentialWrites != 0 {
				t.Fatalf("outer/activation marker writes = %d/%d, want 1/0", unrelated, credentialWrites)
			}
			assertActivationSavepointUnchanged(t, db, repository, ids)
		})
	}
}

func TestActivationSavepointCancellationUsesLiveCleanupContext(t *testing.T) {
	db, repository, input, ids := activationSavepointFixture(t, testActivationAudit{audit: accesspostgres.New()})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repository.admission = func(ctx context.Context, tx Tx, _ DeliveryPublication) error {
		if _, err := tx.Exec(ctx, `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('credential-publication')`); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('unrelated-outer-write')`); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if _, err := repository.ActivateTx(ctx, tx, input); !errors.Is(err, context.Canceled) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("activation after cancellation = %v, want context.Canceled", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("caller commit after canceled activation: %v", err)
	}
	var unrelated int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM activation_savepoint_probe WHERE marker_id='unrelated-outer-write'`).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	if unrelated != 1 {
		t.Fatalf("unrelated outer write count = %d, want 1", unrelated)
	}
	assertActivationSavepointUnchanged(t, db, repository, ids)
}

func TestActivationSavepointSQLFailureRestoresCallerTransaction(t *testing.T) {
	db, repository, input, ids := activationSavepointFixture(t, testActivationAudit{audit: accesspostgres.New()})
	repository.admission = func(ctx context.Context, tx Tx, _ DeliveryPublication) error {
		if _, err := tx.Exec(ctx, `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('credential-publication')`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('unrelated-outer-write')`)
		return err
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO activation_savepoint_probe(marker_id) VALUES ('unrelated-outer-write')`); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	_, activationErr := repository.ActivateTx(t.Context(), tx, input)
	var postgresErr *pgconn.PgError
	if !errors.As(activationErr, &postgresErr) || postgresErr.Code != "23505" {
		_ = tx.Rollback(context.Background())
		t.Fatalf("activation callback error = %v, want unique violation", activationErr)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("caller commit after SQL callback failure: %v", err)
	}
	var unrelated int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM activation_savepoint_probe WHERE marker_id='unrelated-outer-write'`).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	if unrelated != 1 {
		t.Fatalf("unrelated outer write count = %d, want 1", unrelated)
	}
	assertActivationSavepointUnchanged(t, db, repository, ids)
}

type activationSavepointReleaseFailureTx struct {
	pgx.Tx
	releaseContext context.Context
}

func (tx *activationSavepointReleaseFailureTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &activationSavepointNestedReleaseFailureTx{Tx: nested, releaseContext: tx.releaseContext}, nil
}

type activationSavepointNestedReleaseFailureTx struct {
	pgx.Tx
	releaseContext context.Context
}

func (tx *activationSavepointNestedReleaseFailureTx) Commit(context.Context) error {
	return tx.Tx.Commit(tx.releaseContext)
}

func TestActivationSavepointReleaseFailureAbortsCallerTransaction(t *testing.T) {
	db, repository, input, ids := activationSavepointFixture(t, testActivationAudit{audit: accesspostgres.New()})
	releaseContext, cancel := context.WithCancel(t.Context())
	cancel()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &activationSavepointReleaseFailureTx{Tx: tx, releaseContext: releaseContext}
	if _, err := repository.ActivateTx(t.Context(), wrapped, input); !errors.Is(err, context.Canceled) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("activation savepoint release error = %v, want context.Canceled", err)
	}
	if err := wrapped.Commit(t.Context()); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("outer caller commit after failed savepoint release = %v, want closed transaction", err)
	}
	assertActivationSavepointUnchanged(t, db, repository, ids)
}

func TestActivationSavepointSuccessRetainsLocksUntilOuterTransactionEnds(t *testing.T) {
	db, repository, input, ids := activationSavepointFixture(t, testActivationAudit{audit: accesspostgres.New()})
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateTx(t.Context(), tx, input); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("activate in caller transaction: %v", err)
	}
	var markerCount int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM activation_savepoint_probe WHERE marker_id='credential-publication'`).Scan(&markerCount); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if markerCount != 1 {
		_ = tx.Rollback(context.Background())
		t.Fatalf("admission marker visible in outer transaction = %d, want 1", markerCount)
	}
	contender, err := db.Begin(t.Context())
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	_, lockErr := contender.Exec(t.Context(), `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE NOWAIT`, ids.target)
	_ = contender.Rollback(context.Background())
	var postgresErr *pgconn.PgError
	if !errors.As(lockErr, &postgresErr) || postgresErr.Code != "55P03" {
		_ = tx.Rollback(context.Background())
		t.Fatalf("target lock after successful savepoint release = %v, want lock-not-available", lockErr)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertActivationSavepointUnchanged(t, db, repository, ids)
}
