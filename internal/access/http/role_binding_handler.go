package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/access/rolebindings"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// roleBindingCreateRequest is deliberately narrower than access.RoleBinding:
// permission pairs and legacy capabilities are never accepted from an HTTP
// caller. The versioned permission role is expanded by the target authority
// immediately before the repository command is invoked.
type roleBindingCreateRequest struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	SubjectType          string `json:"subjectType"`
	SubjectID            string `json:"subjectId"`
	Role                 string `json:"role"`
	GrantAdminEnvelopeID string `json:"grantAdminEnvelopeId"`
	ExpectedRevision     *int64 `json:"expectedRevision"`
}

type roleBindingDeleteRequest struct {
	ExpectedRevision *int64 `json:"expectedRevision"`
}

func (h Handler) authorizationPolicyScope(r *stdhttp.Request) (access.AuthorizationPolicyScope, error) {
	return h.authorizationPolicyScopeForProject(chi.URLParam(r, "project"))
}

func (h Handler) authorizationPolicyScopeForProject(projectID string) (access.AuthorizationPolicyScope, error) {
	if h.AuthorizationPolicyTargetID == "" || h.AuthorizationPolicyEnvironment == "" {
		return access.AuthorizationPolicyScope{}, fmt.Errorf("%w: target and environment are not configured", access.ErrAuthorizationPolicyInvalidScope)
	}
	scope := access.AuthorizationPolicyScope{
		TargetID: h.AuthorizationPolicyTargetID, ProjectID: projectID,
		Environment: h.AuthorizationPolicyEnvironment,
	}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicyScope{}, err
	}
	return scope, nil
}

// RoleBindingAdministration returns the current target-owned role policy and
// the versioned role catalog for product administration. The current project
// is server-bound; callers cannot select another policy namespace.
func (h Handler) RoleBindingAdministration(ctx context.Context) (access.RoleBindingAdministrationState, error) {
	if h.CurrentProjectID == nil {
		return access.RoleBindingAdministrationState{}, errors.New("active project identity is unavailable")
	}
	projectID, err := h.CurrentProjectID(ctx)
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	scope, err := h.authorizationPolicyScopeForProject(projectID.String())
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	repository, err := h.repository()
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	reader, ok := repository.(access.AuthorizationPolicyReader)
	if !ok {
		return access.RoleBindingAdministrationState{}, errors.New("authorization policy reader is unavailable")
	}
	policy, err := reader.AuthorizationPolicy(ctx, scope)
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	return access.RoleBindingAdministrationState{
		Scope: scope, Revision: policy.Revision, Digest: policy.Digest,
		RoleBindings: append([]access.RoleBinding(nil), policy.RoleBindings...),
		RolePresets:  access.PermissionRolePresets(),
	}, nil
}

// ApplyRoleBindingAdministration performs the same access-owned authorization,
// envelope, CAS, persistence, and audit checks as the public role-binding API.
// It exists so the browser command loop does not need bearer-only API access.
func (h Handler) ApplyRoleBindingAdministration(r *stdhttp.Request, command access.RoleBindingAdministrationCommand) (access.RoleBindingAdministrationState, error) {
	if r == nil || h.CurrentProjectID == nil {
		return access.RoleBindingAdministrationState{}, errors.New("role administration is unavailable")
	}
	projectID, err := h.CurrentProjectID(r.Context())
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	scope, err := h.authorizationPolicyScopeForProject(projectID.String())
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	repository, err := h.repository()
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	if idempotencyKey == "" {
		return access.RoleBindingAdministrationState{}, errors.New("idempotency key is required")
	}
	ports := h.roleBindingAdministrationPorts(r)
	mutation := access.RoleBindingAdministrationMutation{
		Scope: scope, ExpectedRevision: command.ExpectedRevision, IdempotencyKey: idempotencyKey,
		ActorID: h.currentPrincipalID(r), RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r),
	}
	var operation accessgen.GenCommandOperationID
	switch strings.TrimSpace(command.Action) {
	case string(access.RoleBindingAdministrationGrant):
		bindingID := strings.TrimSpace(command.BindingID)
		if bindingID == "" {
			bindingID = "role-binding-" + idempotencyKey
		}
		binding, err := access.NewTypedRoleBinding(bindingID, string(command.Role), command.Subject, command.Role, projectID)
		if err != nil {
			return access.RoleBindingAdministrationState{}, err
		}
		mutation.Action = access.RoleBindingAdministrationGrant
		mutation.Binding = binding
		mutation.IssueAdminEnvelope = true
		operation = accessgen.GenCommandOperationCreateProjectRoleBinding()
	case string(access.RoleBindingAdministrationRevoke):
		mutation.Action = access.RoleBindingAdministrationRevoke
		mutation.BindingID = command.BindingID
		operation = accessgen.GenCommandOperationDeleteProjectRoleBinding()
	default:
		return access.RoleBindingAdministrationState{}, errors.New("unknown role administration action")
	}
	_, err = rolebindings.Apply(
		r.Context(), repository, mutation, ports,
		func(mutate func(access.Repository) (access.AuditEventInput, error)) error {
			return executeAuditedMutation(r, repository, operation, mutate)
		},
	)
	if err != nil {
		return access.RoleBindingAdministrationState{}, err
	}
	return h.RoleBindingAdministration(r.Context())
}

