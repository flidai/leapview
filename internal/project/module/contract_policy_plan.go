package module

import (
	"fmt"
	"slices"
	"sort"

	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identitymodule "github.com/flidai/leapview/internal/project/identityledger/module"
)

// ContractPolicyApprovalInput is explanation input for a later approval
// consumer, not an approval. Its graph digest and policy evidence digest must
// remain attached when that consumer binds a deployment decision.
type ContractPolicyApprovalInput struct {
	Decision           identitymodule.PolicyDecision `json:"decision"`
	GraphDigest        string                        `json:"graphDigest"`
	DirectResource     identitymodule.Resource       `json:"directResource"`
	DependentResources []identitymodule.Resource     `json:"dependentResources"`
}

// ContractPolicyPlan retains immutable graph authority and a detached,
// validated publication decision. Its zero value cannot produce approval input.
type ContractPolicyPlan struct {
	graph    projectgraph.ProjectGraph
	decision identitymodule.PolicyDecision
	valid    bool
}

func PlanContractPolicyEvidence(instanceID string, publication identitymodule.ContractPublication, graph projectgraph.ProjectGraph) (ContractPolicyPlan, error) {
	if instanceID == "" || publication.InstanceID != instanceID {
		return ContractPolicyPlan{}, fmt.Errorf("policy planning publication instance mismatch")
	}
	decision, err := publication.PolicyDecision()
	if err != nil {
		return ContractPolicyPlan{}, err
	}
	if _, _, err := contractPolicyImpact(graph, publication.AuthoredID, publication.ResourceKind, decision.Classification); err != nil {
		return ContractPolicyPlan{}, err
	}
	return ContractPolicyPlan{graph: graph, decision: clonePolicyDecision(decision), valid: true}, nil
}

func (p ContractPolicyPlan) ApprovalInput() (ContractPolicyApprovalInput, error) {
	if !p.valid {
		return ContractPolicyApprovalInput{}, fmt.Errorf("policy planning evidence is unavailable")
	}
	decision := clonePolicyDecision(p.decision)
	direct, dependents, err := contractPolicyImpact(p.graph, decision.Publication.AuthoredID, decision.Publication.ResourceKind, decision.Classification)
	if err != nil {
		return ContractPolicyApprovalInput{}, err
	}
	return ContractPolicyApprovalInput{
		Decision: decision, GraphDigest: p.graph.Digest(), DirectResource: direct, DependentResources: dependents,
	}, nil
}

func clonePolicyDecision(decision identitymodule.PolicyDecision) identitymodule.PolicyDecision {
	decision.Classification.Changes = slices.Clone(decision.Classification.Changes)
	decision.ChangedDimensions = slices.Clone(decision.ChangedDimensions)
	decision.AffectedResources = slices.Clone(decision.AffectedResources)
	return decision
}

// contractPolicyImpact projects graph-owned dependency information into the
// existing authored-resource identity types. It does not discover runtime consumers,
// evaluate access, or grant approval. No graph traversal is reimplemented here.
func contractPolicyImpact(graph projectgraph.ProjectGraph, id projectgraph.ResourceID, kind projectgraph.Kind, result contractversion.Result) (identitymodule.Resource, []identitymodule.Resource, error) {
	var direct identitymodule.Resource
	var dependents []identitymodule.Resource
	if err := graph.Validate(); err != nil {
		return direct, dependents, err
	}
	if err := result.ValidatePublication(); err != nil {
		return direct, dependents, err
	}
	resource, ok := graph.Resource(id)
	if !ok || resource.Kind != kind || !identitymodule.IsAuthoredKind(kind) {
		return direct, dependents, fmt.Errorf("policy publication resource is absent from the planning graph or has a different kind")
	}
	direct = identitymodule.Resource{AuthoredID: id, Kind: kind}
	// Initial publication does not imply the authored graph node was added.
	// This is publication impact, not a reconstructed graph-to-graph diff.
	for _, dependent := range graph.Resources() {
		if dependent.ID == id || !identitymodule.IsAuthoredKind(dependent.Kind) {
			continue
		}
		for _, dependency := range graph.Dependencies(dependent.ID) {
			if dependency == id {
				dependents = append(dependents, identitymodule.Resource{AuthoredID: dependent.ID, Kind: dependent.Kind})
				break
			}
		}
	}
	sort.Slice(dependents, func(i, j int) bool { return dependents[i].AuthoredID < dependents[j].AuthoredID })
	return direct, dependents, nil
}
