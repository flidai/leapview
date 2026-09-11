package http

import (
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

// roleBindingCreateRequest is deliberately narrower than access.RoleBinding:
// capabilities are never accepted from an HTTP caller. The role is expanded
// using the immutable canonical role table immediately before the repository
// command is invoked.
type roleBindingCreateRequest struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SubjectType      string `json:"subjectType"`
	SubjectID        string `json:"subjectId"`
	Role             string `json:"role"`
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
	role, err := access.ParseProjectRole(input.Role)
	if err != nil {
		writeAuthorizationPolicyError(w, fmt.Errorf("%w: role: %w", access.ErrAuthorizationPolicyInvalidBinding, err))
		return
	}
	binding := access.RoleBinding{
		ID: input.ID, Name: input.Name, Subject: subject, Role: role,
		Capabilities: access.ProjectRoleCapabilities(role),
	}
	if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
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
			Role: string(binding.Role), PolicyRevision: policy.Revision, PolicyDigest: policy.Digest,
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
			w.Header().Set("Location", projectRoleBindingLocation(chi.URLParam(r, "project"), row.ID))
			writeJSON(w, stdhttp.StatusCreated, roleBindingDTO(row, policy))
			return
		}
	}
	writeJSONError(w, errors.New("created authorization role binding is missing from policy"), stdhttp.StatusInternalServerError)
}

func projectRoleBindingLocation(project, bindingID string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/role-bindings/" + url.PathEscape(bindingID)
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
	case errors.Is(err, access.ErrAuthorizationPolicyConflict), errors.Is(err, access.ErrAuthorizationPolicyStaleRevision), errors.Is(err, access.ErrAuthorizationPolicyIdempotency):
		status = stdhttp.StatusConflict
	}
	writeJSONError(w, err, status)
}
