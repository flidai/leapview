package deployment

// This file contains the delivery-specific compound authorization contract.
// It deliberately lives beside the delivery plan, while the immutable typed
// security snapshot remains owned by access/snapshot. A delivery plan is not a
// set of independent action and resource arrays: the pair relationship is
// retained for every changed resource, dependency, binding, and transition.

import (
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrDeliveryAuthorityInvalid       = errors.New("invalid delivery authorization plan")
	ErrDeliveryAuthorityDenied        = errors.New("delivery authorization denied")
	ErrDeliveryAuthoritySnapshotDrift = errors.New("delivery authorization snapshot changed")
	errDeliveryAuthorityEvidenceDrift = errors.New("delivery authorization evidence changed")
	ErrDeliveryUndeclaredDependency   = errors.New("delivery execution discovered an undeclared dependency")
)

// DeliveryDependencyUse identifies how an execution dependency is consumed.
// The use is part of the plan and is not inferred from graph reachability.
type DeliveryDependencyKind string

const (
	DeliveryDependencyRead DeliveryDependencyKind = "read"
	DeliveryDependencyUse  DeliveryDependencyKind = "use"
	deliveryDependencyRun  DeliveryDependencyKind = "run"
)

// DeliveryChangedResource is an authored graph mutation. Create is checked
// on the Project because the resource does not exist yet; update/delete are
// checked on the exact resource. Action and resource remain one pair.
type DeliveryChangedResource struct {
	Action   access.Action      `json:"action"`
	Resource access.ResourceRef `json:"resource"`
}

// DeliveryDependency is an execution dependency. Action is optional only to
// make the common read/use/run forms concise; when omitted it is derived from
// Use and the resource kind. Supplying it explicitly still undergoes the same
// kind and use validation.
type DeliveryDependency struct {
	Use            DeliveryDependencyKind `json:"use"`
	Action         access.Action          `json:"action,omitempty"`
	Resource       access.ResourceRef     `json:"resource"`
	EvidenceDigest string                 `json:"evidenceDigest,omitempty"`
}

// DeliveryConnectionBinding is the exact connection authority and
// target-issued binding evidence used by execution. The credential/endpoint
// secret is never represented here.
type DeliveryConnectionBinding struct {
	BindingID      string             `json:"bindingId"`
	Connection     access.ResourceRef `json:"connection"`
	EvidenceDigest string             `json:"evidenceDigest"`
}

// DeliveryTransition is independent delivery lifecycle authority. It is not
// inferred from a changed resource or from a dependency pair.
type DeliveryTransition struct {
	Action access.Action `json:"action"`
}

// DeliveryAuthorizationPlan is the complete compound authority declaration
// for one delivery operation. SnapshotDigest is required before authorization
// or execution and pins all categories to one coherent typed snapshot.
type DeliveryAuthorizationPlan struct {
	ProjectID           projectgraph.ResourceID     `json:"projectId"`
	TargetID            string                      `json:"targetId"`
	ChangedResources    []DeliveryChangedResource   `json:"changedResources,omitempty"`
	Dependencies        []DeliveryDependency        `json:"dependencies,omitempty"`
	ConnectionBindings  []DeliveryConnectionBinding `json:"connectionBindings,omitempty"`
	DeliveryTransitions []DeliveryTransition        `json:"deliveryTransitions,omitempty"`
	SnapshotDigest      string                      `json:"snapshotDigest"`
}

// DeliveryAuthorizationExecution is the immutable category/evidence
// projection carried into an execution boundary. It must exactly match the
// plan: missing or extra dependencies are both rejected.
type DeliveryAuthorizationExecution struct {
	PlanDigest          string                      `json:"planDigest"`
	SnapshotDigest      string                      `json:"snapshotDigest"`
	ChangedResources    []DeliveryChangedResource   `json:"changedResources,omitempty"`
	Dependencies        []DeliveryDependency        `json:"dependencies,omitempty"`
	ConnectionBindings  []DeliveryConnectionBinding `json:"connectionBindings,omitempty"`
	DeliveryTransitions []DeliveryTransition        `json:"deliveryTransitions,omitempty"`
}

