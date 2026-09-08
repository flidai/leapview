//go:build fai520qualification

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	managedpostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/recoveryset/observation"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var errProviderObservationSource = errors.New("provider observation source unavailable")

type providerObservationErrorSource struct{}

func (providerObservationErrorSource) CaptureManagedProjection(context.Context) (manageddata.CapturedProjection, error) {
	return manageddata.CapturedProjection{}, errProviderObservationSource
}

func TestProviderObservationCapturePreservesSourceErrors(t *testing.T) {
	_, err := Capture(t.Context(), providerObservationErrorSource{})
	if !errors.Is(err, errProviderObservationSource) {
		t.Fatalf("source error = %v, want %v", err, errProviderObservationSource)
	}
}

func TestProviderObservationCaptureRejectsDisabledFsync(t *testing.T) {
	fixture := providerObservationFixtureDB(t)
	setProviderObservationFsync(t, fixture.admin, "off")

	_, err := Capture(t.Context(), managedpostgres.New(fixture.db))
	if err == nil {
		t.Fatal("capture succeeded while PostgreSQL fsync was disabled")
	}
	if !errors.Is(err, managedpostgres.ErrInvalid) || !strings.Contains(strings.ToLower(err.Error()), "fsync") {
		t.Fatalf("disabled fsync error = %v, want managed-data invalid fsync diagnostic", err)
	}
}

