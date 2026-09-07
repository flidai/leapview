package module

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestCandidateRegistryDoesNotEnterPortableCompilerSerialization(t *testing.T) {
	fixture := makeProtectedCandidatePhaseFixture(t)
	data, err := os.ReadFile(fixture.request.Source.ProjectArtifactPath)
	require.NoError(t, err)
	artifact, err := projectartifact.Decode(data)
	require.NoError(t, err)
	evidence := release.CandidateCompilerEvidence{Artifact: artifact, Graph: artifact.Graph(), Manifest: artifact.Manifest()}
	before, err := json.Marshal(evidence)
	require.NoError(t, err)
	evidence.SemanticRegistry = &fixture.registry
	after, err := json.Marshal(evidence)
	require.NoError(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("target registry changed portable compiler serialization")
	}
}
