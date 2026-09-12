package module

import (
	"context"
	"errors"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (m *Module) ActivateDashboardPublicationThroughPersistence(ctx context.Context, tx any, projectID projectgraph.ResourceID, name string) error {
	if m == nil || m.persistence == nil || m.persistence.Publication == nil {
		return errors.New("dashboard publication activator is not configured")
	}
	return m.persistence.Publication.ActivateDashboardPublicationPrincipal(ctx, tx, projectID, name)
}
