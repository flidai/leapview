package composectl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootstrapQualificationProjectUsesExplicitIssuerAndEnvironmentToken(t *testing.T) {
	container := &nativePostgresContainerFixture{execOutput: []byte(`{
        "schemaVersion": 1,
        "type": "projectBootstrapped",
        "target": "http://localhost:8080/",
        "projectUid": "project:leapview-evaluation",
        "environment": "evaluation"
    }`)}

	require.NoError(t, bootstrapQualificationProject(
		t.Context(), container, "http://localhost:8080/", "publisher-secret",
	))
	require.Len(t, container.probes, 1)
	command := container.probes[0]
	for _, expected := range []string{
		"env",
		"LEAPVIEW_API_TOKEN=publisher-secret",
		"LEAPVIEW_TARGET=http://localhost:8080",
		"leapview bootstrap-project http://localhost:8080",
		"--project-uid project:leapview-evaluation",
		"--format json",
	} {
		require.Contains(t, command, expected)
	}
}

func TestBootstrapQualificationProjectRejectsMismatchedIdentity(t *testing.T) {
	container := &nativePostgresContainerFixture{execOutput: []byte(`{
        "schemaVersion": 1,
        "type": "projectBootstrapped",
        "target": "http://localhost:8080",
        "projectUid": "project:unexpected",
        "environment": "evaluation"
    }`)}

	err := bootstrapQualificationProject(t.Context(), container, "http://localhost:8080", "publisher-secret")
	require.ErrorContains(t, err, "identity does not match")
	require.NotContains(t, strings.ToLower(err.Error()), "publisher-secret")
}

func TestBootstrapQualificationProjectRejectsMismatchedEnvironment(t *testing.T) {
	container := &nativePostgresContainerFixture{execOutput: []byte(`{
        "schemaVersion": 1,
        "type": "projectBootstrapped",
        "target": "http://localhost:8080",
        "projectUid": "project:leapview-evaluation",
        "environment": "production"
    }`)}

	err := bootstrapQualificationProject(t.Context(), container, "http://localhost:8080", "publisher-secret")
	require.ErrorContains(t, err, "environment does not match")
}