func TestProviderObservationCaptureQualification(t *testing.T) {
	db := providerObservationDB(t)
	ctx := t.Context()
	managed := managedpostgres.New(db)
	collection, err := managed.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID:           projectgraph.ResourceID("collection_observation"),
		ProjectID:    projectgraph.ResourceID("project_observation"),
		ConnectionID: projectgraph.ResourceID("connection_observation"),
		Name:         "Observation",
	})
	if err != nil {
		t.Fatal(err)
	}
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.parquet", Size: 12, SHA256: hash}}}
	session, err := managed.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID:             manageddata.UploadID("upload_observation"),
		CollectionID:   collection.ID,
		Manifest:       manifest,
		StorageBackend: "s3",
		StagingPrefix:  "staging/observation",
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managed.BeginUploadFinalization(ctx, session.ID, jobspkg.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.CompleteUpload(ctx, manageddata.CompleteUploadInput{
		SessionID:  session.ID,
		RevisionID: manageddata.RevisionID("revision_observation"),
		Files:      []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "s3://bucket/objects/orders.parquet"}},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := Capture(ctx, managed)
	if err != nil {
		t.Fatal(err)
	}
	boundary := first.Boundary()
	if err := boundary.Validate(); err != nil {
		t.Fatalf("captured boundary invalid: %v", err)
	}
	inventory := first.Inventory()
	if err := inventory.Validate(); err != nil {
		t.Fatalf("captured inventory invalid: %v", err)
	}
	if len(inventory.Revisions) != 1 || len(inventory.Revisions[0].Files) != 1 {
		t.Fatalf("captured inventory = %#v", inventory)
	}
	digest, err := inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if boundary.InventoryDigest != digest {
		t.Fatalf("boundary inventory digest %q, computed %q", boundary.InventoryDigest, digest)
	}
	if boundary.RestorePointName == "" || boundary.LSN == "" || boundary.SystemIdentity == "" || boundary.DatabaseIdentity == "" || boundary.Timeline == 0 {
		t.Fatalf("captured boundary is incomplete: %#v", boundary)
	}
	assertProviderObservationMarkerFlushed(t, db, boundary.LSN)

	// Accessors must not expose mutable backing slices owned by the capture.
	inventory.Revisions[0].Files[0].Path = "tampered.parquet"
	if got := first.Inventory().Revisions[0].Files[0].Path; got != "orders.parquet" {
		t.Fatalf("inventory accessor leaked mutable state: %q", got)
	}

	second, err := Capture(ctx, managed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Boundary().RestorePointName == boundary.RestorePointName {
		t.Fatal("repeated capture reused restore point name")
	}
	if second.Boundary().InventoryDigest != boundary.InventoryDigest {
		t.Fatalf("repeated capture inventory digest = %q, want %q", second.Boundary().InventoryDigest, boundary.InventoryDigest)
	}
	assertProviderObservationMarkerFlushed(t, db, second.Boundary().LSN)
	wrongMarker := boundary
	wrongMarker.RestorePointName += "_wrong"
	if boundary.Matches(wrongMarker) {
		t.Fatal("boundary accepted a different restore-point identity")
	}
	if err := boundary.ValidateAgainst(wrongMarker); err == nil || !errors.Is(err, observation.ErrInvalid) {
		t.Fatalf("wrong marker comparison error = %v, want observation.ErrInvalid", err)
	}
	wrongLSN := boundary
	wrongLSN.LSN = "0/1"
	if boundary.Matches(wrongLSN) {
		t.Fatal("boundary accepted a different LSN")
	}
	wrongDatabase := boundary
	wrongDatabase.DatabaseIdentity += "_other"
	if boundary.Matches(wrongDatabase) {
		t.Fatal("boundary accepted a different database identity")
	}
}

func TestProviderObservationCaptureLocksWriters(t *testing.T) {
	db := providerObservationDB(t)
	ctx := t.Context()
	managed := managedpostgres.New(db)

	// Keep a ready revision uncommitted while capture queues its SHARE locks.
	// The commit after the server-side lock barrier must be in the captured
	// repeatable-read snapshot.
	writerBefore, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerBefore.Rollback(context.Background()) })
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "before.parquet", Size: 0, SHA256: hash}}}
	manifestJSON, err := manifest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest := manifest.RevisionID()
	if _, err := writerBefore.Exec(ctx, `
INSERT INTO managed_data.collection(collection_id, project_id, connection_id, name, request_digest)
VALUES ('collection_lock_before', 'project_lock', 'connection_lock', 'Lock before', $1)
`, digest); err != nil {
		_ = writerBefore.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := writerBefore.Exec(ctx, `
INSERT INTO managed_data.revision(revision_id, collection_id, sequence, digest, status, manifest, file_count, size_bytes)
VALUES ('revision_lock_before', 'collection_lock_before', 1, $1, 'pending', $2::jsonb, 1, 0)
	`, digest, string(manifestJSON)); err != nil {
		_ = writerBefore.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := writerBefore.Exec(ctx, `
INSERT INTO managed_data.revision_file(revision_id, logical_path, size_bytes, sha256, storage_key)
VALUES ('revision_lock_before', 'before.parquet', 0, $1, 's3://bucket/objects/before.parquet')
`, hash); err != nil {
		_ = writerBefore.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := writerBefore.Exec(ctx, `
UPDATE managed_data.revision SET status='ready', ready_at=clock_timestamp()
WHERE revision_id='revision_lock_before'
`); err != nil {
		_ = writerBefore.Rollback(ctx)
		t.Fatal(err)
	}

	type captureResult struct {
		captured CapturedObservation
		err      error
	}
	captureCtx, cancelCapture := context.WithCancel(ctx)
	defer cancelCapture()
	captureDone := make(chan captureResult, 1)
	captureFinished := make(chan struct{})
	go func() {
		captured, captureErr := Capture(captureCtx, managed)
		captureDone <- captureResult{captured: captured, err: captureErr}
		close(captureFinished)
	}()
	t.Cleanup(func() {
		cancelCapture()
		select {
		case <-captureFinished:
		case <-time.After(5 * time.Second):
		}
	})
	capturePID := waitForProviderObservationShareWait(t, db)
	if capturePID == 0 {
		_ = writerBefore.Rollback(ctx)
		t.Fatal("capture did not queue a SHARE lock")
	}

	writerAfter, err := db.Begin(ctx)
	if err != nil {
		_ = writerBefore.Rollback(ctx)
		t.Fatal(err)
	}
	writerAfterCtx, cancelWriterAfter := context.WithCancel(ctx)
	defer cancelWriterAfter()
	type writerResult struct{ err error }
	writerAfterDone := make(chan writerResult, 1)
	writerAfterFinished := make(chan struct{})
	t.Cleanup(func() {
		cancelWriterAfter()
		select {
		case <-writerAfterFinished:
		case <-time.After(5 * time.Second):
		}
		_ = writerAfter.Rollback(context.Background())
	})
	var writerAfterPID int
	if err := writerAfter.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&writerAfterPID); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, writerErr := writerAfter.Exec(writerAfterCtx, `LOCK TABLE managed_data.upload_session IN ROW EXCLUSIVE MODE`)
		writerAfterDone <- writerResult{err: writerErr}
		close(writerAfterFinished)
	}()
	if !waitForProviderObservationWriterWait(t, db, writerAfterPID) {
		t.Fatal("writer started after capture was admitted before the capture lock")
	}
	var captureBlocksWriter bool
	if err := db.QueryRow(ctx, "SELECT $1 = ANY(pg_blocking_pids($2))", capturePID, writerAfterPID).Scan(&captureBlocksWriter); err != nil {
		t.Fatal(err)
	}
	if !captureBlocksWriter {
		t.Fatalf("writer is not blocked by the queued capture transaction (capture pid %d)", capturePID)
	}

	if err := writerBefore.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-captureFinished:
		result := <-captureDone
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.captured.Inventory().Revisions) != 1 || result.captured.Inventory().Revisions[0].RevisionID != "revision_lock_before" {
			t.Fatalf("capture snapshot omitted committed preceding revision: %#v", result.captured.Inventory().Revisions)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("capture did not complete after the preceding writer committed")
	}
	select {
	case result := <-writerAfterDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer remained blocked after capture completed")
	}

}

