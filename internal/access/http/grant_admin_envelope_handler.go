package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

const maxGrantAdminEnvelopeTTL = 365 * 24 * time.Hour

type grantAdminEnvelopeIssueRequest struct {
	Permissions        []access.PermissionPair `json:"permissions"`
	RecipientSelector  string                  `json:"recipientSelector"`
	RoleVersion        string                  `json:"roleVersion"`
	TargetResourceKind projectgraph.Kind       `json:"targetResourceKind,omitempty"`
	TargetResourceID   projectgraph.ResourceID `json:"targetResourceId,omitempty"`
	TTLSeconds         int64                   `json:"ttlSeconds"`
}

type grantAdminEnvelopeRevokeRequest struct {
	Reason string `json:"reason,omitempty"`
}

// IssueGrantAdminEnvelope issues a project-bound administration envelope.
// Issuer, bound principal, credential evidence, and the manage+delegate
// ceiling are deliberately derived by DurableGrantService.
func (h Handler) IssueGrantAdminEnvelope(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body grantAdminEnvelopeIssueRequest
	if err := decodeStrictJSON(r, &body); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if body.TTLSeconds <= 0 || body.TTLSeconds > int64(maxGrantAdminEnvelopeTTL/time.Second) {
		writeJSONError(w, errors.New("ttlSeconds is outside the permitted expiry bounds"), stdhttp.StatusBadRequest)
		return
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
	var grant access.GrantAdminEnvelope
	if err := executeDurableGrantCommand(r, accessgen.GenCommandOperationIssueGrantAdminEnvelope(), func() error {
		var err error
		grant, err = service.IssueGrantAdminEnvelope(r.Context(), access.GrantAdminEnvelopeRequest{
			TargetProjectID:       projectID,
			TargetResourceKind:    body.TargetResourceKind,
			TargetResourceID:      body.TargetResourceID,
			Permissions:           access.ClonePermissionPairs(body.Permissions),
			RecipientSelector:     body.RecipientSelector,
			RoleVersion:           body.RoleVersion,
			TTL:                   time.Duration(body.TTLSeconds) * time.Second,
			IdempotencyKey:        r.Header.Get("Idempotency-Key"),
			AllowOnwardDelegation: false,
		})
		return err
	}); err != nil {
		writeResourceShareError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, grantAdminEnvelopeDTO(grant))
}

// RevokeGrantAdminEnvelope only permits the original issuer/bound principal
// to revoke. The stored target is re-used for the service's current-authority
// check, so a caller cannot turn project authorization into a wildcard.
func (h Handler) RevokeGrantAdminEnvelope(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body grantAdminEnvelopeRevokeRequest
	if err := decodeStrictJSON(r, &body); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
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
		GrantAdminEnvelope(context.Context, string) (access.GrantAdminEnvelope, error)
	})
	if !ok {
		writeJSONError(w, errors.New("durable grant reader is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "envelope")
	grant, err := reader.GrantAdminEnvelope(r.Context(), id)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	if grant.TargetProjectID != projectID {
		writeJSONError(w, access.ErrGrantNotFound, stdhttp.StatusNotFound)
		return
	}
	principalID := h.currentPrincipalID(r)
	if principalID == "" || principalID != grant.BoundPrincipalID || principalID != grant.Issuer.PrincipalID {
		writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
		return
	}
	service, err := h.DurableGrantService(r)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	target := access.DurableGrantTarget{
		ProjectID:    grant.TargetProjectID,
		ResourceKind: grant.TargetResourceKind,
		ResourceID:   grant.TargetResourceID,
	}
	if err := executeDurableGrantCommand(r, accessgen.GenCommandOperationRevokeGrantAdminEnvelope(), func() error {
		return service.RevokeGrantAdminEnvelopeForTarget(r.Context(), id, body.Reason, target)
	}); err != nil {
		writeResourceShareError(w, err)
		return
	}
	grant, err = reader.GrantAdminEnvelope(r.Context(), id)
	if err != nil {
		writeResourceShareError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, grantAdminEnvelopeDTO(grant))
}

func grantAdminEnvelopeDTO(grant access.GrantAdminEnvelope) map[string]any {
	response := map[string]any{
		"id": grant.ID, "profile": grant.Profile, "projectId": grant.TargetProjectID,
		"permissions":       access.ClonePermissionPairs(grant.Permissions),
		"recipientSelector": grant.RecipientSelector, "roleVersion": grant.RoleVersion,
		"issuedAt": grant.IssuedAt.UTC(), "expiresAt": grant.ExpiresAt.UTC(),
		"fingerprint": grant.Fingerprint, "idempotencyKey": grant.IdempotencyKey,
		"requestDigest": grant.RequestDigest,
	}
	if grant.TargetResourceKind != "" {
		response["targetResourceKind"] = grant.TargetResourceKind
		response["targetResourceId"] = grant.TargetResourceID
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
