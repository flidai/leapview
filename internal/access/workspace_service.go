package access

import "context"

// WorkspaceAccessService is the access-owned surface used by workspace
// presentation. Mutations remain access use cases even when initiated from a
// workspace page.
type WorkspaceAccessService interface {
	PrincipalByID(context.Context, string) (Principal, error)
	SearchPrincipals(context.Context, string, int) ([]Principal, error)
	UpsertPrincipal(context.Context, PrincipalInput) (Principal, error)
	SetPrincipalRole(context.Context, PrincipalRoleInput) (Principal, error)
	RemovePrincipalRoles(context.Context, string, string) error
	CreateRoleBinding(context.Context, RoleBindingInput) (RoleBinding, error)
	UpdateRoleBinding(context.Context, string, string, RoleBindingInput) (RoleBinding, error)
	DeleteRoleBinding(context.Context, string, string) error
	ListRoleBindings(context.Context, string) ([]RoleBinding, error)
	ListRoles(context.Context) ([]Role, error)
	Authorize(context.Context, string, Privilege, ObjectRef) (AuthorizationDecision, error)
	GetSecurableObject(context.Context, ObjectRef) (SecurableObject, error)
	CreateGrant(context.Context, GrantInput) (Grant, error)
	DeleteGrant(context.Context, string, string) error
	ListGrants(context.Context, ObjectRef) ([]Grant, error)
	SearchGroups(context.Context, string, string, int) ([]Group, error)
	ListAllGroups(context.Context) ([]Group, error)
}
