package access

import (
	"sort"
)

// AuthorizationDecision is the explainable form of one effective capability.
// It deliberately carries the same source flags as the public API: a caller
// can distinguish a direct principal grant from group-derived access without
// learning any mutable role implementation details.
type AuthorizationDecision struct {
	Allowed         bool
	Capability      Capability
	Reason          string
	ResourceKind    string
	ResourceID      string
	GrantID         string
	GrantResourceID string
	SubjectType     string
	SubjectID       string
	Inherited       bool
	Owner           bool
	Platform        bool
}

// ExplainRoleBindingAccess projects immutable role-binding evidence for one
// resource.  Subjects must already have been resolved by the identity
// authority (principal followed by its current groups); this function never
// invents group membership or treats a principal's role name as authority.
func ExplainRoleBindingAccess(bindings []RoleBinding, subjects []SubjectRef, resource ResourceRef) []AuthorizationDecision {
	if resource.Validate() != nil {
		return nil
	}
	seenSubjects := make(map[SubjectRef]struct{}, len(subjects))
	for _, subject := range subjects {
		if subject.Validate() == nil {
			seenSubjects[subject] = struct{}{}
		}
	}
	result := make([]AuthorizationDecision, 0)
	for _, binding := range bindings {
		if _, ok := seenSubjects[binding.Subject]; !ok {
			continue
		}
		inherited := binding.Subject.Kind == SubjectKindGroup
		owner := binding.Role == ProjectRoleOwner
		for _, capability := range binding.Capabilities {
			if !SupportsCapability(resource.Kind(), capability) {
				continue
			}
			reason := "direct role binding"
			if inherited {
				reason = "group-inherited role binding"
			}
			if owner {
				reason = "owner role binding"
				if inherited {
					reason = "group-inherited owner role binding"
				}
			}
			result = append(result, AuthorizationDecision{
				Allowed: true, Capability: capability, Reason: reason,
				ResourceKind: string(resource.Kind()), ResourceID: resource.ID().String(),
				GrantID: binding.ID, GrantResourceID: resource.ID().String(),
				SubjectType: string(binding.Subject.Kind), SubjectID: binding.Subject.ID,
				Inherited: inherited, Owner: owner,
			})
		}
	}
	return result
}

// SortAuthorizationDecisions gives all explanation consumers one stable
// ordering.  Multiple decisions for the same capability are retained: those
// rows are the evidence that explains direct plus inherited access.
func SortAuthorizationDecisions(decisions []AuthorizationDecision) {
	sort.SliceStable(decisions, func(i, j int) bool {
		a, b := decisions[i], decisions[j]
		if a.ResourceKind != b.ResourceKind {
			return a.ResourceKind < b.ResourceKind
		}
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		if a.Capability != b.Capability {
			return capabilityIndex(a.Capability) < capabilityIndex(b.Capability)
		}
		if a.Inherited != b.Inherited {
			return !a.Inherited
		}
		if a.GrantID != b.GrantID {
			return a.GrantID < b.GrantID
		}
		return a.SubjectID < b.SubjectID
	})
}

func capabilityIndex(capability Capability) int {
	for index, candidate := range canonicalCapabilityOrder {
		if candidate == capability {
			return index
		}
	}
	return len(canonicalCapabilityOrder)
}
