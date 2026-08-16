package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

var ErrAuditTransaction = apigenfailure.New("audit_transaction", "audit transaction failed")
var ErrPrincipalAlreadyExists = apigenfailure.New("conflict", "principal already exists")

// ErrForbidden is the canonical error for an authorization decision that
// evaluated successfully but did not grant the requested privilege. Access
// consumers may preserve this distinction from repository failures.
var ErrForbidden = errors.New("forbidden")

type Principal struct {
	ID          string
	Kind        PrincipalKind
	Email       string
	DisplayName string
	DisabledAt  string
	BlockedAt   string
	CreatedAt   string
	UpdatedAt   string
	LastSeenAt  string
}

// AccessDisabled reports the effective access state. DisabledAt is controlled
// by the identity lifecycle authority, while BlockedAt is a LeapView-owned
// emergency override. Either state rejects credentials and authorization.
func (principal Principal) AccessDisabled() bool {
	return strings.TrimSpace(principal.DisabledAt) != "" || strings.TrimSpace(principal.BlockedAt) != ""
}

type ThemeMode string

const (
	ThemeSystem          ThemeMode = "system"
	ThemeLight           ThemeMode = "light"
	ThemeDark            ThemeMode = "dark"
	ThemeDarkDimmed      ThemeMode = "dark_dimmed"
	ThemeLightColorblind ThemeMode = "light_colorblind"
	ThemeDarkColorblind  ThemeMode = "dark_colorblind"
	ThemeLightTritanopia ThemeMode = "light_tritanopia"
	ThemeDarkTritanopia  ThemeMode = "dark_tritanopia"
)

func ParseThemeMode(value string) (ThemeMode, bool) {
	theme := ThemeMode(strings.TrimSpace(value))
	switch theme {
	case ThemeSystem, ThemeLight, ThemeDark, ThemeDarkDimmed,
		ThemeLightColorblind, ThemeDarkColorblind,
		ThemeLightTritanopia, ThemeDarkTritanopia:
		return theme, true
	default:
		return "", false
	}
}

type PrincipalPreferences struct {
	PrincipalID string
	Theme       ThemeMode
	UpdatedAt   string
}

type PrincipalPreferencesReader interface {
	PrincipalPreferences(context.Context, string) (PrincipalPreferences, error)
}

type PrincipalPreferencesWriter interface {
	SetPrincipalTheme(context.Context, string, ThemeMode) (PrincipalPreferences, error)
}

type AuditedPrincipalPreferences interface {
	PrincipalPreferencesReader
	SetPrincipalThemeAudited(context.Context, string, ThemeMode) error
}

type PrincipalKind string

const (
	PrincipalKindUser                 PrincipalKind = "user"
	PrincipalKindGroup                PrincipalKind = "group"
	PrincipalKindServicePrincipal     PrincipalKind = "service_principal"
	PrincipalKindDashboardPublication PrincipalKind = "dashboard_publication"
)

type PlatformRoleInput struct {
	PrincipalID string
	Email       string
	DisplayName string
	Role        PlatformRole
}

type PrincipalInput struct {
	ID          string
	Kind        PrincipalKind
	Email       string
	DisplayName string
}

type LocalUserInput struct {
	Email       string
	DisplayName string
	Password    string
	MustChange  bool
}

type LocalPasswordReset struct {
	Principal Principal
	Password  string
}

type LocalCredential struct {
	PrincipalID        string
	MustChangePassword bool
	CreatedAt          string
	UpdatedAt          string
	PasswordChangedAt  string
}

type IdentityManagementSource string

const (
	IdentityManagementLocal    IdentityManagementSource = "local"
	IdentityManagementExternal IdentityManagementSource = "external"
	IdentityManagementSystem   IdentityManagementSource = "system"
)

// PrincipalIdentityManagement describes which subsystem owns profile fields
// and whether a separate local credential exists. A principal can have both an
// external identity and a local credential; in that case the external provider
// still owns synchronized profile fields while the local password remains
// changeable when local authentication is enabled.
type PrincipalIdentityManagement struct {
	Source           IdentityManagementSource
	Provider         string
	HasLocalPassword bool
}

type PrincipalIdentityManagementRepository interface {
	PrincipalIdentityManagement(context.Context, string) (PrincipalIdentityManagement, error)
}

type PrincipalFilter struct {
	Email string
	Query string
}

type ServicePrincipalInput struct {
	ID          string
	DisplayName string
}

