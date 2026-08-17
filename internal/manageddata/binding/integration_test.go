package binding

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddatasqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
)

func TestBinderPinsRevisionAfterEnvironmentPointerChanges(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	servingStates := servingstatesqlite.NewRepository(store.SQLDB())
	candidate, err := servingStates.Create(ctx, servingstate.CreateInput{ProjectID: "project-a", Environment: "prod", Source: servingstate.SourcePublish})
	if err != nil {
		t.Fatal(err)
	}

	repository := manageddatasqlite.NewRepository(store.SQLDB())
	collection, err := repository.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: "orders", ProjectID: "project-a", ConnectionID: "orders", Name: "Orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRevision := createReadyRevision(t, ctx, repository, collection.ID, "orders-v1.csv", "a")
	firstTarget := createValidatedState(t, ctx, store, servingStates, "project-a", "prod")
	activateRevision(t, ctx, servingStates, repository, collection.ID, firstRevision.ID, firstTarget.ID, "")

	validation := servingstate.Validation{ProjectID: "project-a", ManagedDataRevisions: map[string]string{"orders": firstRevision.Digest}}
	secondRevision := createReadyRevision(t, ctx, repository, collection.ID, "orders-v2.csv", "b")
	secondTarget := createValidatedState(t, ctx, store, servingStates, "project-a", "prod")
	activateRevision(t, ctx, servingStates, repository, collection.ID, secondRevision.ID, secondTarget.ID, firstTarget.ID)
	binder, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := binder.AfterArtifactValidation(ctx, candidate, validation); err != nil {
		t.Fatalf("pin artifact revision: %v", err)
	}
	identity := servingIdentity("project-a", "prod", string(candidate.ID))
	bindings, err := repository.ListServingStateBindings(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].RevisionID != firstRevision.ID {
		t.Fatalf("later deployment mutated pinned publish bindings: %#v", bindings)
	}
}

func createReadyRevision(t *testing.T, ctx context.Context, repository *manageddatasqlite.Repository, collectionID projectgraph.ResourceID, path, digestCharacter string) manageddata.Revision {
	t.Helper()
	manifest := manageddata.Manifest{Files: []manageddata.File{{
		Path: path, Size: 1, SHA256: strings.Repeat(digestCharacter, 64),
	}}}
	session, err := repository.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		CollectionID: collectionID, Manifest: manifest, StorageBackend: "local",
		StagingPrefix: "staging/" + path, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repository.CompleteUpload(ctx, manageddata.CompleteUploadInput{
		SessionID: session.ID,
		Files:     []manageddata.StoredFile{{File: manifest.Files[0], StorageKey: "objects/" + digestCharacter}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func createValidatedState(t *testing.T, ctx context.Context, store *platform.Store, repository *servingstatesqlite.Repository, projectID projectgraph.ResourceID, environment servingstate.Environment) servingstate.State {
	t.Helper()
	state, err := repository.Create(ctx, servingstate.CreateInput{ProjectID: projectID, Environment: environment, Source: servingstate.SourcePublish})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE serving_states SET status = 'validated', project_id = ?, project_digest = ? WHERE id = ?`, projectID, "sha256:"+strings.Repeat("a", 64), state.ID); err != nil {
		t.Fatal(err)
	}
	state.Status = servingstate.StatusValidated
	return state
}

func activateRevision(t *testing.T, ctx context.Context, states *servingstatesqlite.Repository, repository *manageddatasqlite.Repository, collectionID projectgraph.ResourceID, revisionID manageddata.RevisionID, targetID, expectedActiveID servingstate.ID) {
	t.Helper()
	identity := servingIdentity("project-a", "prod", string(targetID))
	if err := repository.InstallServingStateBindings(ctx, identity, []manageddata.ServingStateBinding{{
		Identity: identity, CollectionID: collectionID, RevisionID: revisionID,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := states.Activate(ctx, "project-a", "prod", targetID, expectedActiveID); err != nil {
		t.Fatal(err)
	}
}
