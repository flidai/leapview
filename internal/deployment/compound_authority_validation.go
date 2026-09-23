package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

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
		case deliveryDependencyRun:
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
	case deliveryDependencyRun:
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
