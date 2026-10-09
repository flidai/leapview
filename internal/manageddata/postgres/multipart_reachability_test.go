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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func gcMultipartFixture(t *testing.T, r *Repository, suffix, digest string) manageddata.S3MultipartUpload {
	t.Helper()
	c, err := r.CreateCollection(t.Context(), manageddata.CreateCollectionInput{
		ID: projectgraph.ResourceID("collection_gc_" + suffix), ProjectID: "project_gc", ConnectionID: projectgraph.ResourceID("connection_gc_" + suffix), Name: suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := r.CreateUploadSession(t.Context(), manageddata.CreateUploadSessionInput{
		ID: manageddata.UploadID("upload_gc_" + suffix), CollectionID: c.ID,
		Manifest:       manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", SHA256: digest, Size: 0}}},
		StorageBackend: "s3", StagingPrefix: "uploads/gc/" + suffix, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.CreateS3MultipartUpload(t.Context(), manageddata.CreateS3MultipartUploadInput{
		ID: manageddata.MultipartUploadID("multipart_gc_" + suffix), UploadSessionID: u.ID,
		LogicalPath: "data.csv", SHA256: digest, SizeBytes: 0, IdempotencyIdentity: suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func gcBeginCompletion(t *testing.T, r *Repository, m manageddata.S3MultipartUpload) manageddata.S3MultipartUpload {
	t.Helper()
	_, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{
		ID: m.ID, ObjectKey: "blobs/" + m.SHA256, ProviderUploadID: "provider-" + m.ID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := r.BeginS3MultipartCompletion(t.Context(), manageddata.BeginS3MultipartCompletionInput{
		ID: m.ID, IdempotencyIdentity: "complete-" + m.ID.String(), RequestHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return completion.Upload
}

func TestPostgresMultipartReachabilityRetainsTerminalParentIntent(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	r := New(p)
	m := gcBeginCompletion(t, r, gcMultipartFixture(t, r, "terminal", strings.Repeat("b", 64)))
	if err := r.AbortUploadSession(t.Context(), m.UploadSessionID); err != nil {
		t.Fatal(err)
	}
	source, err := NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(snapshot.SHA256s, m.SHA256) {
		t.Fatalf("terminal-parent completing digest is absent from durable reachability: %#v", snapshot)
	}
	if _, err := r.FinishS3MultipartCompletion(t.Context(), m.ID); err != nil {
		t.Fatal(err)
	}
	after, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if containsString(after.SHA256s, m.SHA256) || after.Generation == snapshot.Generation {
		t.Fatalf("authoritative completion did not release exclusive bytes and advance epoch: before=%#v after=%#v", snapshot, after)
	}
}

func TestPostgresMultipartReachabilityEpochDefersStaleSnapshot(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	r := New(p)
	m := gcMultipartFixture(t, r, "epoch", strings.Repeat("c", 64))
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
	if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{
		ID: m.ID, ObjectKey: "blobs/" + m.SHA256, ProviderUploadID: "provider-epoch",
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	err = source.WithStableSnapshot(t.Context(), before.Generation, func(managedmaintenance.ReachabilitySnapshot) error { called = true; return nil })
	if !errors.Is(err, managedmaintenance.ErrReachabilityChanged) || called {
		t.Fatalf("multipart state change reused stale GC authority: callback=%t error=%v", called, err)
	}
}

func TestPostgresMultipartReachabilityPagesAllRetainingStates(t *testing.T) {
	p, _, _, _ := openManagedDataTestPool(t)
	r := New(p)
	want := make(map[string]bool)
	for i := 0; i < 70; i++ {
		digest := fmt.Sprintf("%064x", i+1)
		m := gcMultipartFixture(t, r, fmt.Sprintf("page_%03d", i), digest)
		switch i % 5 {
		case 1:
			if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: m.ID, ObjectKey: "blobs/" + digest, ProviderUploadID: "provider"}); err != nil {
				t.Fatal(err)
			}
		case 2:
			gcBeginCompletion(t, r, m)
		case 3:
			if _, err := r.BeginS3MultipartAbort(t.Context(), manageddata.BeginS3MultipartAbortInput{ID: m.ID, IdempotencyIdentity: "abort"}); err != nil {
				t.Fatal(err)
			}
		case 4:
			if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: m.ID, ObjectKey: "blobs/" + digest, ProviderUploadID: "provider"}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.FailS3MultipartUpload(t.Context(), m.ID, "retryable fixture"); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.AbortUploadSession(t.Context(), m.UploadSessionID); err != nil {
			t.Fatal(err)
		}
		want[digest] = true
	}
	duplicate := gcMultipartFixture(t, r, "duplicate", fmt.Sprintf("%064x", 1))
	if err := r.AbortUploadSession(t.Context(), duplicate.UploadSessionID); err != nil {
		t.Fatal(err)
	}
	// Terminal history must neither retain its exclusive digests nor consume
	// the bounded retaining-state index. Completed zero-byte intents and
	// aborted creating intents both exercise their actual native transitions.
	for i := 0; i < 80; i++ {
		m := gcMultipartFixture(t, r, fmt.Sprintf("history_%03d", i), fmt.Sprintf("%064x", i+1000))
		if i%2 == 0 {
			if _, err := r.InitializeS3MultipartUpload(t.Context(), manageddata.InitializeS3MultipartUploadInput{ID: m.ID, ObjectKey: "blobs/" + m.SHA256, Existing: true}); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := r.BeginS3MultipartAbort(t.Context(), manageddata.BeginS3MultipartAbortInput{ID: m.ID, IdempotencyIdentity: "abort"}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.FinishS3MultipartAbort(t.Context(), m.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.AbortUploadSession(t.Context(), m.UploadSessionID); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewReachabilitySource(p)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.SHA256s) != len(want) {
		t.Fatalf("paged multipart reachability=%d, want %d", len(snapshot.SHA256s), len(want))
	}
	for _, digest := range snapshot.SHA256s {
		if !want[digest] {
			t.Fatalf("unexpected digest %s", digest)
		}
	}
	conn, err := p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), `SET enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), `RESET enable_seqscan`)
	rows, err := conn.Query(t.Context(), `EXPLAIN SELECT multipart_id FROM managed_data.multipart_upload
 WHERE status IN ('creating','open','completing','aborting','failed') AND multipart_id > ''
 ORDER BY multipart_id LIMIT 32`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "multipart_upload_reachability_idx") {
		t.Fatalf("multipart retaining-page index unavailable: %s", plan.String())
	}
}
