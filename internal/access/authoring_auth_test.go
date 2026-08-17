package access

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/graph"
)

func TestAuthoringScopeAllowsOnlyExactTargetProjectAndAction(t *testing.T) {
	scope, err := NewAuthoringScope("instance-prod", graph.ResourceID("finance"), []Capability{
		CapabilityResourcePublish,
		CapabilityResourceManage,
	})
	if err != nil {
		t.Fatalf("NewAuthoringScope() error = %v", err)
	}

	if err := scope.Authorize("instance-prod", "finance", CapabilityResourcePublish); err != nil {
		t.Fatalf("Authorize() exact scope error = %v", err)
	}
	for name, request := range map[string]struct {
		target    string
		project   string
		privilege Capability
	}{
		"other target":  {target: "instance-staging", project: "finance", privilege: CapabilityResourcePublish},
		"other project": {target: "instance-prod", project: "marketing", privilege: CapabilityResourcePublish},
		"other action":  {target: "instance-prod", project: "finance", privilege: CapabilityResourceShare},
	} {
		t.Run(name, func(t *testing.T) {
			err := scope.Authorize(request.target, request.project, request.privilege)
			if !errors.Is(err, ErrAuthoringScopeDenied) {
				t.Fatalf("Authorize() error = %v, want ErrAuthoringScopeDenied", err)
			}
		})
	}
}

func TestAuthoringScopeRejectsMissingOrUnknownBindings(t *testing.T) {
	for name, input := range map[string]struct {
		target     string
		project    string
		privileges []Capability
	}{
		"target":     {project: "finance", privileges: []Capability{CapabilityResourcePublish}},
		"project":    {target: "instance-prod", privileges: []Capability{CapabilityResourcePublish}},
		"actions":    {target: "instance-prod", project: "finance"},
		"unknown":    {target: "instance-prod", project: "finance", privileges: []Capability{"DELETE_EVERYTHING"}},
		"duplicates": {target: "instance-prod", project: "finance", privileges: []Capability{CapabilityResourcePublish, CapabilityResourcePublish}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAuthoringScope(input.target, graph.ResourceID(input.project), input.privileges); err == nil {
				t.Fatal("NewAuthoringScope() succeeded")
			}
		})
	}
}

func TestAuthoringAuthDeviceFlowIssuesShortLivedExactScopeCredential(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newAuthoringAuthMemoryRepository()
	service := newAuthoringAuthTestService(t, repository, &now)
	scope := mustAuthoringScope(t, "instance-prod", "finance", CapabilityResourcePublish)

	started, err := service.BeginDeviceAuthorization(context.Background(), scope)
	if err != nil {
		t.Fatalf("BeginDeviceAuthorization() error = %v", err)
	}
	if started.DeviceCode == "" || started.UserCode == "" || started.ExpiresIn != 10*60 || started.Interval != 5 {
		t.Fatalf("device authorization = %#v", started)
	}
	if strings.Contains(started.VerificationURIComplete, started.DeviceCode) {
		t.Fatal("verification URI leaked the device code")
	}
	if _, err := service.ExchangeDeviceCode(context.Background(), started.DeviceCode); !errors.Is(err, ErrDeviceAuthorizationPending) {
		t.Fatalf("first ExchangeDeviceCode() error = %v, want pending", err)
	}
	if _, err := service.ExchangeDeviceCode(context.Background(), started.DeviceCode); !errors.Is(err, ErrDeviceAuthorizationSlowDown) {
		t.Fatalf("fast ExchangeDeviceCode() error = %v, want slow_down", err)
	}

	now = now.Add(5 * time.Second)
	principal := Principal{ID: "user_1", Kind: PrincipalKindUser, Email: "dev@example.com"}
	repository.principals[principal.ID] = principal
	if err := service.ApproveDeviceAuthorization(context.Background(), principal, started.UserCode); err != nil {
		t.Fatalf("ApproveDeviceAuthorization() error = %v", err)
	}
	tokens, err := service.ExchangeDeviceCode(context.Background(), started.DeviceCode)
	if err != nil {
		t.Fatalf("approved ExchangeDeviceCode() error = %v", err)
	}
	if !strings.HasPrefix(tokens.AccessToken, "lv_cli_access_") ||
		!strings.HasPrefix(tokens.RefreshToken, "lv_cli_refresh_") ||
		tokens.ExpiresIn != 15*60 {
		t.Fatalf("token set = %#v", tokens)
	}

	credential, err := service.Authenticate(
		context.Background(), tokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish,
	)
	if err != nil {
		t.Fatalf("Authenticate() exact scope error = %v", err)
	}
	if credential.Principal.ID != principal.ID || credential.Session.Kind != AuthoringSessionHumanCLI {
		t.Fatalf("credential = %#v", credential)
	}
	for name, request := range map[string]struct {
		target    string
		project   string
		privilege Capability
	}{
		"target":  {target: "instance-staging", project: "finance", privilege: CapabilityResourcePublish},
		"project": {target: "instance-prod", project: "marketing", privilege: CapabilityResourcePublish},
		"action":  {target: "instance-prod", project: "finance", privilege: CapabilityResourceShare},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Authenticate(context.Background(), tokens.AccessToken, request.target, request.project, request.privilege); !errors.Is(err, ErrAuthoringScopeDenied) {
				t.Fatalf("Authenticate() error = %v, want scope denied", err)
			}
		})
	}
}

