package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func allowTestActivation(context.Context, Tx, DeliveryPublication) error { return nil }

func TestActivationRequiresMandatoryAdmission(t *testing.T) {
	r := NewWithOptions(nil, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: &testActivationLineage{}})
	if r.ActivationAdmissionCapable() {
		t.Fatal("missing admission reported as configured")
	}
	if _, err := r.ActivateTx(t.Context(), nil, ActivationInput{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "admission authority") {
		t.Fatalf("missing admission = %v", err)
	}
}

func TestPostgresActivationAdmissionCannotBeBypassed(t *testing.T) {
	for _, entry := range []string{"activate", "transaction", "hook"} {
		t.Run(entry, func(t *testing.T) {
			db := deliveryTestDB(t)
			denied := errors.New("unfinished credential activation")
			calls := 0
			r := NewWithOptions(db, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: &testActivationLineage{}, ActivationAdmission: func(ctx context.Context, tx Tx, publication DeliveryPublication) error {
				calls++
				if tx == nil || publication.TargetID == "" || publication.State != "pending" {
					t.Fatal("incomplete admission identity")
				}
				contender, err := db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = contender.Rollback(context.Background()) }()
				_, err = contender.Exec(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE NOWAIT`, publication.TargetID)
				var pgerr *pgconn.PgError
				if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
					t.Fatalf("target was not locked at admission: %v", err)
				}
				return denied
			}})
			input, ids := prepareLostAckActivation(t, r)
			seedPhysicalRetentionFixture(t, db, ids.seal)
			var err error
			switch entry {
			case "activate":
				_, err = r.Activate(t.Context(), input)
			default:
				tx, beginErr := db.Begin(t.Context())
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				if entry == "hook" {
					_, err = r.ActivateTxWithPreCommitHook(t.Context(), tx, input, allowTestActivation)
				} else {
					_, err = r.ActivateTx(t.Context(), tx, input)
				}
				// A caller committing after a rejected admission cannot commit a partial cutover.
				if commitErr := tx.Commit(t.Context()); commitErr != nil {
					t.Fatal(commitErr)
				}
			}
			if !errors.Is(err, denied) || calls != 1 {
				t.Fatalf("admission = %v, calls %d", err, calls)
			}
			target, err := r.Target(t.Context(), ids.target)
			if err != nil {
				t.Fatal(err)
			}
			publication, err := r.Publication(t.Context(), ids.publication)
			if err != nil {
				t.Fatal(err)
			}
			if target.TargetRevision != 1 || target.ActiveGenerationID != "" || publication.State != "pending" {
				t.Fatalf("rejected admission mutated publication: target=%#v publication=%#v", target, publication)
			}
			var events, audits int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, ids.publication).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, ids.publication).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if events != 0 || audits != 0 {
				t.Fatalf("rejected admission wrote events=%d audits=%d", events, audits)
			}
			r.admission = allowTestActivation
			if _, err := r.Activate(t.Context(), input); err != nil {
				t.Fatalf("admitted activation: %v", err)
			}
			r.admission = func(context.Context, Tx, DeliveryPublication) error {
				t.Fatal("read-only committed replay invoked mutation admission")
				return denied
			}
			replay, err := r.Activate(t.Context(), input)
			if err != nil || !replay.Replay {
				t.Fatalf("read-only replay = %#v, %v", replay, err)
			}
		})
	}
}
