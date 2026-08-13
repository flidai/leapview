package module

import (
	"context"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/workspace"
)

func (m *Module) SecurableObjects(ctx context.Context) ([]access.ObjectRef, error) {
	objects := make([]access.ObjectRef, 0)
	seen := map[string]struct{}{}
	appendWorkspace := func(id, title string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		object := access.WorkspaceObject(id)
		object.DisplayName = title
		objects = append(objects, object)
		seen[id] = struct{}{}
	}
	if m == nil || m.readModel == nil {
		return objects, nil
	}
	rows, err := m.readModel.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		appendWorkspace(string(row.ID), row.Title)
	}
	return objects, nil
}

func (m *Module) ActiveServingStateID(ctx context.Context, workspaceID string) (string, error) {
	if m == nil || m.readModel == nil {
		return "", nil
	}
	row, err := m.readModel.ByID(ctx, workspace.WorkspaceID(workspaceID))
	return string(row.ActiveServingStateID), err
}
