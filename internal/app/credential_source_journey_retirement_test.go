package app

import (
	"net/http"
	"testing"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/stretchr/testify/require"
)

func (f *sourceCredentialHTTPJourney) verifySourceCredentialRetirement(t *testing.T, token string, retained ...string) {
	t.Helper()
	client := credentialgen.NewGenClient(f.transport(token))
	path := "/api/v1/projects/" + sourceJourneyProject + "/targets/" + f.instance + "/connection-bindings/connection:warehouse/credential-drafts/"
	for _, version := range retained {
		status, err := client.GetCredentialVersionStatus(t.Context(), credentialgen.GenGetCredentialVersionStatusClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: version})
		require.NoError(t, err)
		require.Equal(t, "available", status.Body.State)
		require.NotEmpty(t, status.Body.Dependencies)
		f.request(t, http.MethodPost, path+version+"/retire", token, map[string]any{}, http.StatusConflict)
	}
	saved, err := client.SaveCredentialDraft(t.Context(), credentialgen.GenSaveCredentialDraftClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Body: credentialgen.CredentialDraftSaveRequest{Fields: map[string]string{"password": f.source.password}}})
	require.NoError(t, err)
	version := saved.Body.VersionId
	// An attenuated upload-only token cannot inspect or retire a credential.
	f.request(t, http.MethodGet, path+version+"/status", f.authoringToken, nil, http.StatusForbidden)
	f.request(t, http.MethodPost, path+version+"/retire", f.authoringToken, map[string]any{}, http.StatusForbidden)
	status, err := client.GetCredentialVersionStatus(t.Context(), credentialgen.GenGetCredentialVersionStatusClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: version})
	require.NoError(t, err)
	require.Empty(t, status.Body.Dependencies)
	request := credentialgen.GenRetireCredentialVersionClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: version, Body: credentialgen.CredentialVersionRetireRequest{}}
	retired, err := client.RetireCredentialVersion(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, "retired_local", retired.Body.State)
	require.NotNil(t, retired.Body.RetiredAt)
	replay, err := client.RetireCredentialVersion(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, retired.Body.RetiredAt, replay.Body.RetiredAt)
	binding, err := analyticsgen.NewGenClient(f.transport(token)).GetTargetConnectionBinding(t.Context(), analyticsgen.GenGetTargetConnectionBindingClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse"})
	require.NoError(t, err)
	f.request(t, http.MethodPost, path+version+"/validate", token, map[string]any{"expectedBindingRevision": binding.Body.Revision}, http.StatusNotFound)
	snapshot := f.querySource(t, token, "50")
	f.restartWithoutEnvironment(t, true)
	status, err = client.GetCredentialVersionStatus(t.Context(), credentialgen.GenGetCredentialVersionStatusClientRequest{Project: sourceJourneyProject, Target: f.instance, Connection: "connection:warehouse", Version: version})
	require.NoError(t, err)
	require.Equal(t, "retired_local", status.Body.State)
	require.Equal(t, snapshot, f.querySource(t, token, "50"))
}