type roleBindingEnvelopeIssuerFunc func(context.Context, access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error)

func (f roleBindingEnvelopeIssuerFunc) IssueGrantAdminEnvelope(ctx context.Context, request access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error) {
	return f(ctx, request)
}

func (h Handler) roleBindingAdministrationPorts(r *stdhttp.Request) access.RoleBindingAdministrationPorts {
	return access.RoleBindingAdministrationPorts{
		EnvelopeIssuer: roleBindingEnvelopeIssuerFunc(func(ctx context.Context, input access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error) {
			if h.DurableGrantService == nil {
				return access.GrantAdminEnvelope{}, errors.New("durable grant service is unavailable")
			}
			service, err := h.DurableGrantService(r)
			if err != nil {
				return access.GrantAdminEnvelope{}, err
			}
			return service.IssueGrantAdminEnvelope(ctx, input)
		}),
		ResolveCurrentAuthority: func(ctx context.Context, actorID string) (access.RoleBindingAdministrationAuthority, error) {
			if h.CurrentEffectivePermissionOptions == nil {
				return access.RoleBindingAdministrationAuthority{}, access.ErrForbidden
			}
			permissions, err := h.CurrentEffectivePermissionOptions(ctx, actorID)
			if err != nil {
				return access.RoleBindingAdministrationAuthority{}, err
			}
			authority := access.RoleBindingAdministrationAuthority{ActorID: h.currentPrincipalID(r), Permissions: permissions}
			if credential, found := h.currentCredential(r); found {
				authority.Credential = access.RoleBindingAdministrationCredential{
					Class: access.GrantCredentialClassAPIToken, ID: credential.Token.ID,
					Fingerprint: credential.Token.TokenFingerprint, PrincipalID: credential.Token.PrincipalID,
					AuthenticatedPrincipalID: credential.Principal.ID,
					PermissionProfile:        credential.Token.PermissionProfile, TokenPermissions: credential.Token.Permissions,
				}
				return authority, nil
			}
			if h.CurrentSession == nil {
				return access.RoleBindingAdministrationAuthority{}, access.ErrForbidden
			}
			sessionID, found := h.CurrentSession(r)
			if !found || sessionID == "" {
				return access.RoleBindingAdministrationAuthority{}, access.ErrForbidden
			}
			authority.Credential = access.RoleBindingAdministrationCredential{Class: access.GrantCredentialClassSession, ID: sessionID, PrincipalID: actorID}
			return authority, nil
		},
		AuthorizeClaimBootstrap: func(_ context.Context, scope access.AuthorizationPolicyScope, binding access.RoleBinding, actorID string) (bool, error) {
			if h.AuthorizeClaimBootstrapBinding == nil {
				return false, nil
			}
			return h.AuthorizeClaimBootstrapBinding(r, scope, binding, actorID)
		},
	}
}

func roleBindingDTO(binding access.RoleBinding, policy access.AuthorizationPolicy) map[string]any {
	if binding.TypedRoleBinding() {
		return map[string]any{
			"id": binding.ID, "name": binding.Name,
			"subjectType": string(binding.Subject.Kind), "subjectId": binding.Subject.ID,
			"role": string(binding.PermissionRole), "permissionProfile": binding.PermissionProfile,
			"permissions":    access.ClonePermissionPairs(binding.Permissions),
			"policyRevision": policy.Revision, "policyDigest": policy.Digest,
		}
	}
	capabilities := make([]string, 0, len(binding.Capabilities))
	for _, capability := range binding.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	return map[string]any{
		"id": binding.ID, "name": binding.Name,
		"subjectType": string(binding.Subject.Kind), "subjectId": binding.Subject.ID,
		"role": string(binding.Role), "capabilities": capabilities,
		"policyRevision": policy.Revision, "policyDigest": policy.Digest,
	}
}

func roleBindingPolicyMetadata(scope access.AuthorizationPolicyScope, policy access.AuthorizationPolicy) map[string]any {
	return map[string]any{
		"targetId": scope.TargetID, "projectId": scope.ProjectID, "environment": scope.Environment,
		"policyRevision": policy.Revision, "policyDigest": policy.Digest,
	}
}

func validateProjectRoleBindingPermissionCeiling(ceiling, requested []access.PermissionPair) error {
	return rolebindings.ValidatePermissionCeiling(ceiling, requested)
}

