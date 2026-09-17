package snapshot

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// EffectiveTypedPermissionOptions is the bounded compatibility projection
// used while the serving snapshot still stores legacy capability grants. It
// intentionally covers only the three qualified vertical slices below:
// dashboard reads, semantic consumption, and pipeline runs. Every result is
// an exact resource pair from this snapshot's graph; no project wildcard,
// mutation, administration, sharing, publishing, or platform action is
// inferred from legacy authority.
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

	result := make([]access.PermissionPair, 0)
	seenPairs := make(map[string]struct{})
	for _, resource := range s.Project().Resources() {
		action, capability, ok := compatibilityAction(resource.Kind)
		if !ok {
			continue
		}
		resourceRef, err := access.NewResourceRef(resource.ID, resource.Kind)
		if err != nil {
			return nil, fmt.Errorf("typed permission resource %q: %w", resource.ID, err)
		}
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
		if !allowed {
			continue
		}
		pair, err := access.NewExactPermissionPair(action, s.Identity().ProjectID, resourceRef)
		if err != nil {
			return nil, err
		}
		key := typedPermissionPairKey(pair)
		if _, seen := seenPairs[key]; seen {
			continue
		}
		seenPairs[key] = struct{}{}
		result = append(result, pair)
	}
	sort.Slice(result, func(i, j int) bool {
		return typedPermissionPairKey(result[i]) < typedPermissionPairKey(result[j])
	})
	return result, nil
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
