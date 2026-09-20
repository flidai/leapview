package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

const maxResourceShareTTL = 365 * 24 * time.Hour

type resourceShareGrantIssueRequest struct {
	ResourceUID   string                  `json:"resourceUid"`
	ResourceKind  projectgraph.Kind       `json:"resourceKind"`
	ResourceID    projectgraph.ResourceID `json:"resourceId"`
	RecipientType string                  `json:"recipientType"`
	RecipientID   string                  `json:"recipientId"`
	Permissions   []access.PermissionPair `json:"permissions"`
	TTLSeconds    *int64                  `json:"ttlSeconds,omitempty"`
}

type resourceShareGrantRevokeRequest struct {
	Reason string `json:"reason,omitempty"`
}

var durableGrantAuditActions = map[string]string{
	"issueResourceShareGrant":  "resource_share_grant.issued",
	"revokeResourceShareGrant": "resource_share_grant.revoked",
	"issueGrantAdminEnvelope":  "grant_admin_envelope.issued",
	"revokeGrantAdminEnvelope": "grant_admin_envelope.revoked",
}

// executeDurableGrantCommand completes the generated command guard around a
// service-owned mutation. DurableGrantService delegates to PostgreSQL methods
// that already atomically mutate and append their audit event; the executor
// callback therefore observes that completed mutation without opening a
// second repository transaction around it.
func executeDurableGrantCommand(r *stdhttp.Request, operation accessgen.GenCommandOperationID, mutation func() error) error {
	if _, generated := apigencommand.OperationID(r.Context()); !generated {
		return mutation()
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operation.APIGenOperationID(), apigencommand.Execution{
		Transactional: func(_ context.Context, contract apigencommand.Contract) error {
			if expected := durableGrantAuditActions[operation.APIGenOperationID()]; expected != "" && contract.AuditAction != expected {
				return fmt.Errorf("generated audit action %q does not match durable grant action %q", contract.AuditAction, expected)
			}
			return mutation()
		},
	})
}

func (h Handler) IssueResourceShareGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input resourceShareGrantIssueRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeResourceShareError(w, err)
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	recipient, err := access.NewSubjectRef(access.SubjectKind(input.RecipientType), input.RecipientID)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	request := access.ResourceShareGrantRequest{
		Target: access.DurableGrantTarget{
			InstanceID: h.DurableGrantInstanceID, ProjectID: projectID,
			ResourceUID: input.ResourceUID, ResourceID: input.ResourceID, ResourceKind: input.ResourceKind,
		},
		Recipient: recipient, Permissions: access.ClonePermissionPairs(input.Permissions),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
	if input.TTLSeconds != nil {
		if *input.TTLSeconds < 0 || *input.TTLSeconds > int64(maxResourceShareTTL/time.Second) {
			writeJSONError(w, errors.New("ttlSeconds is outside the permitted expiry bounds"), stdhttp.StatusBadRequest)
			return
		}
		request.TTL = time.Duration(*input.TTLSeconds) * time.Second
	}
	if h.DurableGrantService == nil {
		writeJSONError(w, errors.New("durable grant service is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	service, err := h.DurableGrantService(r)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	var grant access.ResourceShareGrant
	if err := executeDurableGrantCommand(r, accessgen.GenCommandOperationIssueResourceShareGrant(), func() error {
		var err error
		grant, err = service.IssueResourceShare(r.Context(), request)
		return err
	}); err != nil {
		writeResourceShareError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, resourceShareGrantDTO(grant))
}

func (h Handler) RevokeResourceShareGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input resourceShareGrantRevokeRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		writeResourceShareError(w, err)
		return
	}
	if h.DurableGrantService == nil {
		writeJSONError(w, errors.New("durable grant service is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	repository, err := h.repository()
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	reader, ok := repository.(interface {
		ResourceShareGrant(context.Context, string) (access.ResourceShareGrant, error)
	})
	if !ok {
		writeJSONError(w, errors.New("durable grant reader is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "grant")
	grant, err := reader.ResourceShareGrant(r.Context(), id)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	if principalID := h.currentPrincipalID(r); principalID == "" || principalID != grant.Issuer.PrincipalID {
		writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
		return
	}
	service, err := h.DurableGrantService(r)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	if err := executeDurableGrantCommand(r, accessgen.GenCommandOperationRevokeResourceShareGrant(), func() error {
		return service.RevokeResourceShareGrantForTarget(r.Context(), id, input.Reason, grant.Target)
	}); err != nil {
		writeResourceShareError(w, err)
		return
	}
	grant, err = reader.ResourceShareGrant(r.Context(), id)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, resourceShareGrantDTO(grant))
}

func resourceShareGrantDTO(grant access.ResourceShareGrant) map[string]any {
	response := map[string]any{
		"id": grant.ID, "profile": grant.Profile, "instanceId": grant.Target.InstanceID,
		"projectId": grant.Target.ProjectID, "resourceUid": grant.Target.ResourceUID,
		"resourceKind": grant.Target.ResourceKind, "resourceId": grant.Target.ResourceID,
		"recipientType": grant.Recipient.Kind, "recipientId": grant.Recipient.ID,
		"permissions": access.ClonePermissionPairs(grant.Permissions), "issuedAt": grant.IssuedAt.UTC(),
		"fingerprint": grant.Fingerprint, "idempotencyKey": grant.IdempotencyKey, "requestDigest": grant.RequestDigest,
	}
	if !grant.ExpiresAt.IsZero() {
		response["expiresAt"] = grant.ExpiresAt.UTC()
	}
	if !grant.RevokedAt.IsZero() {
		response["revokedAt"] = grant.RevokedAt.UTC()
	}
	if grant.RevokedByPrincipalID != "" {
		response["revokedByPrincipalId"] = grant.RevokedByPrincipalID
	}
	if grant.RevocationReason != "" {
		response["revocationReason"] = grant.RevocationReason
	}
	return response
}

func writeResourceShareError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, access.ErrInvalidDurableGrant), errors.Is(err, access.ErrGrantRevokeInvalid), errors.Is(err, access.ErrGrantNoOnwardDelegation), errors.Is(err, access.ErrGrantResourceUIDMismatch), errors.Is(err, access.ErrGrantPermissionCeiling), errors.Is(err, access.ErrGrantCredentialInvalid), errors.Is(err, access.ErrTokenPermissionAttenuationNeeded):
		status = stdhttp.StatusBadRequest
	case errors.Is(err, access.ErrGrantIdempotencyConflict):
		status = stdhttp.StatusConflict
	case errors.Is(err, access.ErrGrantNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrGrantRevoked), errors.Is(err, access.ErrGrantExpired), errors.Is(err, access.ErrGrantPrincipalInactive), errors.Is(err, access.ErrGrantResourceInactive):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrGrantAuthorityUnavailable):
		status = stdhttp.StatusServiceUnavailable
	case errors.Is(err, access.ErrForbidden), errors.Is(err, access.ErrGrantAdminEnvelopeMismatch), errors.Is(err, access.ErrGrantAuthorityInvalid):
		status = stdhttp.StatusForbidden
	}
	writeJSONError(w, err, status)
}
