package deployment

// This file contains the delivery-specific compound authorization contract.
// It deliberately lives beside the delivery plan, while the immutable typed
// security snapshot remains owned by access/snapshot. A delivery plan is not a
// set of independent action and resource arrays: the pair relationship is
// retained for every changed resource, dependency, binding, and transition.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrDeliveryAuthorityInvalid       = errors.New("invalid delivery authorization plan")
	ErrDeliveryAuthorityDenied        = errors.New("delivery authorization denied")
	ErrDeliveryAuthoritySnapshotDrift = errors.New("delivery authorization snapshot changed")
	ErrDeliveryAuthorityEvidenceDrift = errors.New("delivery authorization evidence changed")
	ErrDeliveryUndeclaredDependency   = errors.New("delivery execution discovered an undeclared dependency")
)

// DeliveryDependencyUse identifies how an execution dependency is consumed.
// The use is part of the plan and is not inferred from graph reachability.
type DeliveryDependencyKind string

const (
	DeliveryDependencyRead DeliveryDependencyKind = "read"
	DeliveryDependencyUse  DeliveryDependencyKind = "use"
	DeliveryDependencyRun  DeliveryDependencyKind = "run"
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

func (plan DeliveryAuthorizationPlan) canonical() DeliveryAuthorizationPlan {
	plan.ProjectID = projectgraph.ResourceID(strings.TrimSpace(plan.ProjectID.String()))
	plan.TargetID = strings.TrimSpace(plan.TargetID)
	plan.SnapshotDigest = strings.TrimSpace(plan.SnapshotDigest)
	plan.ChangedResources = append([]DeliveryChangedResource(nil), plan.ChangedResources...)
	plan.Dependencies = append([]DeliveryDependency(nil), plan.Dependencies...)
	plan.ConnectionBindings = append([]DeliveryConnectionBinding(nil), plan.ConnectionBindings...)
	plan.DeliveryTransitions = append([]DeliveryTransition(nil), plan.DeliveryTransitions...)
	for i := range plan.Dependencies {
		plan.Dependencies[i].Use = DeliveryDependencyKind(strings.TrimSpace(string(plan.Dependencies[i].Use)))
		plan.Dependencies[i].Action = access.Action(strings.TrimSpace(string(plan.Dependencies[i].Action)))
		plan.Dependencies[i].EvidenceDigest = strings.TrimSpace(plan.Dependencies[i].EvidenceDigest)
		if plan.Dependencies[i].Action == "" {
			if action, err := dependencyAction(plan.Dependencies[i]); err == nil {
				plan.Dependencies[i].Action = action
			}
		}
	}
	for i := range plan.ConnectionBindings {
		plan.ConnectionBindings[i].BindingID = strings.TrimSpace(plan.ConnectionBindings[i].BindingID)
		plan.ConnectionBindings[i].EvidenceDigest = strings.TrimSpace(plan.ConnectionBindings[i].EvidenceDigest)
	}
	sort.Slice(plan.ChangedResources, func(i, j int) bool {
		return changedResourceKey(plan.ChangedResources[i]) < changedResourceKey(plan.ChangedResources[j])
	})
	sort.Slice(plan.Dependencies, func(i, j int) bool { return dependencyKey(plan.Dependencies[i]) < dependencyKey(plan.Dependencies[j]) })
	sort.Slice(plan.ConnectionBindings, func(i, j int) bool {
		return bindingKey(plan.ConnectionBindings[i]) < bindingKey(plan.ConnectionBindings[j])
	})
	sort.Slice(plan.DeliveryTransitions, func(i, j int) bool { return plan.DeliveryTransitions[i].Action < plan.DeliveryTransitions[j].Action })
	return plan
}

func (plan DeliveryAuthorizationPlan) validateShape() error {
	if err := plan.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", ErrDeliveryAuthorityInvalid, err)
	}
	if err := ValidateDeliveryID(plan.TargetID); err != nil {
		return fmt.Errorf("%w: target identity: %v", ErrDeliveryAuthorityInvalid, err)
	}
	seen := map[string]string{}
	for i, item := range plan.ChangedResources {
		if err := item.Resource.Validate(); err != nil {
			return fmt.Errorf("%w: changed resource %d: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		if err := validateChangedAction(item.Action, item.Resource.Kind()); err != nil {
			return fmt.Errorf("%w: changed resource %d: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		if err := duplicateCategory(seen, "changed", changedResourceKey(item)); err != nil {
			return err
		}
	}
	seen = map[string]string{}
	for i, item := range plan.Dependencies {
		if err := item.Resource.Validate(); err != nil {
			return fmt.Errorf("%w: dependency %d: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		action, err := dependencyAction(item)
		if err != nil {
			return fmt.Errorf("%w: dependency %d: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		plan.Dependencies[i].Action = action
		if item.EvidenceDigest != "" {
			if err := ValidateDeliveryDigest(item.EvidenceDigest); err != nil {
				return fmt.Errorf("%w: dependency %d evidence: %v", ErrDeliveryAuthorityInvalid, i, err)
			}
		}
		if err := duplicateCategory(seen, "dependency", dependencyKey(plan.Dependencies[i])); err != nil {
			return err
		}
	}
	seen = map[string]string{}
	for i, item := range plan.ConnectionBindings {
		if err := item.Connection.Validate(); err != nil || item.Connection.Kind() != projectgraph.KindConnection {
			return fmt.Errorf("%w: connection binding %d resource is not a connection", ErrDeliveryAuthorityInvalid, i)
		}
		if err := ValidateDeliveryID(item.BindingID); err != nil {
			return fmt.Errorf("%w: connection binding %d id: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		if err := ValidateDeliveryDigest(item.EvidenceDigest); err != nil {
			return fmt.Errorf("%w: connection binding %d evidence: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		if err := duplicateCategory(seen, "connection binding", bindingKey(item)); err != nil {
			return err
		}
	}
	seen = map[string]string{}
	for i, item := range plan.DeliveryTransitions {
		if err := validateTransitionAction(item.Action); err != nil {
			return fmt.Errorf("%w: transition %d: %v", ErrDeliveryAuthorityInvalid, i, err)
		}
		if err := duplicateCategory(seen, "transition", string(item.Action)); err != nil {
			return err
		}
	}
	return nil
}

func duplicateCategory(seen map[string]string, category, key string) error {
	if _, ok := seen[key]; ok {
		return fmt.Errorf("%w: duplicate %s authority %q", ErrDeliveryAuthorityInvalid, category, key)
	}
	seen[key] = category
	return nil
}

func validateChangedAction(action access.Action, kind projectgraph.Kind) error {
	definition, ok := access.Permission(action)
	if !ok || len(definition.ResourceKinds) == 0 || definition.ResourceKinds[0] != kind {
		return fmt.Errorf("action %q is not valid for changed kind %q", action, kind)
	}
	if action != createActionForKind(kind) && action != updateActionForKind(kind) && action != deleteActionForKind(kind) {
		return fmt.Errorf("action %q is not a kind-specific create/update/delete action", action)
	}
	return nil
}

func dependencyAction(item DeliveryDependency) (access.Action, error) {
	action := item.Action
	if action == "" {
		switch item.Use {
		case DeliveryDependencyRead:
			action = readActionForKind(item.Resource.Kind())
			if action == "" {
				return "", fmt.Errorf("read dependency kind %q is unsupported", item.Resource.Kind())
			}
		case DeliveryDependencyUse:
			switch item.Resource.Kind() {
			case projectgraph.KindConnection:
				action = access.ActionConnectionUse
			case projectgraph.KindSemanticModel:
				action = access.ActionSemanticConsume
			default:
				action = readActionForKind(item.Resource.Kind())
				if action == "" {
					return "", fmt.Errorf("use dependency kind %q requires an explicit supported action", item.Resource.Kind())
				}
			}
		case DeliveryDependencyRun:
			if item.Resource.Kind() != projectgraph.KindPipeline {
				return "", fmt.Errorf("run dependency kind %q is unsupported", item.Resource.Kind())
			}
			action = access.ActionPipelineRun
		default:
			return "", fmt.Errorf("unsupported dependency use %q", item.Use)
		}
	}
	definition, ok := access.Permission(action)
	if !ok {
		return "", fmt.Errorf("unknown dependency action %q", action)
	}
	validKind := false
	for _, kind := range definition.ResourceKinds {
		validKind = validKind || kind == item.Resource.Kind()
	}
	if !validKind {
		return "", fmt.Errorf("dependency action %q is not valid for kind %q", action, item.Resource.Kind())
	}
	switch item.Use {
	case DeliveryDependencyRead:
		if !strings.HasSuffix(string(action), ".read") {
			return "", fmt.Errorf("read dependency action %q is not a read action", action)
		}
	case DeliveryDependencyUse:
		if action != access.ActionConnectionUse && action != access.ActionSemanticConsume && !strings.HasSuffix(string(action), ".read") {
			return "", fmt.Errorf("use dependency action %q is unsupported", action)
		}
	case DeliveryDependencyRun:
		if action != access.ActionPipelineRun {
			return "", fmt.Errorf("run dependency action %q is unsupported", action)
		}
	default:
		return "", fmt.Errorf("unsupported dependency use %q", item.Use)
	}
	return action, nil
}

func readActionForKind(kind projectgraph.Kind) access.Action {
	switch kind {
	case projectgraph.KindConnection:
		return access.ActionConnectionRead
	case projectgraph.KindSource:
		return access.ActionSourceRead
	case projectgraph.KindModel:
		return access.ActionModelRead
	case projectgraph.KindSemanticModel:
		return access.ActionSemanticRead
	case projectgraph.KindPipeline:
		return access.ActionPipelineRead
	case projectgraph.KindDashboard:
		return access.ActionDashboardRead
	default:
		return ""
	}
}

func validateTransitionAction(action access.Action) error {
	switch action {
	case access.ActionDeliveryPlan, access.ActionDeliveryBuild, access.ActionDeliveryPublish, access.ActionDeliveryApprove, access.ActionDeliveryActivate, access.ActionDeliveryRollback:
		return nil
	default:
		return fmt.Errorf("action %q is not an independent delivery transition", action)
	}
}

func changedResourceKey(item DeliveryChangedResource) string {
	return string(item.Action) + "\x00" + string(item.Resource.Kind()) + "\x00" + item.Resource.ID().String()
}
func dependencyKey(item DeliveryDependency) string {
	action := item.Action
	return string(item.Use) + "\x00" + string(action) + "\x00" + string(item.Resource.Kind()) + "\x00" + item.Resource.ID().String()
}
func bindingKey(item DeliveryConnectionBinding) string {
	return item.BindingID + "\x00" + item.Connection.ID().String()
}

// BindSnapshot attaches the one coherent typed snapshot evidence identity.
func (plan DeliveryAuthorizationPlan) BindSnapshot(snapshot accesssnapshot.AuthorizationSnapshot) (DeliveryAuthorizationPlan, error) {
	if err := plan.validateShape(); err != nil {
		return DeliveryAuthorizationPlan{}, err
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return DeliveryAuthorizationPlan{}, fmt.Errorf("%w: snapshot digest: %v", ErrDeliveryAuthorityInvalid, err)
	}
	plan.SnapshotDigest = digest
	return plan, nil
}

// Digest returns the deterministic identity of all declared authority and its
// selected snapshot evidence. Pair ordering is canonicalized by category.
func (plan DeliveryAuthorizationPlan) Digest() (string, error) {
	plan = plan.canonical()
	if err := plan.validateShape(); err != nil {
		return "", err
	}
	if err := ValidateDeliveryDigest(plan.SnapshotDigest); err != nil {
		return "", fmt.Errorf("%w: snapshot digest is required: %v", ErrDeliveryAuthorityInvalid, err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("%w: encode authority plan: %v", ErrDeliveryAuthorityInvalid, err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Authorize evaluates every pair against one effective typed permission set
// from one coherent snapshot. It intentionally never accepts a collection of
// independently evaluated allow booleans, which would permit stale unions.
func (plan DeliveryAuthorizationPlan) Authorize(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef) error {
	plan = plan.canonical()
	if err := plan.validateShape(); err != nil {
		return err
	}
	if plan.SnapshotDigest == "" {
		return fmt.Errorf("%w: snapshot evidence is required", ErrDeliveryAuthoritySnapshotDrift)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return fmt.Errorf("%w: snapshot evidence unavailable: %v", ErrDeliveryAuthoritySnapshotDrift, err)
	}
	if digest != plan.SnapshotDigest || snapshot.Identity().ProjectID != plan.ProjectID {
		return fmt.Errorf("%w: expected %s, got %s", ErrDeliveryAuthoritySnapshotDrift, plan.SnapshotDigest, digest)
	}
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return fmt.Errorf("%w: typed snapshot evaluation: %v", ErrDeliveryAuthorityDenied, err)
	}
	pairs, err := plan.permissionPairs()
	if err != nil {
		return err
	}
	for i, pair := range pairs {
		if !access.PermissionSetAllows(granted, pair) {
			return fmt.Errorf("%w: pair %d (%s) is not independently authorized", ErrDeliveryAuthorityDenied, i, pair.Action)
		}
	}
	return nil
}

// EvaluateDeliveryAuthorizationPlan authorizes and returns the exact
// category/evidence projection to carry into execution.
func EvaluateDeliveryAuthorizationPlan(plan DeliveryAuthorizationPlan, snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef) (DeliveryAuthorizationExecution, error) {
	if err := plan.Authorize(snapshot, subjects); err != nil {
		return DeliveryAuthorizationExecution{}, err
	}
	plan = plan.canonical()
	planDigest, err := plan.Digest()
	if err != nil {
		return DeliveryAuthorizationExecution{}, err
	}
	return DeliveryAuthorizationExecution{PlanDigest: planDigest, SnapshotDigest: plan.SnapshotDigest, ChangedResources: append([]DeliveryChangedResource(nil), plan.ChangedResources...), Dependencies: append([]DeliveryDependency(nil), plan.Dependencies...), ConnectionBindings: append([]DeliveryConnectionBinding(nil), plan.ConnectionBindings...), DeliveryTransitions: append([]DeliveryTransition(nil), plan.DeliveryTransitions...)}, nil
}

func (plan DeliveryAuthorizationPlan) permissionPairs() ([]access.PermissionPair, error) {
	pairs := make([]access.PermissionPair, 0, len(plan.ChangedResources)+len(plan.Dependencies)+len(plan.ConnectionBindings)+len(plan.DeliveryTransitions))
	for _, item := range plan.ChangedResources {
		var pair access.PermissionPair
		var err error
		if item.Action == createActionForKind(item.Resource.Kind()) {
			pair, err = access.NewProjectPermissionPair(item.Action, plan.ProjectID)
		} else {
			pair, err = access.NewExactPermissionPair(item.Action, plan.ProjectID, item.Resource)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: changed pair: %v", ErrDeliveryAuthorityInvalid, err)
		}
		pairs = append(pairs, pair)
	}
	for _, item := range plan.Dependencies {
		pair, err := access.NewExactPermissionPair(item.Action, plan.ProjectID, item.Resource)
		if err != nil {
			return nil, fmt.Errorf("%w: dependency pair: %v", ErrDeliveryAuthorityInvalid, err)
		}
		pairs = append(pairs, pair)
	}
	for _, item := range plan.ConnectionBindings {
		pair, err := access.NewExactPermissionPair(access.ActionConnectionUse, plan.ProjectID, item.Connection)
		if err != nil {
			return nil, fmt.Errorf("%w: connection binding pair: %v", ErrDeliveryAuthorityInvalid, err)
		}
		pairs = append(pairs, pair)
	}
	for _, item := range plan.DeliveryTransitions {
		pair, err := access.NewProjectPermissionPair(item.Action, plan.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("%w: transition pair: %v", ErrDeliveryAuthorityInvalid, err)
		}
		pairs = append(pairs, pair)
	}
	return pairs, nil
}

// ValidateExecution rejects an execution that changed its plan/evidence or
// discovered an undeclared dependency. Exact category comparison is important:
// a changed resource is not interchangeable with a dependency merely because
// both mention the same graph node.
func (plan DeliveryAuthorizationPlan) ValidateExecution(execution DeliveryAuthorizationExecution) error {
	plan = plan.canonical()
	if err := plan.validateShape(); err != nil {
		return err
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return err
	}
	if execution.PlanDigest != planDigest {
		return fmt.Errorf("%w: plan digest differs", ErrDeliveryAuthorityEvidenceDrift)
	}
	if execution.SnapshotDigest != plan.SnapshotDigest {
		return fmt.Errorf("%w: snapshot digest differs", ErrDeliveryAuthoritySnapshotDrift)
	}
	actual := DeliveryAuthorizationPlan{ProjectID: plan.ProjectID, TargetID: plan.TargetID, ChangedResources: execution.ChangedResources, Dependencies: execution.Dependencies, ConnectionBindings: execution.ConnectionBindings, DeliveryTransitions: execution.DeliveryTransitions, SnapshotDigest: execution.SnapshotDigest}.canonical()
	if err := actual.validateShape(); err != nil {
		return fmt.Errorf("%w: execution evidence: %v", ErrDeliveryAuthorityEvidenceDrift, err)
	}
	if !sameChanged(plan.ChangedResources, actual.ChangedResources) || !sameTransitions(plan.DeliveryTransitions, actual.DeliveryTransitions) || !sameBindingIdentity(plan.ConnectionBindings, actual.ConnectionBindings) {
		return fmt.Errorf("%w: changed resource, binding, or transition evidence differs", ErrDeliveryAuthorityEvidenceDrift)
	}
	if !sameDependencyIdentity(plan.Dependencies, actual.Dependencies) {
		return ErrDeliveryUndeclaredDependency
	}
	if !sameDependencyEvidence(plan.Dependencies, actual.Dependencies) || !sameBindingEvidence(plan.ConnectionBindings, actual.ConnectionBindings) {
		return fmt.Errorf("%w: dependency or binding evidence differs", ErrDeliveryAuthorityEvidenceDrift)
	}
	return nil
}

func sameChanged(left, right []DeliveryChangedResource) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if changedResourceKey(left[i]) != changedResourceKey(right[i]) {
			return false
		}
	}
	return true
}
func sameDependencyIdentity(left, right []DeliveryDependency) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if dependencyKey(left[i]) != dependencyKey(right[i]) {
			return false
		}
	}
	return true
}
func sameDependencyEvidence(left, right []DeliveryDependency) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].EvidenceDigest != right[i].EvidenceDigest {
			return false
		}
	}
	return true
}
func sameBindingIdentity(left, right []DeliveryConnectionBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if bindingKey(left[i]) != bindingKey(right[i]) {
			return false
		}
	}
	return true
}
func sameBindingEvidence(left, right []DeliveryConnectionBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].EvidenceDigest != right[i].EvidenceDigest {
			return false
		}
	}
	return true
}
func sameTransitions(left, right []DeliveryTransition) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Action != right[i].Action {
			return false
		}
	}
	return true
}
