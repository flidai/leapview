package access

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/project/graph"
)

// AuthorizationGrant is target-owned policy intent. The resource is validated
// against the candidate graph when the policy is compiled, before admission.
// It never becomes serving authority without that graph-bound compilation.
type AuthorizationGrant struct {
	ID         string      `json:"id"`
	Name       string      `json:"name,omitempty"`
	Subject    SubjectRef  `json:"subject"`
	Resource   ResourceRef `json:"resource"`
	Capability Capability  `json:"capability,omitempty"`
	// PermissionProfile and Permissions are the exact typed authority for new
	// grants. Capability remains the historical representation; a grant must
	// use exactly one form and legacy capabilities never imply typed authority.
	PermissionProfile string           `json:"permissionProfile,omitempty"`
	Permissions       []PermissionPair `json:"permissions,omitempty"`
}

type AuthorizationGrantInput struct {
	Scope            AuthorizationPolicyScope
	Grant            AuthorizationGrant
	ExpectedRevision int64
	IdempotencyKey   string
}

type AuthorizationGrantWriter interface {
	UpsertAuthorizationGrant(context.Context, AuthorizationGrantInput) (AuthorizationPolicy, error)
}

func ValidateAuthorizationGrant(grant AuthorizationGrant) error {
	invalid := func(err error) error { return fmt.Errorf("%w: grant: %v", ErrAuthorizationPolicyInvalidBinding, err) }
	if grant.ID == "" || strings.TrimSpace(grant.ID) != grant.ID || len(grant.ID) > 255 || strings.ContainsAny(grant.ID, "\x00\r\n") {
		return invalid(fmt.Errorf("invalid id"))
	}
	if strings.TrimSpace(grant.Name) != grant.Name || len(grant.Name) > 255 || strings.ContainsAny(grant.Name, "\x00\r\n") {
		return invalid(fmt.Errorf("invalid name"))
	}
	if err := grant.Subject.Validate(); err != nil {
		return invalid(err)
	}
	if err := grant.Resource.Validate(); err != nil {
		return invalid(err)
	}
	if grant.PermissionProfile != "" || grant.Permissions != nil {
		if grant.Capability != "" {
			return invalid(fmt.Errorf("typed grant cannot carry a legacy capability"))
		}
		if len(grant.Permissions) == 0 {
			return invalid(fmt.Errorf("typed grant requires at least one exact permission pair"))
		}
		if err := ValidateTypedPermissionSet(grant.PermissionProfile, grant.Permissions); err != nil {
			return invalid(err)
		}
		if grant.Resource.Kind() == graph.KindProjectNamespace {
			return invalid(fmt.Errorf("typed grant must target an exact project resource"))
		}
		for index, pair := range grant.Permissions {
			if err := pair.Validate(); err != nil {
				return invalid(fmt.Errorf("typed permission %d: %w", index, err))
			}
			if pair.Target.Scope != PermissionScopeResource || pair.Target.IncludeFuture ||
				pair.Target.ResourceKind != grant.Resource.Kind() || pair.Target.ResourceID != grant.Resource.ID() {
				return invalid(fmt.Errorf("typed permission %d must target the grant's exact resource", index))
			}
		}
		return nil
	}
	if _, err := ParseCapability(string(grant.Capability)); err != nil {
		return invalid(err)
	}
	// NewCanonicalGrant performs authoritative graph validation at compilation;
	// reject kind/capability combinations here as well.
	if !SupportsCapability(grant.Resource.Kind(), grant.Capability) {
		return invalid(fmt.Errorf("capability not allowed for %s", grant.Resource.Kind()))
	}
	return nil
}

// ValidateAuthorizationGrantForScope verifies the target-policy namespace in
// addition to the self-contained grant representation. Typed grants are
// restricted to exact resource pairs for this exact project; they cannot
// carry project, instance, or future-resource authority.
func ValidateAuthorizationGrantForScope(grant AuthorizationGrant, scope AuthorizationPolicyScope) error {
	if err := ValidateAuthorizationGrant(grant); err != nil {
		return err
	}
	if grant.Resource.Kind() == graph.KindProjectNamespace && string(grant.Resource.ID()) != scope.ProjectID {
		return fmt.Errorf("%w: grant belongs to another project", ErrAuthorizationPolicyInvalidBinding)
	}
	if grant.PermissionProfile != "" || grant.Permissions != nil {
		for index, pair := range grant.Permissions {
			if string(pair.Target.ProjectID) != scope.ProjectID {
				return fmt.Errorf("%w: typed permission %d belongs to another project", ErrAuthorizationPolicyInvalidBinding, index)
			}
		}
	}
	return nil
}

func canonicalAuthorizationGrants(scope AuthorizationPolicyScope, input []AuthorizationGrant) ([]AuthorizationGrant, error) {
	grants := append([]AuthorizationGrant(nil), input...)
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	ids := map[string]bool{}
	keys := map[string]bool{}
	for index := range grants {
		grant := &grants[index]
		if err := ValidateAuthorizationGrantForScope(*grant, scope); err != nil {
			return nil, err
		}
		keyPrefix := string(grant.Subject.Kind) + "\x00" + grant.Subject.ID + "\x00" + string(grant.Resource.Kind()) + "\x00" + string(grant.Resource.ID()) + "\x00"
		if grant.PermissionProfile == "" && grant.Permissions == nil {
			key := keyPrefix + "legacy:" + string(grant.Capability)
			if ids[grant.ID] || keys[key] {
				return nil, fmt.Errorf("%w: duplicate grant %q", ErrAuthorizationPolicyConflict, grant.ID)
			}
			ids[grant.ID] = true
			keys[key] = true
			continue
		}
		grant.Permissions = ClonePermissionPairs(grant.Permissions)
		sort.Slice(grant.Permissions, func(i, j int) bool { return grant.Permissions[i].Key() < grant.Permissions[j].Key() })
		if ids[grant.ID] {
			return nil, fmt.Errorf("%w: duplicate grant %q", ErrAuthorizationPolicyConflict, grant.ID)
		}
		ids[grant.ID] = true
		for _, pair := range grant.Permissions {
			key := keyPrefix + "typed:" + pair.Key()
			if keys[key] {
				return nil, fmt.Errorf("%w: duplicate typed grant for %q", ErrAuthorizationPolicyConflict, grant.ID)
			}
			keys[key] = true
		}
	}
	return grants, nil
}

// ValidateAgainst binds policy intent to an authoritative candidate graph.
func (grant AuthorizationGrant) ValidateAgainst(project graph.ProjectGraph) error {
	if err := ValidateAuthorizationGrant(grant); err != nil {
		return err
	}
	if grant.PermissionProfile != "" || grant.Permissions != nil {
		if err := grant.Resource.ValidateAgainst(project); err != nil {
			return err
		}
		for index, pair := range grant.Permissions {
			resource, err := NewResourceRef(pair.Target.ResourceID, pair.Target.ResourceKind)
			if err != nil {
				return fmt.Errorf("typed permission %d resource: %w", index, err)
			}
			if err := resource.ValidateAgainst(project); err != nil {
				return fmt.Errorf("typed permission %d resource: %w", index, err)
			}
		}
		return nil
	}
	_, err := NewCanonicalGrant(project, grant.Subject, grant.Resource, grant.Capability)
	return err
}
