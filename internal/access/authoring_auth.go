package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"
)

import apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"

var (
	ErrAuthoringScopeDenied        = apigenfailure.New("scope_denied", "authoring credential scope denied")
	ErrDeviceAuthorizationPending  = apigenfailure.New("pending", "device authorization pending")
	ErrDeviceAuthorizationSlowDown = apigenfailure.New("rate_limited", "device authorization polling too quickly")
	ErrDeviceAuthorizationExpired  = apigenfailure.New("expired", "device authorization expired")
	ErrDeviceAuthorizationDenied   = apigenfailure.New("denied", "device authorization denied")
	ErrInvalidAuthoringCredential  = apigenfailure.New("invalid_credential", "invalid authoring credential")
	ErrAuthoringCredentialExpired  = apigenfailure.New("credential_expired", "authoring credential expired")
	ErrAuthoringRefreshReplay      = apigenfailure.New("replay", "authoring refresh token replay detected")
	ErrInvalidAuthoringPrincipal   = apigenfailure.New("invalid_principal", "invalid authoring principal")
	ErrInvalidWorkloadLifetime     = apigenfailure.New("invalid_lifetime", "invalid authoring workload lifetime")
)

const AuthoringCLIClientID = "leapview-cli"

type AuthoringSessionKind string

const (
	AuthoringSessionHumanCLI AuthoringSessionKind = "human_cli"
	AuthoringSessionWorkload AuthoringSessionKind = "workload"
)

type DeviceAuthorizationStatus string

const (
	DeviceAuthorizationPending  DeviceAuthorizationStatus = "pending"
	DeviceAuthorizationApproved DeviceAuthorizationStatus = "approved"
	DeviceAuthorizationDenied   DeviceAuthorizationStatus = "denied"
	DeviceAuthorizationConsumed DeviceAuthorizationStatus = "consumed"
)

// AuthoringScope is the non-escalating boundary carried by CLI and workload
// credentials. RBAC still decides whether the principal may perform the
// action; this scope additionally prevents replay against another LeapView
// instance, project, or action.
type AuthoringScope struct {
	TargetID   string
	ProjectID  string
	Privileges []Privilege
}

func NewAuthoringScope(targetID, projectID string, privileges []Privilege) (AuthoringScope, error) {
	targetID = strings.TrimSpace(targetID)
	projectID = strings.TrimSpace(projectID)
	if targetID == "" {
		return AuthoringScope{}, fmt.Errorf("authoring target ID is required")
	}
	if projectID == "" {
		return AuthoringScope{}, fmt.Errorf("authoring project ID is required")
	}
	if len(privileges) == 0 {
		return AuthoringScope{}, fmt.Errorf("at least one authoring action is required")
	}
	validated := make([]Privilege, 0, len(privileges))
	seen := make(map[Privilege]struct{}, len(privileges))
	for _, requested := range privileges {
		privilege, ok := ParsePrivilege(string(requested))
		if !ok {
			return AuthoringScope{}, fmt.Errorf("unknown authoring action %q", requested)
		}
		if _, duplicate := seen[privilege]; duplicate {
			return AuthoringScope{}, fmt.Errorf("duplicate authoring action %q", privilege)
		}
		seen[privilege] = struct{}{}
		validated = append(validated, privilege)
	}
	slices.Sort(validated)
	return AuthoringScope{TargetID: targetID, ProjectID: projectID, Privileges: validated}, nil
}

func (scope AuthoringScope) Authorize(targetID, projectID string, privilege Privilege) error {
	if strings.TrimSpace(targetID) != scope.TargetID ||
		strings.TrimSpace(projectID) != scope.ProjectID ||
		!slices.Contains(scope.Privileges, privilege) {
		return ErrAuthoringScopeDenied
	}
	return nil
}

type DeviceAuthorization struct {
	ID             string
	ClientID       string
	DeviceCodeHash string
	UserCodeHash   string
	Scope          AuthoringScope
	Status         DeviceAuthorizationStatus
	PrincipalID    string
	ExpiresAt      time.Time
	PollInterval   time.Duration
	LastPolledAt   time.Time
	CreatedAt      time.Time
	ApprovedAt     time.Time
	DeniedAt       time.Time
	ConsumedAt     time.Time
}

type DeviceAuthorizationResponse struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int64
	Interval                int64
}

type AuthoringSession struct {
	ID          string
	Kind        AuthoringSessionKind
	ClientID    string
	PrincipalID string
	Scope       AuthoringScope
	CreatedAt   time.Time
	LastUsedAt  time.Time
	ExpiresAt   time.Time
	RevokedAt   time.Time
}

