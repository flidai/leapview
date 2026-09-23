package snapshot

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// EffectiveTypedPermissionOptions returns the action-target pairs that can be
// selected for a token from the supplied principal/group subjects. A typed
// assignment is the authoritative source. Future-resource selectors remain
// explicit options and are also expanded against the immutable graph so the UI
// can offer either an intentional wildcard or a current-resource snapshot.
// Historical generic capabilities do not create token options: only explicit
// typed authority can be delegated to a new credential.
func (s AuthorizationSnapshot) EffectiveTypedPermissionOptions(subjects []access.SubjectRef) ([]access.PermissionPair, error) {
	if err := s.ValidateBound(); err != nil {
		return nil, err
	}

	validatedSubjects := make([]access.SubjectRef, 0, len(subjects))
	seenSubjects := make(map[access.SubjectRef]struct{}, len(subjects))
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			return nil, err
		}
		if _, seen := seenSubjects[subject]; seen {
			continue
		}
		seenSubjects[subject] = struct{}{}
		validatedSubjects = append(validatedSubjects, subject)
	}

	typedAuthority, err := s.EffectiveTypedPermissions(validatedSubjects)
	if err != nil {
		return nil, err
	}
	result := make([]access.PermissionPair, 0)
	seenPairs := make(map[string]struct{})
	add := func(pair access.PermissionPair) {
		key := typedPermissionPairKey(pair)
		if _, seen := seenPairs[key]; seen {
			return
		}
		seenPairs[key] = struct{}{}
		result = append(result, pair)
	}

	// Project-scoped typed pairs are already complete action-target pairs.
	// Administrative and creation pairs name the Project. Future-resource pairs
	// retain their explicit wildcard so issuance can distinguish them from the
	// exact current-resource expansion below.
	for _, pair := range typedAuthority {
		if pair.Target.Scope == access.PermissionScopeProject {
			add(pair)
		}
	}

	// Enumerate every catalog action that is valid for each graph resource.
	// This preserves typed-only authority for actions that have no safe legacy
	// capability mapping, while expanding typed future selectors to exact
	// current resources. PermissionSetAllows also preserves prerequisite and
	// action/resource pairing semantics.
	definitions := access.PermissionCatalog()
	for _, resource := range s.Project().Resources() {
		resourceRef, err := access.NewResourceRef(resource.ID, resource.Kind)
		if err != nil {
			return nil, fmt.Errorf("typed permission resource %q: %w", resource.ID, err)
		}
		for _, definition := range definitions {
			if definition.Scope != access.PermissionScopeResource || !permissionDefinitionSupportsKind(definition, resource.Kind) {
				continue
			}
			pair, err := access.NewExactPermissionPair(definition.Action, s.Identity().ProjectID, resourceRef)
			if err != nil {
				return nil, err
			}
			if access.PermissionSetAllows(typedAuthority, pair) {
				add(pair)
			}
		}

	}
	sort.Slice(result, func(i, j int) bool {
		return typedPermissionPairKey(result[i]) < typedPermissionPairKey(result[j])
	})
	return result, nil
}

func permissionDefinitionSupportsKind(definition access.PermissionDefinition, kind graph.Kind) bool {
	for _, supported := range definition.ResourceKinds {
		if supported == kind {
			return true
		}
	}
	return false
}

func typedPermissionPairKey(pair access.PermissionPair) string {
	target := pair.Target
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t|%s", pair.Action, target.Scope, target.InstanceID, target.ProjectID, target.ResourceKind, target.ResourceID, target.IncludeFuture, pair.Profile)
}
