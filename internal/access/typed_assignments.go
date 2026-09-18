package access

import (
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ValidateTypedPermissionSet validates the version and exact PairSet carried
// by a durable assignment. A nil pair slice is omitted authority; an empty,
// non-nil slice is an explicit deny-all assignment.
func ValidateTypedPermissionSet(profile string, pairs []PermissionPair) error {
	if profile != PermissionCatalogProfile {
		return fmt.Errorf("%w: unsupported permission profile %q", ErrInvalidPermissionPair, profile)
	}
	if pairs == nil {
		return ErrTokenPermissionsNeeded
	}
	if err := ValidatePermissionPairs(pairs); err != nil {
		return err
	}
	return nil
}

// ValidateTypedRoleBinding validates only the new, profile-pinned assignment
// representation. It deliberately does not derive typed pairs from the
// legacy capability bundle.
func ValidateTypedRoleBinding(binding RoleBinding) error {
	if binding.PermissionProfile == "" && binding.Permissions == nil {
		return fmt.Errorf("%w: typed role binding is absent", ErrAuthorizationPolicyInvalidBinding)
	}
	if binding.Role != "" {
		return fmt.Errorf("%w: typed role binding cannot carry legacy role %q", ErrAuthorizationPolicyInvalidBinding, binding.Role)
	}
	if binding.Capabilities != nil {
		return fmt.Errorf("%w: typed role binding cannot carry legacy capabilities", ErrAuthorizationPolicyInvalidBinding)
	}
	if err := ValidateTypedPermissionSet(binding.PermissionProfile, binding.Permissions); err != nil {
		return fmt.Errorf("%w: typed permissions: %w", ErrAuthorizationPolicyInvalidBinding, err)
	}
	if err := binding.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %w", ErrAuthorizationPolicyInvalidBinding, err)
	}
	if !binding.PermissionRole.Valid() {
		return fmt.Errorf("%w: permission role %q is invalid", ErrAuthorizationPolicyInvalidBinding, binding.PermissionRole)
	}
	return nil
}

// ValidateTypedRoleBindingForProject verifies that the persisted PairSet is
// exactly the expansion of its versioned PermissionRole for this project.
// Pair order is part of the canonical expansion and is checked as well as set
// identity, so a role catalog edit cannot be hidden behind the same role name.
func ValidateTypedRoleBindingForProject(binding RoleBinding, projectID projectgraph.ResourceID) error {
	if err := ValidateTypedRoleBinding(binding); err != nil {
		return err
	}
	expected, err := ExpandPermissionRole(binding.PermissionRole, projectID)
	if err != nil {
		return err
	}
	if len(binding.Permissions) != len(expected) {
		return fmt.Errorf("%w: permission role %q expansion has %d pairs, want %d", ErrAuthorizationPolicyInvalidBinding, binding.PermissionRole, len(binding.Permissions), len(expected))
	}
	for i := range expected {
		if binding.Permissions[i].Key() != expected[i].Key() {
			return fmt.Errorf("%w: permission role %q expansion pair %d is not canonical", ErrAuthorizationPolicyInvalidBinding, binding.PermissionRole, i)
		}
	}
	return nil
}

// TypedRoleBinding reports whether the assignment has a complete typed
// representation. It is intentionally strict and does not treat a legacy
// capability-only row as a wildcard.
func (binding RoleBinding) TypedRoleBinding() bool {
	return binding.PermissionProfile != "" || binding.Permissions != nil
}

// ClonePermissionPairs returns a defensive copy suitable for persistence or
// snapshot construction.
func ClonePermissionPairs(pairs []PermissionPair) []PermissionPair {
	return append([]PermissionPair(nil), pairs...)
}

// ExpandPermissionRole expands one versioned presentation role into its
// exact typed project-scoped authority. Resource actions use an explicit
// future-resource selector, preserving the declared project/kind boundary
// without enumerating a mutable graph or forming a Cartesian product.
func ExpandPermissionRole(role PermissionRole, projectID projectgraph.ResourceID) ([]PermissionPair, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("permission role project: %w", err)
	}
	actions, ok := PermissionRoleActions(role)
	if !ok {
		return nil, fmt.Errorf("%w: unknown permission role %q", ErrInvalidPermissionCatalog, role)
	}
	pairs := make([]PermissionPair, 0, len(actions))
	for _, action := range actions {
		definition, ok := Permission(action)
		if !ok {
			return nil, fmt.Errorf("%w: role %q contains unknown action %q", ErrInvalidPermissionCatalog, role, action)
		}
		var pair PermissionPair
		var err error
		if definition.Scope == PermissionScopeProject {
			pair, err = NewProjectPermissionPair(action, projectID)
		} else if len(definition.ResourceKinds) == 1 {
			pair, err = NewFutureProjectPermissionPair(action, projectID, definition.ResourceKinds[0])
		} else {
			return nil, fmt.Errorf("%w: role action %q has no single resource kind", ErrInvalidPermissionCatalog, action)
		}
		if err != nil {
			return nil, fmt.Errorf("expand permission role %q action %q: %w", role, action, err)
		}
		pairs = append(pairs, pair)
	}
	if err := ValidateTypedPermissionSet(PermissionCatalogProfile, pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}

// NewTypedRoleBinding creates a profile-pinned assignment with the exact
// expansion captured at issuance time. It intentionally leaves the legacy
// ProjectRole and capabilities fields empty.
func NewTypedRoleBinding(id, name string, subject SubjectRef, role PermissionRole, projectID projectgraph.ResourceID) (RoleBinding, error) {
	pairs, err := ExpandPermissionRole(role, projectID)
	if err != nil {
		return RoleBinding{}, fmt.Errorf("%w: permission role %q: %v", ErrAuthorizationPolicyInvalidBinding, role, err)
	}
	binding := RoleBinding{ID: id, Name: name, Subject: subject, PermissionProfile: PermissionCatalogProfile, Permissions: pairs, PermissionRole: role}
	if err := ValidateTypedRoleBindingForProject(binding, projectID); err != nil {
		return RoleBinding{}, err
	}
	return binding, nil
}
