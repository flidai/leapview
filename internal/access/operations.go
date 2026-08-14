package access

import (
	"context"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
)

type OperationSurface = apigencommand.Surface

const (
	OperationSurfaceAPI = apigencommand.SurfaceAPI
	OperationSurfaceCLI = apigencommand.SurfaceCLI
	OperationSurfaceUI  = apigencommand.SurfaceUI
)

type RoleBindingInvocation struct {
	PrincipalID      string
	Surface          OperationSurface
	RequestID        string
	CorrelationID    string
	IdempotencyKey   string
	ConcurrencyToken string
	OperationClaims  []string
}

// RoleBindingCommander is the transport-neutral execution port for role
// binding commands.
type RoleBindingCommander interface {
	CreateRoleBinding(context.Context, RoleBindingInvocation, RoleBindingInput) (RoleBinding, error)
	UpdateRoleBinding(context.Context, RoleBindingInvocation, string, string, RoleBindingInput) (RoleBinding, error)
	DeleteRoleBinding(context.Context, RoleBindingInvocation, string, string) (RoleBinding, error)
}

type RoleBindingOperations = RoleBindingCommander

// RoleBindingBatchCommander atomically creates multiple bindings under one
// generated command invocation while retaining one audit event per binding.
type RoleBindingBatchCommander interface {
	CreateRoleBindings(context.Context, RoleBindingInvocation, []RoleBindingInput) ([]RoleBinding, error)
}

// GrantInvocation attributes a transport-neutral grant command to its actor
// and invoking surface.
type GrantInvocation = RoleBindingInvocation

// GrantCommander is the transport-neutral execution port shared by API and UI
// grant mutations.
type GrantCommander interface {
	CreateGrant(context.Context, GrantInvocation, GrantInput) (Grant, error)
	UpdateGrant(context.Context, GrantInvocation, string, string, GrantInput) (Grant, error)
	DeleteGrant(context.Context, GrantInvocation, string, string) (Grant, error)
}

type GrantOperations = GrantCommander

// GrantBatchCommander atomically creates multiple object grants under one
// generated command invocation while retaining one audit event per grant.
type GrantBatchCommander interface {
	CreateGrants(context.Context, GrantInvocation, []GrantInput) ([]Grant, error)
}

// WorkspaceCommandPrivileges is the capability-neutral authorization policy
// consumed by workspace UI routes. Access derives it from generated command
// contracts at the composition boundary.
type WorkspaceCommandPrivileges struct {
	RoleBindingUpsert Privilege
	RoleBindingDelete Privilege
	GrantUpsert       Privilege
	GrantDelete       Privilege
}
