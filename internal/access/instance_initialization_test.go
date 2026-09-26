package access

import (
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestInitialProjectClaimPermissionsAreInstanceScoped(t *testing.T) {
	pairs, err := InitialProjectClaimPermissions("instance_demo")
	require.NoError(t, err)
	require.Len(t, pairs, 1)
	require.Equal(t, PermissionCatalogProfile, pairs[0].Profile)
	require.Equal(t, ActionInstanceProjectClaim, pairs[0].Action)
	require.Equal(t, PermissionTarget{Scope: PermissionScopeInstance, InstanceID: "instance_demo"}, pairs[0].Target)
	require.NoError(t, ValidatePermissionPairs(pairs))

	_, err = InitialProjectClaimPermissions("")
	require.ErrorIs(t, err, ErrInvalidPermissionPair)
}

func TestInitialProjectPublisherPermissionsCaptureBootstrapRoles(t *testing.T) {
	projectID := graph.ResourceID("project_demo")
	pairs, err := InitialProjectPublisherPermissions(projectID)
	require.NoError(t, err)
	require.Len(t, pairs, 32)
	require.NoError(t, ValidatePermissionPairs(pairs))

	byAction := make(map[Action]PermissionPair, len(pairs))
	lastKey := ""
	for _, pair := range pairs {
		require.Equal(t, PermissionCatalogProfile, pair.Profile)
		require.Equal(t, projectID, pair.Target.ProjectID)
		require.NotEqual(t, ActionConnectionManage, pair.Action)
		require.Greater(t, pair.Key(), lastKey, "publisher permission pairs must have canonical sorted order")
		lastKey = pair.Key()
		byAction[pair.Action] = pair
	}

	for _, action := range []Action{
		ActionProjectAccessManage,
		ActionDashboardCreate,
		ActionConnectionUse,
		ActionDeliveryPublish,
		ActionDeliveryRollback,
	} {
		require.Contains(t, byAction, action)
	}
	for _, action := range []Action{ActionConnectionManage, ActionDeliveryApprove, ActionDashboardDelete} {
		require.NotContains(t, byAction, action)
	}
	require.Equal(t, PermissionScopeProject, byAction[ActionProjectAccessManage].Target.Scope)
	require.Equal(t, PermissionScopeProject, byAction[ActionDashboardCreate].Target.Scope)
	require.True(t, byAction[ActionConnectionUse].Target.IncludeFuture)
	require.Equal(t, graph.KindConnection, byAction[ActionConnectionUse].Target.ResourceKind)
	require.Equal(t, PermissionScopeProject, byAction[ActionDeliveryRollback].Target.Scope)

	_, err = InitialProjectPublisherPermissions("")
	require.ErrorIs(t, err, graph.ErrInvalidResourceID)
}

func TestInitialProjectClaimPublisherTokenNameTrimsCredentialID(t *testing.T) {
	require.Equal(t, APITokenNameInitialProjectClaimPublisherPrefix+"claim_123", InitialProjectClaimPublisherTokenName("  claim_123 \t"))
}
