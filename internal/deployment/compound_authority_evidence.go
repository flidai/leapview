package deployment

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
)

// BindSnapshot captures the coherent snapshot identity for later evaluation.
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
		return fmt.Errorf("%w: plan digest differs", errDeliveryAuthorityEvidenceDrift)
	}
	if execution.SnapshotDigest != plan.SnapshotDigest {
		return fmt.Errorf("%w: snapshot digest differs", ErrDeliveryAuthoritySnapshotDrift)
	}
	actual := DeliveryAuthorizationPlan{ProjectID: plan.ProjectID, TargetID: plan.TargetID, ChangedResources: execution.ChangedResources, Dependencies: execution.Dependencies, ConnectionBindings: execution.ConnectionBindings, DeliveryTransitions: execution.DeliveryTransitions, SnapshotDigest: execution.SnapshotDigest}.canonical()
	if err := actual.validateShape(); err != nil {
		return fmt.Errorf("%w: execution evidence: %v", errDeliveryAuthorityEvidenceDrift, err)
	}
	if !sameChanged(plan.ChangedResources, actual.ChangedResources) || !sameTransitions(plan.DeliveryTransitions, actual.DeliveryTransitions) || !sameBindingIdentity(plan.ConnectionBindings, actual.ConnectionBindings) {
		return fmt.Errorf("%w: changed resource, binding, or transition evidence differs", errDeliveryAuthorityEvidenceDrift)
	}
	if !sameDependencyIdentity(plan.Dependencies, actual.Dependencies) {
		return ErrDeliveryUndeclaredDependency
	}
	if !sameDependencyEvidence(plan.Dependencies, actual.Dependencies) || !sameBindingEvidence(plan.ConnectionBindings, actual.ConnectionBindings) {
		return fmt.Errorf("%w: dependency or binding evidence differs", errDeliveryAuthorityEvidenceDrift)
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
