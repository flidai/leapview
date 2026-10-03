package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewAction(t *testing.T) {
	action, err := NewAction("workspace.access.role-binding.create", "createRoleBinding")
	require.NoError(t, err)
	require.Equal(t, "workspace.access.role-binding.create", action.ActionID())
	require.Equal(t, "createRoleBinding", action.OperationID())
	require.False(t, action.ReplayForbidden())
	require.True(t, action.Valid())
}

func TestNewNonReplayableAction(t *testing.T) {
	action, err := NewNonReplayableAction("credential.draft.create", "createCredentialDraft")
	require.NoError(t, err)
	require.True(t, action.ReplayForbidden())
	require.True(t, action.Valid())
	require.True(t, MustNonReplayableAction("credential.draft.create", "createCredentialDraft").ReplayForbidden())
}

func TestNewActionRejectsUnstableActionID(t *testing.T) {
	_, err := NewAction("Create Role Binding", "createRoleBinding")
	require.ErrorIs(t, err, ErrInvalidAction)
}
