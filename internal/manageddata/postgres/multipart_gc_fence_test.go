package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	managedmaintenance "github.com/flidai/leapview/internal/manageddata/maintenance"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresMultipartGCStableFenceBlocksEverySourceWriter(t *testing.T) {
	p, _, database, _ := openManagedDataTestPool(t)
	stable, err := p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stable.Release()
	source, err := NewReachabilitySource(stable.Conn())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	writer, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(context.Background())
	for _, table := range []string{"multipart_upload", "upload_session", "revision", "retention_root"} {
		t.Run(table, func(t *testing.T) {
			snapshot, err := source.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			writeCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			err = source.WithStableSnapshot(t.Context(), snapshot.Generation, func(managedmaintenance.ReachabilitySnapshot) error {
				// Even an empty UPDATE acquires the exact lifecycle table's write
				// lock. Observe its backend waiting on this stable transaction,
				// rather than treating a timed sleep as evidence of exclusion.
				go func() {
					_, err := writer.Exec(writeCtx, "UPDATE managed_data."+table+" SET "+map[string]string{"multipart_upload": "status=status", "upload_session": "status=status", "revision": "status=status", "retention_root": "state=state"}[table]+" WHERE false")
					done <- err
				}()
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					var blocked bool
					if err := admin.QueryRow(t.Context(), `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, stable.Conn().PgConn().PID(), writer.PgConn().PID()).Scan(&blocked); err != nil {
						return err
					}
					if blocked {
						return nil
					}
					select {
					case err := <-done:
						return fmt.Errorf("%s writer escaped stable fence: %v", table, err)
					case <-deadline.C:
						return fmt.Errorf("%s writer never blocked on exact GC backend", table)
					case <-tick.C:
					}
				}
			})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-writeCtx.Done():
				t.Fatal("writer did not finish after stable callback released its locks")
			}
		})
	}
	// Runtime can use the narrow helper, but does not gain immutable-root
	// mutation privileges to acquire those locks itself.
	if _, err := p.Exec(t.Context(), `UPDATE managed_data.retention_root SET state=state WHERE false`); err == nil {
		t.Fatal("GC helper broadened runtime retention-root mutation privileges")
	} else {
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "42501" {
			t.Fatalf("root mutation denial=%v", err)
		}
	}
}

func TestPostgresMultipartGCNowaitDefersAndReleasesPartialFence(t *testing.T) {
	p, _, database, _ := openManagedDataTestPool(t)
	source, err := NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	probe, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close(context.Background())
	for _, table := range []string{"multipart_upload", "retention_root"} {
		t.Run(table, func(t *testing.T) {
			snapshot, err := source.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			writer, err := admin.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Rollback(context.Background())
			if _, err := writer.Exec(t.Context(), "LOCK TABLE managed_data."+table+" IN ROW EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			called := false
			err = source.WithStableSnapshot(ctx, snapshot.Generation, func(managedmaintenance.ReachabilitySnapshot) error { called = true; return nil })
			if !errors.Is(err, managedmaintenance.ErrReachabilityChanged) || called {
				t.Fatalf("contended GC did not defer before delete: callback=%t error=%v", called, err)
			}
			if ctx.Err() != nil {
				t.Fatal("deferral relied on context timeout instead of NOWAIT")
			}
			// Row-exclusive locks are compatible with the original writer, but
			// conflict with any partially acquired SHARE fence left behind.
			tx, err := probe.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(t.Context(), `LOCK TABLE managed_data.multipart_upload, managed_data.upload_session, managed_data.revision, managed_data.retention_root IN ROW EXCLUSIVE MODE NOWAIT`); err != nil {
				t.Fatalf("deferred GC stranded partial source locks: %v", err)
			}
		})
	}
}

func TestPostgresMultipartReachabilityInsertAndDeleteAdvanceEpoch(t *testing.T) {
	p, _, database, _ := openManagedDataTestPool(t)
	r := New(p)
	m := gcMultipartFixture(t, r, "insert_delete", strings.Repeat("d", 64))
	if err := r.AbortUploadSession(t.Context(), m.UploadSessionID); err != nil {
		t.Fatal(err)
	}
	source, err := NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	before, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	// An authorized metadata owner removes an unresolved zero-byte intent;
	// immutable runtime clients cannot delete it. The trigger must still fence
	// every old digest set in the same transaction as that source deletion.
	if _, err := admin.Exec(t.Context(), `DELETE FROM managed_data.multipart_upload WHERE multipart_id=$1`, m.ID.String()); err != nil {
		t.Fatal(err)
	}
	after, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation == before.Generation || containsString(after.SHA256s, m.SHA256) {
		t.Fatalf("multipart delete failed to invalidate retention: before=%#v after=%#v", before, after)
	}
	// Compare an upload-only generation with one created by multipart INSERT.
	n := gcMultipartFixture(t, r, "insert", strings.Repeat("e", 64))

	base, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateS3MultipartUpload(t.Context(), manageddata.CreateS3MultipartUploadInput{ID: "multipart_gc_insert_again", UploadSessionID: n.UploadSessionID, LogicalPath: "data.csv", SHA256: n.SHA256, SizeBytes: 0, IdempotencyIdentity: "insert-again"}); err != nil {
		t.Fatal(err)
	}
	last, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if last.Generation == base.Generation || !containsString(last.SHA256s, n.SHA256) {
		t.Fatalf("multipart insert failed to advance epoch and retain digest: before=%#v after=%#v", base, last)
	}
}