type ServicePrincipalSecretInput struct {
	Name      string
	ExpiresAt time.Time
}

type ServicePrincipalSecret struct {
	ID                 string
	ServicePrincipalID string
	Name               string
	Secret             string
	ExpiresAt          string
	CreatedAt          string
	RevokedAt          string
}

type ExternalIdentityInput struct {
	Provider    string
	TenantID    string
	Subject     string
	Email       string
	DisplayName string
}

type Group struct {
	ID         string
	Provider   string
	ExternalID string
	Name       string
	CreatedAt  string
}

type GroupInput struct {
	ID         string
	Provider   string
	ExternalID string
	Name       string
}

type GroupMember struct {
	GroupID     string
	PrincipalID string
	Kind        PrincipalKind
	Email       string
	DisplayName string
	CreatedAt   string
}

type SCIMUserInput struct {
	ID          string
	ExternalID  string
	UserName    string
	Email       string
	DisplayName string
	Active      bool
}

type SCIMUser struct {
	Principal  Principal
	ExternalID string
}

type SCIMUserFilter struct {
	ID         string
	ExternalID string
	UserName   string
}

type SCIMGroupInput struct {
	ID         string
	ExternalID string
	Name       string
	MemberIDs  []string
}

type SCIMGroupFilter struct {
	ID          string
	ExternalID  string
	DisplayName string
}

type APITokenInput struct {
	PrincipalID  string
	Name         string
	Capabilities []Capability
	ExpiresAt    time.Time
}

const APITokenNameInitialPublisher = "initial-publisher"

type APIToken struct {
	ID           string
	PrincipalID  string
	Name         string
	Capabilities []Capability
	ExpiresAt    string
	CreatedAt    string
	LastUsedAt   string
	RevokedAt    string
}

type APICredential struct {
	Principal Principal
	Token     APIToken
	Authoring *AuthoringSession
}

type CredentialEvidence struct {
	Class       string
	ID          string
	PrincipalID string
	ExpiresAt   time.Time
}

type Session struct {
	ID                string
	PrincipalID       string
	Kind              SessionKind
	InstanceID        string
	ProfileID         string
	ClientID          string
	ExpiresAt         string
	AbsoluteExpiresAt string
	CreatedAt         string
	LastSeenAt        string
	RevokedAt         string
}

type SessionKind string

const (
	SessionKindBrowser SessionKind = "browser"
	SessionKindDesktop SessionKind = "desktop"

	DesktopSessionIdleTimeout      = 30 * time.Minute
	DesktopSessionAbsoluteLifetime = 8 * time.Hour
)

type DesktopSession struct {
	SessionID         string
	PrincipalID       string
	InstanceID        string
	ProfileID         string
	ClientID          string
	ExpiresAt         string
	AbsoluteExpiresAt string
	CreatedAt         string
}

type DesktopSessionRepository interface {
	CreateDesktopSession(
		ctx context.Context,
		principalID, instanceID, profileID string,
		ttl time.Duration,
	) (string, error)
	DesktopSessionForToken(ctx context.Context, token string) (DesktopSession, error)
	RevokeDesktopSession(ctx context.Context, token, instanceID, profileID string) error
}

type AuditEventInput struct {
	PrincipalID   string
	Action        string
	ResourceKind  string
	ResourceID    string
	Capability    Capability
	Status        string
	RequestID     string
	CorrelationID string
	MetadataJSON  string
}

type AuditEventFilter struct {
	PrincipalID  string
	Action       string
	ResourceKind string
	ResourceID   string
	Capability   Capability
	From         string
	To           string
	PageToken    string
	CursorTime   string
	CursorID     string
	Limit        int
}

type AuditEvent struct {
	ID            string
	PrincipalID   string
	Action        string
	ResourceKind  string
	ResourceID    string
	Capability    Capability
	Status        string
	RequestID     string
	CorrelationID string
	MetadataJSON  string
	CreatedAt     string
}

