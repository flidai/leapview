package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobssqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func TestCollectionIdentityUsesProjectAndConnection(t *testing.T) {
	ctx, _, repo := testRepository(t)
	first, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ID: "orders-a", ProjectID: "project-a", ConnectionID: "warehouse", Name: "Orders A"})
	if err != nil {
		t.Fatalf("create first collection: %v", err)
	}
	second, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ID: "orders-b", ProjectID: "project-b", ConnectionID: "warehouse", Name: "Orders B"})
	if err != nil {
		t.Fatalf("same connection name in another project: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("collections share ID %q", first.ID)
	}

	got, err := repo.CollectionByProjectConnection(ctx, "project-a", "warehouse")
	if err != nil {
		t.Fatalf("lookup collection: %v", err)
	}
	if got.ID != first.ID || got.ProjectID != "project-a" || got.ConnectionID != "warehouse" {
		t.Fatalf("lookup = %#v", got)
	}

	retry, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ID: first.ID, ProjectID: "project-a", ConnectionID: "warehouse", Name: "Orders A"})
	if err != nil {
		t.Fatalf("idempotent create retry: %v", err)
	}
	if retry.ID != first.ID {
		t.Fatalf("retry ID = %q, want %q", retry.ID, first.ID)
	}
	_, err = repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ID: "different", ProjectID: "project-a", ConnectionID: "warehouse", Name: "Conflicting"})
	if !errors.Is(err, manageddata.ErrConflict) {
		t.Fatalf("conflicting project+connection error = %v, want conflict", err)
	}
}

func TestCompleteUploadCreatesImmutableRevisionAtomically(t *testing.T) {
	ctx, store, repo := testRepository(t)
	collection := createCollection(t, ctx, repo, "customers", "project-a", "customers")
	manifest := manageddata.Manifest{Files: []manageddata.File{
		{Path: "customers.csv", Size: 12, SHA256: strings.Repeat("a", 64)},
		{Path: "regions.csv", Size: 7, SHA256: strings.Repeat("b", 64)},
	}}
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: "upload-1", CollectionID: collection.ID, Manifest: manifest, StorageBackend: "local",
		StagingPrefix: "sessions/upload-1", CreatedBy: "principal-1", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create upload session: %v", err)
	}

	revision, err := repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: session.ID, Files: []manageddata.StoredFile{
		{File: manifest.Files[1], StorageKey: "objects/b", MediaType: "text/csv", ETag: "etag-b"},
		{File: manifest.Files[0], StorageKey: "objects/a", MediaType: "text/csv", ETag: "etag-a"},
	}})
	if err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	if revision.Digest != manifest.RevisionID() || revision.Status != manageddata.RevisionStatusReady || revision.Sequence != 1 {
		t.Fatalf("revision = %#v", revision)
	}
	files, err := repo.ListRevisionFiles(ctx, revision.ID)
	if err != nil {
		t.Fatalf("list revision files: %v", err)
	}
	if len(files) != 2 || files[0].Path != "customers.csv" || files[1].Path != "regions.csv" {
		t.Fatalf("files = %#v", files)
	}
	completed, err := repo.UploadSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatalf("get upload session: %v", err)
	}
	if completed.Status != manageddata.UploadStatusComplete || completed.RevisionID != revision.ID {
		t.Fatalf("completed session = %#v", completed)
	}
	if _, err := store.ExecContext(ctx, `UPDATE managed_data_revisions SET digest = ? WHERE id = ?`, "sha256:"+strings.Repeat("f", 64), revision.ID); err == nil {
		t.Fatal("ready revision metadata was mutable")
	}
}

