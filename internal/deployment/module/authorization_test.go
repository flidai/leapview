package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

type publicationStateStore map[servingstate.ID]servingstate.State

func (s publicationStateStore) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	return s[id], nil
}

func TestPublicationAuthorizationUsesProjectEnvironmentBoundary(t *testing.T) {
	states := publicationStateStore{
		"state_sales": {
			ID: "state_sales", WorkspaceID: "sales", ProjectID: "leapview-showcase",
			DashboardPublicationsJSON: `{"executive-sales":{}}`,
		},
		"state_visuals": {
			ID: "state_visuals", WorkspaceID: "visuals", ProjectID: "leapview-showcase",
			DashboardPublicationsJSON: `{"visual-showcase":{}}`,
		},
	}
	var authorized []access.ObjectRef
	err := authorizePublicationDeployment(t.Context(), "release-principal", "prod", []apiadapter.TargetRequest{
		{Workspace: "sales", CandidateID: "state_sales"},
		{Workspace: "visuals", CandidateID: "state_visuals"},
	}, PublicationAuthorizationConfig{
		States: states,
		AuthorizeObject: func(_ context.Context, actor string, privilege access.Privilege, object access.ObjectRef) (bool, error) {
			require.Equal(t, "release-principal", actor)
			require.Equal(t, access.PrivilegeManagePublications, privilege)
			authorized = append(authorized, object)
			return true, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, []access.ObjectRef{access.ProjectEnvironmentObject("leapview-showcase", "prod")}, authorized)
}

func TestPublicationAuthorizationRejectsMixedProjects(t *testing.T) {
	states := publicationStateStore{
		"state_sales": {
			ID: "state_sales", WorkspaceID: "sales", ProjectID: "leapview-showcase",
			DashboardPublicationsJSON: `{"executive-sales":{}}`,
		},
		"state_other": {
			ID: "state_other", WorkspaceID: "other", ProjectID: "other-project",
			DashboardPublicationsJSON: `{"other":{}}`,
		},
	}
	err := authorizePublicationDeployment(t.Context(), "release-principal", "prod", []apiadapter.TargetRequest{
		{Workspace: "sales", CandidateID: "state_sales"},
		{Workspace: "other", CandidateID: "state_other"},
	}, PublicationAuthorizationConfig{
		States: states,
		AuthorizeObject: func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error) {
			t.Fatal("mixed-project deployment reached authorization")
			return false, nil
		},
	})
	require.ErrorContains(t, err, "multiple projects")
}
