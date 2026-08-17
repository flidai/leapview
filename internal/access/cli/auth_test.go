package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/securestore"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type fakeAuthoringOAuthClient struct {
	request         DeviceAuthorizationRequest
	authorization   DeviceAuthorization
	err             error
	refreshRequest  OAuthRefreshRequest
	refreshToken    *oauth2.Token
	refreshErr      error
	workloadRequest WorkloadIdentityRequest
	workloadToken   *oauth2.Token
	workloadErr     error
	revokeRequest   OAuthRevokeRequest
	revokeErr       error
}

func (client *fakeAuthoringOAuthClient) Begin(_ context.Context, request DeviceAuthorizationRequest) (DeviceAuthorization, error) {
	client.request = request
	return client.authorization, client.err
}

func (client *fakeAuthoringOAuthClient) Refresh(_ context.Context, request OAuthRefreshRequest) (*oauth2.Token, error) {
	client.refreshRequest = request
	return client.refreshToken, client.refreshErr
}

func (client *fakeAuthoringOAuthClient) Workload(_ context.Context, request WorkloadIdentityRequest) (*oauth2.Token, error) {
	client.workloadRequest = request
	return client.workloadToken, client.workloadErr
}

func (client *fakeAuthoringOAuthClient) Revoke(_ context.Context, request OAuthRevokeRequest) error {
	client.revokeRequest = request
	return client.revokeErr
}

type fakeDeviceAuthorization struct {
	challenge DeviceChallenge
	token     *oauth2.Token
	err       error
}

func (authorization fakeDeviceAuthorization) Challenge() DeviceChallenge {
	return authorization.challenge
}

func (authorization fakeDeviceAuthorization) Token(context.Context) (*oauth2.Token, error) {
	return authorization.token, authorization.err
}

type memorySecrets struct {
	values map[string]string
	err    error
}

func (store *memorySecrets) Set(_ context.Context, account, secret string) error {
	if store.err != nil {
		return store.err
	}
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[account] = secret
	return nil
}

func (store *memorySecrets) Get(_ context.Context, account string) (string, error) {
	if store.err != nil {
		return "", store.err
	}
	value, ok := store.values[account]
	if !ok {
		return "", securestore.ErrNotFound
	}
	return value, nil
}

func (store *memorySecrets) Delete(_ context.Context, account string) error {
	if store.err != nil {
		return store.err
	}
	if _, ok := store.values[account]; !ok {
		return securestore.ErrNotFound
	}
	delete(store.values, account)
	return nil
}

func TestLoginUsesOAuthDeviceFlowAndNativeCredentialReference(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	oauthClient := &fakeAuthoringOAuthClient{
		authorization: fakeDeviceAuthorization{
			challenge: DeviceChallenge{
				UserCode:                "ABCD-EFGH",
				VerificationURI:         "https://prod.example.com/device",
				VerificationURIComplete: "https://prod.example.com/device?user_code=ABCD-EFGH",
				ExpiresIn:               10 * time.Minute,
			},
			token: (&oauth2.Token{
				AccessToken: "access-secret", RefreshToken: "refresh-secret",
				TokenType: "Bearer", Expiry: now.Add(15 * time.Minute),
			}).WithExtra(map[string]any{
				"session_id": "session-1", "session_kind": "human_cli",
				"target_id": "lvinst_prod", "project_id": "analytics",
			}),
		},
	}
	secrets := &memorySecrets{}
	profiles := cliapi.NewProfileStore(filepath.Join(t.TempDir(), "cli.json"))
	var opened, shown string
	auth := Authenticator{
		OAuth:    oauthClient,
		Profiles: profiles,
		Secrets:  secrets,
		Now:      func() time.Time { return now },
		OpenBrowser: func(uri string) error {
			opened = uri
			return nil
		},
	}
	result, err := auth.Login(context.Background(), LoginRequest{
		Name: "prod", Origin: "https://prod.example.com", InstanceID: "lvinst_prod",
		Environment: "production", ProjectID: "analytics",
		Capabilities: []string{"RESOURCE_EDIT", "RESOURCE_PUBLISH"},
	}, func(challenge DeviceChallenge) { shown = challenge.UserCode })
	require.NoError(t, err)
	if shown != "ABCD-EFGH" || opened != "https://prod.example.com/device?user_code=ABCD-EFGH" {
		t.Fatalf("challenge shown=%q opened=%q", shown, opened)
	}
	if result.SessionID != "session-1" {
		t.Fatalf("result=%+v", result)
	}
	if oauthClient.request.Origin != "https://prod.example.com" ||
		oauthClient.request.ProjectID != "analytics" ||
		strings.Join(oauthClient.request.Capabilities, ",") != "RESOURCE_EDIT,RESOURCE_PUBLISH" {
		t.Fatalf("device request=%+v", oauthClient.request)
	}
	profile, err := profiles.Get("prod")
	require.NoError(t, err)
	if profile.InstanceID != "lvinst_prod" || profile.CredentialAccount == "" {
		t.Fatalf("profile = %+v", profile)
	}
	encoded := secrets.values[profile.CredentialAccount]
	if !strings.Contains(encoded, "access-secret") || !strings.Contains(encoded, "refresh-secret") {
		t.Fatalf("native credential = %q", encoded)
	}
}