func TestBeginUploadFinalizationRollsBackWhenWorkflowCannotBeRecorded(t *testing.T) {
	ctx, db, base := testRepository(t)
	collection := createCollection(t, ctx, base, "atomic", "project-a", "atomic")
	session, err := base.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: "upload-atomic", CollectionID: collection.ID, Manifest: manageddata.Manifest{},
		StorageBackend: "local", StagingPrefix: "staging/upload-atomic", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected workflow failure")
	repo := NewRepositoryWithWorkflow(db, jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return injected
	}))
	_, err = repo.BeginUploadFinalization(ctx, session.ID, jobs.WorkflowIntent{
		Job: jobs.EnqueueInput{ID: "upload:" + session.ID.String() + ":finalize"},
	})
	if !errors.Is(err, injected) {
		t.Fatalf("BeginUploadFinalization() error = %v, want injected failure", err)
	}
	current, err := repo.UploadSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != manageddata.UploadStatusOpen {
		t.Fatalf("status after workflow failure = %q, want open", current.Status)
	}
}

func TestAbortUploadSessionWithWorkflowRollsBackStateOnEventFailure(t *testing.T) {
	ctx, db, base := testRepository(t)
	collection := createCollection(t, ctx, base, "abort-atomic", "project-a", "abort-atomic")
	session, err := base.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{ID: "upload-abort-atomic", CollectionID: collection.ID, Manifest: manageddata.Manifest{}, StorageBackend: "local", StagingPrefix: "staging/upload-abort-atomic", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected workflow failure")
	repo := NewRepositoryWithWorkflow(db, jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error { return injected }))
	err = repo.AbortUploadSessionWithWorkflow(ctx, session.ID, jobs.WorkflowIntent{Event: jobs.EventInput{EventType: "upload_session.cancelled"}})
	if !errors.Is(err, injected) {
		t.Fatalf("abort error=%v", err)
	}
	current, err := repo.UploadSessionByID(ctx, session.ID)
	if err != nil || current.Status != manageddata.UploadStatusOpen {
		t.Fatalf("session after rollback=%#v err=%v", current, err)
	}
}

