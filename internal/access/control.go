package access

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/project/graph"
)

// The live control authority is deliberately smaller than the serving
// snapshot. Roles are a closed Go-owned vocabulary; this package stores only
// assignments and grants that can change during an instance's lifetime.
var (
	ErrControlInvalidInput      = errors.New("invalid access control input")
	ErrControlNotFound          = errors.New("access control object not found")
	ErrControlConflict          = errors.New("access control conflict")
	ErrControlRevisionConflict  = errors.New("access control revision conflict")
	ErrControlIdentityConflict  = errors.New("access control identity conflict")
	ErrControlTargetNotFound    = errors.New("access control target not found")
	ErrControlTargetConflict    = errors.New("access control target conflict")
	ErrControlReferenceConflict = errors.New("access control durable reference conflict")
	ErrControlRevoked           = errors.New("access control object is revoked")
)

type ControlReferenceLifecycle string

const (
	ControlReferenceActive    ControlReferenceLifecycle = "active"
	ControlReferenceSuspended ControlReferenceLifecycle = "suspended"
)

// RoleAssignment is one mutable project-scoped assignment under an
// instance's active project binding. Instance-wide platform administration
// remains owned by access.platform_role_binding and PlatformRole.
// ID, instance, project, subject, and role form its immutable identity.
// Name, revision, and revocation are mutable state owned by the live store.
type RoleAssignment struct {
	ID         string
	InstanceID string
	ProjectID  string
	Subject    SubjectRef
	Role       string
	Name       string
	Revision   int64
	CreatedAt  string
	UpdatedAt  string
	RevokedAt  string
}

type RoleAssignmentInput struct {
	ID               string
	InstanceID       string
	ProjectID        string
	Subject          SubjectRef
	Role             string
	Name             string
	ExpectedRevision int64
	ActorID          string
	RequestID        string
	CorrelationID    string
}

// ControlGrant is one explicit mutable subject/resource/capability grant.
// The resource reference is canonical and was validated against the graph at
// creation or update. ReferenceLifecycle is projected from the durable
// resource-reference row, so a tombstoned target remains visible but denied.
type ControlGrant struct {
	ID                     string
	InstanceID             string
	ProjectID              string
	Subject                SubjectRef
	Resource               ResourceRef
	Capability             Capability
	Name                   string
	Revision               int64
	CreatedAt              string
	UpdatedAt              string
	RevokedAt              string
	ReferenceLifecycle     ControlReferenceLifecycle
	ReferenceSuspendedAt   string
	ReferenceReactivatedAt string
}

type ControlGrantInput struct {
	ID               string
	InstanceID       string
	ProjectID        string
	Subject          SubjectRef
	Resource         ResourceRef
	Capability       Capability
	Name             string
	ExpectedRevision int64
	ActorID          string
	RequestID        string
	CorrelationID    string
}

// ControlState is the exact instance-scoped projection consumed by activation
// reconciliation. Revision changes with every live control mutation, while
// the returned rows are read from one database snapshot.
type ControlState struct {
	InstanceID      string
	ProjectID       string
	Revision        int64
	RoleAssignments []RoleAssignment
	Grants          []ControlGrant
}

// ControlStateSeed is the one-time migration input from an already validated
// active authorization snapshot. It never replaces an existing live state:
// an exact retry replays, while any changed binding conflicts.
type ControlStateSeed struct {
	InstanceID      string
	ProjectID       string
	ActorID         string
	RoleAssignments []RoleAssignmentInput
	Grants          []ControlGrantInput
}

