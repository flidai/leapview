package snapshot

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// ControlSeedFromSnapshot creates the reviewed one-time live-control import
// from an already bound compatibility snapshot. DataPolicy is intentionally
// absent: its transitional authored semantics remain owned by FAI-649.
func ControlSeedFromSnapshot(instanceID, actorID string, snapshot AuthorizationSnapshot) (access.ControlStateSeed, error) {
	if err := snapshot.ValidateBound(); err != nil {
		return access.ControlStateSeed{}, err
	}
	seed := access.ControlStateSeed{InstanceID: instanceID, ProjectID: snapshot.Identity().ProjectID.String(), ActorID: actorID}
	for _, binding := range snapshot.RoleBindings() {
		seed.RoleAssignments = append(seed.RoleAssignments, access.RoleAssignmentInput{
			ID: binding.ID, InstanceID: instanceID, ProjectID: seed.ProjectID,
			Subject: binding.Subject, Role: string(binding.Role), Name: binding.Name,
		})
	}
	for _, grant := range snapshot.Grants() {
		// Historical snapshots could express PROJECT_ADMIN as a direct grant
		// against the synthetic Project root. The live boundary has no Project
		// grant resource, so preserve that authority as the equivalent canonical
		// admin role assignment during the one-time import.
		if grant.Canonical.Resource().Kind() == graph.KindProject {
			if grant.Canonical.Capability() != access.CapabilityProjectAdmin {
				return access.ControlStateSeed{}, fmt.Errorf("project grant %q has unsupported capability %q", grant.ID, grant.Canonical.Capability())
			}
			seed.RoleAssignments = append(seed.RoleAssignments, access.RoleAssignmentInput{
				ID: grant.ID, InstanceID: instanceID, ProjectID: seed.ProjectID, Name: grant.Name,
				Subject: grant.Canonical.Subject(), Role: string(access.ProjectRoleAdmin),
			})
			continue
		}
		seed.Grants = append(seed.Grants, access.ControlGrantInput{
			ID: grant.ID, InstanceID: instanceID, ProjectID: seed.ProjectID, Name: grant.Name,
			Subject: grant.Canonical.Subject(), Resource: grant.Canonical.Resource(), Capability: grant.Canonical.Capability(),
		})
	}
	return seed, nil
}

// FromControlState projects the current instance-administered role/grant
// authority into the immutable representation consumed by one serving
// generation. Transitional DataPolicy evidence is carried separately and is
// deliberately not moved into the live control store by FAI-616.
func FromControlState(identity graph.ServingIdentity, project graph.ProjectGraph, state access.ControlState, transitionalPolicies []DataPolicy) (AuthorizationSnapshot, error) {
	if state.InstanceID == "" {
		return AuthorizationSnapshot{}, fmt.Errorf("control state instance identity is required")
	}
	if state.ProjectID != project.ProjectID().String() || identity.ProjectID != project.ProjectID() {
		return AuthorizationSnapshot{}, fmt.Errorf("control state project %q does not match serving graph %q", state.ProjectID, project.ProjectID())
	}
	bindings := make([]RoleBinding, 0, len(state.RoleAssignments))
	for _, stored := range state.RoleAssignments {
		if stored.InstanceID != state.InstanceID || stored.ProjectID != state.ProjectID {
			return AuthorizationSnapshot{}, fmt.Errorf("role assignment %q crosses control authority", stored.ID)
		}
		if stored.RevokedAt != "" {
			continue
		}
		role, err := access.ParseProjectRole(stored.Role)
		if err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("role assignment %q: %w", stored.ID, err)
		}
		bindings = append(bindings, RoleBinding{
			ID: stored.ID, Name: stored.Name, Subject: stored.Subject, Role: role,
			Capabilities: access.ProjectRoleCapabilities(role),
		})
	}
	grants := make([]Grant, 0, len(state.Grants))
	for _, stored := range state.Grants {
		if stored.InstanceID != state.InstanceID || stored.ProjectID != state.ProjectID {
			return AuthorizationSnapshot{}, fmt.Errorf("grant %q crosses control authority", stored.ID)
		}
		if stored.RevokedAt != "" || stored.ReferenceLifecycle == access.ControlReferenceSuspended {
			continue
		}
		if stored.ReferenceLifecycle != access.ControlReferenceActive {
			return AuthorizationSnapshot{}, fmt.Errorf("grant %q has invalid reference lifecycle %q", stored.ID, stored.ReferenceLifecycle)
		}
		canonical, err := access.NewCanonicalGrant(project, stored.Subject, stored.Resource, stored.Capability)
		if err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("grant %q: %w", stored.ID, err)
		}
		grants = append(grants, Grant{ID: stored.ID, Name: stored.Name, Canonical: canonical})
	}
	return NewAuthorizationSnapshotWithRoleBindings(identity, project, bindings, grants, transitionalPolicies)
}