func TestAbortUploadSessionWithWorkflowConcurrentReplayEmitsOneEvent(t *testing.T) {
	ctx, db, base := testRepository(t)
	collection := createCollection(t, ctx, base, "abort-concurrent", "project-a", "abort-concurrent")
	session, err := base.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{ID: "upload-abort-concurrent", CollectionID: collection.ID, Manifest: manageddata.Manifest{}, StorageBackend: "local", StagingPrefix: "staging/upload-abort-concurrent", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	workflow := jobssqlite.NewRepository(db)
	repo := NewRepositoryWithWorkflow(db, workflow)
	intent := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "upload:" + session.ID.String() + ":cancelled", ResourceKind: "upload", ResourceID: session.ID.String(), EventType: "upload_session.cancelled", Data: []byte(`{"status":"cancelled"}`)}}
	var group sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			if callErr := repo.AbortUploadSessionWithWorkflow(ctx, session.ID, intent); callErr == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	if successes != 1 {
		t.Fatalf("successful cancellations=%d, want one", successes)
	}
	events, err := workflow.ListEvents(ctx, "upload", session.ID.String(), 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "upload_session.cancelled" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestMultipartDigestClaimFencesLaterIntentAcrossConnectionsAndAllowsExpiryTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "multipart-claims.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	firstDB := open()
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, firstDB, "../../platform/migrations"); err != nil {
		t.Fatal(err)
	}
	secondDB := open()
	first, second := NewRepository(firstDB), NewRepository(secondDB)
	digest := strings.Repeat("a", 64)

	firstGeneration, claimed, err := first.ClaimS3MultipartDigest(ctx, digest, "multipart-b", time.Now().UTC().Add(5*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if _, duplicate, duplicateErr := second.ClaimS3MultipartDigest(ctx, digest, "multipart-b", time.Now().UTC().Add(5*time.Minute)); duplicateErr != nil || duplicate {
		t.Fatalf("concurrent retry with same owner claim = %v, %v; want fenced", duplicate, duplicateErr)
	}
	if renewed, renewErr := first.RenewS3MultipartDigest(ctx, digest, "multipart-b", firstGeneration, time.Now().UTC().Add(5*time.Minute)); renewErr != nil || !renewed {
		t.Fatalf("current generation renewal = %v, %v", renewed, renewErr)
	}
	_, claimed, err = second.ClaimS3MultipartDigest(ctx, digest, "multipart-a", time.Now().UTC().Add(5*time.Minute))
	if err != nil || claimed {
		t.Fatalf("later lexicographically smaller intent claim = %v, %v; want fenced", claimed, err)
	}
	if err := second.ReleaseS3MultipartDigest(ctx, digest, "multipart-a", firstGeneration); err != nil {
		t.Fatal(err)
	}
	_, claimed, err = second.ClaimS3MultipartDigest(ctx, digest, "multipart-a", time.Now().UTC().Add(5*time.Minute))
	if err != nil || claimed {
		t.Fatalf("non-owner release displaced claim: claim = %v, %v", claimed, err)
	}

	if _, err := firstDB.ExecContext(ctx, `UPDATE managed_data_multipart_claims SET lease_until = datetime('now', '-1 second') WHERE sha256 = ?`, digest); err != nil {
		t.Fatal(err)
	}
	secondGeneration, claimed, err := second.ClaimS3MultipartDigest(ctx, digest, "multipart-a", time.Now().UTC().Add(5*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("expired claim takeover = %v, %v", claimed, err)
	}
	if secondGeneration <= firstGeneration {
		t.Fatalf("takeover generation = %d, want greater than %d", secondGeneration, firstGeneration)
	}
	if renewed, renewErr := first.RenewS3MultipartDigest(ctx, digest, "multipart-b", firstGeneration, time.Now().UTC().Add(5*time.Minute)); renewErr != nil || renewed {
		t.Fatalf("stale generation renewal = %v, %v; want fenced", renewed, renewErr)
	}
	if err := first.ReleaseS3MultipartDigest(ctx, digest, "multipart-b", firstGeneration); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := secondDB.QueryRowContext(ctx, `SELECT owner_id FROM managed_data_multipart_claims WHERE sha256 = ?`, digest).Scan(&owner); err != nil || owner != "multipart-a" {
		t.Fatalf("stale owner released takeover: owner=%q err=%v", owner, err)
	}
}

func TestCompleteUploadRollsBackWhenStoredFilesDoNotMatchManifest(t *testing.T) {
	ctx, store, repo := testRepository(t)
	collection := createCollection(t, ctx, repo, "orders", "project-a", "orders")
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "orders.csv", Size: 4, SHA256: strings.Repeat("a", 64)}}}
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{ID: "upload-bad", CollectionID: collection.ID, Manifest: manifest, StorageBackend: "local", StagingPrefix: "staging/upload-bad", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: session.ID, Files: []manageddata.StoredFile{{File: manageddata.File{Path: "orders.csv", Size: 5, SHA256: strings.Repeat("b", 64)}, StorageKey: "objects/bad"}}})
	if err == nil {
		t.Fatal("CompleteUpload() unexpectedly succeeded")
	}
	var revisionCount int
	if err := store.QueryRowContext(ctx, `SELECT count(*) FROM managed_data_revisions`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 0 {
		t.Fatalf("revision count = %d, want 0", revisionCount)
	}
	got, err := repo.UploadSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != manageddata.UploadStatusOpen {
		t.Fatalf("session status = %q, want open", got.Status)
	}
}

