package module

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	dashboardpublication "github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

type PublicationAuthorizationConfig struct {
	States interface {
		ByID(context.Context, servingstate.ID) (servingstate.State, error)
	}
	AuthorizeResource func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error)
	Bypass            func(string) bool
}

func (m *Module) publicationAuthorizer(config PublicationAuthorizationConfig) func(context.Context, string, string, string) error {
	if config.States == nil {
		return nil
	}
	return func(ctx context.Context, actor, environment, generationID string) error {
		return authorizePublicationDeployment(ctx, actor, environment, generationID, config)
	}
}

func (m *Module) AuthorizePublicationDeployment(ctx context.Context, actor, environment, generationID string) error {
	if m == nil || m.jobs.Authorize == nil {
		return nil
	}
	return m.jobs.Authorize(ctx, actor, environment, generationID)
}

func authorizePublicationDeployment(ctx context.Context, actor, environment, generationID string, config PublicationAuthorizationConfig) error {
	environmentValue := servingstate.Environment(environment)
	if environmentValue != servingstate.Environment("prod") {
		return nil
	}
	state, err := config.States.ByID(ctx, servingstate.ID(generationID))
	if err != nil {
		return err
	}
	if err := state.ProjectID.Validate(); err != nil {
		return fmt.Errorf("publication serving state %q has no project identity", generationID)
	}
	var configured map[string]dashboardpublication.Definition
	if state.DashboardPublicationsJSON != "" {
		if err := json.Unmarshal([]byte(state.DashboardPublicationsJSON), &configured); err != nil {
			return err
		}
	}
	if len(configured) == 0 || config.Bypass != nil && config.Bypass(actor) {
		return nil
	}
	if config.AuthorizeResource == nil {
		return ErrPublicationForbidden
	}
	seen := make(map[projectgraph.ResourceID]struct{}, len(configured))
	for _, definition := range configured {
		dashboardID, err := projectgraph.NewResourceID(definition.Dashboard)
		if err != nil {
			return fmt.Errorf("publication dashboard identity is invalid: %w", err)
		}
		if _, ok := seen[dashboardID]; ok {
			continue
		}
		seen[dashboardID] = struct{}{}
		resource, err := access.NewResourceRef(dashboardID, projectgraph.KindDashboard)
		if err != nil {
			return err
		}
		allowed, err := config.AuthorizeResource(ctx, actor, state.ProjectID, resource, access.CapabilityResourcePublish)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrPublicationForbidden
		}
	}
	return nil
}
