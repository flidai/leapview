package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
)

var ErrAuditTransaction = apigenfailure.New("audit_transaction", "audit transaction failed")
var ErrPrincipalAlreadyExists = apigenfailure.New("conflict", "principal already exists")
var ErrPrincipalOwnsSecurableObject = apigenfailure.New("conflict", "principal owns a securable object; transfer ownership before deletion")

type Privilege string

const (
	PrivilegeUseWorkspace             Privilege = "USE_WORKSPACE"
	PrivilegeViewItem                 Privilege = "VIEW_ITEM"
	PrivilegeEditItem                 Privilege = "EDIT_ITEM"
	PrivilegeManageItem               Privilege = "MANAGE_ITEM"
	PrivilegeQueryData                Privilege = "QUERY_DATA"
	PrivilegePreviewData              Privilege = "PREVIEW_DATA"
	PrivilegeTestDataPolicy           Privilege = "TEST_DATA_POLICY"
	PrivilegeRefreshData              Privilege = "REFRESH_DATA"
	PrivilegeAuthorProject            Privilege = "AUTHOR_PROJECT"
	PrivilegePublishRelease           Privilege = "PUBLISH_RELEASE"
	PrivilegeReviewCandidate          Privilege = "REVIEW_CANDIDATE"
	PrivilegeRequestDeployment        Privilege = "REQUEST_DEPLOYMENT"
	PrivilegeApproveDeployment        Privilege = "APPROVE_DEPLOYMENT"
	PrivilegeDeploy                   Privilege = "DEPLOY"
	PrivilegeActivateDeployment       Privilege = "ACTIVATE_DEPLOYMENT"
	PrivilegeVerifyDeployment         Privilege = "VERIFY_DEPLOYMENT"
	PrivilegeRollbackDeployment       Privilege = "ROLLBACK_DEPLOYMENT"
	PrivilegeManagePublications       Privilege = "MANAGE_PUBLICATIONS"
	PrivilegeUseAgent                 Privilege = "USE_AGENT"
	PrivilegeViewAgent                Privilege = "VIEW_AGENT"
	PrivilegeManageGrants             Privilege = "MANAGE_GRANTS"
	PrivilegeViewAudit                Privilege = "VIEW_AUDIT"
	PrivilegeManageWorkspace          Privilege = "MANAGE_WORKSPACE"
	PrivilegeManagePlatform           Privilege = "MANAGE_PLATFORM"
	PrivilegeViewData                 Privilege = "VIEW_DATA"
	PrivilegeIngestData               Privilege = "INGEST_DATA"
	PrivilegeManageConnectionMetadata Privilege = "MANAGE_CONNECTION_METADATA"
	PrivilegeTestConnection           Privilege = "TEST_CONNECTION"
	PrivilegeViewConnectionHealth     Privilege = "VIEW_CONNECTION_HEALTH"
)

func ParsePrivilege(value string) (Privilege, bool) {
	privilege := Privilege(strings.TrimSpace(value))
	switch privilege {
	case PrivilegeUseWorkspace, PrivilegeViewItem, PrivilegeEditItem, PrivilegeManageItem,
		PrivilegeQueryData, PrivilegePreviewData, PrivilegeTestDataPolicy, PrivilegeRefreshData,
		PrivilegeAuthorProject, PrivilegePublishRelease, PrivilegeReviewCandidate,
		PrivilegeRequestDeployment, PrivilegeApproveDeployment, PrivilegeDeploy,
		PrivilegeActivateDeployment, PrivilegeVerifyDeployment, PrivilegeRollbackDeployment,
		PrivilegeManagePublications, PrivilegeUseAgent, PrivilegeViewAgent,
		PrivilegeManageGrants, PrivilegeViewAudit, PrivilegeManageWorkspace,
		PrivilegeManagePlatform, PrivilegeViewData, PrivilegeIngestData,
		PrivilegeManageConnectionMetadata, PrivilegeTestConnection, PrivilegeViewConnectionHealth:
		return privilege, true
	default:
		return "", false
	}
}

