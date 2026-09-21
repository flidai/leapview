package binding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestBinderPinsRevisionAfterEnvironmentPointerChanges(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t, manageddatapostgres.ApplySchema)
	candidate := servingstate.State{ID: "candidate", ProjectID: "project-a", Environment: "prod", Source: servingstate.SourcePublish}

	repository := manageddatapostgres.New(pool)
	collection, err := repository.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: "orders", ProjectID: "project-a", ConnectionID: "orders", Name: "Orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRevision := createReadyRevision(t, ctx, repository, collection.ID, "orders-v1.csv", "a")
	firstTarget := createValidatedState("first-target", "project-a", "prod")
	activateRevision(t, ctx, repository, collection.ID, firstRevision.ID, firstTarget.ID)

	validation := servingstate.Validation{ProjectID: "project-a", ManagedDataRevisions: map[string]string{"orders": firstRevision.Digest}}
	secondRevision := createReadyRevision(t, ctx, repository, collection.ID, "orders-v2.csv", "b")
	secondTarget := createValidatedState("second-target", "project-a", "prod")
	activateRevision(t, ctx, repository, collection.ID, secondRevision.ID, secondTarget.ID)
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

func createReadyRevision(t *testing.T, ctx context.Context, repository *manageddatapostgres.Repository, collectionID projectgraph.ResourceID, path, digestCharacter string) manageddata.Revision {
	t.Helper()
	manifest := manageddata.Manifest{Files: []manageddata.File{{
		Path: path, Size: 1, SHA256: strings.Repeat(digestCharacter, 64),
	}}}
	session, err := repository.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: manageddata.UploadID("upload_" + digestCharacter), CollectionID: collectionID, Manifest: manifest, StorageBackend: "local",
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

func createValidatedState(id servingstate.ID, projectID projectgraph.ResourceID, environment servingstate.Environment) servingstate.State {
	return servingstate.State{ID: id, ProjectID: projectID, Environment: environment, Status: servingstate.StatusValidated, ProjectDigest: "sha256:" + strings.Repeat("a", 64)}
}

func activateRevision(t *testing.T, ctx context.Context, repository *manageddatapostgres.Repository, collectionID projectgraph.ResourceID, revisionID manageddata.RevisionID, targetID servingstate.ID) {
	t.Helper()
	identity := servingIdentity("project-a", "prod", string(targetID))
	if err := repository.InstallServingStateBindings(ctx, identity, []manageddata.ServingStateBinding{{
		Identity: identity, CollectionID: collectionID, RevisionID: revisionID,
	}}); err != nil {
		t.Fatal(err)
	}
}