type AuthoringCredential struct {
	ID               string
	Principal        Principal
	Session          AuthoringSession
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

type DeviceCredentialIssue struct {
	DeviceCodeHash   string
	ClientID         string
	Now              time.Time
	SessionID        string
	CredentialID     string
	AccessTokenHash  string
	RefreshTokenHash string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

type WorkloadCredentialIssue struct {
	Session         AuthoringSession
	CredentialID    string
	AccessTokenHash string
	AccessExpiresAt time.Time
}

type AuthoringCredentialRotation struct {
	RefreshTokenHash    string
	Now                 time.Time
	CredentialID        string
	AccessTokenHash     string
	RefreshTokenHashNew string
	AccessExpiresAt     time.Time
	RefreshExpiresAt    time.Time
}

// AuthoringAuthRepository is an Access-owned persistence port. Implementations
// must make device-code consumption, refresh rotation, replay-family
// revocation, and credential creation atomic.
type AuthoringAuthRepository interface {
	CreateDeviceAuthorization(context.Context, DeviceAuthorization) error
	DeviceAuthorizationByUserCodeHash(context.Context, string) (DeviceAuthorization, error)
	ApproveDeviceAuthorization(context.Context, string, string, time.Time) error
	DenyDeviceAuthorization(context.Context, string, string, time.Time) error
	IssueDeviceCredential(context.Context, DeviceCredentialIssue) (AuthoringCredential, error)
	CreateWorkloadCredential(context.Context, WorkloadCredentialIssue) (AuthoringCredential, error)
	RotateAuthoringCredential(context.Context, AuthoringCredentialRotation) (AuthoringCredential, error)
	AuthoringCredentialByAccessTokenHash(context.Context, string, time.Time) (AuthoringCredential, error)
	ListAuthoringSessions(context.Context, string) ([]AuthoringSession, error)
	RevokeAuthoringSession(context.Context, string, string, time.Time) error
	RevokeAuthoringSessionByAccessTokenHash(context.Context, string, time.Time) error
	PrincipalForServicePrincipalSecret(context.Context, string, string) (Principal, error)
}

type AuthoringAuthConfig struct {
	InstanceID      string
	CanonicalOrigin string
	DeviceTTL       time.Duration
	PollInterval    time.Duration
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	WorkloadMaxTTL  time.Duration
	Now             func() time.Time
	Random          io.Reader
}

type AuthoringAuthService struct {
	repository      AuthoringAuthRepository
	instanceID      string
	canonicalOrigin string
	deviceTTL       time.Duration
	pollInterval    time.Duration
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
	workloadMaxTTL  time.Duration
	now             func() time.Time
	random          io.Reader
}

func (service *AuthoringAuthService) InstanceID() string {
	if service == nil {
		return ""
	}
	return service.instanceID
}

func NewAuthoringAuthService(repository AuthoringAuthRepository, config AuthoringAuthConfig) (*AuthoringAuthService, error) {
	if repository == nil {
		return nil, fmt.Errorf("authoring auth repository is required")
	}
	instanceID := strings.TrimSpace(config.InstanceID)
	if instanceID == "" {
		return nil, fmt.Errorf("authoring auth instance ID is required")
	}
	origin, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(config.CanonicalOrigin), "/"))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, fmt.Errorf("authoring auth canonical origin is invalid")
	}
	if origin.Scheme != "https" && origin.Hostname() != "localhost" && origin.Hostname() != "127.0.0.1" && origin.Hostname() != "::1" {
		return nil, fmt.Errorf("authoring auth canonical origin must use HTTPS")
	}
	if config.DeviceTTL <= 0 {
		config.DeviceTTL = 10 * time.Minute
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if config.AccessTokenTTL <= 0 {
		config.AccessTokenTTL = 15 * time.Minute
	}
	if config.RefreshTokenTTL <= config.AccessTokenTTL {
		return nil, fmt.Errorf("authoring refresh-token lifetime must exceed access-token lifetime")
	}
	if config.WorkloadMaxTTL <= 0 {
		config.WorkloadMaxTTL = 30 * time.Minute
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	return &AuthoringAuthService{
		repository: repository, instanceID: instanceID, canonicalOrigin: strings.TrimSuffix(origin.String(), "/"),
		deviceTTL: config.DeviceTTL, pollInterval: config.PollInterval,
		accessTokenTTL: config.AccessTokenTTL, refreshTokenTTL: config.RefreshTokenTTL,
		workloadMaxTTL: config.WorkloadMaxTTL, now: config.Now, random: config.Random,
	}, nil
}

func (service *AuthoringAuthService) BeginDeviceAuthorization(ctx context.Context, scope AuthoringScope) (DeviceAuthorizationResponse, error) {
	if err := service.validateScope(scope); err != nil {
		return DeviceAuthorizationResponse{}, err
	}
	now := service.now().UTC()
	deviceCode, err := service.randomSecret("lv_cli_device_", 32)
	if err != nil {
		return DeviceAuthorizationResponse{}, err
	}
	userCode, err := service.randomUserCode()
	if err != nil {
		return DeviceAuthorizationResponse{}, err
	}
	id, err := service.randomSecret("da_", 18)
	if err != nil {
		return DeviceAuthorizationResponse{}, err
	}
	record := DeviceAuthorization{
		ID: id, ClientID: AuthoringCLIClientID,
		DeviceCodeHash: hashAuthoringSecret(deviceCode), UserCodeHash: hashAuthoringSecret(normalizeUserCode(userCode)),
		Scope: scope, Status: DeviceAuthorizationPending,
		ExpiresAt: now.Add(service.deviceTTL), PollInterval: service.pollInterval, CreatedAt: now,
	}
	if err := service.repository.CreateDeviceAuthorization(ctx, record); err != nil {
		return DeviceAuthorizationResponse{}, err
	}
	verificationURI := service.canonicalOrigin + "/device"
	return DeviceAuthorizationResponse{
		DeviceCode: deviceCode, UserCode: userCode, VerificationURI: verificationURI,
		VerificationURIComplete: verificationURI + "?user_code=" + url.QueryEscape(userCode),
		ExpiresIn:               int64(service.deviceTTL / time.Second), Interval: int64(service.pollInterval / time.Second),
	}, nil
}

func (service *AuthoringAuthService) ApproveDeviceAuthorization(ctx context.Context, principal Principal, userCode string) error {
	if !validHumanAuthoringPrincipal(principal) {
		return ErrInvalidAuthoringPrincipal
	}
	hash := hashAuthoringSecret(normalizeUserCode(userCode))
	record, err := service.repository.DeviceAuthorizationByUserCodeHash(ctx, hash)
	if err != nil {
		return err
	}
	if record.ClientID != AuthoringCLIClientID || record.Scope.TargetID != service.instanceID {
		return ErrDeviceAuthorizationDenied
	}
	return service.repository.ApproveDeviceAuthorization(ctx, record.ID, principal.ID, service.now().UTC())
}

func (service *AuthoringAuthService) DenyDeviceAuthorization(ctx context.Context, principal Principal, userCode string) error {
	if !validHumanAuthoringPrincipal(principal) {
		return ErrInvalidAuthoringPrincipal
	}
	hash := hashAuthoringSecret(normalizeUserCode(userCode))
	record, err := service.repository.DeviceAuthorizationByUserCodeHash(ctx, hash)
	if err != nil {
		return err
	}
	return service.repository.DenyDeviceAuthorization(ctx, record.ID, principal.ID, service.now().UTC())
}

func (service *AuthoringAuthService) ExchangeDeviceCode(ctx context.Context, deviceCode string) (AuthoringTokenSet, error) {
	now := service.now().UTC()
	accessToken, refreshToken, issue, err := service.newHumanCredentialIssue(deviceCode, now)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	credential, err := service.repository.IssueDeviceCredential(ctx, issue)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	if err := service.validateIssuedCredential(credential, AuthoringSessionHumanCLI); err != nil {
		return AuthoringTokenSet{}, err
	}
	return tokenSet(accessToken, refreshToken, now, credential), nil
}

type AuthoringTokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int64
	Session      AuthoringSession
}