func (h Handler) ListProjectRoleBindings(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(access.AuthorizationPolicyReader)
	if !ok {
		writeJSONError(w, errors.New("authorization policy reader is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	scope, err := h.authorizationPolicyScope(r)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	policy, err := reader.AuthorizationPolicy(r.Context(), scope)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(policy.RoleBindings))
	for _, binding := range policy.RoleBindings {
		items = append(items, roleBindingDTO(binding, policy))
	}
	page, next, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	response := roleBindingPolicyMetadata(scope, policy)
	response["items"] = page
	response["page"] = map[string]any{"nextCursor": next}
	writeJSON(w, stdhttp.StatusOK, response)
}

func (h Handler) CreateProjectRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input roleBindingCreateRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.ExpectedRevision == nil {
		writeJSONError(w, errors.New("expectedRevision is required"), stdhttp.StatusBadRequest)
		return
	}
	scope, err := h.authorizationPolicyScope(r)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	subject, err := access.NewSubjectRef(access.SubjectKind(input.SubjectType), input.SubjectID)
	if err != nil {
		writeAuthorizationPolicyError(w, fmt.Errorf("%w: subject: %w", access.ErrAuthorizationPolicyInvalidBinding, err))
		return
	}
	binding, err := access.NewTypedRoleBinding(input.ID, input.Name, subject, access.PermissionRole(input.Role), projectgraph.ResourceID(scope.ProjectID))
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if _, ok := repo.(access.AuthorizationPolicyWriter); !ok {
		writeJSONError(w, errors.New("authorization policy writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	ports := h.roleBindingAdministrationPorts(r)
	policy, err := rolebindings.Apply(r.Context(), repo, access.RoleBindingAdministrationMutation{
		Scope: scope, Action: access.RoleBindingAdministrationGrant, Binding: binding,
		GrantAdminEnvelopeID: input.GrantAdminEnvelopeID, ExpectedRevision: *input.ExpectedRevision,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), ActorID: h.currentPrincipalID(r),
		RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r),
	}, ports, func(mutate func(access.Repository) (access.AuditEventInput, error)) error {
		return executeAuditedMutation(r, repo, accessgen.GenCommandOperationCreateProjectRoleBinding(), mutate)
	})
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	for _, row := range policy.RoleBindings {
		if row.ID == binding.ID {
			writeJSON(w, stdhttp.StatusCreated, roleBindingDTO(row, policy))
			return
		}
	}
	writeJSONError(w, errors.New("created authorization role binding is missing from policy"), stdhttp.StatusInternalServerError)
}

func (h Handler) DeleteProjectRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input roleBindingDeleteRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.ExpectedRevision == nil {
		writeJSONError(w, errors.New("expectedRevision is required"), stdhttp.StatusBadRequest)
		return
	}
	bindingID := chi.URLParam(r, "binding")
	if err := access.ValidateAuthorizationRoleBindingID(bindingID); err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	scope, err := h.authorizationPolicyScope(r)
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if _, ok := repo.(access.AuthorizationPolicyWriter); !ok {
		writeJSONError(w, errors.New("authorization policy writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	ports := h.roleBindingAdministrationPorts(r)
	policy, err := rolebindings.Apply(r.Context(), repo, access.RoleBindingAdministrationMutation{
		Scope: scope, Action: access.RoleBindingAdministrationRevoke, BindingID: bindingID,
		ExpectedRevision: *input.ExpectedRevision, IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ActorID: h.currentPrincipalID(r), RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r),
	}, ports, func(mutate func(access.Repository) (access.AuditEventInput, error)) error {
		return executeAuditedMutation(r, repo, accessgen.GenCommandOperationDeleteProjectRoleBinding(), mutate)
	})
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, roleBindingPolicyMetadata(scope, policy))
}

func writeAuthorizationPolicyError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, access.ErrAuthorizationPolicyInvalidBinding):
		status = stdhttp.StatusBadRequest
	case errors.Is(err, access.ErrAuthorizationPolicyInvalidScope):
		status = stdhttp.StatusServiceUnavailable
	case errors.Is(err, access.ErrAuthorizationPolicyNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrGrantNotFound), errors.Is(err, access.ErrGrantRevoked), errors.Is(err, access.ErrGrantExpired), errors.Is(err, access.ErrGrantPrincipalInactive), errors.Is(err, access.ErrGrantAdminEnvelopeMismatch):
		status = stdhttp.StatusForbidden
	case errors.Is(err, access.ErrAuthorizationPolicyConflict), errors.Is(err, access.ErrAuthorizationPolicyStaleRevision), errors.Is(err, access.ErrAuthorizationPolicyIdempotency):
		status = stdhttp.StatusConflict
	}
	writeJSONError(w, err, status)
}
