package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

const maxAccessTransitionAssignments = 128

// AccessTransitionRoleIntent selects a versioned role from the typed catalog.
// The operator cannot supply an arbitrary permission list or legacy role.
type AccessTransitionRoleIntent struct {
	BindingID string `json:"bindingId"`
	Name      string `json:"name,omitempty"`
	Principal string `json:"principalId"`
	Role      string `json:"role"`
}

// AccessTransitionGrantIntent selects actions for one exact resource. Plan
// emits one durable grant per action, matching the serving snapshot contract.
type AccessTransitionGrantIntent struct {
	GrantID      string   `json:"grantId"`
	Name         string   `json:"name,omitempty"`
	Principal    string   `json:"principalId"`
	ResourceID   string   `json:"resourceId"`
	ResourceKind string   `json:"resourceKind"`
	Actions      []string `json:"actions"`
}

// AccessTransitionIntent is the semantic, cycle-free portion of an operator
// request. It can be hashed into a NativeRequest before that request receives
// its maintenance operation identity. Publisher and reviewer principals are
// explicit so subsequent plan/build/publication work cannot choose them later.
type AccessTransitionIntent struct {
	TargetID                    string                        `json:"targetId"`
	Environment                 string                        `json:"environment"`
	ProjectID                   string                        `json:"projectId"`
	ExpectedPolicyRevision      int64                         `json:"expectedPolicyRevision"`
	ExpectedPolicyDigest        string                        `json:"expectedPolicyDigest"`
	ExpectedServingGeneration   string                        `json:"expectedServingGeneration"`
	ExpectedServingPolicyDigest string                        `json:"expectedServingPolicyDigest"`
	PublisherPrincipalID        string                        `json:"publisherPrincipalId"`
	ReviewerPrincipalID         string                        `json:"reviewerPrincipalId"`
	RoleBindings                []AccessTransitionRoleIntent  `json:"roleBindings"`
	Grants                      []AccessTransitionGrantIntent `json:"grants"`
}

// StageAccessTransitionRequest binds semantic intent to a concrete admitted
// maintenance invocation. Apply requires an installation-owned verifier; the
// string fields alone are not proof that maintenance is active.
// AccessTransitionPlan contains only typed authority. Its semantic digest is
// safe to bind into NativeRequest and the maintenance journal.
type AccessTransitionPlan struct {
	TargetID             string
	Environment          string
	ProjectID            graph.ResourceID
	PublisherPrincipalID string
	ReviewerPrincipalID  string
	RoleBindings         []access.RoleBinding
	Grants               []access.AuthorizationGrant
	IntentDigest         string
}

