package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

var ErrAuditTransaction = apigenfailure.New("audit_transaction", "audit transaction failed")
var ErrPrincipalAlreadyExists = apigenfailure.New("conflict", "principal already exists")
var ErrLocalPasswordPolicy = errors.New("local password does not meet policy")

// Authorization policy is target-owned mutable control state. The portable
// project manifest intentionally has no revision or digest fields; these
// values are supplied only by this target authority and are consumed by
// release planning as an exact qualified input.
const AuthorizationPolicyProfile = "leapview.authorization-policy/v1"

var (
	ErrAuthorizationPolicyNotFound       = errors.New("authorization policy was not found")
	ErrAuthorizationPolicyConflict       = errors.New("authorization policy conflicts with current state")
	ErrAuthorizationPolicyStaleRevision  = errors.New("authorization policy revision is stale")
	ErrAuthorizationPolicyIdempotency    = errors.New("authorization policy idempotency key conflicts with current state")
	ErrAuthorizationPolicyInvalidScope   = errors.New("authorization policy scope is invalid")
	ErrAuthorizationPolicyInvalidBinding = errors.New("authorization policy role binding is invalid")
)

// AuthorizationPolicyScope is the complete target-owned namespace for a
// mutable authorization policy. TargetID is required even when project and
// environment are already known: the same project can be served by several
// independently controlled targets.
type AuthorizationPolicyScope struct {
	TargetID    string `json:"targetId"`
	ProjectID   string `json:"projectId"`
	Environment string `json:"environment"`
}

// RoleBinding is the canonical project-wide RBAC assignment shared by the
// mutable target policy and immutable serving snapshots. Capabilities are
// captured rather than expanded at read time so a release can bind the exact
// role semantics it was qualified against.
type RoleBinding struct {
	ID           string       `json:"id"`
	Name         string       `json:"name,omitempty"`
	Subject      SubjectRef   `json:"subject"`
	Role         ProjectRole  `json:"role"`
	Capabilities []Capability `json:"capabilities"`
}

// AuthorizationPolicy is the exact target-owned policy document plus its
// durable authority metadata. RoleBindings is always a defensive copy when
// returned by a repository.
type AuthorizationPolicy struct {
	Scope        AuthorizationPolicyScope
	Revision     int64
	Digest       string
	RoleBindings []RoleBinding
}

// AuthorizationRoleBindingInput is one exact-role upsert command. The
// expected revision is a compare-and-swap fence: zero creates the first
// policy revision, while a positive value must equal the current revision.
// IdempotencyKey is mandatory so a retry after an unknown commit cannot apply
// a second mutation.
type AuthorizationRoleBindingInput struct {
	Scope            AuthorizationPolicyScope
	Binding          RoleBinding
	ExpectedRevision int64
	IdempotencyKey   string
}

// AuthorizationPolicyReader is intentionally narrow so release planning can
// consume target policy state without gaining mutation authority.
type AuthorizationPolicyReader interface {
	AuthorizationPolicy(context.Context, AuthorizationPolicyScope) (AuthorizationPolicy, error)
	AuthorizationPolicyRevision(context.Context, AuthorizationPolicyScope, int64) (AuthorizationPolicy, error)
}

// AuthorizationPolicyWriter is the mutation half of the target policy
// boundary. Implementations must preserve CAS and idempotency semantics.
type AuthorizationPolicyWriter interface {
	UpsertAuthorizationRoleBinding(context.Context, AuthorizationRoleBindingInput) (AuthorizationPolicy, error)
}

type AuthorizationPolicyRepository interface {
	AuthorizationPolicyReader
	AuthorizationPolicyWriter
}

// ValidateAuthorizationRoleBinding applies the same role and capability
// validation used by serving snapshots without requiring a project graph.
func ValidateAuthorizationRoleBinding(binding RoleBinding) error {
	if strings.TrimSpace(binding.ID) != binding.ID || binding.ID == "" || len(binding.ID) > 255 || strings.ContainsAny(binding.ID, "\x00\r\n") {
		return fmt.Errorf("%w: binding id is invalid", ErrAuthorizationPolicyInvalidBinding)
	}
	if strings.TrimSpace(binding.Name) != binding.Name || len(binding.Name) > 255 || strings.ContainsAny(binding.Name, "\x00\r\n") {
		return fmt.Errorf("%w: binding name is invalid", ErrAuthorizationPolicyInvalidBinding)
	}
	if err := binding.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %w", ErrAuthorizationPolicyInvalidBinding, err)
	}
	role, err := ParseProjectRole(string(binding.Role))
	if err != nil {
		return fmt.Errorf("%w: role: %w", ErrAuthorizationPolicyInvalidBinding, err)
	}
	want := ProjectRoleCapabilities(role)
	if len(binding.Capabilities) != len(want) {
		return fmt.Errorf("%w: role %q requires exactly %d capabilities", ErrAuthorizationPolicyInvalidBinding, role, len(want))
	}
	for index, capability := range binding.Capabilities {
		if capability != want[index] {
			return fmt.Errorf("%w: role %q has a non-canonical capability bundle", ErrAuthorizationPolicyInvalidBinding, role)
		}
	}
	return nil
}

