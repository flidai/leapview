package publication

import (
	"context"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Action string

const (
	ActionSuspend Action = "suspend"
	ActionResume  Action = "resume"
	ActionRotate  Action = "rotate"
)

type ServiceRepository interface {
	GetByPublicID(context.Context, string) (Publication, error)
	Suspend(context.Context, projectgraph.ResourceID, string, string, int64) (Publication, error)
	Resume(context.Context, projectgraph.ResourceID, string, string, int64) (Publication, error)
	Rotate(context.Context, projectgraph.ResourceID, string, string, int64) (Publication, error)
}

type Service struct {
	repository ServiceRepository
	revoke     func(string)
}

func NewService(repository ServiceRepository, revoke func(string)) *Service {
	return &Service{repository: repository, revoke: revoke}
}

func (s *Service) ResolvePublic(ctx context.Context, publicID string) (Publication, error) {
	if s == nil || s.repository == nil {
		return Publication{}, ErrNotFound
	}
	row, err := s.repository.GetByPublicID(ctx, publicID)
	if err != nil || row.Status() != StatusActive {
		return Publication{}, ErrNotFound
	}
	return row, nil
}

func (s *Service) Mutate(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, action Action, expectedRevision int64) (Publication, error) {
	if s == nil || s.repository == nil {
		return Publication{}, ErrNotFound
	}
	var row Publication
	var err error
	switch action {
	case ActionSuspend:
		row, err = s.repository.Suspend(ctx, projectID, name, actorID, expectedRevision)
	case ActionResume:
		row, err = s.repository.Resume(ctx, projectID, name, actorID, expectedRevision)
	case ActionRotate:
		row, err = s.repository.Rotate(ctx, projectID, name, actorID, expectedRevision)
	default:
		return Publication{}, fmt.Errorf("%w: unsupported publication action %q", ErrConflict, action)
	}
	if err != nil {
		return Publication{}, err
	}
	if s.revoke != nil && (action == ActionSuspend || action == ActionRotate) {
		s.revoke(row.ID)
	}
	return row, nil
}
