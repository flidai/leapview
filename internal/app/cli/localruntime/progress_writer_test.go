package localruntime

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type progressErrorWriter struct{ err error }

func (writer progressErrorWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestReadyProgressWriterFailurePreservesAppliedRuntime(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, &fakeRunner{artifacts: testQualificationArtifacts(t)})
	failure := errors.New("progress stream closed")
	options.Stdout = progressErrorWriter{failure}
	controller, err := New(options)
	require.NoError(t, err)
	state, err := controller.Start(t.Context())
	require.ErrorIs(t, err, failure)
	require.Equal(t, statusApplied, state.Status)
	require.Equal(t, phaseReady, state.Phase)
	retained, exists, err := loadState(filepath.Join(stateDirectory(stateRoot, state.Checkout.ID), stateFileName))
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, statusApplied, retained.Status)
	require.Nil(t, retained.LastError)
}
