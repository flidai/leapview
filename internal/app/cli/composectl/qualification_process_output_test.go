package composectl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationCommandSeparatesProgressFromSuccessfulJSON(t *testing.T) {
	output, err := (osQualificationCommandExecutor{}).Execute(t.Context(), qualificationCommandRequest{
		Executable: "sh", StdoutOnly: true,
		Arguments: []string{"-c", `printf 'Container starting\n' >&2; printf '{"ok":true}\n'`},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(output))
}

func TestQualificationCommandRetainsBothStreamsOnFailure(t *testing.T) {
	output, err := (osQualificationCommandExecutor{}).Execute(t.Context(), qualificationCommandRequest{
		Executable: "sh", StdoutOnly: true,
		Arguments: []string{"-c", `printf 'partial result\n'; printf 'bootstrap failed\n' >&2; exit 7`},
	})
	require.Error(t, err)
	require.Contains(t, string(output), "partial result")
	require.Contains(t, string(output), "bootstrap failed")
}

func TestQualificationCommandKeepsStderrForLogCollection(t *testing.T) {
	output, err := (osQualificationCommandExecutor{}).Execute(t.Context(), qualificationCommandRequest{
		Executable: "sh",
		Arguments:  []string{"-c", `printf 'application log\n' >&2`},
	})
	require.NoError(t, err)
	require.Equal(t, "application log", strings.TrimSpace(string(output)))
}
