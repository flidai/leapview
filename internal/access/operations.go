package access

import (
	"context"
	"fmt"
	"strings"
)

// OperationID identifies one transport-neutral application operation.
type OperationID string

type OperationKind string
type OperationSurface string

const (
	OperationKindCommand OperationKind = "command"
	OperationKindQuery   OperationKind = "query"

	OperationSurfaceAPI OperationSurface = "api"
	OperationSurfaceCLI OperationSurface = "cli"
	OperationSurfaceUI  OperationSurface = "ui"
)

const (
	OperationCreateRoleBinding OperationID = "createRoleBinding"
	OperationUpdateRoleBinding OperationID = "updateRoleBinding"
	OperationDeleteRoleBinding OperationID = "deleteRoleBinding"
	OperationCreateGrant       OperationID = "createGrant"
	OperationUpdateGrant       OperationID = "updateGrant"
	OperationDeleteGrant       OperationID = "deleteGrant"
)

type OperationTarget struct {
	Type      SecurableType
	Parameter string
}

// OperationDescriptor is the hard application contract shared by every
// transport that exposes an operation.
type OperationDescriptor struct {
	ID         OperationID
	Kind       OperationKind
	Owner      string
	Target     OperationTarget
	Privilege  Privilege
	AuditEvent string
	// HTTPIdempotency and HTTPConcurrency describe API transport policy; the
	// command executor does not treat them as cross-surface guarantees.
	HTTPIdempotency string
	HTTPConcurrency string
	ExposedSurfaces []OperationSurface
}

func (d OperationDescriptor) Exposes(surface OperationSurface) bool {
	for _, exposed := range d.ExposedSurfaces {
		if exposed == surface {
			return true
		}
	}
	return false
}

// OperationCatalog is an immutable application view of generated operation
// contracts. Transports receive this view through composition rather than
// maintaining their own authorization or audit metadata.
type OperationCatalog struct {
	descriptors map[OperationID]OperationDescriptor
}

func NewOperationCatalog(descriptors []OperationDescriptor) (OperationCatalog, error) {
	catalog := OperationCatalog{descriptors: make(map[OperationID]OperationDescriptor, len(descriptors))}
	for _, descriptor := range descriptors {
		descriptor.ID = OperationID(strings.TrimSpace(string(descriptor.ID)))
		if descriptor.ID == "" {
			return OperationCatalog{}, fmt.Errorf("operation ID is required")
		}
		if descriptor.Kind != OperationKindCommand {
			return OperationCatalog{}, fmt.Errorf("operation %q must be a command", descriptor.ID)
		}
		if strings.TrimSpace(descriptor.Owner) == "" || descriptor.Target.Type == "" || strings.TrimSpace(descriptor.Target.Parameter) == "" {
			return OperationCatalog{}, fmt.Errorf("operation %q owner and target are required", descriptor.ID)
		}
		if _, ok := ParsePrivilege(string(descriptor.Privilege)); !ok {
			return OperationCatalog{}, fmt.Errorf("operation %q has invalid privilege %q", descriptor.ID, descriptor.Privilege)
		}
		if strings.TrimSpace(descriptor.AuditEvent) == "" {
			return OperationCatalog{}, fmt.Errorf("operation %q audit event is required", descriptor.ID)
		}
		if len(descriptor.ExposedSurfaces) == 0 {
			return OperationCatalog{}, fmt.Errorf("operation %q must expose at least one surface", descriptor.ID)
		}
		if _, exists := catalog.descriptors[descriptor.ID]; exists {
			return OperationCatalog{}, fmt.Errorf("operation %q is duplicated", descriptor.ID)
		}
		descriptor.ExposedSurfaces = append([]OperationSurface(nil), descriptor.ExposedSurfaces...)
		catalog.descriptors[descriptor.ID] = descriptor
	}
	return catalog, nil
}

func (c OperationCatalog) DescribeOperation(id OperationID) (OperationDescriptor, bool) {
	descriptor, ok := c.descriptors[id]
	descriptor.ExposedSurfaces = append([]OperationSurface(nil), descriptor.ExposedSurfaces...)
	return descriptor, ok
}

type RoleBindingInvocation struct {
	PrincipalID      string
	Surface          OperationSurface
	RequestID        string
	CorrelationID    string
	IdempotencyKey   string
	ConcurrencyToken string
}

// RoleBindingCommander is the transport-neutral execution port for role
// binding commands.
type RoleBindingCommander interface {
	CreateRoleBinding(context.Context, RoleBindingInvocation, RoleBindingInput) (RoleBinding, error)
	UpdateRoleBinding(context.Context, RoleBindingInvocation, string, string, RoleBindingInput) (RoleBinding, error)
	DeleteRoleBinding(context.Context, RoleBindingInvocation, string, string) (RoleBinding, error)
}

// RoleBindingOperations combines execution with the generated descriptors
// needed by non-API transports to enforce the same contract.
type RoleBindingOperations interface {
	RoleBindingCommander
	DescribeOperation(OperationID) (OperationDescriptor, bool)
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

// GrantOperations combines execution with the generated descriptors used by
// every grant mutation surface.
type GrantOperations interface {
	GrantCommander
	DescribeOperation(OperationID) (OperationDescriptor, bool)
}