func (service *AuthoringAuthService) Refresh(ctx context.Context, refreshToken string) (AuthoringTokenSet, error) {
	if !strings.HasPrefix(refreshToken, "lv_cli_refresh_") {
		return AuthoringTokenSet{}, ErrInvalidAuthoringCredential
	}
	now := service.now().UTC()
	accessToken, err := service.randomSecret("lv_cli_access_", 32)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	nextRefreshToken, err := service.randomSecret("lv_cli_refresh_", 32)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	credentialID, err := service.randomSecret("ac_", 18)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	credential, err := service.repository.RotateAuthoringCredential(ctx, AuthoringCredentialRotation{
		RefreshTokenHash: hashAuthoringSecret(refreshToken), Now: now, CredentialID: credentialID,
		AccessTokenHash: hashAuthoringSecret(accessToken), RefreshTokenHashNew: hashAuthoringSecret(nextRefreshToken),
		AccessExpiresAt: now.Add(service.accessTokenTTL), RefreshExpiresAt: now.Add(service.refreshTokenTTL),
	})
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	if err := service.validateIssuedCredential(credential, AuthoringSessionHumanCLI); err != nil {
		return AuthoringTokenSet{}, err
	}
	return tokenSet(accessToken, nextRefreshToken, now, credential), nil
}