// ControlStore is the narrow live access-control port. Implementations must
// use the instance ID on every operation; project IDs never form an instance
// namespace by themselves.
type ControlStore interface {
	InitializeControlState(context.Context, ControlStateSeed, graph.ProjectGraph) (ControlState, error)
	RoleAssignment(context.Context, string, string) (RoleAssignment, error)
	ListRoleAssignments(context.Context, string) ([]RoleAssignment, error)
	CreateRoleAssignment(context.Context, RoleAssignmentInput) (RoleAssignment, error)
	UpdateRoleAssignment(context.Context, RoleAssignmentInput) (RoleAssignment, error)
	RevokeRoleAssignment(context.Context, string, string, int64, string) (RoleAssignment, error)
	DeleteRoleAssignment(context.Context, string, string, int64, string) (RoleAssignment, error)

	ControlGrant(context.Context, string, string) (ControlGrant, error)
	ListControlGrants(context.Context, string, string) ([]ControlGrant, error)
	CreateGrant(context.Context, ControlGrantInput, graph.ProjectGraph) (ControlGrant, error)
	UpdateGrant(context.Context, ControlGrantInput, graph.ProjectGraph) (ControlGrant, error)
	RevokeGrant(context.Context, string, string, int64, string) (ControlGrant, error)
	ReactivateGrant(context.Context, string, string, int64, graph.ProjectGraph, string) (ControlGrant, error)
	ControlState(context.Context, string) (ControlState, error)
}

// Validate checks a role assignment without relying on database state.
func (input RoleAssignmentInput) Validate() error {
	if err := validateControlToken(input.InstanceID, "instance id"); err != nil {
		return err
	}
	if input.ID != "" {
		if err := validateControlResourceID(input.ID, "role assignment id"); err != nil {
			return err
		}
	}
	if input.ExpectedRevision < 0 {
		return fmt.Errorf("%w: expected revision must not be negative", ErrControlInvalidInput)
	}
	if err := input.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %w", ErrControlInvalidInput, err)
	}
	if err := validateControlName(input.Name); err != nil {
		return err
	}
	if err := validateControlResourceID(input.ProjectID, "project id"); err != nil {
		return err
	}
	if _, err := ParseProjectRole(input.Role); err != nil {
		return fmt.Errorf("%w: role: %w", ErrControlInvalidInput, err)
	}
	return nil
}

// ValidateAgainstGraph performs the mandatory contextual validation for a
// grant. A live grant cannot target the internal Project root: project-wide
// access is represented by a project role assignment, and the identity ledger
// intentionally tracks only authored analytics resources.
func (input ControlGrantInput) ValidateAgainstGraph(project graph.ProjectGraph) (CanonicalGrant, error) {
	if err := validateControlToken(input.InstanceID, "instance id"); err != nil {
		return CanonicalGrant{}, err
	}
	if input.ID != "" {
		if err := validateControlResourceID(input.ID, "grant id"); err != nil {
			return CanonicalGrant{}, err
		}
	}
	if input.ExpectedRevision < 0 {
		return CanonicalGrant{}, fmt.Errorf("%w: expected revision must not be negative", ErrControlInvalidInput)
	}
	if err := validateControlName(input.Name); err != nil {
		return CanonicalGrant{}, err
	}
	if err := project.Validate(); err != nil {
		return CanonicalGrant{}, fmt.Errorf("%w: project graph: %w", ErrControlInvalidInput, err)
	}
	if input.ProjectID != "" && input.ProjectID != project.ProjectID().String() {
		return CanonicalGrant{}, fmt.Errorf("%w: project id %q does not match graph %q", ErrControlTargetConflict, input.ProjectID, project.ProjectID())
	}
	if input.Resource.Kind() == graph.KindProject {
		return CanonicalGrant{}, fmt.Errorf("%w: project root grants are represented by project role assignments", ErrControlInvalidInput)
	}
	grant, err := NewCanonicalGrant(project, input.Subject, input.Resource, input.Capability)
	if err != nil {
		return CanonicalGrant{}, err
	}
	return grant, nil
}

func validateControlToken(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%w: %s", ErrControlInvalidInput, label)
	}
	return nil
}

func validateControlResourceID(value, label string) error {
	if _, err := graph.NewResourceID(value); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrControlInvalidInput, label, err)
	}
	return nil
}

func validateControlName(value string) error {
	if len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: name is invalid", ErrControlInvalidInput)
	}
	return nil
}
