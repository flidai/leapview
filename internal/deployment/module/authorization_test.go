package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

type publicationStateStore map[servingstate.ID]servingstate.State

func (s publicationStateStore) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	state, ok := s[id]
	if !ok {
		return servingstate.State{}, errors.New("state not found")
	}
	return state, nil
}

func TestPublicationAuthorizationUsesDashboardResourceCapabilities(t *testing.T) {
	states := publicationStateStore{"generation_1": {
		ID: "generation_1", ProjectID: "leapview-showcase", Environment: "prod",
		DashboardPublicationsJSON: `{"executive":{"dashboard":"executive-sales"},"website":{"dashboard":"visual-showcase"}}`,
	}}
	type decision struct {
		project    projectgraph.ResourceID
		resource   projectgraph.ResourceID
		capability access.Capability
	}
	var authorized []decision
	err := authorizePublicationDeployment(t.Context(), "principal:release", "prod", "generation_1", PublicationAuthorizationConfig{
		States: states,
		AuthorizeResource: func(_ context.Context, actor string, project projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			require.Equal(t, "principal:release", actor)
			require.Equal(t, projectgraph.KindDashboard, resource.Kind())
			authorized = append(authorized, decision{project: project, resource: resource.ID(), capability: capability})
			return true, nil
		},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []decision{
		{project: "leapview-showcase", resource: "executive-sales", capability: access.CapabilityResourcePublish},
		{project: "leapview-showcase", resource: "visual-showcase", capability: access.CapabilityResourcePublish},
	}, authorized)
}

func TestPublicationAuthorizationRejectsDeniedDashboard(t *testing.T) {
	states := publicationStateStore{"generation_1": {
		ID: "generation_1", ProjectID: "leapview-showcase", Environment: "prod",
		DashboardPublicationsJSON: `{"website":{"dashboard":"visual-showcase"}}`,
	}}
	err := authorizePublicationDeployment(t.Context(), "principal:viewer", "prod", "generation_1", PublicationAuthorizationConfig{
		States: states,
		AuthorizeResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return false, nil
		},
	})
	require.ErrorIs(t, err, ErrPublicationForbidden)
}

func TestPublicationAuthorizationSkipsNonProductionAndEmptySnapshots(t *testing.T) {
	states := publicationStateStore{
		"generation_dev":   {ID: "generation_dev", ProjectID: "project", Environment: "dev", DashboardPublicationsJSON: `{"website":{"dashboard":"showcase"}}`},
		"generation_empty": {ID: "generation_empty", ProjectID: "project", Environment: "prod", DashboardPublicationsJSON: `{}`},
	}
	config := PublicationAuthorizationConfig{
		States: states,
		AuthorizeResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			t.Fatal("authorization called for an ungoverned deployment")
			return false, nil
		},
	}
	require.NoError(t, authorizePublicationDeployment(t.Context(), "principal:viewer", "dev", "generation_dev", config))
	require.NoError(t, authorizePublicationDeployment(t.Context(), "principal:viewer", "prod", "generation_empty", config))
}

func TestPublicationAuthorizationRejectsInvalidEvidence(t *testing.T) {
	for name, state := range map[string]servingstate.State{
		"project":   {ID: "generation_1", Environment: "prod", DashboardPublicationsJSON: `{}`},
		"snapshot":  {ID: "generation_1", ProjectID: "project", Environment: "prod", DashboardPublicationsJSON: `{`},
		"dashboard": {ID: "generation_1", ProjectID: "project", Environment: "prod", DashboardPublicationsJSON: `{"website":{"dashboard":" dashboard"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			err := authorizePublicationDeployment(t.Context(), "principal:release", "prod", "generation_1", PublicationAuthorizationConfig{
				States: publicationStateStore{"generation_1": state},
				AuthorizeResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
					return true, nil
				},
			})
			require.Error(t, err)
		})
	}
}
