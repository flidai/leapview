package contractpublication

import projectgraph "github.com/flidai/leapview/internal/project/graph"

const PolicyAffectedResourceScope = "published-resource-only"

// PolicyAffectedResource is the immutable graph-planning seed attached to a
// publication. FAI-622 binds the directly published authored resource; a
// Project graph consumer may expand its dependants without reconstructing or
// mutating this evidence.
type PolicyAffectedResource struct {
	InstanceID   string                  `json:"instanceId"`
	AuthoredID   projectgraph.ResourceID `json:"authoredId"`
	ResourceKind projectgraph.Kind       `json:"resourceKind"`
	Scope        string                  `json:"scope"`
}

func directAffectedResources(candidate PublicationIdentity) []PolicyAffectedResource {
	return []PolicyAffectedResource{{
		InstanceID: candidate.InstanceID, AuthoredID: candidate.AuthoredID,
		ResourceKind: candidate.ResourceKind, Scope: PolicyAffectedResourceScope,
	}}
}

func equalAffectedResources(left, right []PolicyAffectedResource) bool {
	return equalJSON(left, right)
}
