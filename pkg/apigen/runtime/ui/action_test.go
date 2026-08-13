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
	require.True(t, action.Valid())
}

func TestNewActionRejectsUnstableActionID(t *testing.T) {
	_, err := NewAction("Create Role Binding", "createRoleBinding")
	require.ErrorIs(t, err, ErrInvalidAction)
}