func TestHeadlessLoginShowsCodeWithoutOpeningBrowser(t *testing.T) {
	now := time.Now().UTC()
	opened := false
	auth, _, _ := testAuthenticator(t, now)
	auth.OpenBrowser = func(string) error {
		opened = true
		return nil
	}
	var challenge DeviceChallenge
	_, err := auth.Login(context.Background(), LoginRequest{
		Name: "ci", Origin: "https://example.test", InstanceID: "lvinst_prod",
		ProjectID: "project", Capabilities: []string{"RESOURCE_EDIT"}, Headless: true,
	}, func(value DeviceChallenge) { challenge = value })
	require.NoError(t, err)
	if opened || challenge.UserCode == "" || challenge.VerificationURI == "" {
		t.Fatalf("opened=%v challenge=%+v", opened, challenge)
	}
}

func TestLoginFailsClosedWhenNativeStoreIsUnavailable(t *testing.T) {
	now := time.Now().UTC()
	auth, profiles, _ := testAuthenticator(t, now)
	auth.Secrets = &memorySecrets{err: errors.New("keychain locked")}
	_, err := auth.Login(context.Background(), LoginRequest{
		Name: "prod", Origin: "https://example.test", InstanceID: "lvinst_prod",
		ProjectID: "project", Capabilities: []string{"RESOURCE_EDIT"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("Login error = %v", err)
	}
	if _, err := profiles.Get("prod"); !errors.Is(err, cliapi.ErrProfileNotFound) {
		t.Fatalf("profile persisted after keychain failure: %v", err)
	}
}

func TestResolveRefreshesBeforeClockSkewAndPersistsRotation(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	auth, profiles, secrets := testAuthenticator(t, now)
	oauthClient := auth.OAuth.(*fakeAuthoringOAuthClient)
	oauthClient.refreshToken = (&oauth2.Token{
		AccessToken: "access-new", RefreshToken: "refresh-new",
		TokenType: "Bearer", Expiry: now.Add(15 * time.Minute),
	}).WithExtra(map[string]any{
		"session_id": "session-1", "session_kind": string(access.AuthoringSessionHumanCLI),
		"target_id": "lvinst_prod", "project_id": "project",
	})
	account := "target/account"
	if err := profiles.Put("prod", cliapi.TargetProfile{
		Origin: "https://example.test", InstanceID: "lvinst_prod", ProjectID: "project", CredentialAccount: account,
	}); err != nil {
		t.Fatal(err)
	}
	putCredential(t, secrets, account, credentialDocument{
		AccessToken: "access-old", RefreshToken: "refresh-old",
		AccessExpiresAt: now.Add(20 * time.Second), SessionID: "session-old",
	})
	resolved, err := auth.Resolve(context.Background(), "prod")
	require.NoError(t, err)
	if resolved.AccessToken != "access-new" || resolved.Profile.Origin != "https://example.test" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if oauthClient.refreshRequest.Origin != "https://example.test" ||
		oauthClient.refreshRequest.RefreshToken != "refresh-old" {
		t.Fatalf("refresh request=%+v", oauthClient.refreshRequest)
	}
	var stored credentialDocument
	if err := json.Unmarshal([]byte(secrets.values[account]), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "refresh-new" || stored.AccessToken != "access-new" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestResolvePurgesCompromisedRefreshFamily(t *testing.T) {
	now := time.Now().UTC()
	auth, profiles, secrets := testAuthenticator(t, now)
	auth.OAuth.(*fakeAuthoringOAuthClient).refreshErr = &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		Body:      []byte(`{"error":"invalid_grant"}`),
		ErrorCode: "invalid_grant",
	}
	account := "target/account"
	if err := profiles.Put("prod", cliapi.TargetProfile{
		Origin: "https://example.test", InstanceID: "lvinst_prod", ProjectID: "project", CredentialAccount: account,
	}); err != nil {
		t.Fatal(err)
	}
	putCredential(t, secrets, account, credentialDocument{
		AccessToken: "access-old", RefreshToken: "refresh-replayed",
		AccessExpiresAt: now.Add(-time.Minute), SessionID: "session-old",
	})
	if _, err := auth.Resolve(context.Background(), "prod"); err == nil {
		t.Fatal("compromised refresh succeeded")
	}
	if _, ok := secrets.values[account]; ok {
		t.Fatal("compromised credential remained in native storage")
	}
	if _, err := profiles.Get("prod"); !errors.Is(err, cliapi.ErrProfileNotFound) {
		t.Fatalf("compromised profile remained: %v", err)
	}
}

func TestLogoutRevokesServerSessionAndDeletesLocalState(t *testing.T) {
	now := time.Now().UTC()
	auth, profiles, secrets := testAuthenticator(t, now)
	oauthClient := auth.OAuth.(*fakeAuthoringOAuthClient)
	account := "target/account"
	if err := profiles.Put("prod", cliapi.TargetProfile{
		Origin: "https://example.test", InstanceID: "lvinst_prod", ProjectID: "project", CredentialAccount: account,
	}); err != nil {
		t.Fatal(err)
	}
	putCredential(t, secrets, account, credentialDocument{
		AccessToken: "access-token", RefreshToken: "refresh-token",
		AccessExpiresAt: now.Add(time.Minute), SessionID: "session",
	})
	if err := auth.Logout(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	if oauthClient.revokeRequest.Origin != "https://example.test" ||
		oauthClient.revokeRequest.AccessToken != "access-token" {
		t.Fatalf("revoke request=%+v", oauthClient.revokeRequest)
	}
	if _, ok := secrets.values[account]; ok {
		t.Fatal("credential remained after logout")
	}
	if _, err := profiles.Get("prod"); !errors.Is(err, cliapi.ErrProfileNotFound) {
		t.Fatalf("profile remained after logout: %v", err)
	}
}

func TestExchangeWorkloadIdentityUsesExactGeneratedScopeWithoutPersistence(t *testing.T) {
	now := time.Now().UTC()
	oauthClient := &fakeAuthoringOAuthClient{
		workloadToken: (&oauth2.Token{
			AccessToken: "workload-access", TokenType: "Bearer", Expiry: now.Add(10 * time.Minute),
		}).WithExtra(map[string]any{
			"session_id": "session-1", "session_kind": "workload",
			"target_id": "lvinst_prod", "project_id": "analytics",
		}),
	}
	request := WorkloadIdentityRequest{
		Origin: "https://prod.example.com", InstanceID: "lvinst_prod", ProjectID: "analytics",
		ClientID: "sp-ci", ClientSecret: "service-secret",
		Capabilities: []string{"RESOURCE_EDIT", "RESOURCE_PUBLISH"}, Lifetime: 10 * time.Minute,
	}
	result, err := ExchangeWorkloadIdentity(context.Background(), oauthClient, request, func() time.Time { return now })
	require.NoError(t, err)
	if oauthClient.workloadRequest.Origin != request.Origin ||
		strings.Join(oauthClient.workloadRequest.Capabilities, ",") != "RESOURCE_EDIT,RESOURCE_PUBLISH" {
		t.Fatalf("workload request = %+v", oauthClient.workloadRequest)
	}
	if result.AccessToken != "workload-access" || result.ExpiresAt != now.Add(10*time.Minute) ||
		result.SessionID != "session-1" {
		t.Fatalf("result = %+v", result)
	}
}

func testAuthenticator(t *testing.T, now time.Time) (Authenticator, *cliapi.ProfileStore, *memorySecrets) {
	t.Helper()
	profiles := cliapi.NewProfileStore(filepath.Join(t.TempDir(), "cli.json"))
	secrets := &memorySecrets{}
	return Authenticator{
		OAuth: &fakeAuthoringOAuthClient{authorization: fakeDeviceAuthorization{
			challenge: DeviceChallenge{
				UserCode:                "USER-CODE",
				VerificationURI:         "https://example.test/device",
				VerificationURIComplete: "https://example.test/device?user_code=USER-CODE",
				ExpiresIn:               10 * time.Minute,
			},
			token: (&oauth2.Token{
				AccessToken: "access", RefreshToken: "refresh",
				TokenType: "Bearer", Expiry: now.Add(15 * time.Minute),
			}).WithExtra(map[string]any{
				"session_id": "session-1", "session_kind": string(access.AuthoringSessionHumanCLI),
				"target_id": "lvinst_prod", "project_id": "project",
			}),
		}},
		Profiles: profiles, Secrets: secrets,
		Now: func() time.Time { return now },
	}, profiles, secrets
}

func putCredential(t *testing.T, store *memorySecrets, account string, credential credentialDocument) {
	t.Helper()
	credential.Version = credentialVersion
	content, err := json.Marshal(credential)
	require.NoError(t, err)
	store.values = map[string]string{account: string(content)}
}
