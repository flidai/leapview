package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ResolveTusTarget resolves the project and connection owning a resumable
// upload. The TUS URL contains only the opaque staging upload ID, so callers
// must resolve its session metadata rather than infer scope from that ID.
// Unknown IDs are reported as managed-data not-found and backend failures are
// kept distinguishable for the caller's response policy.
func (m *Module) ResolveTusTarget(ctx context.Context, uploadID string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
	if m == nil || m.resolveTusTarget == nil {
		return "", "", fmt.Errorf("%w: tus target resolver is unavailable", control.ErrBackend)
	}
	return m.resolveTusTarget(ctx, uploadID)
}

type tusTargetRepository interface {
	UploadSessionByID(context.Context, manageddata.UploadID) (manageddata.UploadSession, error)
	CollectionByID(context.Context, projectgraph.ResourceID) (manageddata.Collection, error)
}

func newTusTargetResolver(engine storage.ResumableUploadEngine, repository tusTargetRepository) func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
	if engine == nil || repository == nil {
		return nil
	}
	return func(ctx context.Context, uploadID string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
		if uploadID == "" || uploadID != strings.TrimSpace(uploadID) {
			return "", "", fmt.Errorf("%w: upload id is required", control.ErrInvalid)
		}
		staged, err := engine.Resume(ctx, uploadID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return "", "", control.ErrNotFound
			}
			return "", "", fmt.Errorf("%w: resolve tus staging: %v", control.ErrBackend, err)
		}
		if staged.ID != uploadID {
			return "", "", fmt.Errorf("%w: tus staging id mismatch", control.ErrIntegrity)
		}
		sessionID := staged.Metadata["session_id"]
		parsedSessionID, err := manageddata.ParseUploadID(sessionID)
		if err != nil {
			return "", "", control.ErrNotFound
		}
		session, err := repository.UploadSessionByID(ctx, parsedSessionID)
		if err != nil {
			if errors.Is(err, manageddata.ErrNotFound) || errors.Is(err, control.ErrNotFound) {
				return "", "", control.ErrNotFound
			}
			return "", "", fmt.Errorf("%w: resolve upload session: %v", control.ErrBackend, err)
		}
		if session.ID != parsedSessionID {
			return "", "", fmt.Errorf("%w: upload session id mismatch", control.ErrIntegrity)
		}
		collection, err := repository.CollectionByID(ctx, session.CollectionID)
		if err != nil {
			if errors.Is(err, manageddata.ErrNotFound) || errors.Is(err, control.ErrNotFound) {
				return "", "", control.ErrNotFound
			}
			return "", "", fmt.Errorf("%w: resolve upload collection: %v", control.ErrBackend, err)
		}
		if collection.ID != session.CollectionID {
			return "", "", fmt.Errorf("%w: upload collection id mismatch", control.ErrIntegrity)
		}
		projectID, err := projectgraph.NewResourceID(collection.ProjectID.String())
		if err != nil {
			return "", "", fmt.Errorf("%w: invalid upload project: %v", control.ErrIntegrity, err)
		}
		connectionID, err := projectgraph.NewResourceID(collection.ConnectionID.String())
		if err != nil {
			return "", "", fmt.Errorf("%w: invalid upload connection: %v", control.ErrIntegrity, err)
		}
		return projectID, connectionID, nil
	}
}
