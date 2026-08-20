// Package apiadapter maps managed-data metadata to transport-neutral control contracts.
package apiadapter

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var canonicalDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Repository interface {
	CollectionByProjectConnection(context.Context, projectgraph.ResourceID, projectgraph.ResourceID) (manageddata.Collection, error)
	RevisionByID(context.Context, manageddata.RevisionID) (manageddata.Revision, error)
	ListRevisions(context.Context, projectgraph.ResourceID) ([]manageddata.Revision, error)
	ListUploadSessions(context.Context, projectgraph.ResourceID) ([]manageddata.UploadSession, error)
	UploadSessionIDByRevisionID(context.Context, manageddata.RevisionID) (manageddata.UploadID, error)
	ActiveEnvironmentPointer(context.Context, projectgraph.ResourceID, manageddata.Environment) (manageddata.EnvironmentPointer, error)
}

func (a *Adapter) ListUploadSessions(ctx context.Context, collectionID string) ([]manageddata.UploadSession, error) {
	if collectionID != strings.TrimSpace(collectionID) {
		return nil, control.ErrInvalid
	}
	parsedCollectionID, err := projectgraph.NewResourceID(collectionID)
	if err != nil {
		return nil, control.ErrInvalid
	}
	rows, err := a.repository.ListUploadSessions(ctx, parsedCollectionID)
	if err != nil {
		return nil, publicError(err)
	}
	for _, row := range rows {
		if row.CollectionID != parsedCollectionID {
			return nil, control.ErrBackend
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt == rows[j].CreatedAt {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt > rows[j].CreatedAt
	})
	return rows, nil
}

type Adapter struct {
	repository Repository
}

func New(repository Repository) (*Adapter, error) {
	if repository == nil {
		return nil, fmt.Errorf("managed-data repository is required")
	}
	return &Adapter{repository: repository}, nil
}

func (a *Adapter) CollectionByProjectConnection(ctx context.Context, project, connection string) (manageddata.Collection, error) {
	if project != strings.TrimSpace(project) || connection != strings.TrimSpace(connection) {
		return manageddata.Collection{}, control.ErrInvalid
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		return manageddata.Collection{}, control.ErrInvalid
	}
	connectionID, err := projectgraph.NewResourceID(connection)
	if err != nil {
		return manageddata.Collection{}, control.ErrInvalid
	}
	collection, err := a.repository.CollectionByProjectConnection(ctx, projectID, connectionID)
	if err != nil {
		return manageddata.Collection{}, publicError(err)
	}
	if collection.ProjectID != projectID || collection.ConnectionID != connectionID || collection.Status != manageddata.CollectionStatusActive {
		return manageddata.Collection{}, control.ErrNotFound
	}
	return collection, nil
}

func (a *Adapter) RevisionByID(ctx context.Context, collectionID, publicID string) (control.RevisionMetadata, error) {
	if collectionID != strings.TrimSpace(collectionID) || publicID != strings.TrimSpace(publicID) {
		return control.RevisionMetadata{}, control.ErrInvalid
	}
	parsedCollectionID, collectionErr := projectgraph.NewResourceID(collectionID)
	if collectionErr != nil || !canonicalDigest.MatchString(publicID) {
		return control.RevisionMetadata{}, control.ErrInvalid
	}
	revision, err := a.scopedRevisionByDigest(ctx, parsedCollectionID, publicID)
	if err != nil {
		return control.RevisionMetadata{}, err
	}
	return a.revisionMetadata(ctx, revision)
}

func (a *Adapter) ListRevisions(ctx context.Context, collectionID string) ([]control.RevisionMetadata, error) {
	if collectionID != strings.TrimSpace(collectionID) {
		return nil, control.ErrInvalid
	}
	parsedCollectionID, parseErr := projectgraph.NewResourceID(collectionID)
	if parseErr != nil {
		return nil, control.ErrInvalid
	}
	rows, err := a.repository.ListRevisions(ctx, parsedCollectionID)
	if err != nil {
		return nil, publicError(err)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sequence == rows[j].Sequence {
			return rows[i].Digest > rows[j].Digest
		}
		return rows[i].Sequence > rows[j].Sequence
	})
	out := make([]control.RevisionMetadata, 0, len(rows))
	for _, revision := range rows {
		if revision.CollectionID != parsedCollectionID {
			return nil, control.ErrBackend
		}
		if revision.Status != manageddata.RevisionStatusReady {
			continue
		}
		metadata, metadataErr := a.revisionMetadata(ctx, revision)
		if metadataErr != nil {
			return nil, metadataErr
		}
		out = append(out, metadata)
	}
	return out, nil
}

