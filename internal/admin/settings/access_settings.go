package settings

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// AccessSettingsSignal is the small project-bound authorization surface used
// by the administrator UI. Role bindings are direct assignments in the
// target-owned policy; grants deliberately have no controls here because
// grant administration is not part of this surface yet.
type AccessSettingsSignal struct {
	ProjectID                    string                      `json:"projectId"`
	PolicyRevision               int64                       `json:"policyRevision"`
	PolicyDigest                 string                      `json:"policyDigest"`
	RoleBindings                 []AccessSettingsRoleBinding `json:"roleBindings"`
	Roles                        []AccessSettingsRole        `json:"roles"`
	EffectiveAccess              AccessEffectiveAccess       `json:"effectiveAccess"`
	GrantAdministrationAvailable bool                        `json:"grantAdministrationAvailable"`
	GrantAdministrationLabel     string                      `json:"grantAdministrationLabel"`
	Message                      string                      `json:"message,omitempty"`
	Error                        string                      `json:"error,omitempty"`
	Loading                      bool                        `json:"loading"`
}

// EffectiveAccessProvider is backed by the active immutable serving
// generation. The settings surface receives already-resolved identity
// subjects and never reads mutable role or grant rows to explain access.
type EffectiveAccessProvider func(context.Context, string) ([]access.AuthorizationDecision, error)

type AccessEffectiveAccess struct {
	Decisions []AccessEffectiveDecision `json:"decisions"`
	Loading   bool                      `json:"loading"`
	Error     string                    `json:"error,omitempty"`
}

type AccessEffectiveDecision struct {
	Allowed         bool   `json:"allowed"`
	Authority       string `json:"authority"`
	Capability      string `json:"capability"`
	Reason          string `json:"reason"`
	ResourceKind    string `json:"resourceKind"`
	ResourceID      string `json:"resourceId"`
	Inherited       bool   `json:"inherited"`
	Owner           bool   `json:"owner"`
	Platform        bool   `json:"platform"`
	GrantID         string `json:"grantId,omitempty"`
	GrantResourceID string `json:"grantResourceId,omitempty"`
	SubjectType     string `json:"subjectType,omitempty"`
	SubjectID       string `json:"subjectId,omitempty"`
}

type AccessSettingsRoleBinding struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	SubjectType  string   `json:"subjectType"`
	SubjectID    string   `json:"subjectId"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
}

type AccessSettingsRole struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

type AccessSettingsCommand struct {
	Action           string `json:"action"`
	BindingID        string `json:"bindingId,omitempty"`
	BindingName      string `json:"bindingName,omitempty"`
	SubjectType      string `json:"subjectType,omitempty"`
	SubjectID        string `json:"subjectId,omitempty"`
	Role             string `json:"role,omitempty"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
}

type AccessSettingsScope struct {
	TargetID    string
	ProjectID   string
	Environment string
}

const grantAdministrationUnavailableLabel = "Grant administration unavailable in this surface."

func NormalizeAccessSettingsCommand(command AccessSettingsCommand) AccessSettingsCommand {
	command.Action = strings.TrimSpace(command.Action)
	command.BindingID = strings.TrimSpace(command.BindingID)
	command.BindingName = strings.TrimSpace(command.BindingName)
	command.SubjectType = strings.TrimSpace(command.SubjectType)
	command.SubjectID = strings.TrimSpace(command.SubjectID)
	command.Role = strings.TrimSpace(command.Role)
	return command
}

func accessSettingsInitialState(scope AccessSettingsScope) AccessSettingsSignal {
	return AccessSettingsSignal{
		ProjectID: scope.ProjectID, RoleBindings: []AccessSettingsRoleBinding{},
		Roles: []AccessSettingsRole{}, EffectiveAccess: AccessEffectiveAccess{Decisions: []AccessEffectiveDecision{}, Loading: true}, GrantAdministrationAvailable: false,
		GrantAdministrationLabel: grantAdministrationUnavailableLabel,
		Loading:                  true,
	}
}

func accessSettingsScope(scope AccessSettingsScope) (access.AuthorizationPolicyScope, error) {
	result := access.AuthorizationPolicyScope{TargetID: strings.TrimSpace(scope.TargetID), ProjectID: strings.TrimSpace(scope.ProjectID), Environment: strings.TrimSpace(scope.Environment)}
	if err := access.ValidateAuthorizationPolicyScope(result); err != nil {
		return access.AuthorizationPolicyScope{}, err
	}
	return result, nil
}

