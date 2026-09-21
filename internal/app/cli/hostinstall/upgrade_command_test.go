//go:build cgo && duckdb_arrow

package hostinstall

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostPayloadControllerExposesUpgradeCommand(t *testing.T) {
	host := Command(t.Context(), CommandOptions{})
	command, _, err := host.Find([]string{"upgrade"})
	require.NoError(t, err)
	require.Equal(t, "upgrade", command.Name())
	require.NotNil(t, command.Flags().Lookup("operation-id"))
	require.NotNil(t, command.Flags().Lookup("candidate-image"))
	require.NotNil(t, command.Flags().Lookup("phase"))
}
