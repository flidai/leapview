// Package cli owns command-line adapters for the Access capability.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/securestore"
	"golang.org/x/oauth2"
)

const (
	credentialVersion = 1
	refreshClockSkew  = 30 * time.Second
)

type DeviceChallenge struct {
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
}

type LoginRequest struct {
	Name         string
	Origin       string
	InstanceID   string
	Environment  string
	ProjectID    string
	Capabilities []string
	Headless     bool
}

type LoginResult struct {
	SessionID string
	Profile   cliapi.TargetProfile
}

type ResolvedCredential struct {
	Profile     cliapi.TargetProfile
	AccessToken string
	ExpiresAt   time.Time
}

type WorkloadIdentityRequest struct {
	Origin       string
	InstanceID   string
	ProjectID    string
	ClientID     string
	ClientSecret string
	Capabilities []string
	Lifetime     time.Duration
}

type WorkloadIdentityResult struct {
	AccessToken string
	ExpiresAt   time.Time
	SessionID   string
}

type credentialDocument struct {
	Version         int       `json:"version"`
	AccessToken     string    `json:"accessToken"`
	RefreshToken    string    `json:"refreshToken"`
	AccessExpiresAt time.Time `json:"accessExpiresAt"`
	SessionID       string    `json:"sessionId"`
}

// Authenticator implements human CLI device login and credential lifecycle.
// Profiles contain references only; token material stays in the native store.
type Authenticator struct {
	OAuth       AuthoringOAuthClient
	Profiles    *cliapi.ProfileStore
	Secrets     securestore.Store
	OpenBrowser func(string) error
	Now         func() time.Time
}

