package sqlite

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/stretchr/testify/require"
)

func TestDecodeAccessPolicyRejectsTrailingJSON(t *testing.T) {
	if _, err := decodeAccessPolicyJSON(`{} {"dataPolicies":{}}`); err == nil {
		t.Fatal("decodeAccessPolicyJSON accepted multiple JSON values")
	}
}

func TestInstallPolicyPreservesProjectEnvironmentGrants(t *testing.T) {
	_, repository := openAccessRepo(t, t.Context())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "release_controller", Kind: access.PrincipalKindServicePrincipal,
		DisplayName: "Release controller",
	})
	require.NoError(t, err)
	projectGrant, err := repository.CreateGrant(t.Context(), access.GrantInput{
		Object:      access.ProjectEnvironmentObject("test", "prod"),
		SubjectType: access.SubjectServicePrincipal, SubjectID: principal.ID,
		Privilege: access.PrivilegeApproveDeployment,
	})
	require.NoError(t, err)
	workspaceGrant, err := repository.CreateGrant(t.Context(), access.GrantInput{
		Object:      access.WorkspaceObject("test"),
		SubjectType: access.SubjectServicePrincipal, SubjectID: principal.ID,
		Privilege: access.PrivilegeViewItem,
	})
	require.NoError(t, err)

	require.NoError(t, installPolicy(t.Context(), repository, "test", accesssnapshot.AccessPolicy{}))

	_, err = repository.GetGrant(t.Context(), "test", projectGrant.ID)
	require.NoError(t, err)
	_, err = repository.GetGrant(t.Context(), "test", workspaceGrant.ID)
	require.Error(t, err)
}