const (
	RoleOwner               = "owner"
	RoleAdmin               = "admin"
	RoleDeployer            = "deployer"
	RoleContributor         = "contributor"
	RoleEditor              = "editor"
	RoleMember              = "member"
	RoleViewer              = "viewer"
	RoleDataDeployer        = "data_deployer"
	RoleConnectionOperator  = "connection_operator"
	RolePlatformAdmin       = "platform_admin"
	RoleProjectAuthor       = "project_author"
	RoleReleasePublisher    = "release_publisher"
	RoleDeploymentReviewer  = "deployment_reviewer"
	RoleDeploymentActivator = "deployment_activator"
	RoleDeploymentVerifier  = "deployment_verifier"
	RoleRollbackOperator    = "rollback_operator"
)

var defaultRoles = []Role{
	{
		Name: RoleOwner,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeEditItem,
			PrivilegeManageItem,
			PrivilegeQueryData,
			PrivilegePreviewData,
			PrivilegeTestDataPolicy,
			PrivilegeRefreshData,
			PrivilegeAuthorProject,
			PrivilegePublishRelease,
			PrivilegeReviewCandidate,
			PrivilegeRequestDeployment,
			PrivilegeApproveDeployment,
			PrivilegeDeploy,
			PrivilegeActivateDeployment,
			PrivilegeVerifyDeployment,
			PrivilegeRollbackDeployment,
			PrivilegeManagePublications,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
			PrivilegeManageGrants,
			PrivilegeViewAudit,
			PrivilegeManageWorkspace,
		},
	},
	{
		Name: RoleAdmin,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeEditItem,
			PrivilegeManageItem,
			PrivilegeQueryData,
			PrivilegePreviewData,
			PrivilegeTestDataPolicy,
			PrivilegeRefreshData,
			PrivilegeAuthorProject,
			PrivilegePublishRelease,
			PrivilegeReviewCandidate,
			PrivilegeRequestDeployment,
			PrivilegeApproveDeployment,
			PrivilegeDeploy,
			PrivilegeActivateDeployment,
			PrivilegeVerifyDeployment,
			PrivilegeRollbackDeployment,
			PrivilegeManagePublications,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
			PrivilegeManageGrants,
			PrivilegeViewAudit,
			PrivilegeManageWorkspace,
		},
	},
	{
		Name: RoleDeployer,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeQueryData,
			PrivilegeRefreshData,
			PrivilegePublishRelease,
			PrivilegeReviewCandidate,
			PrivilegeRequestDeployment,
			PrivilegeDeploy,
			PrivilegeActivateDeployment,
			PrivilegeVerifyDeployment,
			PrivilegeRollbackDeployment,
		},
	},
	{
		Name: RoleContributor,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeEditItem,
			PrivilegeQueryData,
			PrivilegeRefreshData,
			PrivilegeAuthorProject,
			PrivilegePublishRelease,
			PrivilegeRequestDeployment,
			PrivilegeDeploy,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
		},
	},
	{
		Name: RoleEditor,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeEditItem,
			PrivilegeQueryData,
			PrivilegeRefreshData,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
		},
	},
	{
		Name: RoleMember,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeEditItem,
			PrivilegeManageItem,
			PrivilegeQueryData,
			PrivilegeRefreshData,
			PrivilegeAuthorProject,
			PrivilegeDeploy,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
		},
	},
	{
		Name: RoleViewer,
		Privileges: []Privilege{
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeQueryData,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
		},
	},
	{
		Name: RoleDataDeployer,
		Privileges: []Privilege{
			PrivilegeViewData,
			PrivilegeIngestData,
		},
	},
	{
		Name: RoleConnectionOperator,
		Privileges: []Privilege{
			PrivilegeManageConnectionMetadata,
			PrivilegeTestConnection,
			PrivilegeViewConnectionHealth,
		},
	},
	{
		Name:       RoleProjectAuthor,
		Privileges: []Privilege{PrivilegeAuthorProject},
	},
	{
		Name: RoleReleasePublisher,
		Privileges: []Privilege{
			PrivilegePublishRelease,
			PrivilegeRequestDeployment,
		},
	},
	{
		Name: RoleDeploymentReviewer,
		Privileges: []Privilege{
			PrivilegeReviewCandidate,
			PrivilegeApproveDeployment,
		},
	},
	{
		Name:       RoleDeploymentActivator,
		Privileges: []Privilege{PrivilegeActivateDeployment},
	},
	{
		Name:       RoleDeploymentVerifier,
		Privileges: []Privilege{PrivilegeVerifyDeployment},
	},
	{
		Name:       RoleRollbackOperator,
		Privileges: []Privilege{PrivilegeRollbackDeployment},
	},
	{
		Name: RolePlatformAdmin,
		Privileges: []Privilege{
			PrivilegeManagePlatform,
			PrivilegeViewData,
			PrivilegeIngestData,
			PrivilegeUseWorkspace,
			PrivilegeViewItem,
			PrivilegeEditItem,
			PrivilegeManageItem,
			PrivilegeQueryData,
			PrivilegePreviewData,
			PrivilegeTestDataPolicy,
			PrivilegeRefreshData,
			PrivilegeAuthorProject,
			PrivilegePublishRelease,
			PrivilegeReviewCandidate,
			PrivilegeRequestDeployment,
			PrivilegeApproveDeployment,
			PrivilegeDeploy,
			PrivilegeActivateDeployment,
			PrivilegeVerifyDeployment,
			PrivilegeRollbackDeployment,
			PrivilegeManagePublications,
			PrivilegeUseAgent,
			PrivilegeViewAgent,
			PrivilegeManageGrants,
			PrivilegeViewAudit,
			PrivilegeManageWorkspace,
			PrivilegeManageConnectionMetadata,
			PrivilegeTestConnection,
			PrivilegeViewConnectionHealth,
		},
	},
}