type WorkloadIdentityInput struct {
	ClientID     string
	ClientSecret string
	Scope        AuthoringScope
	Lifetime     time.Duration
}

func (service *AuthoringAuthService) ExchangeWorkloadIdentity(ctx context.Context, input WorkloadIdentityInput) (AuthoringTokenSet, error) {
	if err := service.validateScope(input.Scope); err != nil {
		return AuthoringTokenSet{}, err
	}
	if input.Lifetime <= 0 || input.Lifetime > service.workloadMaxTTL {
		return AuthoringTokenSet{}, fmt.Errorf("%w: must be between zero and %s", ErrInvalidWorkloadLifetime, service.workloadMaxTTL)
	}
	principal, err := service.repository.PrincipalForServicePrincipalSecret(ctx, strings.TrimSpace(input.ClientID), input.ClientSecret)
	if err != nil || principal.Kind != PrincipalKindServicePrincipal || principal.AccessDisabled() {
		return AuthoringTokenSet{}, ErrInvalidAuthoringPrincipal
	}
	now := service.now().UTC()
	accessToken, err := service.randomSecret("lv_workload_access_", 32)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	sessionID, err := service.randomSecret("as_", 18)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	credentialID, err := service.randomSecret("ac_", 18)
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	expiresAt := now.Add(input.Lifetime)
	credential, err := service.repository.CreateWorkloadCredential(ctx, WorkloadCredentialIssue{
		Session: AuthoringSession{
			ID: sessionID, Kind: AuthoringSessionWorkload, ClientID: principal.ID,
			PrincipalID: principal.ID, Scope: input.Scope, CreatedAt: now, ExpiresAt: expiresAt,
		},
		CredentialID: credentialID, AccessTokenHash: hashAuthoringSecret(accessToken), AccessExpiresAt: expiresAt,
	})
	if err != nil {
		return AuthoringTokenSet{}, err
	}
	if err := service.validateIssuedCredential(credential, AuthoringSessionWorkload); err != nil {
		return AuthoringTokenSet{}, err
	}
	return tokenSet(accessToken, "", now, credential), nil
}

func (service *AuthoringAuthService) Authenticate(ctx context.Context, accessToken, targetID, projectID string, privilege Privilege) (AuthoringCredential, error) {
	credential, err := service.Resolve(ctx, accessToken)
	if err != nil {
		return AuthoringCredential{}, err
	}
	if err := credential.Session.Scope.Authorize(targetID, projectID, privilege); err != nil {
		return AuthoringCredential{}, err
	}
	return credential, nil
}

