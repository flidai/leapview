package manifest

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
)

// FromAuthorizationPolicy lowers target-owned intent into the one document
// used by both candidate compilation and admission. Compilation still validates
// every resource against the candidate graph before it becomes serving authority.
func FromAuthorizationPolicy(policy access.AuthorizationPolicy) (AccessPolicy, error) {
	result := AccessPolicy{RoleBindings: make(map[string]RoleBinding, len(policy.RoleBindings))}
	for _, binding := range policy.RoleBindings {
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return AccessPolicy{}, err
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
			ID: binding.ID, Name: binding.Name, Role: string(binding.Role), Subject: subject,
		}
	}
	result.Grants = make(map[string]Grant, len(policy.Grants))
	for _, grant := range policy.Grants {
		if err := access.ValidateAuthorizationGrant(grant); err != nil {
			return AccessPolicy{}, err
		}
		if _, ok := result.Grants[grant.ID]; ok {
			return AccessPolicy{}, fmt.Errorf("duplicate grant %q", grant.ID)
		}
		subject := Subject{Kind: string(grant.Subject.Kind)}
		if grant.Subject.Kind == access.SubjectKindPrincipal {
			subject.PrincipalID = grant.Subject.ID
		} else {
			subject.Group = grant.Subject.ID
		}
		result.Grants[grant.ID] = Grant{ID: grant.ID, Name: grant.Name, Subject: subject, Object: SecurableRef{Kind: string(grant.Resource.Kind()), ID: string(grant.Resource.ID())}, Capability: string(grant.Capability)}
	}
	return result, nil
}