// Plan validates semantic operator intent without reading or changing target
// state. Maintenance invocation identity is intentionally not part of the
// digest, avoiding a NativeRequest identity cycle.
func (r AccessTransitionIntent) Plan() (AccessTransitionPlan, error) {
	projectID, err := graph.NewResourceID(r.ProjectID)
	if err != nil {
		return AccessTransitionPlan{}, err
	}
	if !stableTransitionID(r.TargetID) || !stableTransitionID(r.Environment) || r.ExpectedPolicyRevision < 1 || !canonicalTransitionDigest(r.ExpectedPolicyDigest) || !stableTransitionID(r.ExpectedServingGeneration) || !canonicalTransitionDigest(r.ExpectedServingPolicyDigest) {
		return AccessTransitionPlan{}, errors.New("target, environment, positive expected policy revision, stable serving identity, and canonical policy digests are required")
	}
	if !stableTransitionID(r.PublisherPrincipalID) || !stableTransitionID(r.ReviewerPrincipalID) || r.PublisherPrincipalID == r.ReviewerPrincipalID {
		return AccessTransitionPlan{}, errors.New("distinct explicit publisher and reviewer principals are required")
	}
	if len(r.RoleBindings)+len(r.Grants) == 0 || len(r.RoleBindings)+len(r.Grants) > maxAccessTransitionAssignments {
		return AccessTransitionPlan{}, fmt.Errorf("transition requires between 1 and %d explicit assignments", maxAccessTransitionAssignments)
	}
	plan := AccessTransitionPlan{TargetID: r.TargetID, Environment: r.Environment, ProjectID: projectID, PublisherPrincipalID: r.PublisherPrincipalID, ReviewerPrincipalID: r.ReviewerPrincipalID}
	seenRoles := make(map[string]struct{}, len(r.RoleBindings))
	seenRoleAuthority := make(map[string]struct{}, len(r.RoleBindings))
	for _, intent := range r.RoleBindings {
		if _, duplicate := seenRoles[intent.BindingID]; duplicate {
			return AccessTransitionPlan{}, errors.New("duplicate role binding ID in transition intent")
		}
		seenRoles[intent.BindingID] = struct{}{}
		binding, err := access.NewTypedRoleBinding(
			intent.BindingID,
			intent.Name,
			access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: intent.Principal},
			access.PermissionRole(intent.Role),
			projectID,
		)
		if err != nil {
			return AccessTransitionPlan{}, fmt.Errorf("typed role binding %q: %w", intent.BindingID, err)
		}
		roleKey := intent.Principal + "\x00" + intent.Role
		if _, duplicate := seenRoleAuthority[roleKey]; duplicate {
			return AccessTransitionPlan{}, errors.New("duplicate principal role in transition intent")
		}
		seenRoleAuthority[roleKey] = struct{}{}
		plan.RoleBindings = append(plan.RoleBindings, binding)
	}
	seenGrants := make(map[string]struct{}, len(r.Grants))
	seenPersistedGrants := make(map[string]struct{}, len(r.Grants))
	seenGrantAuthority := make(map[string]struct{}, len(r.Grants))
	for _, intent := range r.Grants {
		if _, duplicate := seenGrants[intent.GrantID]; duplicate {
			return AccessTransitionPlan{}, errors.New("duplicate grant ID in transition intent")
		}
		seenGrants[intent.GrantID] = struct{}{}
		resource, err := access.NewResourceRef(graph.ResourceID(intent.ResourceID), graph.Kind(intent.ResourceKind))
		if err != nil {
			return AccessTransitionPlan{}, fmt.Errorf("grant %q resource: %w", intent.GrantID, err)
		}
		if len(intent.Actions) == 0 {
			return AccessTransitionPlan{}, fmt.Errorf("grant %q requires at least one exact action", intent.GrantID)
		}
		if len(intent.Actions) > 32 {
			return AccessTransitionPlan{}, fmt.Errorf("grant %q exceeds the action limit", intent.GrantID)
		}
		pairs := make([]access.PermissionPair, 0, len(intent.Actions))
		seenActions := make(map[access.Action]struct{}, len(intent.Actions))
		for _, value := range intent.Actions {
			action := access.Action(value)
			if _, duplicate := seenActions[action]; duplicate {
				return AccessTransitionPlan{}, fmt.Errorf("grant %q repeats action %q", intent.GrantID, action)
			}
			seenActions[action] = struct{}{}
			definition, ok := access.Permission(action)
			if !ok || definition.Scope != access.PermissionScopeResource || (!definition.Delegable && !(action == access.ActionConnectionManage && resource.Kind() == "connection" && intent.Principal == r.PublisherPrincipalID)) {
				return AccessTransitionPlan{}, fmt.Errorf("grant %q requires an allowed exact-resource transition action", intent.GrantID)
			}
			pair, err := access.NewExactPermissionPair(action, projectID, resource)
			if err != nil {
				return AccessTransitionPlan{}, fmt.Errorf("grant %q action %q: %w", intent.GrantID, action, err)
			}
			pairs = append(pairs, pair)
		}
		grant := access.AuthorizationGrant{
			ID: intent.GrantID, Name: intent.Name,
			Subject:  access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: intent.Principal},
			Resource: resource, PermissionProfile: access.PermissionCatalogProfile,
			Permissions: pairs,
		}
		if err := access.ValidateAuthorizationGrantForScope(grant, access.AuthorizationPolicyScope{ProjectID: projectID.String()}); err != nil {
			return AccessTransitionPlan{}, fmt.Errorf("grant %q: %w", intent.GrantID, err)
		}
		// Serving snapshots store one exact permission pair per grant row.
		// Normalize before policy capture so policy and snapshot identities agree.
		for _, pair := range pairs {
			pairKey := intent.Principal + "\x00" + string(pair.Action) + "\x00" + intent.ResourceKind + "\x00" + intent.ResourceID
			if _, duplicate := seenGrantAuthority[pairKey]; duplicate {
				return AccessTransitionPlan{}, errors.New("duplicate principal permission in transition intent")
			}
			seenGrantAuthority[pairKey] = struct{}{}
			stored := grant
			stored.Permissions = []access.PermissionPair{pair}
			if len(pairs) > 1 {
				identity := projectID.String() + "\x00" + intent.GrantID + "\x00" + string(pair.Action)
				digest := sha256.Sum256([]byte(identity))
				stored.ID = "transition-grant:" + hex.EncodeToString(digest[:])
			}
			if _, duplicate := seenPersistedGrants[stored.ID]; duplicate {
				return AccessTransitionPlan{}, errors.New("duplicate persisted grant ID in transition intent")
			}
			seenPersistedGrants[stored.ID] = struct{}{}
			plan.Grants = append(plan.Grants, stored)
			if len(plan.RoleBindings)+len(plan.Grants) > maxAccessTransitionAssignments {
				return AccessTransitionPlan{}, errors.New("expanded transition assignments exceed the limit")
			}
		}
	}
	sort.Slice(plan.RoleBindings, func(i, j int) bool { return plan.RoleBindings[i].ID < plan.RoleBindings[j].ID })
	sort.Slice(plan.Grants, func(i, j int) bool { return plan.Grants[i].ID < plan.Grants[j].ID })
	canonical := struct {
		TargetID             string                        `json:"targetId"`
		Environment          string                        `json:"environment"`
		ProjectID            string                        `json:"projectId"`
		ExpectedRevision     int64                         `json:"expectedPolicyRevision"`
		ExpectedPolicyDigest string                        `json:"expectedPolicyDigest"`
		ServingGeneration    string                        `json:"expectedServingGeneration"`
		ServingPolicyDigest  string                        `json:"expectedServingPolicyDigest"`
		PublisherPrincipalID string                        `json:"publisherPrincipalId"`
		ReviewerPrincipalID  string                        `json:"reviewerPrincipalId"`
		RoleBindings         []AccessTransitionRoleIntent  `json:"roleBindings"`
		Grants               []AccessTransitionGrantIntent `json:"grants"`
	}{
		TargetID: r.TargetID, Environment: r.Environment,
		ProjectID: projectID.String(), ExpectedRevision: r.ExpectedPolicyRevision,
		ExpectedPolicyDigest: r.ExpectedPolicyDigest, ServingGeneration: r.ExpectedServingGeneration,
		ServingPolicyDigest:  r.ExpectedServingPolicyDigest,
		PublisherPrincipalID: r.PublisherPrincipalID, ReviewerPrincipalID: r.ReviewerPrincipalID,
		RoleBindings: append([]AccessTransitionRoleIntent(nil), r.RoleBindings...),
		Grants:       cloneAccessTransitionGrants(r.Grants),
	}
	sort.Slice(canonical.RoleBindings, func(i, j int) bool { return canonical.RoleBindings[i].BindingID < canonical.RoleBindings[j].BindingID })
	sort.Slice(canonical.Grants, func(i, j int) bool { return canonical.Grants[i].GrantID < canonical.Grants[j].GrantID })
	for i := range canonical.Grants {
		sort.Strings(canonical.Grants[i].Actions)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return AccessTransitionPlan{}, err
	}
	digest := sha256.Sum256(append([]byte("leapview/access-transition/v1\n"), encoded...))
	plan.IntentDigest = "sha256:" + hex.EncodeToString(digest[:])
	return plan, nil
}

func cloneAccessTransitionGrants(grants []AccessTransitionGrantIntent) []AccessTransitionGrantIntent {
	if grants == nil {
		return nil
	}
	cloned := make([]AccessTransitionGrantIntent, len(grants))
	for i, grant := range grants {
		cloned[i] = grant
		cloned[i].Actions = append([]string(nil), grant.Actions...)
	}
	return cloned
}

func stableTransitionID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 200 && !strings.ContainsAny(value, "\x00\r\n")
}

func canonicalTransitionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, ch := range value[len("sha256:"):] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}