// NewDeliveryAuthorizationPlan validates and canonicalizes a compound plan.
// The plan is intentionally unbound until BindSnapshot is called; this keeps
// planning transport-neutral while making authorization fail closed if the
// caller forgets to select coherent evidence.
func NewDeliveryAuthorizationPlan(projectID projectgraph.ResourceID, targetID string, changed []DeliveryChangedResource, dependencies []DeliveryDependency, bindings []DeliveryConnectionBinding, transitions []DeliveryTransition) (DeliveryAuthorizationPlan, error) {
	plan := DeliveryAuthorizationPlan{ProjectID: projectID, TargetID: targetID, ChangedResources: changed, Dependencies: dependencies, ConnectionBindings: bindings, DeliveryTransitions: transitions}
	plan = plan.canonical()
	if err := plan.validateShape(); err != nil {
		return DeliveryAuthorizationPlan{}, err
	}
	return plan, nil
}

// DeliveryAuthorizationPlanFromBundle converts existing compiler graph-impact
// evidence into the separated compound contract. Compiler Changes are
// authored mutations; dependency edges are execution dependencies and never
// become update authority. Transitions are supplied independently by the
// caller for the requested lifecycle step.
func DeliveryAuthorizationPlanFromBundle(projectID projectgraph.ResourceID, targetID string, graph projectgraph.ProjectGraph, bundle projectcompiler.BundlePlan, transitions []access.Action) (DeliveryAuthorizationPlan, error) {
	return DeliveryAuthorizationPlanFromBundleWithBindings(projectID, targetID, graph, bundle, nil, transitions)
}

// DeliveryAuthorizationPlanFromBundleWithBindings is the binding-aware form
// used by native planning. Binding evidence is already resolved by the
// target-owned connection authority and is carried as exact non-secret
// identities into the compound declaration.
func DeliveryAuthorizationPlanFromBundleWithBindings(projectID projectgraph.ResourceID, targetID string, graph projectgraph.ProjectGraph, bundle projectcompiler.BundlePlan, bindings []DeliveryConnectionBinding, transitions []access.Action) (DeliveryAuthorizationPlan, error) {
	changed := make([]DeliveryChangedResource, 0, len(bundle.Changes))
	for _, item := range bundle.Changes {
		resource, ok := graph.Resource(projectgraph.ResourceID(item.ID))
		if !ok {
			kind, parseErr := projectgraph.ParseKind(item.Type)
			if parseErr != nil {
				return DeliveryAuthorizationPlan{}, fmt.Errorf("%w: changed resource %q is absent from graph and has invalid kind %q", ErrDeliveryAuthorityInvalid, item.ID, item.Type)
			}
			resource = projectgraph.Resource{ID: projectgraph.ResourceID(item.ID), Kind: kind, Name: item.Key}
		}
		action, err := changedResourceAction(item.Action, resource.Kind)
		if err != nil {
			return DeliveryAuthorizationPlan{}, err
		}
		changed = append(changed, DeliveryChangedResource{Action: action, Resource: mustResourceRef(resource)})
	}
	// The compiler's dependency changes are a review delta, not an execution
	// closure. Authorize every relation in the candidate graph so an unchanged
	// source/model/connection cannot disappear from the compound authority just
	// because its edge was unchanged. Keep delta-only removal dependencies as a
	// compatibility fallback when the removed endpoint is absent from the
	// candidate graph.
	dependencies := make([]DeliveryDependency, 0, len(graph.Edges())+len(bundle.DependencyChanges))
	dependencyKeys := make(map[string]struct{}, cap(dependencies))
	appendDependency := func(item DeliveryDependency) error {
		action, err := dependencyAction(item)
		if err != nil {
			return err
		}
		item.Action = action
		key := dependencyKey(item)
		if _, exists := dependencyKeys[key]; exists {
			return nil
		}
		dependencyKeys[key] = struct{}{}
		dependencies = append(dependencies, item)
		return nil
	}
	for _, edge := range graph.Edges() {
		resource, ok := graph.Resource(edge.To)
		if !ok {
			return DeliveryAuthorizationPlan{}, fmt.Errorf("%w: dependency %q is absent from graph", ErrDeliveryAuthorityInvalid, edge.To)
		}
		use, err := dependencyUseForRelation(edge.Relation, resource.Kind)
		if err != nil {
			return DeliveryAuthorizationPlan{}, err
		}
		if err := appendDependency(DeliveryDependency{Use: use, Resource: mustResourceRef(resource)}); err != nil {
			return DeliveryAuthorizationPlan{}, err
		}
	}
	for _, item := range bundle.DependencyChanges {
		resourceID := projectgraph.ResourceID(item.To)
		resource, ok := graph.Resource(resourceID)
		if !ok {
			kind, parseErr := projectgraph.ParseKind(item.ResourceKind)
			if parseErr != nil {
				return DeliveryAuthorizationPlan{}, fmt.Errorf("%w: dependency %q is absent from graph and has invalid kind %q", ErrDeliveryAuthorityInvalid, resourceID, item.ResourceKind)
			}
			resource = projectgraph.Resource{ID: resourceID, Kind: kind, Name: resourceID.String()}
		}
		use, err := dependencyUseForRelation(item.Type, resource.Kind)
		if err != nil {
			return DeliveryAuthorizationPlan{}, err
		}
		if err := appendDependency(DeliveryDependency{Use: use, Resource: mustResourceRef(resource)}); err != nil {
			return DeliveryAuthorizationPlan{}, err
		}
	}
	planTransitions := make([]DeliveryTransition, len(transitions))
	for i, action := range transitions {
		planTransitions[i] = DeliveryTransition{Action: action}
	}
	return NewDeliveryAuthorizationPlan(projectID, targetID, changed, dependencies, bindings, planTransitions)
}

