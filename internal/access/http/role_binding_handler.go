package http

import (
	"errors"
	"fmt"
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
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
	if h.AuthorizationPolicyTargetID == "" || h.AuthorizationPolicyEnvironment == "" {
		return access.AuthorizationPolicyScope{}, fmt.Errorf("%w: target and environment are not configured", access.ErrAuthorizationPolicyInvalidScope)
	}
	scope := access.AuthorizationPolicyScope{
		TargetID: h.AuthorizationPolicyTargetID, ProjectID: chi.URLParam(r, "project"),
		Environment: h.AuthorizationPolicyEnvironment,
	}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicyScope{}, err
	}
	return scope, nil
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
	var policy access.AuthorizationPolicy
	operation := accessgen.GenCommandOperationCreateProjectRoleBinding()
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		writer, ok := tx.(access.AuthorizationPolicyWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional authorization policy writer is unavailable")
		}
		envelopes, ok := tx.(access.CurrentGrantAdminEnvelopeReader)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional grant administration authority is unavailable")
		}
		actorID := h.currentPrincipalID(r)
		envelope, envelopeErr := envelopes.CurrentGrantAdminEnvelopeForMutation(r.Context(), input.GrantAdminEnvelopeID, actorID)
		if envelopeErr != nil {
			return access.AuditEventInput{}, envelopeErr
		}
		if envelopeErr := access.ValidateGrantAdminEnvelopeRoleBinding(envelope, actorID, projectgraph.ResourceID(scope.ProjectID), binding.Subject, binding.PermissionRole, binding.Permissions); envelopeErr != nil {
			return access.AuditEventInput{}, envelopeErr
		}
		policy, err = writer.UpsertAuthorizationRoleBinding(r.Context(), access.AuthorizationRoleBindingInput{
			Scope: scope, Binding: binding, ExpectedRevision: *input.ExpectedRevision,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, encodeErr := accessgen.EncodeGenCreateProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{
			TargetId: scope.TargetID, ProjectId: scope.ProjectID, Environment: scope.Environment,
			BindingId: binding.ID, SubjectType: string(binding.Subject.Kind), SubjectId: binding.Subject.ID,
			Role: string(binding.PermissionRole), PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "role_binding.created", h.currentPrincipalID(r), "role_binding", binding.ID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
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
	var policy access.AuthorizationPolicy
	operation := accessgen.GenCommandOperationDeleteProjectRoleBinding()
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		reader, readOK := tx.(access.AuthorizationPolicyReader)
		writer, writeOK := tx.(access.AuthorizationPolicyWriter)
		if !readOK || !writeOK {
			return access.AuditEventInput{}, errors.New("transactional authorization policy reader/writer is unavailable")
		}
		// Read the caller-fenced immutable revision, not the mutable head. This
		// preserves the removed binding's audit fields for an exact idempotent
		// replay after the head has already advanced.
		current, readErr := reader.AuthorizationPolicyRevision(r.Context(), scope, *input.ExpectedRevision)
		if readErr != nil {
			return access.AuditEventInput{}, readErr
		}
		var removed access.RoleBinding
		found := false
		for _, row := range current.RoleBindings {
			if row.ID == bindingID {
				removed, found = row, true
				break
			}
		}
		if !found {
			return access.AuditEventInput{}, fmt.Errorf("%w: role binding %q", access.ErrAuthorizationPolicyNotFound, bindingID)
		}
		policy, err = writer.RemoveAuthorizationRoleBinding(r.Context(), access.AuthorizationRoleBindingDeleteInput{
			Scope: scope, BindingID: bindingID, ExpectedRevision: *input.ExpectedRevision,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		role := string(removed.PermissionRole)
		if role == "" {
			role = string(removed.Role)
		}
		metadata, encodeErr := accessgen.EncodeGenDeleteProjectRoleBindingAuditPayload(accessgen.GenSchemaRoleBindingAuditPayload{
			TargetId: scope.TargetID, ProjectId: scope.ProjectID, Environment: scope.Environment,
			BindingId: removed.ID, SubjectType: string(removed.Subject.Kind), SubjectId: removed.Subject.ID,
			Role: role, PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "role_binding.removed", h.currentPrincipalID(r), "role_binding", removed.ID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
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
