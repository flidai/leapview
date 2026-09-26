package http

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// authorizeTypedCommand resolves the closed command union to its exact typed
// mutation action after decoding. An unknown command cannot fall back to a
// broad edit capability when a typed boundary is installed.
func (h AuthoringAPI) authorizeTypedCommand(ctx context.Context, projectID projectgraph.ResourceID, command authoring.Command) error {
	if h.AuthorizeTypedDashboardAction == nil {
		return nil
	}
	action, mapped := authoringCommandTypedAction(command)
	if !mapped {
		action = ""
	}
	typed, allowed, err := h.AuthorizeTypedDashboardAction(ctx, projectID, projectgraph.ResourceID(command.DashboardID), action)
	if err != nil {
		return err
	}
	if typed && (!mapped || !allowed) {
		return access.ErrForbidden
	}
	return nil
}