func mustResourceRef(resource projectgraph.Resource) access.ResourceRef {
	ref, _ := access.NewResourceRef(resource.ID, resource.Kind)
	return ref
}

func changedResourceAction(operation string, kind projectgraph.Kind) (access.Action, error) {
	var action access.Action
	switch operation {
	case "add":
		action = createActionForKind(kind)
	case "change":
		action = updateActionForKind(kind)
	case "remove":
		action = deleteActionForKind(kind)
	default:
		return "", fmt.Errorf("%w: unsupported changed-resource operation %q", ErrDeliveryAuthorityInvalid, operation)
	}
	if action == "" {
		return "", fmt.Errorf("%w: no kind-specific %s action for %q", ErrDeliveryAuthorityInvalid, operation, kind)
	}
	return action, nil
}

func createActionForKind(kind projectgraph.Kind) access.Action {
	switch kind {
	case projectgraph.KindConnection:
		return access.ActionConnectionCreate
	case projectgraph.KindSource:
		return access.ActionSourceCreate
	case projectgraph.KindModel:
		return access.ActionModelCreate
	case projectgraph.KindSemanticModel:
		return access.ActionSemanticCreate
	case projectgraph.KindPipeline:
		return access.ActionPipelineCreate
	case projectgraph.KindDashboard:
		return access.ActionDashboardCreate
	default:
		return ""
	}
}

func updateActionForKind(kind projectgraph.Kind) access.Action {
	switch kind {
	case projectgraph.KindConnection:
		return access.ActionConnectionManage
	case projectgraph.KindSource:
		return access.ActionSourceUpdate
	case projectgraph.KindModel:
		return access.ActionModelUpdate
	case projectgraph.KindSemanticModel:
		return access.ActionSemanticUpdate
	case projectgraph.KindPipeline:
		return access.ActionPipelineUpdate
	case projectgraph.KindDashboard:
		return access.ActionDashboardUpdate
	default:
		return ""
	}
}

func deleteActionForKind(kind projectgraph.Kind) access.Action {
	switch kind {
	case projectgraph.KindConnection:
		return access.ActionConnectionManage
	case projectgraph.KindSource:
		return access.ActionSourceDelete
	case projectgraph.KindModel:
		return access.ActionModelDelete
	case projectgraph.KindSemanticModel:
		return access.ActionSemanticDelete
	case projectgraph.KindPipeline:
		return access.ActionPipelineDelete
	case projectgraph.KindDashboard:
		return access.ActionDashboardDelete
	default:
		return ""
	}
}

func dependencyUseForRelation(relation string, kind projectgraph.Kind) (DeliveryDependencyKind, error) {
	switch relation {
	case "reads_source", "reads_model", "uses_model", "uses_semantic_model", "uses_connection", "refreshes":
		if relation == "refreshes" {
			if kind != projectgraph.KindSemanticModel {
				return "", fmt.Errorf("%w: refreshes relation must target a semantic model", ErrDeliveryAuthorityInvalid)
			}
			// A delivery build consumes the SemanticModel targeted by the
			// authored Pipeline relation. It does not trigger an approved
			// Pipeline revision; runtime refresh admission checks pipeline.run
			// independently at that later operation boundary.
			return DeliveryDependencyUse, nil
		}
		if relation == "uses_connection" {
			return DeliveryDependencyUse, nil
		}
		if relation == "uses_model" || relation == "uses_semantic_model" {
			return DeliveryDependencyUse, nil
		}
		return DeliveryDependencyRead, nil
	default:
		return "", fmt.Errorf("%w: dependency relation %q has no authority meaning", ErrDeliveryAuthorityInvalid, relation)
	}
}