type Repository interface {
	PrincipalByID(ctx context.Context, id string) (Principal, error)
	ListPrincipals(ctx context.Context, filter PrincipalFilter) ([]Principal, error)
	SearchPrincipals(ctx context.Context, query string, limit int) ([]Principal, error)
	UpsertPrincipal(ctx context.Context, input PrincipalInput) (Principal, error)
	CreateLocalUser(ctx context.Context, input LocalUserInput) (LocalPasswordReset, error)
	VerifyLocalPassword(ctx context.Context, email, password string) (Principal, LocalCredential, error)
	ResetLocalPassword(ctx context.Context, principalID string) (LocalPasswordReset, error)
	ChangeLocalPassword(ctx context.Context, principalID, currentPassword, newPassword string) (LocalCredential, error)
	LocalCredential(ctx context.Context, principalID string) (LocalCredential, error)
	CreateServicePrincipal(ctx context.Context, input ServicePrincipalInput) (Principal, error)
	ListServicePrincipals(ctx context.Context) ([]Principal, error)
	UpdateServicePrincipal(ctx context.Context, id string, input ServicePrincipalInput) (Principal, error)
	DeleteServicePrincipal(ctx context.Context, id string) error
	CreateServicePrincipalSecret(ctx context.Context, servicePrincipalID string, input ServicePrincipalSecretInput) (string, ServicePrincipalSecret, error)
	RevokeServicePrincipalSecret(ctx context.Context, servicePrincipalID, secretID string) error
	PrincipalForServicePrincipalSecret(ctx context.Context, servicePrincipalID, secret string) (Principal, error)
	BootstrapAdmin(ctx context.Context, email string) error
	SetPlatformRole(ctx context.Context, input PlatformRoleInput) (Principal, error)
	ResolveExternalPrincipal(ctx context.Context, input ExternalIdentityInput) (Principal, error)
	UpsertSCIMUser(ctx context.Context, input SCIMUserInput) (SCIMUser, error)
	ListSCIMUsers(ctx context.Context, filter SCIMUserFilter) ([]SCIMUser, error)
	DisableSCIMUser(ctx context.Context, principalID string) (SCIMUser, error)
	UpsertGroup(ctx context.Context, input GroupInput) (Group, error)
	ListGroups(ctx context.Context) ([]Group, error)
	SearchGroups(ctx context.Context, query string, limit int) ([]Group, error)
	ListAllGroups(ctx context.Context) ([]Group, error)
	DeleteGroup(ctx context.Context, groupID string) error
	AddGroupMember(ctx context.Context, groupID, principalID string) error
	RemoveGroupMember(ctx context.Context, groupID, principalID string) error
	ListGroupMembersByGroup(ctx context.Context, groupID string) ([]GroupMember, error)
	ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error)
	UpsertSCIMGroup(ctx context.Context, input SCIMGroupInput) (Group, error)
	ListSCIMGroups(ctx context.Context, filter SCIMGroupFilter) ([]Group, error)
	DeleteSCIMGroup(ctx context.Context, groupID string) error
	AddSCIMGroupMember(ctx context.Context, groupID, principalID string) error
	RemoveSCIMGroupMember(ctx context.Context, groupID, principalID string) error
	ListSCIMGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error)
	CreateSession(ctx context.Context, principalID string, ttl time.Duration) (string, error)
	PrincipalForToken(ctx context.Context, token string) (Principal, error)
	DeleteSession(ctx context.Context, token string) error
	ListSessions(ctx context.Context, principalID string) ([]Session, error)
	RevokeSession(ctx context.Context, id string) error
	RevokeSessionForPrincipal(ctx context.Context, principalID, id string) error
	CreateAPIToken(ctx context.Context, principalID, name string) (string, error)
	CreateAPITokenWithMetadata(ctx context.Context, input APITokenInput) (string, APIToken, error)
	PrincipalForAPIToken(ctx context.Context, token string) (Principal, error)
	CredentialForAPIToken(ctx context.Context, token string) (APICredential, error)
	ListAPITokens(ctx context.Context, principalID string) ([]APIToken, error)
	RevokeAPIToken(ctx context.Context, id string) error
	RevokeAPITokenForPrincipal(ctx context.Context, principalID, id string) error
	RecordAuditEvent(ctx context.Context, input AuditEventInput) error
	ListAuditEvents(ctx context.Context, filter AuditEventFilter) ([]AuditEvent, error)
}

// AuditedMutationRepository commits a privileged mutation and its audit event
// as one unit. Production repositories should implement this so a successful
// mutation can never exist without its corresponding audit record.
type AuditedMutationRepository interface {
	RunAuditedMutation(context.Context, func(Repository) (AuditEventInput, error)) error
}

type AuditedMutationBatchRepository interface {
	RunAuditedMutationBatch(context.Context, func(Repository) ([]AuditEventInput, error)) error
}

func PrincipalIDForEmail(email string) string {
	return "email_" + stableID(NormalizeEmail(email))
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func stableID(value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return hex.EncodeToString(sum[:])[:32]
}
