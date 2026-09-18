package snapshot

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// EffectiveTypedPermissionOptions returns the exact action-target pairs that
// can be selected for a token from the supplied principal/group subjects. A
// typed assignment is the authoritative source; future-resource selectors
// are expanded against the immutable graph so the picker never silently
// persists a wildcard. Legacy capability grants remain a bounded projection
// for the three qualified vertical slices below: dashboard reads, semantic
// consumption, and pipeline runs. No typed or legacy authority is inferred
// across action, resource, project, or subject boundaries.
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

	// Project-scoped typed pairs are already exact action-target pairs. They
	// do not name a graph resource because they authorize operations such as
	// project administration or creation on the bound project.
	for _, pair := range typedAuthority {
		if pair.Target.Scope == access.PermissionScopeProject && !pair.Target.IncludeFuture {
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

		// Preserve the bounded legacy compatibility projection. Pipeline runs
		// intentionally require an exact legacy grant; a broad legacy role's
		// RESOURCE_USE must not become operational execution authority.
		action, capability, ok := compatibilityAction(resource.Kind)
		if ok {
			allowed := false
			for _, subject := range validatedSubjects {
				permitted, err := s.compatibilityPermissionAllowed(subject, resourceRef, capability, action)
				if err != nil {
					return nil, err
				}
				if permitted {
					allowed = true
					break
				}
			}
			if allowed {
				pair, err := access.NewExactPermissionPair(action, s.Identity().ProjectID, resourceRef)
				if err != nil {
					return nil, err
				}
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

func (s AuthorizationSnapshot) compatibilityPermissionAllowed(subject access.SubjectRef, resource access.ResourceRef, capability access.Capability, action access.Action) (bool, error) {
	// A legacy Project role's RESOURCE_USE applied to every compatible graph
	// kind. Treating that broad bundle as pipeline.run would, for example,
	// turn the old Viewer role into operational execution authority. Pipeline
	// run is therefore projected only from an exact legacy resource grant.
	if action == access.ActionPipelineRun {
		for _, grant := range s.Grants() {
			canonical := grant.Canonical
			if canonical.Subject() == subject && canonical.Resource() == resource && canonical.Capability() == capability {
				return true, nil
			}
		}
		return false, nil
	}
	// This helper is the explicit, bounded compatibility path used by the
	// legacy token picker. It is deliberately separate from AllowsTyped: only
	// the qualified dashboard-read and semantic-consume projections are
	// retained while surfaces migrate. Typed evaluation never calls this path.
	return s.Allows(subject, resource, capability)
}

func compatibilityAction(kind graph.Kind) (access.Action, access.Capability, bool) {
	switch kind {
	case graph.KindDashboard:
		return access.ActionDashboardRead, access.CapabilityResourceRead, true
	case graph.KindSemanticModel:
		return access.ActionSemanticConsume, access.CapabilityResourceUse, true
	case graph.KindPipeline:
		return access.ActionPipelineRun, access.CapabilityResourceUse, true
	default:
		return "", "", false
	}
}

func typedPermissionPairKey(pair access.PermissionPair) string {
	target := pair.Target
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t|%s", pair.Action, target.Scope, target.InstanceID, target.ProjectID, target.ResourceKind, target.ResourceID, target.IncludeFuture, pair.Profile)
}
