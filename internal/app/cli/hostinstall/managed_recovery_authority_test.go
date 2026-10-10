package hostinstall

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestManagedAuthorityInitializationCommandRequiresPrivateInput(t *testing.T) {
	command := Command(t.Context(), CommandOptions{})
	command.SetArgs([]string{"init-managed-recovery-authority"})
	require.ErrorContains(t, command.Execute(), `required flag(s) "input" not set`)
	command = Command(t.Context(), CommandOptions{})
	command.SetArgs([]string{"init-managed-recovery-authority", "--input", "/does-not-exist"})
	require.ErrorContains(t, command.Execute(), "private managed authority initialization input unavailable")
}
