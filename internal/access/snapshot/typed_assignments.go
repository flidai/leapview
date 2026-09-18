package snapshot

import (
	"errors"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrTypedSnapshotSubjectRequired = errors.New("typed snapshot grant subject is required")
	ErrTypedSnapshotProjectMismatch = errors.New("typed snapshot pair project does not match serving identity")
)

// NewTypedGrant constructs a typed grant before graph binding. The grant's
// pair targets are checked against the authoritative graph by the snapshot
// constructor; this constructor only establishes the profile/set boundary.
func NewTypedGrant(id, name string, subject access.SubjectRef, pairs []access.PermissionPair) (Grant, error) {
	if id == "" {
		return Grant{}, errors.New("typed grant id is required")
	}
	if err := subject.Validate(); err != nil {
		return Grant{}, err
	}
	if len(pairs) == 0 {
		if pairs == nil {
			return Grant{}, access.ErrTokenPermissionsNeeded
		}
	}
	profile := access.PermissionCatalogProfile
	if len(pairs) > 0 {
		profile = pairs[0].Profile
	}
	if err := access.ValidateTypedPermissionSet(profile, pairs); err != nil {
		return Grant{}, err
	}
	return Grant{ID: id, Name: name, Subject: subject, PermissionProfile: profile, Permissions: access.ClonePermissionPairs(pairs)}, nil
}

// NewTypedPermissionGrant is an explicit singular-pair spelling for callers
// that issue one exact grant at a time.
func NewTypedPermissionGrant(id, name string, subject access.SubjectRef, pair access.PermissionPair) (Grant, error) {
	return NewTypedGrant(id, name, subject, []access.PermissionPair{pair})
}

func validateTypedGrant(grant Grant, identity graph.ServingIdentity, project graph.ProjectGraph) error {
	if err := access.ValidateTypedPermissionSet(grant.PermissionProfile, grant.Permissions); err != nil {
		return err
	}
	subject := grant.Subject
	if subject == (access.SubjectRef{}) && grant.Canonical.Validate() == nil {
		subject = grant.Canonical.Subject()
	}
	if err := subject.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrTypedSnapshotSubjectRequired, err)
	}
	for i, pair := range grant.Permissions {
		target := pair.Target
		if target.ProjectID != identity.ProjectID {
			return fmt.Errorf("pair %d: %w %q", i, ErrTypedSnapshotProjectMismatch, target.ProjectID)
		}
		switch target.Scope {
		case access.PermissionScopeResource:
			resource, err := access.NewResourceRef(target.ResourceID, target.ResourceKind)
			if err != nil {
				return fmt.Errorf("pair %d resource: %w", i, err)
			}
			if err := resource.ValidateAgainst(project); err != nil {
				return fmt.Errorf("pair %d resource: %w", i, err)
			}
		case access.PermissionScopeProject:
			// Project actions are bound to the serving identity. Future
			// selectors are valid for role bindings but not for an exact grant.
			if target.IncludeFuture {
				return fmt.Errorf("pair %d: exact grant cannot include future resources", i)
			}
		case access.PermissionScopeInstance:
			return fmt.Errorf("pair %d: instance authority cannot be installed in a project snapshot", i)
		default:
			return fmt.Errorf("pair %d: unsupported target scope %q", i, target.Scope)
		}
	}
	return nil
}

// AllowsTyped evaluates a requested typed action/resource pair against the
// immutable snapshot. It unions only already-typed assignments for the
// subject, then evaluates the catalog prerequisite closure. Legacy generic
// capabilities are deliberately ignored here.
func (s AuthorizationSnapshot) AllowsTyped(subject access.SubjectRef, requested access.PermissionPair) (bool, error) {
	if err := s.ValidateBound(); err != nil {
		return false, err
	}
	if err := subject.Validate(); err != nil {
		return false, err
	}
	if err := requested.Validate(); err != nil {
		return false, err
	}
	if err := validateTypedPairNamespace(requested, s.identity, s.project); err != nil {
		return false, err
	}
	granted, err := s.EffectiveTypedPermissions([]access.SubjectRef{subject})
	if err != nil {
		return false, err
	}
	return access.PermissionSetAllows(granted, requested), nil
}

// EffectiveTypedPermissions returns the exact profile-pinned pair union for
// the supplied principal/group subjects. Group and service-principal
// identities use the same SubjectRef path: service principals are explicit
// principal subjects and groups are explicit group subjects.
func (s AuthorizationSnapshot) EffectiveTypedPermissions(subjects []access.SubjectRef) ([]access.PermissionPair, error) {
	if err := s.ValidateBound(); err != nil {
		return nil, err
	}
	seenSubjects := make(map[access.SubjectRef]struct{}, len(subjects))
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			return nil, err
		}
		seenSubjects[subject] = struct{}{}
	}
	seenPairs := make(map[string]access.PermissionPair)
	for _, grant := range s.grants {
		if grant.PermissionProfile == "" && grant.Permissions == nil {
			continue
		}
		subject := grant.Subject
		if subject == (access.SubjectRef{}) && grant.Canonical.Validate() == nil {
			subject = grant.Canonical.Subject()
		}
		if _, ok := seenSubjects[subject]; !ok {
			continue
		}
		if err := access.ValidateTypedPermissionSet(grant.PermissionProfile, grant.Permissions); err != nil {
			return nil, fmt.Errorf("grant %q: %w", grant.ID, err)
		}
		for _, pair := range grant.Permissions {
			seenPairs[pair.Key()] = pair
		}
	}
	for _, binding := range s.roleBindings {
		if _, ok := seenSubjects[binding.Subject]; !ok {
			continue
		}
		if !binding.TypedRoleBinding() {
			// A legacy capability bundle is ambiguous in the typed catalog and
			// therefore has no typed evaluation authority.
			continue
		}
		if err := access.ValidateTypedPermissionSet(binding.PermissionProfile, binding.Permissions); err != nil {
			return nil, fmt.Errorf("role binding %q: %w", binding.ID, err)
		}
		for _, pair := range binding.Permissions {
			seenPairs[pair.Key()] = pair
		}
	}
	result := make([]access.PermissionPair, 0, len(seenPairs))
	for _, pair := range seenPairs {
		result = append(result, pair)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key() < result[j].Key() })
	return result, nil
}

func validateTypedPairNamespace(pair access.PermissionPair, identity graph.ServingIdentity, project graph.ProjectGraph) error {
	if pair.Target.ProjectID != identity.ProjectID {
		return ErrTypedSnapshotProjectMismatch
	}
	if pair.Target.Scope != access.PermissionScopeResource {
		return nil
	}
	resource, err := access.NewResourceRef(pair.Target.ResourceID, pair.Target.ResourceKind)
	if err != nil {
		return err
	}
	return resource.ValidateAgainst(project)
}

func validateTypedRoleBindingTargets(binding access.RoleBinding, identity graph.ServingIdentity, project graph.ProjectGraph) error {
	for i, pair := range binding.Permissions {
		if err := validateTypedPairNamespace(pair, identity, project); err != nil {
			// Future-resource role selectors intentionally have no graph node to
			// validate. They remain safe because the catalog pins their project,
			// kind, and includeFuture shape.
			if pair.Target.Scope == access.PermissionScopeProject && pair.Target.IncludeFuture {
				if pair.Target.ProjectID != identity.ProjectID {
					return fmt.Errorf("pair %d: %w", i, err)
				}
				continue
			}
			return fmt.Errorf("pair %d: %w", i, err)
		}
	}
	return nil
}