// Resolve validates an opaque authoring access token without authorizing an
// action. HTTP adapters must subsequently enforce its exact scope.
func (service *AuthoringAuthService) Resolve(ctx context.Context, accessToken string) (AuthoringCredential, error) {
	if !strings.HasPrefix(accessToken, "lv_cli_access_") && !strings.HasPrefix(accessToken, "lv_workload_access_") {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	now := service.now().UTC()
	credential, err := service.repository.AuthoringCredentialByAccessTokenHash(ctx, hashAuthoringSecret(accessToken), now)
	if err != nil {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	if credential.Principal.AccessDisabled() || credential.Session.RevokedAt.IsZero() == false {
		return AuthoringCredential{}, ErrInvalidAuthoringCredential
	}
	if !now.Before(credential.AccessExpiresAt) || (!credential.Session.ExpiresAt.IsZero() && !now.Before(credential.Session.ExpiresAt)) {
		return AuthoringCredential{}, ErrAuthoringCredentialExpired
	}
	return credential, nil
}

func (service *AuthoringAuthService) ListSessions(ctx context.Context, principalID string) ([]AuthoringSession, error) {
	if strings.TrimSpace(principalID) == "" {
		return nil, ErrInvalidAuthoringPrincipal
	}
	return service.repository.ListAuthoringSessions(ctx, principalID)
}

func (service *AuthoringAuthService) RevokeSession(ctx context.Context, principalID, sessionID string) error {
	if strings.TrimSpace(principalID) == "" || strings.TrimSpace(sessionID) == "" {
		return ErrInvalidAuthoringCredential
	}
	return service.repository.RevokeAuthoringSession(ctx, principalID, sessionID, service.now().UTC())
}

func (service *AuthoringAuthService) RevokeAccessToken(ctx context.Context, accessToken string) error {
	if !strings.HasPrefix(accessToken, "lv_cli_access_") && !strings.HasPrefix(accessToken, "lv_workload_access_") {
		return ErrInvalidAuthoringCredential
	}
	return service.repository.RevokeAuthoringSessionByAccessTokenHash(
		ctx, hashAuthoringSecret(accessToken), service.now().UTC(),
	)
}

func (service *AuthoringAuthService) validateScope(scope AuthoringScope) error {
	validated, err := NewAuthoringScope(scope.TargetID, scope.ProjectID, scope.Privileges)
	if err != nil {
		return err
	}
	if validated.TargetID != service.instanceID {
		return ErrAuthoringScopeDenied
	}
	return nil
}

func (service *AuthoringAuthService) validateIssuedCredential(credential AuthoringCredential, kind AuthoringSessionKind) error {
	if credential.ID == "" || credential.Principal.ID == "" || credential.Session.ID == "" ||
		credential.Session.Kind != kind || credential.Session.Scope.TargetID != service.instanceID {
		return ErrInvalidAuthoringCredential
	}
	return nil
}

func (service *AuthoringAuthService) newHumanCredentialIssue(deviceCode string, now time.Time) (string, string, DeviceCredentialIssue, error) {
	if !strings.HasPrefix(deviceCode, "lv_cli_device_") {
		return "", "", DeviceCredentialIssue{}, ErrInvalidAuthoringCredential
	}
	accessToken, err := service.randomSecret("lv_cli_access_", 32)
	if err != nil {
		return "", "", DeviceCredentialIssue{}, err
	}
	refreshToken, err := service.randomSecret("lv_cli_refresh_", 32)
	if err != nil {
		return "", "", DeviceCredentialIssue{}, err
	}
	sessionID, err := service.randomSecret("as_", 18)
	if err != nil {
		return "", "", DeviceCredentialIssue{}, err
	}
	credentialID, err := service.randomSecret("ac_", 18)
	if err != nil {
		return "", "", DeviceCredentialIssue{}, err
	}
	return accessToken, refreshToken, DeviceCredentialIssue{
		DeviceCodeHash: hashAuthoringSecret(deviceCode), ClientID: AuthoringCLIClientID, Now: now,
		SessionID: sessionID, CredentialID: credentialID,
		AccessTokenHash: hashAuthoringSecret(accessToken), RefreshTokenHash: hashAuthoringSecret(refreshToken),
		AccessExpiresAt: now.Add(service.accessTokenTTL), RefreshExpiresAt: now.Add(service.refreshTokenTTL),
	}, nil
}

func (service *AuthoringAuthService) randomSecret(prefix string, byteCount int) (string, error) {
	bytes := make([]byte, byteCount)
	if _, err := io.ReadFull(service.random, bytes); err != nil {
		return "", fmt.Errorf("generate authoring credential: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (service *AuthoringAuthService) randomUserCode() (string, error) {
	const alphabet = "BCDFGHJKLMNPQRSTVWXYZ23456789"
	bytes := make([]byte, 8)
	if _, err := io.ReadFull(service.random, bytes); err != nil {
		return "", fmt.Errorf("generate device user code: %w", err)
	}
	for index := range bytes {
		bytes[index] = alphabet[int(bytes[index])%len(alphabet)]
	}
	return string(bytes[:4]) + "-" + string(bytes[4:]), nil
}

func validHumanAuthoringPrincipal(principal Principal) bool {
	return strings.TrimSpace(principal.ID) != "" &&
		principal.Kind == PrincipalKindUser &&
		!principal.AccessDisabled()
}

func tokenSet(accessToken, refreshToken string, now time.Time, credential AuthoringCredential) AuthoringTokenSet {
	return AuthoringTokenSet{
		AccessToken: accessToken, RefreshToken: refreshToken, TokenType: "Bearer",
		ExpiresIn: int64(credential.AccessExpiresAt.Sub(now) / time.Second), Session: credential.Session,
	}
}

func hashAuthoringSecret(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func normalizeUserCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	return strings.ReplaceAll(value, "-", "")
}