func TestServingStateBindingsAllowMultipleCollections(t *testing.T) {
	ctx, store, repo := testRepository(t)
	firstCollection, firstRevision := readyRevision(t, ctx, repo, "inventory", "project-a", "inventory", "inventory.csv", "c")
	secondCollection, secondRevision := readyRevision(t, ctx, repo, "prices", "project-a", "prices", "prices.csv", "d")
	insertProjectState(t, ctx, store, "project-a", "state-1", "prod", "validated")
	bindings := []manageddata.ServingStateBinding{
		{Identity: servingIdentity("project-a", "prod", "state-1"), CollectionID: firstCollection.ID, RevisionID: firstRevision.ID},
		{Identity: servingIdentity("project-a", "prod", "state-1"), CollectionID: secondCollection.ID, RevisionID: secondRevision.ID},
	}
	if err := repo.InstallServingStateBindings(ctx, servingIdentity("project-a", "prod", "state-1"), bindings); err != nil {
		t.Fatalf("replace bindings: %v", err)
	}
	got, err := repo.ListServingStateBindings(ctx, servingIdentity("project-a", "prod", "state-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CollectionID != firstCollection.ID || got[1].CollectionID != secondCollection.ID {
		t.Fatalf("bindings = %#v", got)
	}
}

func testRepository(t *testing.T) (context.Context, *sql.DB, *Repository) {
	t.Helper()
	ctx := context.Background()
	store, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "leapview.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	store.SetMaxOpenConns(1)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, store, "../../platform/migrations"); err != nil {
		_ = store.Close()
		t.Fatalf("migrate platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return ctx, store, NewRepository(store)
}

func createCollection(t *testing.T, ctx context.Context, repo *Repository, id, projectID, connectionName string) manageddata.Collection {
	t.Helper()
	collection, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: projectgraph.ResourceID(id), ProjectID: projectgraph.ResourceID(projectID),
		ConnectionID: projectgraph.ResourceID(connectionName), Name: connectionName,
	})
	if err != nil {
		t.Fatal(err)
	}
	return collection
}

func readyRevision(t *testing.T, ctx context.Context, repo *Repository, id, projectID, connectionName, path, digestChar string) (manageddata.Collection, manageddata.Revision) {
	t.Helper()
	collection := createCollection(t, ctx, repo, id, projectID, connectionName)
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: path, Size: 1, SHA256: strings.Repeat(digestChar, 64)}}}
	session, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{CollectionID: collection.ID, Manifest: manifest, StorageBackend: "local", StagingPrefix: "staging/" + path, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: session.ID, Files: []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "objects/" + digestChar}}})
	if err != nil {
		t.Fatal(err)
	}
	return collection, revision
}

func insertProjectState(t *testing.T, ctx context.Context, db *sql.DB, projectID, stateID, environment, status string) {
	t.Helper()
	insertServingState(t, ctx, db, projectID, stateID, environment, status)
}

func insertServingState(t *testing.T, ctx context.Context, db *sql.DB, projectID, stateID, environment, status string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status, source) VALUES (?, ?, ?, ?, 'publish')`, stateID, projectID, environment, status); err != nil {
		t.Fatal(err)
	}
}

func setActiveState(t *testing.T, ctx context.Context, db *sql.DB, projectID, environment, stateID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO project_active_serving_states (project_id, environment, serving_state_id) VALUES (?, ?, ?) ON CONFLICT(project_id, environment) DO UPDATE SET serving_state_id = excluded.serving_state_id`, projectID, environment, stateID); err != nil {
		t.Fatal(err)
	}
}

func assertActiveState(t *testing.T, ctx context.Context, db *sql.DB, projectID, environment, want string) {
	t.Helper()
	var got string
	if err := db.QueryRowContext(ctx, `SELECT serving_state_id FROM project_active_serving_states WHERE project_id = ? AND environment = ?`, projectID, environment).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("active serving state for %s = %q, want %q", projectID, got, want)
	}
}

func servingIdentity(projectID, environment, generationID string) projectgraph.ServingIdentity {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), environment, generationID)
	if err != nil {
		panic(err)
	}
	return identity
}

func assertServingStateStatus(t *testing.T, ctx context.Context, db *sql.DB, stateID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRowContext(ctx, `SELECT status FROM serving_states WHERE id = ?`, stateID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("serving state %s status = %q, want %q", stateID, got, want)
	}
}