func LoadAccessSettings(ctx context.Context, repository access.Repository, scope AccessSettingsScope) (AccessSettingsSignal, error) {
	return loadAccessSettings(ctx, repository, scope, "", nil)
}

// LoadAccessSettingsForPrincipal loads the active-project policy and the
// current administrator's effective-access provenance into one signal. The
// principal is supplied by the authenticated browser session; callers cannot
// select a different project or principal through browser query parameters.
func LoadAccessSettingsForPrincipal(ctx context.Context, repository access.Repository, scope AccessSettingsScope, principalID string, provider EffectiveAccessProvider) (AccessSettingsSignal, error) {
	return loadAccessSettings(ctx, repository, scope, strings.TrimSpace(principalID), provider)
}

func loadAccessSettings(ctx context.Context, repository access.Repository, scope AccessSettingsScope, principalID string, provider EffectiveAccessProvider) (AccessSettingsSignal, error) {
	state := accessSettingsInitialState(scope)
	validated, err := accessSettingsScope(scope)
	if err != nil {
		state.Loading = false
		state.EffectiveAccess.Loading = false
		state.EffectiveAccess.Error = "Effective access explanation is unavailable."
		return state, err
	}
	reader, ok := repository.(access.AuthorizationPolicyReader)
	if !ok {
		state.Loading = false
		state.EffectiveAccess.Loading = false
		state.EffectiveAccess.Error = "Effective access explanation is unavailable."
		state.Error = "Project role bindings are unavailable."
		return state, nil
	}
	policy, err := reader.AuthorizationPolicy(ctx, validated)
	if err != nil {
		state.Loading = false
		state.EffectiveAccess.Loading = false
		state.EffectiveAccess.Error = "Effective access explanation is unavailable."
		return state, err
	}
	state.PolicyRevision, state.PolicyDigest = policy.Revision, policy.Digest
	state.RoleBindings = make([]AccessSettingsRoleBinding, 0, len(policy.RoleBindings))
	for _, binding := range policy.RoleBindings {
		state.RoleBindings = append(state.RoleBindings, accessSettingsBindingSignal(binding))
	}
	sort.SliceStable(state.RoleBindings, func(i, j int) bool { return state.RoleBindings[i].ID < state.RoleBindings[j].ID })
	state.Roles = accessSettingsRoleCatalog()
	state.Loading = false
	AttachEffectiveAccess(ctx, &state, principalID, provider)
	return state, nil
}

// AttachEffectiveAccess refreshes only the generation-owned explanation
// portion of a settings signal. It is used after a role-binding mutation so
// the browser never displays stale direct/inherited evidence.
func AttachEffectiveAccess(ctx context.Context, state *AccessSettingsSignal, principalID string, provider EffectiveAccessProvider) {
	if state == nil {
		return
	}
	state.EffectiveAccess = AccessEffectiveAccess{Decisions: []AccessEffectiveDecision{}, Loading: false}
	if provider == nil {
		state.EffectiveAccess.Error = "Effective access explanation is unavailable."
		return
	}
	if strings.TrimSpace(principalID) == "" {
		state.EffectiveAccess.Error = "Effective access explanation requires an authenticated administrator."
		return
	}
	decisions, err := provider(ctx, strings.TrimSpace(principalID))
	if err != nil {
		state.EffectiveAccess.Error = "Effective access explanation is unavailable."
		return
	}
	state.EffectiveAccess.Decisions = make([]AccessEffectiveDecision, 0, len(decisions))
	for _, decision := range decisions {
		state.EffectiveAccess.Decisions = append(state.EffectiveAccess.Decisions, accessEffectiveDecisionSignal(decision))
	}
}