type Principal struct {
	ID          string
	Kind        PrincipalKind
	Email       string
	DisplayName string
	DisabledAt  string
	CreatedAt   string
	UpdatedAt   string
	LastSeenAt  string
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

type Role struct {
	Name       string
	Privileges []Privilege
}

type PrincipalKind string

const (
	PrincipalKindUser                 PrincipalKind = "user"
	PrincipalKindGroup                PrincipalKind = "group"
	PrincipalKindServicePrincipal     PrincipalKind = "service_principal"
	PrincipalKindDashboardPublication PrincipalKind = "dashboard_publication"
)

type SecurableType string

const (
	SecurablePlatform           SecurableType = "platform"
	SecurableWorkspace          SecurableType = "workspace"
	SecurableProjectEnvironment SecurableType = "project_environment"
	SecurableDashboard          SecurableType = "dashboard"
	SecurableSemanticModel      SecurableType = "semantic_model"
	SecurableSemanticField      SecurableType = "semantic_field"
	SecurableSource             SecurableType = "source"
	SecurableModelTable         SecurableType = "model_table"
	SecurableDataset            SecurableType = "dataset"
	SecurableTable              SecurableType = "table"
	SecurableColumn             SecurableType = "column"
)

type ObjectRef struct {
	Type        SecurableType
	WorkspaceID string
	ObjectID    string
	ParentID    string
	DisplayName string
}

func PlatformObject() ObjectRef {
	return ObjectRef{Type: SecurablePlatform}
}

func WorkspaceObject(workspaceID string) ObjectRef {
	return ObjectRef{Type: SecurableWorkspace, WorkspaceID: strings.TrimSpace(workspaceID)}
}

// ProjectEnvironmentObject is the Access-owned authorization boundary for
// project publication and deployment operations on one target environment.
// Project environments are global children of the platform rather than
// workspace assets because one atomic deployment may target many workspaces.
func ProjectEnvironmentObject(projectID, environment string) ObjectRef {
	return ObjectRef{
		Type:        SecurableProjectEnvironment,
		WorkspaceID: strings.TrimSpace(projectID),
		ObjectID:    strings.TrimSpace(environment),
		ParentID:    PlatformObject().CanonicalID(),
	}
}

func ItemObject(typ SecurableType, workspaceID, objectID string) ObjectRef {
	return ObjectRef{Type: typ, WorkspaceID: strings.TrimSpace(workspaceID), ObjectID: strings.TrimSpace(objectID)}
}

func ItemObjectWithParent(typ SecurableType, workspaceID, objectID string, parent ObjectRef) ObjectRef {
	object := ItemObject(typ, workspaceID, objectID)
	object.ParentID = parent.CanonicalID()
	return object
}

func (r ObjectRef) CanonicalID() string {
	switch r.Type {
	case SecurablePlatform:
		return "platform"
	case SecurableWorkspace:
		return "workspace:" + r.WorkspaceID
	default:
		return string(r.Type) + ":" + r.WorkspaceID + ":" + r.ObjectID
	}
}

func (r ObjectRef) Parent() (ObjectRef, bool) {
	switch r.Type {
	case SecurablePlatform:
		return ObjectRef{}, false
	case SecurableWorkspace:
		return PlatformObject(), true
	case SecurableProjectEnvironment:
		return PlatformObject(), true
	default:
		return WorkspaceObject(r.WorkspaceID), true
	}
}

type SecurableObject struct {
	ID               string
	Type             SecurableType
	WorkspaceID      string
	ParentID         string
	OwnerPrincipalID string
	DisplayName      string
	CreatedAt        string
	UpdatedAt        string
}

type Grant struct {
	ID          string
	ObjectID    string
	ObjectType  SecurableType
	WorkspaceID string
	SubjectType SubjectType
	SubjectID   string
	Privilege   Privilege
	CreatedAt   string
}

type GrantView struct {
	Grant
	Inherited    bool
	ParentID     string
	ParentType   SecurableType
	ParentObject string
}

type GrantInput struct {
	Object      ObjectRef
	SubjectType SubjectType
	SubjectID   string
	Privilege   Privilege
}

type AuthorizationDecision struct {
	Allowed       bool
	Privilege     Privilege
	Object        ObjectRef
	Reason        AuthorizationReason
	GrantID       string
	GrantObjectID string
	SubjectType   SubjectType
	SubjectID     string
	Inherited     bool
	Owner         bool
	Platform      bool
}

type AuthorizationReason string

const (
	ReasonMissingPrincipal  AuthorizationReason = "missing_principal"
	ReasonMissingPrivilege  AuthorizationReason = "missing_privilege"
	ReasonPrincipalDisabled AuthorizationReason = "principal_disabled"
	ReasonUnknownObject     AuthorizationReason = "unknown_object"
	ReasonOwner             AuthorizationReason = "owner"
	ReasonPlatformAdmin     AuthorizationReason = "platform_admin"
	ReasonGrant             AuthorizationReason = "grant"
	ReasonNoGrant           AuthorizationReason = "no_grant"
	ReasonMissingObject     AuthorizationReason = "missing_object"
)

type AuthorizationCheck struct {
	Privilege Privilege
	Object    ObjectRef
}

type SubjectType string

const (
	SubjectPrincipal            SubjectType = "principal"
	SubjectGroup                SubjectType = "group"
	SubjectServicePrincipal     SubjectType = "service_principal"
	SubjectDashboardPublication SubjectType = "dashboard_publication"
)

type RoleBinding struct {
	ID          string
	WorkspaceID string
	SubjectType SubjectType
	SubjectID   string
	PrincipalID string
	GroupID     string
	Email       string
	DisplayName string
	GroupName   string
	Role        string
	CreatedAt   string
}

type RoleBindingInput struct {
	ID          string
	WorkspaceID string
	SubjectType SubjectType
	SubjectID   string
	Role        string
}

type PrincipalRoleInput struct {
	WorkspaceID string
	Email       string
	DisplayName string
	Role        string
}

type PlatformRoleInput struct {
	PrincipalID string
	Email       string
	DisplayName string
	Role        string
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

type DataPolicy struct {
	ID             string
	WorkspaceID    string
	ObjectID       string
	SubjectType    SubjectType
	SubjectID      string
	PolicyType     string
	ExpressionJSON string
	Compiled       accesspolicy.Compiled `json:"-"`
	CreatedAt      string
	UpdatedAt      string
}

type DataPolicyInput struct {
	ID             string
	Object         ObjectRef
	SubjectType    SubjectType
	SubjectID      string
	PolicyType     string
	ExpressionJSON string
}

type ExternalIdentityInput struct {
	Provider    string
	TenantID    string
	Subject     string
	Email       string
	DisplayName string
}

type Group struct {
	ID          string
	WorkspaceID string
	Provider    string
	ExternalID  string
	Name        string
	CreatedAt   string
}

type GroupInput struct {
	ID          string
	WorkspaceID string
	Provider    string
	ExternalID  string
	Name        string
}

type GroupMember struct {
	GroupID     string
	WorkspaceID string
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
	PrincipalID string
	WorkspaceID string
	Name        string
	Privileges  []Privilege
	ExpiresAt   time.Time
}

const APITokenNameInitialPublisher = "initial-publisher"

type APIToken struct {
	ID          string
	PrincipalID string
	WorkspaceID string
	Name        string
	Privileges  []Privilege
	ExpiresAt   string
	CreatedAt   string
	LastUsedAt  string
	RevokedAt   string
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
	WorkspaceID   string
	PrincipalID   string
	Action        string
	TargetType    string
	TargetID      string
	Privilege     Privilege
	Status        string
	RequestID     string
	CorrelationID string
	MetadataJSON  string
}

type AuditEventFilter struct {
	WorkspaceID string
	PrincipalID string
	Action      string
	TargetType  string
	TargetID    string
	From        string
	To          string
	PageToken   string
	CursorTime  string
	CursorID    string
	Limit       int
}

type AuditEvent struct {
	ID            string
	WorkspaceID   string
	PrincipalID   string
	Action        string
	TargetType    string
	TargetID      string
	Privilege     Privilege
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
	SetPrincipalRole(ctx context.Context, input PrincipalRoleInput) (Principal, error)
	SetPlatformRole(ctx context.Context, input PlatformRoleInput) (Principal, error)
	RemovePrincipalRoles(ctx context.Context, workspaceID, principalID string) error
	CreateRoleBinding(ctx context.Context, input RoleBindingInput) (RoleBinding, error)
	GetRoleBinding(ctx context.Context, workspaceID, id string) (RoleBinding, error)
	UpdateRoleBinding(ctx context.Context, workspaceID, id string, input RoleBindingInput) (RoleBinding, error)
	DeleteRoleBinding(ctx context.Context, workspaceID, id string) error
	ListRoleBindings(ctx context.Context, workspaceID string) ([]RoleBinding, error)
	ListRoles(ctx context.Context) ([]Role, error)
	Authorize(ctx context.Context, principalID string, privilege Privilege, object ObjectRef) (AuthorizationDecision, error)
	AuthorizeAny(ctx context.Context, principalID string, privilege Privilege, objects []ObjectRef) (AuthorizationDecision, error)
	AuthorizeBatch(ctx context.Context, principalID string, checks []AuthorizationCheck) ([]AuthorizationDecision, error)
	EffectivePrivileges(ctx context.Context, principalID string, object ObjectRef) ([]Privilege, error)
	EffectiveAccess(ctx context.Context, principalID string, object ObjectRef) ([]AuthorizationDecision, error)
	UpsertSecurableObject(ctx context.Context, object ObjectRef, ownerPrincipalID string) (SecurableObject, error)
	GetSecurableObject(ctx context.Context, object ObjectRef) (SecurableObject, error)
	CreateGrant(ctx context.Context, input GrantInput) (Grant, error)
	GetGrant(ctx context.Context, workspaceID, id string) (Grant, error)
	DeleteGrant(ctx context.Context, workspaceID, id string) error
	ListGrants(ctx context.Context, object ObjectRef) ([]Grant, error)
	ListGrantsWithOptions(ctx context.Context, object ObjectRef, includeInherited bool) ([]GrantView, error)
	SetObjectOwner(ctx context.Context, object ObjectRef, ownerPrincipalID string) (SecurableObject, error)
	UpsertDataPolicy(ctx context.Context, input DataPolicyInput) (DataPolicy, error)
	GetDataPolicy(ctx context.Context, workspaceID, id string) (DataPolicy, error)
	ListDataPolicies(ctx context.Context, object ObjectRef) ([]DataPolicy, error)
	ListDataPoliciesWithOptions(ctx context.Context, object ObjectRef, includeInherited bool) ([]DataPolicy, error)
	ListEffectiveDataPolicies(ctx context.Context, principalID string, object ObjectRef, includeInherited bool) ([]DataPolicy, error)
	DeleteDataPolicy(ctx context.Context, workspaceID, id string) error
	CreateServicePrincipal(ctx context.Context, input ServicePrincipalInput) (Principal, error)
	ListServicePrincipals(ctx context.Context) ([]Principal, error)
	UpdateServicePrincipal(ctx context.Context, id string, input ServicePrincipalInput) (Principal, error)
	DeleteServicePrincipal(ctx context.Context, id string) error
	CreateServicePrincipalSecret(ctx context.Context, servicePrincipalID string, input ServicePrincipalSecretInput) (string, ServicePrincipalSecret, error)
	RevokeServicePrincipalSecret(ctx context.Context, servicePrincipalID, secretID string) error
	PrincipalForServicePrincipalSecret(ctx context.Context, servicePrincipalID, secret string) (Principal, error)
	BootstrapAdmin(ctx context.Context, workspaceID, email string) error
	ResolveExternalPrincipal(ctx context.Context, input ExternalIdentityInput) (Principal, error)
	UpsertSCIMUser(ctx context.Context, input SCIMUserInput) (SCIMUser, error)
	ListSCIMUsers(ctx context.Context, filter SCIMUserFilter) ([]SCIMUser, error)
	DisableSCIMUser(ctx context.Context, principalID string) (SCIMUser, error)
	UpsertGroup(ctx context.Context, input GroupInput) (Group, error)
	ListGroups(ctx context.Context, workspaceID string) ([]Group, error)
	SearchGroups(ctx context.Context, workspaceID, query string, limit int) ([]Group, error)
	ListAllGroups(ctx context.Context) ([]Group, error)
	DeleteGroup(ctx context.Context, workspaceID, groupID string) error
	AddGroupMember(ctx context.Context, workspaceID, groupID, principalID string) error
	RemoveGroupMember(ctx context.Context, workspaceID, groupID, principalID string) error
	ListGroupMembersByGroup(ctx context.Context, groupID string) ([]GroupMember, error)
	ListGroupMembers(ctx context.Context, workspaceID, groupID string) ([]GroupMember, error)
	UpsertSCIMGroup(ctx context.Context, input SCIMGroupInput) (Group, error)
	ListSCIMGroups(ctx context.Context, filter SCIMGroupFilter) ([]Group, error)
	DeleteSCIMGroup(ctx context.Context, groupID string) error
	AddSCIMGroupMember(ctx context.Context, groupID, principalID string) error
	RemoveSCIMGroupMember(ctx context.Context, groupID, principalID string) error
	ListSCIMGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error)
	ListAllRoleBindings(ctx context.Context) ([]RoleBinding, error)
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

func DefaultRoles() []Role {
	roles := make([]Role, len(defaultRoles))
	for i, role := range defaultRoles {
		roles[i] = Role{
			Name:       role.Name,
			Privileges: append([]Privilege(nil), role.Privileges...),
		}
	}
	return roles
}

func KnownPrivileges() []Privilege {
	return []Privilege{
		PrivilegeUseWorkspace,
		PrivilegeViewItem,
		PrivilegeEditItem,
		PrivilegeManageItem,
		PrivilegeQueryData,
		PrivilegePreviewData,
		PrivilegeTestDataPolicy,
		PrivilegeRefreshData,
		PrivilegeAuthorProject,
		PrivilegePublishRelease,
		PrivilegeReviewCandidate,
		PrivilegeRequestDeployment,
		PrivilegeApproveDeployment,
		PrivilegeDeploy,
		PrivilegeActivateDeployment,
		PrivilegeVerifyDeployment,
		PrivilegeRollbackDeployment,
		PrivilegeManagePublications,
		PrivilegeUseAgent,
		PrivilegeViewAgent,
		PrivilegeManageGrants,
		PrivilegeViewAudit,
		PrivilegeManageWorkspace,
		PrivilegeManagePlatform,
		PrivilegeViewData,
		PrivilegeIngestData,
		PrivilegeManageConnectionMetadata,
		PrivilegeTestConnection,
		PrivilegeViewConnectionHealth,
	}
}

func DashboardPublicationSubjectID(workspaceID, publication string) string {
	return "dashboard_publication:" + strings.TrimSpace(workspaceID) + "." + strings.TrimSpace(publication)
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