func (auth Authenticator) Login(ctx context.Context, request LoginRequest, notify func(DeviceChallenge)) (LoginResult, error) {
	if err := auth.validate(); err != nil {
		return LoginResult{}, err
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Origin = strings.TrimRight(strings.TrimSpace(request.Origin), "/")
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	if request.Name == "" || request.Origin == "" || request.InstanceID == "" || request.ProjectID == "" {
		return LoginResult{}, fmt.Errorf("login target name, origin, instance identity, and project are required")
	}
	if len(request.Capabilities) == 0 {
		return LoginResult{}, fmt.Errorf("login requires at least one authoring capability")
	}
	if existing, err := auth.Profiles.Get(request.Name); err == nil {
		if existing.InstanceID != request.InstanceID || existing.Origin != request.Origin || existing.ProjectID != request.ProjectID {
			return LoginResult{}, fmt.Errorf("target profile %q already identifies another instance, origin, or project; log out before replacing it", request.Name)
		}
	} else if !errors.Is(err, cliapi.ErrProfileNotFound) {
		return LoginResult{}, err
	}
	authorization, err := auth.OAuth.Begin(ctx, DeviceAuthorizationRequest{
		Origin: request.Origin, ProjectID: request.ProjectID,
		Capabilities: append([]string(nil), request.Capabilities...),
	})
	if err != nil {
		return LoginResult{}, fmt.Errorf("begin device authorization: %w", err)
	}
	challenge := authorization.Challenge()
	if notify != nil {
		notify(challenge)
	}
	if !request.Headless && auth.OpenBrowser != nil {
		if err := auth.OpenBrowser(challenge.VerificationURIComplete); err != nil {
			return LoginResult{}, fmt.Errorf("open device authorization in browser: %w", err)
		}
	}
	token, err := authorization.Token(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	details, err := oauthAuthoringTokenDetails(token)
	if err != nil {
		return LoginResult{}, err
	}
	if details.TargetID != request.InstanceID || details.ProjectID != request.ProjectID {
		return LoginResult{}, fmt.Errorf("device authorization returned credentials for an unexpected target or project")
	}
	account := credentialAccount(request.InstanceID, request.ProjectID)
	credential := credentialDocument{
		Version: credentialVersion, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		AccessExpiresAt: token.Expiry.UTC(), SessionID: details.SessionID,
	}
	if err := auth.storeCredential(ctx, account, credential); err != nil {
		return LoginResult{}, err
	}
	profile := cliapi.TargetProfile{
		Origin: request.Origin, InstanceID: request.InstanceID, Environment: request.Environment,
		ProjectID: request.ProjectID, CredentialAccount: account,
	}
	if err := auth.Profiles.Put(request.Name, profile); err != nil {
		_ = auth.Secrets.Delete(ctx, account)
		return LoginResult{}, err
	}
	return LoginResult{SessionID: details.SessionID, Profile: profile}, nil
}

// Resolve returns a usable access credential, rotating it before expiry to
// absorb ordinary client/server clock skew and long-running publish steps.
func (auth Authenticator) Resolve(ctx context.Context, name string) (ResolvedCredential, error) {
	if err := auth.validate(); err != nil {
		return ResolvedCredential{}, err
	}
	profile, err := auth.Profiles.Get(strings.TrimSpace(name))
	if err != nil {
		return ResolvedCredential{}, err
	}
	credential, err := auth.loadCredential(ctx, profile.CredentialAccount)
	if err != nil {
		return ResolvedCredential{}, err
	}
	if auth.now().Add(refreshClockSkew).Before(credential.AccessExpiresAt) {
		return ResolvedCredential{Profile: profile, AccessToken: credential.AccessToken, ExpiresAt: credential.AccessExpiresAt}, nil
	}
	token, refreshErr := auth.OAuth.Refresh(ctx, OAuthRefreshRequest{
		Origin: profile.Origin, RefreshToken: credential.RefreshToken,
	})
	if refreshErr != nil {
		if invalidOAuthRefreshError(refreshErr) {
			cleanupErr := auth.purgeLocal(ctx, name, profile.CredentialAccount)
			if cleanupErr != nil {
				return ResolvedCredential{}, errors.Join(refreshErr, cleanupErr)
			}
		}
		return ResolvedCredential{}, fmt.Errorf("refresh CLI credential: %w", refreshErr)
	}
	details, err := oauthAuthoringTokenDetails(token)
	if err != nil {
		return ResolvedCredential{}, err
	}
	if details.TargetID != profile.InstanceID || details.ProjectID != profile.ProjectID {
		return ResolvedCredential{}, fmt.Errorf("refresh response changed target or project scope")
	}
	credential = credentialDocument{
		Version: credentialVersion, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		AccessExpiresAt: token.Expiry.UTC(), SessionID: details.SessionID,
	}
	if err := auth.storeCredential(ctx, profile.CredentialAccount, credential); err != nil {
		return ResolvedCredential{}, err
	}
	return ResolvedCredential{Profile: profile, AccessToken: credential.AccessToken, ExpiresAt: credential.AccessExpiresAt}, nil
}

// ResolveOrigin selects the exact profile whose native credential matches the
// access token rejected during a long-running operation, then rotates it.
func (auth Authenticator) ResolveOrigin(ctx context.Context, origin, accessToken string) (ResolvedCredential, error) {
	if err := auth.validate(); err != nil {
		return ResolvedCredential{}, err
	}
	profiles, err := auth.Profiles.ProfilesByOrigin(origin)
	if err != nil {
		return ResolvedCredential{}, err
	}
	for _, candidate := range profiles {
		credential, err := auth.loadCredential(ctx, candidate.Profile.CredentialAccount)
		if errors.Is(err, securestore.ErrNotFound) {
			continue
		}
		if err != nil {
			return ResolvedCredential{}, err
		}
		if credential.AccessToken == accessToken {
			return auth.Resolve(ctx, candidate.Name)
		}
	}
	return ResolvedCredential{}, cliapi.ErrProfileNotFound
}

func (auth Authenticator) Logout(ctx context.Context, name string) error {
	if err := auth.validate(); err != nil {
		return err
	}
	profile, err := auth.Profiles.Get(strings.TrimSpace(name))
	if err != nil {
		return err
	}
	credential, credentialErr := auth.loadCredential(ctx, profile.CredentialAccount)
	var revokeErr error
	if credentialErr == nil {
		revokeErr = auth.OAuth.Revoke(ctx, OAuthRevokeRequest{
			Origin: profile.Origin, AccessToken: credential.AccessToken,
		})
	}
	cleanupErr := auth.purgeLocal(ctx, name, profile.CredentialAccount)
	if errors.Is(credentialErr, securestore.ErrNotFound) {
		credentialErr = nil
	}
	return errors.Join(revokeErr, credentialErr, cleanupErr)
}

// ExchangeWorkloadIdentity creates a non-refreshable, exact-scope credential
// for an ephemeral CI workload. The service-principal secret and returned
// access token are never persisted by this adapter.
func ExchangeWorkloadIdentity(
	ctx context.Context,
	client AuthoringOAuthClient,
	request WorkloadIdentityRequest,
	now func() time.Time,
) (WorkloadIdentityResult, error) {
	if client == nil {
		return WorkloadIdentityResult{}, fmt.Errorf("authoring OAuth client is required")
	}
	if strings.TrimSpace(request.Origin) == "" || strings.TrimSpace(request.InstanceID) == "" ||
		strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.ClientID) == "" ||
		strings.TrimSpace(request.ClientSecret) == "" || len(request.Capabilities) == 0 ||
		request.Lifetime <= 0 {
		return WorkloadIdentityResult{}, fmt.Errorf("workload target, instance, project, client credentials, capabilities, and lifetime are required")
	}
	token, err := client.Workload(ctx, request)
	if err != nil {
		return WorkloadIdentityResult{}, fmt.Errorf("exchange workload identity: %w", err)
	}
	details, err := oauthWorkloadTokenDetails(token)
	if err != nil {
		return WorkloadIdentityResult{}, err
	}
	clock := time.Now
	if now != nil {
		clock = now
	}
	current := clock().UTC()
	if details.TargetID != request.InstanceID || details.ProjectID != request.ProjectID ||
		!token.Expiry.After(current) || token.Expiry.After(current.Add(request.Lifetime)) {
		return WorkloadIdentityResult{}, fmt.Errorf("workload identity response changed the requested scope or lifetime")
	}
	return WorkloadIdentityResult{
		AccessToken: token.AccessToken,
		ExpiresAt:   token.Expiry,
		SessionID:   details.SessionID,
	}, nil
}

func (auth Authenticator) validate() error {
	switch {
	case auth.OAuth == nil:
		return fmt.Errorf("authoring OAuth client is required")
	case auth.Profiles == nil:
		return fmt.Errorf("target profile store is required")
	case auth.Secrets == nil:
		return fmt.Errorf("native credential store is required")
	default:
		return nil
	}
}

func (auth Authenticator) now() time.Time {
	if auth.Now != nil {
		return auth.Now().UTC()
	}
	return time.Now().UTC()
}

func (auth Authenticator) storeCredential(ctx context.Context, account string, credential credentialDocument) error {
	content, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode native credential: %w", err)
	}
	if err := auth.Secrets.Set(ctx, account, string(content)); err != nil {
		return fmt.Errorf("store native credential: %w", err)
	}
	return nil
}

