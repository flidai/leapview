package access

import "context"

// RoleBindingAdministrationState is the transport-neutral read model for the
// target-owned project policy. It deliberately returns captured bindings and
// the versioned preset catalog separately: the UI may explain a role, but it
// must never re-expand or reinterpret persisted authority.
type RoleBindingAdministrationState struct {
	Scope               AuthorizationPolicyScope
	Revision            int64
	Digest              string
	RoleBindings        []RoleBinding
	RolePresets         []PermissionRolePreset
	ActiveBindingIDs    []string
	ActiveSnapshotReady bool
}

// RoleBindingAdministrationCommand is the narrow role mutation accepted from
// product administration. Project and actor identity are server-bound.
type RoleBindingAdministrationCommand struct {
	Action           string
	BindingID        string
	Subject          SubjectRef
	Role             PermissionRole
	ExpectedRevision int64
	IdempotencyKey   string
}

type RoleBindingAdministrationAction string

const (
	RoleBindingAdministrationGrant  RoleBindingAdministrationAction = "grant_role"
	RoleBindingAdministrationRevoke RoleBindingAdministrationAction = "revoke_role"
)

// RoleBindingAdministrationMutation contains the server-resolved policy
// scope and caller command shared by browser and REST adapters.
type RoleBindingAdministrationMutation struct {
	Scope                AuthorizationPolicyScope
	Action               RoleBindingAdministrationAction
	Binding              RoleBinding
	BindingID            string
	GrantAdminEnvelopeID string
	IssueAdminEnvelope   bool
	ExpectedRevision     int64
	IdempotencyKey       string
	ActorID              string
	RequestID            string
	CorrelationID        string
}

// RoleBindingAdministrationCredential is the authenticated credential proof
// used by the common grant and revoke path. TokenPermissions are checked only
// for API tokens; browser sessions are tied to their durable session ID.
type RoleBindingAdministrationCredential struct {
	Class                    string
	ID                       string
	Fingerprint              string
	PrincipalID              string
	AuthenticatedPrincipalID string
	PermissionProfile        string
	TokenPermissions         []PermissionPair
}

// RoleBindingAdministrationAuthority is resolved from current serving
// authority and request credential by the transport adapter.
type RoleBindingAdministrationAuthority struct {
	ActorID     string
	Permissions []PermissionPair
	Credential  RoleBindingAdministrationCredential
}

type RoleBindingAdministrationEnvelopeIssuer interface {
	IssueGrantAdminEnvelope(context.Context, GrantAdminEnvelopeRequest) (GrantAdminEnvelope, error)
}

// RoleBindingAdministrationPorts are request-bound capabilities supplied by
// the composition/transport boundary. Bootstrap remains a separate capability
// available only to the canonical project-claim flow.
type RoleBindingAdministrationPorts struct {
	EnvelopeIssuer          RoleBindingAdministrationEnvelopeIssuer
	ResolveCurrentAuthority func(context.Context, string) (RoleBindingAdministrationAuthority, error)
	AuthorizeClaimBootstrap func(context.Context, AuthorizationPolicyScope, RoleBinding, string) (bool, error)
}

// RoleBindingAdministrationAuditRunner binds operation metadata and executes
// the mutation callback in the audited repository transaction.
type RoleBindingAdministrationAuditRunner func(func(Repository) (AuditEventInput, error)) error
