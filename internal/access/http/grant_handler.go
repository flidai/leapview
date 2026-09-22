package http

import (
	"errors"
	"fmt"
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	protocolgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	"github.com/flidai/leapview/internal/project/graph"
)

type grantCreateRequest struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ResourceKind     string `json:"resourceKind"`
	ResourceID       string `json:"resourceId"`
	SubjectType      string `json:"subjectType"`
	SubjectID        string `json:"subjectId"`
	Capability       string `json:"capability"`
	ExpectedRevision *int64 `json:"expectedRevision"`
}

func grantDTO(g access.AuthorizationGrant, p access.AuthorizationPolicy) map[string]any {
	return map[string]any{"id": g.ID, "name": g.Name, "resourceId": g.Resource.ID(), "resourceKind": g.Resource.Kind(), "subjectType": g.Subject.Kind, "subjectId": g.Subject.ID, "capability": g.Capability, "policyRevision": p.Revision, "policyDigest": p.Digest}
}
func (h Handler) ListGrants(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeAuthorizationPolicyError(w, err)
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
	items := make([]map[string]any, 0, len(policy.Grants))
	for _, grant := range policy.Grants {
		if kind := r.URL.Query().Get("resourceKind"); kind != "" && kind != string(grant.Resource.Kind()) {
			continue
		}
		if id := r.URL.Query().Get("resourceId"); id != "" && id != string(grant.Resource.ID()) {
			continue
		}
		items = append(items, grantDTO(grant, policy))
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
func (h Handler) CreateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input grantCreateRequest
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
	resource, err := access.NewResourceRef(graph.ResourceID(input.ResourceID), graph.Kind(input.ResourceKind))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	grant := access.AuthorizationGrant{ID: input.ID, Name: input.Name, Subject: access.SubjectRef{Kind: access.SubjectKind(input.SubjectType), ID: input.SubjectID}, Resource: resource, Capability: access.Capability(input.Capability)}
	if err := access.ValidateAuthorizationGrant(grant); err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	var policy access.AuthorizationPolicy
	operation := accessgen.GenCommandOperationCreateGrant()
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		writer, ok := tx.(access.AuthorizationGrantWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional authorization grant writer is unavailable")
		}
		policy, err = writer.UpsertAuthorizationGrant(r.Context(), access.AuthorizationGrantInput{Scope: scope, Grant: grant, ExpectedRevision: *input.ExpectedRevision, IdempotencyKey: r.Header.Get("Idempotency-Key")})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, err := accessgen.EncodeGenCreateGrantAuditPayload(accessgen.GenSchemaGrantAuditPayload{OperationId: "createGrant", ResourceId: input.ResourceID, ResourceKind: protocolgen.ResourceKind(input.ResourceKind), Environment: scope.Environment, SubjectId: input.SubjectID, SubjectType: input.SubjectType, Capability: protocolgen.Capability(input.Capability), Surface: "api"})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		event := auditInput(r, "grant.created", h.currentPrincipalID(r), "grant", grant.ID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		writeAuthorizationPolicyError(w, err)
		return
	}
	for _, g := range policy.Grants {
		if g.ID == grant.ID {
			writeJSON(w, stdhttp.StatusCreated, grantDTO(g, policy))
			return
		}
	}
	writeJSONError(w, fmt.Errorf("created grant is missing from policy"), stdhttp.StatusInternalServerError)
}