func (auth Authenticator) loadCredential(ctx context.Context, account string) (credentialDocument, error) {
	content, err := auth.Secrets.Get(ctx, account)
	if err != nil {
		return credentialDocument{}, err
	}
	var credential credentialDocument
	if err := json.Unmarshal([]byte(content), &credential); err != nil {
		return credentialDocument{}, fmt.Errorf("decode native credential: %w", err)
	}
	if credential.Version != credentialVersion || credential.AccessToken == "" || credential.RefreshToken == "" ||
		credential.AccessExpiresAt.IsZero() || credential.SessionID == "" {
		return credentialDocument{}, fmt.Errorf("native credential is incomplete or uses an unsupported version")
	}
	return credential, nil
}

func (auth Authenticator) purgeLocal(ctx context.Context, name, account string) error {
	secretErr := auth.Secrets.Delete(ctx, account)
	if errors.Is(secretErr, securestore.ErrNotFound) {
		secretErr = nil
	}
	profileErr := auth.Profiles.Delete(name)
	if errors.Is(profileErr, cliapi.ErrProfileNotFound) {
		profileErr = nil
	}
	return errors.Join(secretErr, profileErr)
}

func credentialAccount(instanceID, projectID string) string {
	digest := sha256.Sum256([]byte(instanceID + "\x00" + projectID))
	return "target/" + hex.EncodeToString(digest[:])
}

func invalidOAuthRefreshError(err error) bool {
	var retrieveError *oauth2.RetrieveError
	return errors.As(err, &retrieveError) && retrieveError.ErrorCode == "invalid_grant"
}