func (a *Adapter) EnvironmentPointer(ctx context.Context, collectionID string, environment manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	if collectionID != strings.TrimSpace(collectionID) {
		return manageddata.EnvironmentPointer{}, control.ErrInvalid
	}
	parsedCollectionID, parseErr := projectgraph.NewResourceID(collectionID)
	if parseErr != nil {
		return manageddata.EnvironmentPointer{}, control.ErrInvalid
	}
	pointer, err := a.repository.ActiveEnvironmentPointer(ctx, parsedCollectionID, environment)
	if err != nil {
		return manageddata.EnvironmentPointer{}, publicError(err)
	}
	if pointer.CollectionID != parsedCollectionID || pointer.Environment != environment {
		return manageddata.EnvironmentPointer{}, control.ErrNotFound
	}
	revision, err := a.repository.RevisionByID(ctx, pointer.RevisionID)
	if err != nil {
		return manageddata.EnvironmentPointer{}, publicError(err)
	}
	if revision.CollectionID != parsedCollectionID || revision.Status != manageddata.RevisionStatusReady || !canonicalDigest.MatchString(revision.Digest) {
		return manageddata.EnvironmentPointer{}, control.ErrBackend
	}
	pointer.RevisionDigest = revision.Digest
	return pointer, nil
}

func (a *Adapter) revisionMetadata(ctx context.Context, revision manageddata.Revision) (control.RevisionMetadata, error) {
	if revision.Status != manageddata.RevisionStatusReady || !canonicalDigest.MatchString(revision.Digest) {
		return control.RevisionMetadata{}, control.ErrNotFound
	}
	uploadID, err := a.repository.UploadSessionIDByRevisionID(ctx, revision.ID)
	if err != nil {
		return control.RevisionMetadata{}, publicError(err)
	}
	if uploadID.String() == "" {
		return control.RevisionMetadata{}, control.ErrBackend
	}
	return control.RevisionMetadata{Revision: revision, PublicID: revision.Digest, UploadSessionID: uploadID.String()}, nil
}

func (a *Adapter) scopedRevisionByDigest(ctx context.Context, collectionID projectgraph.ResourceID, digest string) (manageddata.Revision, error) {
	rows, err := a.repository.ListRevisions(ctx, collectionID)
	if err != nil {
		return manageddata.Revision{}, publicError(err)
	}
	var found *manageddata.Revision
	for index := range rows {
		if rows[index].CollectionID != collectionID {
			return manageddata.Revision{}, control.ErrBackend
		}
		if rows[index].Digest != digest || rows[index].Status != manageddata.RevisionStatusReady {
			continue
		}
		if found != nil {
			return manageddata.Revision{}, control.ErrBackend
		}
		copy := rows[index]
		found = &copy
	}
	if found == nil {
		return manageddata.Revision{}, control.ErrNotFound
	}
	return *found, nil
}

func publicError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, manageddata.ErrNotFound), errors.Is(err, control.ErrNotFound):
		return control.ErrNotFound
	case errors.Is(err, manageddata.ErrConflict), errors.Is(err, control.ErrConflict):
		return control.ErrConflict
	case errors.Is(err, control.ErrInvalid):
		return control.ErrInvalid
	default:
		return control.ErrBackend
	}
}

var _ control.MetadataRepository = (*Adapter)(nil)
