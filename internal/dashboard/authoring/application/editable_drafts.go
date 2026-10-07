package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
)

// EditableDrafts returns draft dashboards the actor can edit. Catalog.List is
// intentionally VIEW-scoped and omits unpublished drafts, so this separate
// read path enumerates only draft lifecycle candidates and reuses Draft's
// EDIT authorization and exact-revision checks before returning their source.
func (a *Application) EditableDrafts(ctx context.Context, request catalog.ListRequest) ([]DraftRead, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return nil, err
	}
	actorID := strings.TrimSpace(request.ActorID)
	if actorID == "" {
		return nil, fmt.Errorf("actor id is required")
	}
	lifecycles, err := a.repository.List(ctx, project)
	if err != nil {
		return nil, err
	}
	editable := make([]DraftRead, 0, len(lifecycles))
	for _, lifecycle := range lifecycles {
		if lifecycle.Status != authoring.LifecycleStatusDraft || lifecycle.Draft == nil {
			continue
		}
		read, err := a.Draft(ctx, DraftRequest{ProjectID: project, ActorID: actorID, DashboardID: lifecycle.ID})
		if err != nil {
			if errors.Is(err, access.ErrForbidden) || errors.Is(err, authoring.ErrNotFound) {
				continue
			}
			return nil, err
		}
		editable = append(editable, read)
	}
	return editable, nil
}