func TestAuthoringAuthRefreshRotationDetectsReplayAndRevokesFamily(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newAuthoringAuthMemoryRepository()
	service := newAuthoringAuthTestService(t, repository, &now)
	tokens := authorizeAuthoringDevice(t, service, repository, &now)

	now = now.Add(14 * time.Minute)
	rotated, err := service.Refresh(context.Background(), tokens.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if rotated.RefreshToken == tokens.RefreshToken {
		t.Fatal("Refresh() did not rotate the refresh token")
	}
	if _, err := service.Authenticate(context.Background(), tokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish); !errors.Is(err, ErrInvalidAuthoringCredential) {
		t.Fatalf("old access token error = %v, want invalid credential", err)
	}

	if _, err := service.Refresh(context.Background(), tokens.RefreshToken); !errors.Is(err, ErrAuthoringRefreshReplay) {
		t.Fatalf("replayed Refresh() error = %v, want replay", err)
	}
	if _, err := service.Authenticate(context.Background(), rotated.AccessToken, "instance-prod", "finance", CapabilityResourcePublish); !errors.Is(err, ErrInvalidAuthoringCredential) {
		t.Fatalf("replacement access token after replay error = %v, want invalid credential", err)
	}
}

func TestAuthoringAuthRevocationExpiryAndWorkloadIdentity(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newAuthoringAuthMemoryRepository()
	service := newAuthoringAuthTestService(t, repository, &now)
	humanTokens := authorizeAuthoringDevice(t, service, repository, &now)
	humanCredential, err := service.Authenticate(context.Background(), humanTokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish)
	if err != nil {
		t.Fatalf("Authenticate() human error = %v", err)
	}
	if err := service.RevokeSession(context.Background(), humanCredential.Principal.ID, humanCredential.Session.ID); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), humanTokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish); !errors.Is(err, ErrInvalidAuthoringCredential) {
		t.Fatalf("revoked Authenticate() error = %v, want invalid credential", err)
	}

	workload := Principal{ID: "sp_ci", Kind: PrincipalKindServicePrincipal, DisplayName: "CI"}
	repository.principals[workload.ID] = workload
	repository.serviceSecrets[workload.ID] = "secret"
	workloadTokens, err := service.ExchangeWorkloadIdentity(context.Background(), WorkloadIdentityInput{
		ClientID: workload.ID, ClientSecret: "secret",
		Scope:    mustAuthoringScope(t, "instance-prod", "finance", CapabilityResourcePublish),
		Lifetime: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ExchangeWorkloadIdentity() error = %v", err)
	}
	if workloadTokens.RefreshToken != "" || !strings.HasPrefix(workloadTokens.AccessToken, "lv_workload_access_") {
		t.Fatalf("workload token set = %#v", workloadTokens)
	}
	if _, err := service.Authenticate(context.Background(), workloadTokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish); err != nil {
		t.Fatalf("Authenticate() workload error = %v", err)
	}
	if err := service.RevokeAccessToken(context.Background(), workloadTokens.AccessToken); err != nil {
		t.Fatalf("RevokeAccessToken() error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), workloadTokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish); !errors.Is(err, ErrInvalidAuthoringCredential) {
		t.Fatalf("revoke-by-token Authenticate() error = %v, want invalid credential", err)
	}

	workloadTokens, err = service.ExchangeWorkloadIdentity(context.Background(), WorkloadIdentityInput{
		ClientID: workload.ID, ClientSecret: "secret",
		Scope:    mustAuthoringScope(t, "instance-prod", "finance", CapabilityResourcePublish),
		Lifetime: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("second ExchangeWorkloadIdentity() error = %v", err)
	}
	now = now.Add(5*time.Minute + time.Nanosecond)
	if _, err := service.Authenticate(context.Background(), workloadTokens.AccessToken, "instance-prod", "finance", CapabilityResourcePublish); !errors.Is(err, ErrAuthoringCredentialExpired) {
		t.Fatalf("expired workload error = %v, want expired", err)
	}
}

func TestAuthoringAuthRejectsDisabledHumanAndExcessiveWorkloadLifetime(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repository := newAuthoringAuthMemoryRepository()
	service := newAuthoringAuthTestService(t, repository, &now)
	scope := mustAuthoringScope(t, "instance-prod", "finance", CapabilityResourcePublish)
	started, err := service.BeginDeviceAuthorization(context.Background(), scope)
	if err != nil {
		t.Fatalf("BeginDeviceAuthorization() error = %v", err)
	}
	disabled := Principal{ID: "user_disabled", Kind: PrincipalKindUser, DisabledAt: now.Format(time.RFC3339)}
	if err := service.ApproveDeviceAuthorization(context.Background(), disabled, started.UserCode); !errors.Is(err, ErrInvalidAuthoringPrincipal) {
		t.Fatalf("ApproveDeviceAuthorization() error = %v, want invalid principal", err)
	}

	workload := Principal{ID: "sp_ci", Kind: PrincipalKindServicePrincipal}
	repository.principals[workload.ID] = workload
	repository.serviceSecrets[workload.ID] = "secret"
	_, err = service.ExchangeWorkloadIdentity(context.Background(), WorkloadIdentityInput{
		ClientID: workload.ID, ClientSecret: "secret", Scope: scope, Lifetime: 31 * time.Minute,
	})
	if !errors.Is(err, ErrInvalidWorkloadLifetime) {
		t.Fatalf("ExchangeWorkloadIdentity() error = %v, want invalid lifetime", err)
	}
}

func newAuthoringAuthTestService(t *testing.T, repository *authoringAuthMemoryRepository, now *time.Time) *AuthoringAuthService {
	t.Helper()
	service, err := NewAuthoringAuthService(repository, AuthoringAuthConfig{
		InstanceID:      "instance-prod",
		CanonicalOrigin: "https://prod.leapview.example",
		DeviceTTL:       10 * time.Minute,
		PollInterval:    5 * time.Second,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 8 * time.Hour,
		WorkloadMaxTTL:  30 * time.Minute,
		Now:             func() time.Time { return *now },
		Random:          rand.Reader,
	})
	if err != nil {
		t.Fatalf("NewAuthoringAuthService() error = %v", err)
	}
	return service
}

func mustAuthoringScope(t *testing.T, target, project string, privileges ...Capability) AuthoringScope {
	t.Helper()
	projectID, err := graph.NewResourceID(project)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := NewAuthoringScope(target, projectID, privileges)
	if err != nil {
		t.Fatalf("NewAuthoringScope() error = %v", err)
	}
	return scope
}

func authorizeAuthoringDevice(t *testing.T, service *AuthoringAuthService, repository *authoringAuthMemoryRepository, now *time.Time) AuthoringTokenSet {
	t.Helper()
	started, err := service.BeginDeviceAuthorization(context.Background(), mustAuthoringScope(t, "instance-prod", "finance", CapabilityResourcePublish))
	if err != nil {
		t.Fatalf("BeginDeviceAuthorization() error = %v", err)
	}
	principal := Principal{ID: "user_1", Kind: PrincipalKindUser, Email: "dev@example.com"}
	repository.principals[principal.ID] = principal
	if err := service.ApproveDeviceAuthorization(context.Background(), principal, started.UserCode); err != nil {
		t.Fatalf("ApproveDeviceAuthorization() error = %v", err)
	}
	tokens, err := service.ExchangeDeviceCode(context.Background(), started.DeviceCode)
	if err != nil {
		t.Fatalf("ExchangeDeviceCode() error = %v", err)
	}
	return tokens
}

type authoringAuthMemoryCredential struct {
	credential  AuthoringCredential
	accessHash  string
	refreshHash string
	active      bool
}

type authoringAuthMemoryRepository struct {
	devices             map[string]DeviceAuthorization
	deviceIDsByCode     map[string]string
	deviceIDsByUser     map[string]string
	principals          map[string]Principal
	serviceSecrets      map[string]string
	sessions            map[string]AuthoringSession
	credentials         map[string]authoringAuthMemoryCredential
	credentialByAccess  map[string]string
	credentialByRefresh map[string]string
}

func newAuthoringAuthMemoryRepository() *authoringAuthMemoryRepository {
	return &authoringAuthMemoryRepository{
		devices: make(map[string]DeviceAuthorization), deviceIDsByCode: make(map[string]string),
		deviceIDsByUser: make(map[string]string), principals: make(map[string]Principal),
		serviceSecrets: make(map[string]string), sessions: make(map[string]AuthoringSession),
		credentials:        make(map[string]authoringAuthMemoryCredential),
		credentialByAccess: make(map[string]string), credentialByRefresh: make(map[string]string),
	}
}

func (repository *authoringAuthMemoryRepository) CreateDeviceAuthorization(_ context.Context, record DeviceAuthorization) error {
	if _, exists := repository.devices[record.ID]; exists {
		return errors.New("duplicate device authorization")
	}
	repository.devices[record.ID] = record
	repository.deviceIDsByCode[record.DeviceCodeHash] = record.ID
	repository.deviceIDsByUser[record.UserCodeHash] = record.ID
	return nil
}

func (repository *authoringAuthMemoryRepository) DeviceAuthorizationByUserCodeHash(_ context.Context, hash string) (DeviceAuthorization, error) {
	id := repository.deviceIDsByUser[hash]
	record, ok := repository.devices[id]
	if !ok {
		return DeviceAuthorization{}, ErrInvalidAuthoringCredential
	}
	return record, nil
}

func (repository *authoringAuthMemoryRepository) ApproveDeviceAuthorization(_ context.Context, id, principalID string, now time.Time) error {
	record, ok := repository.devices[id]
	if !ok {
		return ErrInvalidAuthoringCredential
	}
	if record.Status != DeviceAuthorizationPending || !now.Before(record.ExpiresAt) {
		return ErrDeviceAuthorizationExpired
	}
	record.Status = DeviceAuthorizationApproved
	record.PrincipalID = principalID
	record.ApprovedAt = now
	repository.devices[id] = record
	return nil
}

func (repository *authoringAuthMemoryRepository) DenyDeviceAuthorization(_ context.Context, id, principalID string, now time.Time) error {
	record, ok := repository.devices[id]
	if !ok {
		return ErrInvalidAuthoringCredential
	}
	if record.Status != DeviceAuthorizationPending || !now.Before(record.ExpiresAt) {
		return ErrDeviceAuthorizationExpired
	}
	record.Status = DeviceAuthorizationDenied
	record.PrincipalID = principalID
	record.DeniedAt = now
	repository.devices[id] = record
	return nil
}

func (repository *authoringAuthMemoryRepository) IssueDeviceCredential(_ context.Context, issue DeviceCredentialIssue) (AuthoringCredential, error) {
	id := repository.deviceIDsByCode[issue.DeviceCodeHash]
	record, ok := repository.devices[id]
	if !ok || record.ClientID != issue.ClientID {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	if !issue.Now.Before(record.ExpiresAt) {
		return AuthoringCredential{}, ErrDeviceAuthorizationExpired
	}
	switch record.Status {
	case DeviceAuthorizationPending:
		if !record.LastPolledAt.IsZero() && issue.Now.Sub(record.LastPolledAt) < record.PollInterval {
			return AuthoringCredential{}, ErrDeviceAuthorizationSlowDown
		}
		record.LastPolledAt = issue.Now
		repository.devices[id] = record
		return AuthoringCredential{}, ErrDeviceAuthorizationPending
	case DeviceAuthorizationDenied:
		return AuthoringCredential{}, ErrDeviceAuthorizationDenied
	case DeviceAuthorizationConsumed:
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	case DeviceAuthorizationApproved:
	default:
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	principal, ok := repository.principals[record.PrincipalID]
	if !ok {
		return AuthoringCredential{}, ErrInvalidAuthoringPrincipal
	}
	session := AuthoringSession{
		ID: issue.SessionID, Kind: AuthoringSessionHumanCLI, ClientID: record.ClientID,
		PrincipalID: principal.ID, Scope: record.Scope, CreatedAt: issue.Now, ExpiresAt: issue.RefreshExpiresAt,
	}
	credential := AuthoringCredential{
		ID: issue.CredentialID, Principal: principal, Session: session,
		AccessExpiresAt: issue.AccessExpiresAt, RefreshExpiresAt: issue.RefreshExpiresAt,
	}
	repository.storeCredential(credential, issue.AccessTokenHash, issue.RefreshTokenHash)
	record.Status = DeviceAuthorizationConsumed
	record.ConsumedAt = issue.Now
	repository.devices[id] = record
	return credential, nil
}

func (repository *authoringAuthMemoryRepository) CreateWorkloadCredential(_ context.Context, issue WorkloadCredentialIssue) (AuthoringCredential, error) {
	principal, ok := repository.principals[issue.Session.PrincipalID]
	if !ok {
		return AuthoringCredential{}, ErrInvalidAuthoringPrincipal
	}
	credential := AuthoringCredential{
		ID: issue.CredentialID, Principal: principal, Session: issue.Session, AccessExpiresAt: issue.AccessExpiresAt,
	}
	repository.storeCredential(credential, issue.AccessTokenHash, "")
	return credential, nil
}

func (repository *authoringAuthMemoryRepository) RotateAuthoringCredential(_ context.Context, rotation AuthoringCredentialRotation) (AuthoringCredential, error) {
	id := repository.credentialByRefresh[rotation.RefreshTokenHash]
	stored, ok := repository.credentials[id]
	if !ok {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	session := repository.sessions[stored.credential.Session.ID]
	if !stored.active {
		session.RevokedAt = rotation.Now
		repository.sessions[session.ID] = session
		return AuthoringCredential{}, ErrAuthoringRefreshReplay
	}
	if !session.RevokedAt.IsZero() || !rotation.Now.Before(stored.credential.RefreshExpiresAt) {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	stored.active = false
	repository.credentials[id] = stored
	session.ExpiresAt = rotation.RefreshExpiresAt
	credential := AuthoringCredential{
		ID: rotation.CredentialID, Principal: stored.credential.Principal, Session: session,
		AccessExpiresAt: rotation.AccessExpiresAt, RefreshExpiresAt: rotation.RefreshExpiresAt,
	}
	repository.storeCredential(credential, rotation.AccessTokenHash, rotation.RefreshTokenHashNew)
	return credential, nil
}

func (repository *authoringAuthMemoryRepository) AuthoringCredentialByAccessTokenHash(_ context.Context, hash string, now time.Time) (AuthoringCredential, error) {
	id := repository.credentialByAccess[hash]
	stored, ok := repository.credentials[id]
	if !ok || !stored.active {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	session := repository.sessions[stored.credential.Session.ID]
	if !session.RevokedAt.IsZero() {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	session.LastUsedAt = now
	repository.sessions[session.ID] = session
	stored.credential.Session = session
	repository.credentials[id] = stored
	return stored.credential, nil
}

func (repository *authoringAuthMemoryRepository) ListAuthoringSessions(_ context.Context, principalID string) ([]AuthoringSession, error) {
	var sessions []AuthoringSession
	for _, session := range repository.sessions {
		if session.PrincipalID == principalID {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

func (repository *authoringAuthMemoryRepository) RevokeAuthoringSession(_ context.Context, principalID, sessionID string, now time.Time) error {
	session, ok := repository.sessions[sessionID]
	if !ok || session.PrincipalID != principalID {
		return ErrInvalidAuthoringCredential
	}
	session.RevokedAt = now
	repository.sessions[sessionID] = session
	return nil
}

func (repository *authoringAuthMemoryRepository) RevokeAuthoringSessionByAccessTokenHash(_ context.Context, hash string, now time.Time) error {
	id := repository.credentialByAccess[hash]
	stored, ok := repository.credentials[id]
	if !ok {
		return ErrInvalidAuthoringCredential
	}
	session := repository.sessions[stored.credential.Session.ID]
	session.RevokedAt = now
	repository.sessions[session.ID] = session
	return nil
}

func (repository *authoringAuthMemoryRepository) PrincipalForServicePrincipalSecret(_ context.Context, clientID, secret string) (Principal, error) {
	if repository.serviceSecrets[clientID] != secret {
		return Principal{}, ErrInvalidAuthoringPrincipal
	}
	principal, ok := repository.principals[clientID]
	if !ok {
		return Principal{}, ErrInvalidAuthoringPrincipal
	}
	return principal, nil
}

func (repository *authoringAuthMemoryRepository) storeCredential(credential AuthoringCredential, accessHash, refreshHash string) {
	repository.sessions[credential.Session.ID] = credential.Session
	repository.credentials[credential.ID] = authoringAuthMemoryCredential{
		credential: credential, accessHash: accessHash, refreshHash: refreshHash, active: true,
	}
	repository.credentialByAccess[accessHash] = credential.ID
	if refreshHash != "" {
		repository.credentialByRefresh[refreshHash] = credential.ID
	}
}
