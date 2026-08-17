package module

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type tusTargetEngine struct {
	upload storage.Upload
	err    error
}

func (e tusTargetEngine) Create(context.Context, storage.CreateUpload) (storage.Upload, error) {
	return storage.Upload{}, errors.New("unsupported")
}
func (e tusTargetEngine) Resume(context.Context, string) (storage.Upload, error) {
	return e.upload, e.err
}
func (e tusTargetEngine) WriteChunk(context.Context, string, int64, io.Reader) (storage.Upload, error) {
	return storage.Upload{}, errors.New("unsupported")
}
func (e tusTargetEngine) Finalize(context.Context, string, storage.Blob) (storage.Blob, error) {
	return storage.Blob{}, errors.New("unsupported")
}
func (e tusTargetEngine) Abort(context.Context, string) error { return errors.New("unsupported") }

type tusTargetRepo struct {
	session    manageddata.UploadSession
	collection manageddata.Collection
	sessionErr error
	collectErr error
}

func (r tusTargetRepo) UploadSessionByID(context.Context, manageddata.UploadID) (manageddata.UploadSession, error) {
	return r.session, r.sessionErr
}
func (r tusTargetRepo) CollectionByID(context.Context, projectgraph.ResourceID) (manageddata.Collection, error) {
	return r.collection, r.collectErr
}

func TestResolveTusTarget(t *testing.T) {
	validSession := manageddata.UploadID("session-1")
	baseRepo := tusTargetRepo{
		session: manageddata.UploadSession{ID: validSession, CollectionID: projectgraph.ResourceID("collection-1")},
		collection: manageddata.Collection{
			ID: projectgraph.ResourceID("collection-1"), ProjectID: projectgraph.ResourceID("project-1"), ConnectionID: projectgraph.ResourceID("connection-1"),
		},
	}
	tests := []struct {
		name      string
		uploadID  string
		engine    tusTargetEngine
		repo      tusTargetRepo
		wantProj  projectgraph.ResourceID
		wantConn  projectgraph.ResourceID
		wantError error
	}{
		{name: "valid", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: baseRepo, wantProj: "project-1", wantConn: "connection-1"},
		{name: "unknown staging", uploadID: "tus-1", engine: tusTargetEngine{err: storage.ErrNotFound}, repo: baseRepo, wantError: control.ErrNotFound},
		{name: "unknown session", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: tusTargetRepo{sessionErr: manageddata.ErrNotFound}, wantError: control.ErrNotFound},
		{name: "backend", uploadID: "tus-1", engine: tusTargetEngine{err: errors.New("disk offline")}, repo: baseRepo, wantError: control.ErrBackend},
		{name: "staging id mismatch", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "other", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: baseRepo, wantError: control.ErrIntegrity},
		{name: "session id mismatch", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: tusTargetRepo{session: manageddata.UploadSession{ID: "other", CollectionID: baseRepo.session.CollectionID}, collection: baseRepo.collection}, wantError: control.ErrIntegrity},
		{name: "collection id mismatch", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: tusTargetRepo{session: baseRepo.session, collection: manageddata.Collection{ID: "other", ProjectID: "project-1", ConnectionID: "connection-1"}}, wantError: control.ErrIntegrity},
		{name: "invalid staging id", uploadID: " tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: baseRepo, wantError: control.ErrInvalid},
		{name: "invalid session id", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": "bad session"}}}, repo: baseRepo, wantError: control.ErrNotFound},
		{name: "invalid project", uploadID: "tus-1", engine: tusTargetEngine{upload: storage.Upload{ID: "tus-1", Metadata: map[string]string{"session_id": validSession.String()}}}, repo: tusTargetRepo{session: baseRepo.session, collection: manageddata.Collection{ProjectID: "bad project", ConnectionID: "connection-1"}}, wantError: control.ErrIntegrity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolve := newTusTargetResolver(test.engine, test.repo)
			projectID, connectionID, err := resolve(context.Background(), test.uploadID)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("error = %v, want %v", err, test.wantError)
				}
				return
			}
			if err != nil || projectID != test.wantProj || connectionID != test.wantConn {
				t.Fatalf("target = %q/%q, err = %v; want %q/%q", projectID, connectionID, err, test.wantProj, test.wantConn)
			}
		})
	}
}