func accessEffectiveDecisionSignal(decision access.AuthorizationDecision) AccessEffectiveDecision {
	authority := "Direct"
	switch {
	case !decision.Allowed:
		authority = "Denied"
	case decision.Platform:
		authority = "Platform"
	case decision.Owner:
		authority = "Owner"
	case strings.Contains(decision.Reason, "group-inherited"):
		authority = "Group-derived"
	case strings.Contains(decision.Reason, "grant"):
		authority = "Compiled"
	}
	return AccessEffectiveDecision{
		Allowed: decision.Allowed, Authority: authority, Capability: string(decision.Capability), Reason: decision.Reason,
		ResourceKind: decision.ResourceKind, ResourceID: decision.ResourceID, Inherited: decision.Inherited,
		Owner: decision.Owner, Platform: decision.Platform, GrantID: decision.GrantID, GrantResourceID: decision.GrantResourceID,
		SubjectType: decision.SubjectType, SubjectID: decision.SubjectID,
	}
}

func accessSettingsRoleCatalog() []AccessSettingsRole {
	roles := make([]AccessSettingsRole, 0, len(access.CanonicalProjectRoles()))
	for _, role := range access.CanonicalProjectRoles() {
		capabilities := access.ProjectRoleCapabilities(role)
		encoded := make([]string, 0, len(capabilities))
		for _, capability := range capabilities {
			encoded = append(encoded, string(capability))
		}
		roles = append(roles, AccessSettingsRole{Name: string(role), Capabilities: encoded})
	}
	return roles
}

func accessSettingsBindingSignal(binding access.RoleBinding) AccessSettingsRoleBinding {
	capabilities := make([]string, 0, len(binding.Capabilities))
	for _, capability := range binding.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	return AccessSettingsRoleBinding{ID: binding.ID, Name: binding.Name, SubjectType: string(binding.Subject.Kind), SubjectID: binding.Subject.ID, Role: string(binding.Role), Capabilities: capabilities}
}

func ApplyAccessSettingsCommand(ctx context.Context, repository access.Repository, scope AccessSettingsScope, command AccessSettingsCommand, idempotencyKey string) (AccessSettingsSignal, error) {
	command = NormalizeAccessSettingsCommand(command)
	validated, err := accessSettingsScope(scope)
	if err != nil {
		return AccessSettingsSignal{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return AccessSettingsSignal{}, errors.New("idempotency key is required")
	}
	writer, ok := repository.(access.AuthorizationPolicyWriter)
	if !ok {
		return AccessSettingsSignal{}, errors.New("project role binding administration is unavailable")
	}
	var policy access.AuthorizationPolicy
	switch command.Action {
	case "create":
		subject, subjectErr := access.NewSubjectRef(access.SubjectKind(command.SubjectType), command.SubjectID)
		if subjectErr != nil {
			return AccessSettingsSignal{}, fmt.Errorf("subject: %w", subjectErr)
		}
		role, roleErr := access.ParseProjectRole(command.Role)
		if roleErr != nil {
			return AccessSettingsSignal{}, roleErr
		}
		binding := access.RoleBinding{ID: command.BindingID, Name: command.BindingName, Subject: subject, Role: role, Capabilities: access.ProjectRoleCapabilities(role)}
		if validateErr := access.ValidateAuthorizationRoleBinding(binding); validateErr != nil {
			return AccessSettingsSignal{}, validateErr
		}
		policy, err = writer.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: validated, Binding: binding, ExpectedRevision: command.ExpectedRevision, IdempotencyKey: idempotencyKey})
	case "delete":
		if command.BindingID == "" {
			return AccessSettingsSignal{}, errors.New("binding id is required")
		}
		policy, err = writer.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: validated, BindingID: command.BindingID, ExpectedRevision: command.ExpectedRevision, IdempotencyKey: idempotencyKey})
	default:
		return AccessSettingsSignal{}, errors.New("unknown project access command")
	}
	if err != nil {
		return AccessSettingsSignal{}, err
	}
	state := accessSettingsInitialState(scope)
	state.PolicyRevision, state.PolicyDigest = policy.Revision, policy.Digest
	state.RoleBindings = make([]AccessSettingsRoleBinding, 0, len(policy.RoleBindings))
	for _, binding := range policy.RoleBindings {
		state.RoleBindings = append(state.RoleBindings, accessSettingsBindingSignal(binding))
	}
	sort.SliceStable(state.RoleBindings, func(i, j int) bool { return state.RoleBindings[i].ID < state.RoleBindings[j].ID })
	state.Roles = accessSettingsRoleCatalog()
	state.Loading = false
	state.Message = "Project role bindings updated."
	return state, nil
}
