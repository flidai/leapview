package module

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/servingstate"
)

type PublicationAuthorizationConfig struct {
	States interface {
		ByID(context.Context, servingstate.ID) (servingstate.State, error)
	}
	AuthorizeObject func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error)
	Bypass          func(string) bool
}

func (m *Module) publicationAuthorizer(config PublicationAuthorizationConfig) func(context.Context, string, string, []apiadapter.TargetRequest) error {
	if config.States == nil {
		return nil
	}
	return func(ctx context.Context, actor, environment string, targets []apiadapter.TargetRequest) error {
		return authorizePublicationDeployment(ctx, actor, environment, targets, config)
	}
}

func (m *Module) AuthorizePublicationDeployment(ctx context.Context, actor, environment string, targets []apiadapter.TargetRequest) error {
	if m == nil || m.jobs.Authorize == nil {
		return nil
	}
	return m.jobs.Authorize(ctx, actor, environment, targets)
}

func authorizePublicationDeployment(ctx context.Context, actor, environment string, targets []apiadapter.TargetRequest, config PublicationAuthorizationConfig) error {
	normalizedEnvironment := servingstate.NormalizeEnvironment(servingstate.Environment(environment))
	if normalizedEnvironment != servingstate.Environment("prod") {
		return nil
	}
	projectID := ""
	requiresAuthorization := false
	for _, target := range targets {
		state, err := config.States.ByID(ctx, servingstate.ID(target.CandidateID))
		if err != nil {
			return err
		}
		stateProjectID := strings.TrimSpace(state.ProjectID)
		if stateProjectID == "" {
			return fmt.Errorf("publication serving state %q has no project identity", target.CandidateID)
		}
		if projectID == "" {
			projectID = stateProjectID
		} else if projectID != stateProjectID {
			return fmt.Errorf("publication deployment spans multiple projects: %q and %q", projectID, stateProjectID)
		}
		var configured map[string]json.RawMessage
		if state.DashboardPublicationsJSON != "" {
			if err := json.Unmarshal([]byte(state.DashboardPublicationsJSON), &configured); err != nil {
				return err
			}
		}
		if len(configured) > 0 {
			requiresAuthorization = true
		}
	}
	if !requiresAuthorization || config.Bypass != nil && config.Bypass(actor) {
		return nil
	}
	if config.AuthorizeObject == nil {
		return ErrPublicationForbidden
	}
	allowed, err := config.AuthorizeObject(
		ctx,
		actor,
		access.PrivilegeManagePublications,
		access.ProjectEnvironmentObject(projectID, string(normalizedEnvironment)),
	)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrPublicationForbidden
	}
	return nil
}
