package manifest

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// AccessPolicyFromAuthorizationPolicy converts target-owned mutable policy
// state into the exact immutable manifest document used for candidate
// planning and generation admission. Keeping this projection here ensures
// both boundaries preserve the same typed authority representation.
func AccessPolicyFromAuthorizationPolicy(policy access.AuthorizationPolicy) (AccessPolicy, error) {
	if err := access.ValidateAuthorizationPolicyScope(policy.Scope); err != nil {
		return AccessPolicy{}, fmt.Errorf("target authorization policy scope: %w", err)
	}
	projectID := projectgraph.ResourceID(policy.Scope.ProjectID)
	if err := projectID.Validate(); err != nil {
		return AccessPolicy{}, fmt.Errorf("target authorization policy project: %w", err)
	}

	result := AccessPolicy{RoleBindings: make(map[string]RoleBinding, len(policy.RoleBindings))}
	for _, binding := range policy.RoleBindings {
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return AccessPolicy{}, fmt.Errorf("target authorization role binding %q: %w", binding.ID, err)
		}
		if binding.TypedRoleBinding() {
			if err := access.ValidateTypedRoleBindingForProject(binding, projectID); err != nil {
				return AccessPolicy{}, fmt.Errorf("target authorization role binding %q: %w", binding.ID, err)
			}
		}

		subject := Subject{Kind: string(binding.Subject.Kind)}
		switch binding.Subject.Kind {
		case access.SubjectKindPrincipal:
			subject.PrincipalID = binding.Subject.ID
		case access.SubjectKindGroup:
			subject.Group = binding.Subject.ID
		default:
			return AccessPolicy{}, fmt.Errorf("unsupported target authorization subject kind %q", binding.Subject.Kind)
		}
		if _, duplicate := result.RoleBindings[binding.ID]; duplicate {
			return AccessPolicy{}, fmt.Errorf("duplicate target authorization role binding %q", binding.ID)
		}
		result.RoleBindings[binding.ID] = RoleBinding{
			ID:                binding.ID,
			Name:              binding.Name,
			Role:              string(binding.Role),
			Subject:           subject,
			PermissionProfile: binding.PermissionProfile,
			Permissions:       access.ClonePermissionPairs(binding.Permissions),
			PermissionRole:    binding.PermissionRole,
		}
	}
	return result, nil
}
