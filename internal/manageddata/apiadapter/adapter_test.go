package apiadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestRepositoryRevisionIDsAreCanonicalDigests(t *testing.T) {
	adapter := mustNew(t, fixtureRepository())
	for _, test := range []struct {
		id      string
		wantErr error
	}{
		{id: "revision_a", wantErr: control.ErrInvalid},
		{id: "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", wantErr: control.ErrInvalid},
		{id: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", wantErr: control.ErrNotFound},
	} {
		_, err := adapter.RevisionByID(context.Background(), "collection_a", test.id)
		if !errors.Is(err, test.wantErr) {
			t.Fatalf("RevisionByID(%q) error = %v, want %v", test.id, err, test.wantErr)
		}
	}
	got, err := adapter.RevisionByID(context.Background(), "collection_a", digestA)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicID != digestA || got.Revision.ID != "revision_a" || got.UploadSessionID != "upload_a" {
		t.Fatalf("revision = %#v", got)
	}
}

func TestListRevisionsIsDeterministicAndIncludesProvenance(t *testing.T) {
	repository := fixtureRepository()
	repository.uploads["revision_b"] = "upload_b"
	adapter := mustNew(t, repository)
	got, err := adapter.ListRevisions(context.Background(), "collection_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].PublicID != digestB || got[0].UploadSessionID != "upload_b" || got[1].PublicID != digestA {
		t.Fatalf("revisions = %#v", got)
	}
}

func TestEnvironmentPointerExposesDeploymentAndPublicRevision(t *testing.T) {
	adapter := mustNew(t, fixtureRepository())
	got, err := adapter.EnvironmentPointer(context.Background(), "collection_a", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.RevisionID != "revision_a" || got.RevisionDigest != digestA || got.DeploymentID != "deployment_a" {
		t.Fatalf("pointer = %#v", got)
	}
}

func TestEnvironmentPointerPrefersCanonicalActiveGenerationBinding(t *testing.T) {
	legacy := fixtureRepository()
	active := activeFakeRepository{
		fakeRepository: legacy,
		active: manageddata.EnvironmentPointer{
			CollectionID: "collection_a", Environment: "prod", RevisionID: "revision_b", DeploymentID: "publication_b",
		},
	}
	adapter, err := New(&active)
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.EnvironmentPointer(context.Background(), "collection_a", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.RevisionID != "revision_b" || got.RevisionDigest != digestB || got.DeploymentID != "publication_b" {
		t.Fatalf("active pointer = %#v", got)
	}
}

func mustNew(t *testing.T, repository *fakeRepository) *Adapter {
	t.Helper()
	adapter, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func fixtureRepository() *fakeRepository {
	return &fakeRepository{
		collections: map[string]manageddata.Collection{
			"project_a\x00orders": {ID: projectgraph.ResourceID("collection_a"), ProjectID: projectgraph.ResourceID("project_a"), ConnectionID: projectgraph.ResourceID("orders"), Status: manageddata.CollectionStatusActive},
		},
		revisions: []manageddata.Revision{
			{ID: manageddata.RevisionID("revision_a"), CollectionID: projectgraph.ResourceID("collection_a"), Sequence: 1, Digest: digestA, Status: manageddata.RevisionStatusReady},
			{ID: manageddata.RevisionID("revision_b"), CollectionID: projectgraph.ResourceID("collection_a"), Sequence: 2, Digest: digestB, Status: manageddata.RevisionStatusReady},
		},
		uploads: map[string]string{"revision_a": "upload_a"},
		pointer: manageddata.EnvironmentPointer{CollectionID: projectgraph.ResourceID("collection_a"), Environment: "prod", RevisionID: manageddata.RevisionID("revision_a"), DeploymentID: "deployment_a"},
	}
}

type fakeRepository struct {
	collections map[string]manageddata.Collection
	revisions   []manageddata.Revision
	uploads     map[string]string
	pointer     manageddata.EnvironmentPointer
}

type activeFakeRepository struct {
	*fakeRepository
	active manageddata.EnvironmentPointer
}

func (r *activeFakeRepository) ActiveEnvironmentPointer(context.Context, projectgraph.ResourceID, manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	return r.active, nil
}

func (r *fakeRepository) ListUploadSessions(_ context.Context, collectionID projectgraph.ResourceID) ([]manageddata.UploadSession, error) {
	return []manageddata.UploadSession{{ID: manageddata.UploadID("upload_a"), CollectionID: collectionID, CreatedAt: "2026-01-01T00:00:00Z"}}, nil
}

func (r *fakeRepository) CollectionByProjectConnection(_ context.Context, project, connection projectgraph.ResourceID) (manageddata.Collection, error) {
	value, ok := r.collections[project.String()+"\x00"+connection.String()]
	if !ok {
		return manageddata.Collection{}, manageddata.ErrNotFound
	}
	return value, nil
}

func (r *fakeRepository) RevisionByID(_ context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	for _, revision := range r.revisions {
		if revision.ID == id {
			return revision, nil
		}
	}
	return manageddata.Revision{}, manageddata.ErrNotFound
}

func (r *fakeRepository) ListRevisions(_ context.Context, collectionID projectgraph.ResourceID) ([]manageddata.Revision, error) {
	var result []manageddata.Revision
	for _, revision := range r.revisions {
		if revision.CollectionID == collectionID {
			result = append(result, revision)
		}
	}
	return result, nil
}

func (r *fakeRepository) UploadSessionIDByRevisionID(_ context.Context, revisionID manageddata.RevisionID) (manageddata.UploadID, error) {
	id, ok := r.uploads[revisionID.String()]
	if !ok {
		return "", manageddata.ErrNotFound
	}
	return manageddata.UploadID(id), nil
}

func (r *fakeRepository) ActiveEnvironmentPointer(context.Context, projectgraph.ResourceID, manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	return r.pointer, nil
}

func TestInterfaceConformance(t *testing.T) {
	var _ control.MetadataRepository = (*Adapter)(nil)
}