// ValidateAuthorizationPolicyScope rejects malformed or ambiguous target
// namespaces before any database access. It is deliberately stricter than a
// SQL text type so callers cannot smuggle scope through whitespace/control
// characters.
func ValidateAuthorizationPolicyScope(scope AuthorizationPolicyScope) error {
	for label, value := range map[string]string{"target id": scope.TargetID, "project id": scope.ProjectID, "environment": scope.Environment} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n\t") {
			return fmt.Errorf("%w: %s is invalid", ErrAuthorizationPolicyInvalidScope, label)
		}
	}
	return nil
}

// AuthorizationPolicyDigest computes the stable identity of one qualified
// target policy. Input ordering never affects the result; duplicate binding
// IDs or subject/role keys are rejected rather than silently canonicalized.
func AuthorizationPolicyDigest(scope AuthorizationPolicyScope, bindings []RoleBinding) (string, error) {
	if err := ValidateAuthorizationPolicyScope(scope); err != nil {
		return "", err
	}
	canonical := append([]RoleBinding(nil), bindings...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	seenID := make(map[string]struct{}, len(canonical))
	seenSubjectRole := make(map[string]struct{}, len(canonical))
	for i := range canonical {
		if err := ValidateAuthorizationRoleBinding(canonical[i]); err != nil {
			return "", fmt.Errorf("binding %q: %w", canonical[i].ID, err)
		}
		if _, exists := seenID[canonical[i].ID]; exists {
			return "", fmt.Errorf("%w: duplicate binding id %q", ErrAuthorizationPolicyConflict, canonical[i].ID)
		}
		seenID[canonical[i].ID] = struct{}{}
		key := string(canonical[i].Subject.Kind) + "\x00" + canonical[i].Subject.ID + "\x00" + string(canonical[i].Role)
		if _, exists := seenSubjectRole[key]; exists {
			return "", fmt.Errorf("%w: duplicate subject/role for %q", ErrAuthorizationPolicyConflict, canonical[i].ID)
		}
		seenSubjectRole[key] = struct{}{}
	}
	wire := struct {
		Profile      string                   `json:"profile"`
		Scope        AuthorizationPolicyScope `json:"scope"`
		RoleBindings []RoleBinding            `json:"roleBindings"`
	}{Profile: AuthorizationPolicyProfile, Scope: scope, RoleBindings: canonical}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("encode authorization policy digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

const (
	MinimumLocalPasswordCharacters = 12
	MaximumLocalPasswordBytes      = 1024
)

// ValidateLocalPassword applies the single local-credential boundary used by
// browser and API password changes. Passwords are opaque: callers must not
// trim or otherwise normalize them before validation or hashing.
func ValidateLocalPassword(password string) error {
	switch {
	case !utf8.ValidString(password):
		return fmt.Errorf("%w: password must be valid UTF-8", ErrLocalPasswordPolicy)
	case utf8.RuneCountInString(password) < MinimumLocalPasswordCharacters:
		return fmt.Errorf("%w: password must contain at least %d characters", ErrLocalPasswordPolicy, MinimumLocalPasswordCharacters)
	case len(password) > MaximumLocalPasswordBytes:
		return fmt.Errorf("%w: password must not exceed %d bytes", ErrLocalPasswordPolicy, MaximumLocalPasswordBytes)
	case isCommonOrBreachedLocalPassword(password):
		return fmt.Errorf("%w: password is too common or has appeared in a password breach", ErrLocalPasswordPolicy)
	default:
		return nil
	}
}

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

// BootstrapAPITokenEvidenceReader is the narrow durable revalidation port
// used by the protected first-activation path. Implementations must resolve
// the token by its durable ID (never by request-held capabilities), bind it
// to the actor, and require a currently enabled platform administrator.
type BootstrapAPITokenEvidenceReader interface {
	BootstrapAPITokenEvidence(context.Context, string, string, time.Time) (APIToken, error)
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

// PlatformAdminReader is the narrow durable identity query used by
// instance-wide administration. It deliberately does not expose project
// roles or serving-generation state: platform administration is granted only
// by the durable platform_role_bindings table and the principal's current
// enabled state.
type PlatformAdminReader interface {
	IsPlatformAdmin(context.Context, string) (bool, error)
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