func waitForProviderObservationShareWait(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := db.QueryRow(ctx, `
SELECT COALESCE((
  SELECT pid::int FROM pg_locks
  WHERE relation='managed_data.revision'::regclass
    AND mode='ShareLock' AND NOT granted
  ORDER BY pid LIMIT 1
), 0)
`).Scan(&pid)
		if err == nil && pid != 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
	}
}

func waitForProviderObservationWriterWait(t *testing.T, db *pgxpool.Pool, pid int) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE pid=$1 AND relation='managed_data.upload_session'::regclass
    AND mode='RowExclusiveLock' AND NOT granted
)
`, pid).Scan(&waiting)
		if err == nil && waiting {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

type providerObservationFixture struct {
	db    *pgxpool.Pool
	admin *pgxpool.Pool
}

func providerObservationDB(t *testing.T) *pgxpool.Pool {
	return providerObservationFixtureDB(t).db
}

func providerObservationFixtureDB(t *testing.T) *providerObservationFixture {
	t.Helper()
	if !postgrestest.Required() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, postgrestest.PostgreSQL18Image,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("leapview-conformance-secret"),
		// The postgres module defaults to fsync=off. Replacing its command with
		// the stock foreground server lets this qualification fixture verify and
		// control the real cluster setting itself.
		testcontainers.WithCmd("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		testcontainers.WithLogger(log.TestLogger(t)),
	)
	if err != nil {
		if postgrestest.Required() {
			t.Fatalf("required PostgreSQL 18 conformance container: %v", err)
		}
		t.Skipf("PostgreSQL 18 conformance container unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if err := admin.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	setProviderObservationFsync(t, admin, "on")
	if _, err := admin.Exec(ctx, "CREATE DATABASE observation_capture_test"); err != nil {
		t.Fatal(err)
	}
	dbURL, err := container.ConnectionString(ctx, "sslmode=disable", "dbname=observation_capture_test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	assertProviderObservationFsync(t, p, "on")
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := managedpostgres.ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var walLevel string
	if err := p.QueryRow(ctx, "SHOW wal_level").Scan(&walLevel); err != nil {
		t.Fatal(err)
	}
	if walLevel == "minimal" {
		t.Fatalf("provider observation qualification requires wal_level replica or higher, got %q", walLevel)
	}
	return &providerObservationFixture{db: p, admin: admin}
}

func setProviderObservationFsync(t *testing.T, db *pgxpool.Pool, value string) {
	t.Helper()
	query := "ALTER SYSTEM SET fsync = 'on'"
	if value == "off" {
		query = "ALTER SYSTEM SET fsync = 'off'"
	}
	if _, err := db.Exec(t.Context(), query); err != nil {
		t.Fatalf("set PostgreSQL fsync=%s: %v", value, err)
	}
	if _, err := db.Exec(t.Context(), "SELECT pg_reload_conf()"); err != nil {
		t.Fatalf("reload PostgreSQL fsync=%s: %v", value, err)
	}
	assertProviderObservationFsync(t, db, value)
}

func assertProviderObservationFsync(t *testing.T, db *pgxpool.Pool, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var got string
		err := db.QueryRow(ctx, "SHOW fsync").Scan(&got)
		if err == nil && strings.EqualFold(strings.TrimSpace(got), want) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("PostgreSQL fsync = %q, want %q (last error: %v)", got, want, err)
		case <-ticker.C:
		}
	}
}

func assertProviderObservationMarkerFlushed(t *testing.T, db *pgxpool.Pool, lsn string) {
	t.Helper()
	var flushed bool
	if err := db.QueryRow(t.Context(), "SELECT pg_current_wal_flush_lsn() >= $1::pg_lsn", lsn).Scan(&flushed); err != nil {
		t.Fatal(err)
	}
	if !flushed {
		t.Fatalf("restore point LSN %s was not flushed", lsn)
	}
}
