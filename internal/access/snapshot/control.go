package snapshot

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// ProjectLiveAuthorizationSnapshot resolves the active instance authority
// from the live control store while retaining the compatibility snapshot's
// transitional DataPolicy evidence. The compatibility snapshot is used only
// for the create-once import when no live control state exists; it never
// replaces an already initialized live state.
//
// A missing state is initialized once. If another activation initializes it
// first, the initialization conflict is treated as the expected race and the
// state is reloaded. All other errors fail closed. Suspended, non-revoked
// grants are reactivated through the store's compare-and-swap operation
// against the exact serving graph before the final projection is read.
func ProjectLiveAuthorizationSnapshot(
	ctx context.Context,
	store access.ControlStore,
	instanceID, actorID string,
	identity graph.ServingIdentity,
	project graph.ProjectGraph,
	compatibility AuthorizationSnapshot,
) (AuthorizationSnapshot, error) {
	if store == nil {
		return AuthorizationSnapshot{}, errors.New("live access control store is required")
	}
	if err := ctx.Err(); err != nil {
		return AuthorizationSnapshot{}, err
	}
	if instanceID == "" {
		return AuthorizationSnapshot{}, errors.New("live access control instance identity is required")
	}
	if err := identity.Validate(); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("live authorization identity: %w", err)
	}
	if err := project.Validate(); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("live authorization project graph: %w", err)
	}
	if compatibility.Identity() != identity {
		return AuthorizationSnapshot{}, fmt.Errorf("live authorization compatibility identity does not match serving identity")
	}
	if err := compatibility.Validate(project); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("live authorization compatibility snapshot: %w", err)
	}

	state, err := store.ControlState(ctx, instanceID)
	if errors.Is(err, access.ErrControlNotFound) {
		seed, seedErr := ControlSeedFromSnapshot(instanceID, actorID, compatibility)
		if seedErr != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("seed live authorization control state: %w", seedErr)
		}
		state, err = store.InitializeControlState(ctx, seed, project)
		if errors.Is(err, access.ErrControlConflict) {
			// Another initializer won the create-once race. Its state is the
			// authority, including any edits made before this read completed.
			state, err = store.ControlState(ctx, instanceID)
		}
	}
	if err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("read live authorization control state: %w", err)
	}
	if err := validateControlStateAuthority(state, instanceID, project); err != nil {
		return AuthorizationSnapshot{}, err
	}
	if _, err := FromControlState(identity, project, state, nil); err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("validate live authorization control state: %w", err)
	}

	for _, grant := range state.Grants {
		if grant.RevokedAt != "" || grant.ReferenceLifecycle != access.ControlReferenceSuspended {
			continue
		}
		if err := grant.Resource.Validate(); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("live authorization grant %q resource: %w", grant.ID, err)
		}
		if err := grant.Subject.Validate(); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("live authorization grant %q subject: %w", grant.ID, err)
		}
		if err := access.ValidateCapabilityForKind(grant.Resource.Kind(), grant.Capability); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("live authorization grant %q capability: %w", grant.ID, err)
		}
		resource, found := project.Resource(grant.Resource.ID())
		if !found {
			// A suspended reference to a resource absent from this graph is
			// intentionally retained as denied. Reactivation belongs to the
			// explicit target restore path, not ordinary runtime preparation.
			continue
		}
		if resource.Kind != grant.Resource.Kind() {
			return AuthorizationSnapshot{}, fmt.Errorf("live authorization grant %q resource kind %q does not match graph kind %q", grant.ID, grant.Resource.Kind(), resource.Kind)
		}
		if _, err := store.ReactivateGrant(ctx, instanceID, grant.ID, grant.Revision, project, actorID); err != nil {
			return AuthorizationSnapshot{}, fmt.Errorf("reactivate live authorization grant %q: %w", grant.ID, err)
		}
	}
	finalState, err := store.ControlState(ctx, instanceID)
	if err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("reload live authorization control state: %w", err)
	}
	if err := validateControlStateAuthority(finalState, instanceID, project); err != nil {
		return AuthorizationSnapshot{}, err
	}
	projected, err := FromControlState(identity, project, finalState, compatibility.DataPolicies())
	if err != nil {
		return AuthorizationSnapshot{}, fmt.Errorf("project live authorization control state: %w", err)
	}
	return projected, nil
}

func validateControlStateAuthority(state access.ControlState, instanceID string, project graph.ProjectGraph) error {
	if state.InstanceID != instanceID {
		return fmt.Errorf("live authorization control state instance %q does not match requested instance %q", state.InstanceID, instanceID)
	}
	if state.ProjectID != project.ProjectID().String() {
		return fmt.Errorf("live authorization control state project %q does not match serving graph %q", state.ProjectID, project.ProjectID())
	}
	return nil
}

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
