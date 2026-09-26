package service

import (
	"context"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/project/graph"
)

// authorizeDependencyChange proves authority over a newly selected semantic
// model after reduction but before the revised draft can be retained.
func (s *Service) authorizeDependencyChange(ctx context.Context, projectID graph.ResourceID, current, next authoring.DashboardLifecycle, command authoring.Command) error {
	return s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: command.Provenance.ActorID, ProjectID: projectID, DashboardID: current.ID,
		OwnerPrincipalID: current.OwnerPrincipalID, SemanticModel: next.SemanticModel,
		DependencyChange: true, Target: AuthorizationTargetAuthoredDashboard,
		Visibility: next.Visibility, Action: authoring.AuthorizationActionEdit,
	})
}
