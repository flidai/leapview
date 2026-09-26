package composectl

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBootstrapQualificationProjectUsesExplicitIssuerAndEnvironmentToken(t *testing.T) {
	container := &nativePostgresContainerFixture{execOutput: []byte(`{
        "schemaVersion": 1,
        "type": "projectBootstrapped",
        "target": "http://localhost:8080/",
        "projectUid": "project:leapview-evaluation",
        "environment": "evaluation",
        "claimCredentialId": "00000000-0000-0000-0000-000000000001",
        "publisherToken": "publisher-secret",
        "publisherTokenExpiresAt": "` + time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano) + `"
    }`)}

	result, err := bootstrapQualificationProject(
		t.Context(), container, "http://localhost:8080/", "claim-secret",
	)
	require.NoError(t, err)
	require.Equal(t, "publisher-secret", result.PublisherToken)
	require.Len(t, container.probes, 1)
	command := container.probes[0]
	for _, expected := range []string{
		"env",
		"LEAPVIEW_API_TOKEN=claim-secret",
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
        "environment": "evaluation",
        "claimCredentialId": "00000000-0000-0000-0000-000000000001",
        "publisherToken": "publisher-secret",
        "publisherTokenExpiresAt": "` + time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano) + `"
    }`)}

	_, err := bootstrapQualificationProject(t.Context(), container, "http://localhost:8080", "publisher-secret")
	require.ErrorContains(t, err, "identity does not match")
	require.NotContains(t, strings.ToLower(err.Error()), "publisher-secret")
}

func TestBootstrapQualificationProjectRejectsMismatchedEnvironment(t *testing.T) {
	container := &nativePostgresContainerFixture{execOutput: []byte(`{
        "schemaVersion": 1,
        "type": "projectBootstrapped",
        "target": "http://localhost:8080",
        "projectUid": "project:leapview-evaluation",
        "environment": "production",
        "claimCredentialId": "00000000-0000-0000-0000-000000000001",
        "publisherToken": "publisher-secret",
        "publisherTokenExpiresAt": "` + time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano) + `"
    }`)}

	_, err := bootstrapQualificationProject(t.Context(), container, "http://localhost:8080", "publisher-secret")
	require.ErrorContains(t, err, "environment does not match")
}

func TestAcknowledgeQualificationProjectClaimUsesPublisherAfterCallerPersists(t *testing.T) {
	container := &nativePostgresContainerFixture{}
	require.NoError(t, acknowledgeQualificationProjectClaim(
		t.Context(), container, "http://localhost:8080/", "publisher-secret", "00000000-0000-0000-0000-000000000001",
	))
	require.Len(t, container.probes, 1)
	command := container.probes[0]
	for _, expected := range []string{
		"LEAPVIEW_API_TOKEN=publisher-secret",
		"acknowledge-project-claim-publisher http://localhost:8080 project:leapview-evaluation",
		"--claim-credential-id 00000000-0000-0000-0000-000000000001",
	} {
		require.Contains(t, command, expected)
	}
}
