package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type projectClaimPublisherAcknowledgeRequest struct {
	ClaimCredentialID string `json:"claimCredentialId"`
}

// ExchangeProjectClaimPublisher rotates the bounded initial publisher token
// while the one-use instance claim token remains active. The durable claim is
// resolved independently of the path and the service revalidates both inside
// the same audited transaction as token issuance.
func (h Handler) ExchangeProjectClaimPublisher(w http.ResponseWriter, r *http.Request) {
	principal, credential, ok := h.initialProjectClaimCredential(w, r)
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil {
		writeJSONError(w, errors.New("project not found"), http.StatusNotFound)
		return
	}
	claimedProjectID, claimedBy, ok := h.resolveProjectClaim(w, r)
	if !ok {
		return
	}
	if claimedProjectID != projectID.String() || claimedBy != principal.ID {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return
	}
	repository, err := h.repository()
	if err != nil {
		writeProjectClaimPublisherUnavailable(w)
		return
	}
	var credentials access.ProjectClaimPublisherCredentials
	err = executeAuditedMutation(r, repository, accessgen.GenCommandOperationExchangeProjectClaimPublisher(), func(tx access.Repository) (access.AuditEventInput, error) {
		writer, ok := tx.(access.ProjectClaimPublisherRepository)
		if !ok {
			return access.AuditEventInput{}, errors.New("project claim publisher repository is unavailable")
		}
		credentials, err = writer.ExchangeProjectClaimPublisher(r.Context(), access.ProjectClaimPublisherExchangeInput{
			InstanceID: h.DurableGrantInstanceID, ProjectID: projectID.String(), PrincipalID: principal.ID,
			ClaimCredentialID: credential.Token.ID, ClaimedProjectID: claimedProjectID, ClaimedBy: claimedBy,
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, err := accessgen.EncodeGenExchangeProjectClaimPublisherAuditPayload(accessgen.GenSchemaProjectClaimPublisherAuditPayload{
			ClaimCredentialId: credentials.ClaimCredentialID, PublisherCredentialId: credentials.PublisherCredentialID,
			RevokedPublisherCredentialIds: append([]string(nil), credentials.RevokedPublisherCredentialIDs...),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		event := auditInput(r, "project.claim_publisher.exchanged", principal.ID, "api_token", credentials.PublisherCredentialID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		writeProjectClaimPublisherError(w, err)
		return
	}
	writeSecretJSON(w, http.StatusCreated, accessgen.ProjectClaimPublisherExchangeResponse{
		ClaimCredentialId: credentials.ClaimCredentialID, PublisherToken: credentials.PublisherToken,
		PublisherTokenExpiresAt: credentials.PublisherTokenExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

// AcknowledgeProjectClaimPublisher revokes the claim credential only when the
// current bearer is the active publisher credential named by the caller.
func (h Handler) AcknowledgeProjectClaimPublisher(w http.ResponseWriter, r *http.Request) {
	principal, credential, ok := h.currentPublisherCredential(w, r)
	if !ok {
		return
	}
	var request projectClaimPublisherAcknowledgeRequest
	if err := decodeStrictJSON(r, &request); err != nil || strings.TrimSpace(request.ClaimCredentialID) == "" || strings.TrimSpace(request.ClaimCredentialID) != request.ClaimCredentialID {
		writeJSONError(w, errors.New("claimCredentialId is required"), http.StatusBadRequest)
		return
	}
	projectID, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
	if err != nil {
		writeJSONError(w, errors.New("project not found"), http.StatusNotFound)
		return
	}
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, projectID)
	if err != nil || credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil ||
		validateProjectRoleBindingPermissionCeiling(credential.Token.Permissions, []access.PermissionPair{manage}) != nil {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return
	}
	claimedProjectID, claimedBy, ok := h.resolveProjectClaim(w, r)
	if !ok {
		return
	}
	if claimedProjectID != projectID.String() || claimedBy != principal.ID {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return
	}
	repository, err := h.repository()
	if err != nil {
		writeProjectClaimPublisherUnavailable(w)
		return
	}
	err = executeAuditedMutation(r, repository, accessgen.GenCommandOperationAcknowledgeProjectClaimPublisher(), func(tx access.Repository) (access.AuditEventInput, error) {
		writer, ok := tx.(access.ProjectClaimPublisherRepository)
		if !ok {
			return access.AuditEventInput{}, errors.New("project claim publisher repository is unavailable")
		}
		if err := writer.AcknowledgeProjectClaimPublisher(r.Context(), access.ProjectClaimPublisherAcknowledgeInput{
			InstanceID: h.DurableGrantInstanceID, ProjectID: projectID.String(), PrincipalID: principal.ID,
			ClaimCredentialID: request.ClaimCredentialID, PublisherCredentialID: credential.Token.ID,
			ClaimedProjectID: claimedProjectID, ClaimedBy: claimedBy,
		}); err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, err := accessgen.EncodeGenAcknowledgeProjectClaimPublisherAuditPayload(accessgen.GenSchemaProjectClaimPublisherAuditPayload{
			ClaimCredentialId: request.ClaimCredentialID, PublisherCredentialId: credential.Token.ID,
			RevokedPublisherCredentialIds: []string{},
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		event := auditInput(r, "project.claim_publisher.acknowledged", principal.ID, "api_token", request.ClaimCredentialID, access.CapabilityProjectAdmin, "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		writeProjectClaimPublisherError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) initialProjectClaimCredential(w http.ResponseWriter, r *http.Request) (Principal, access.APICredential, bool) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
		return Principal{}, access.APICredential{}, false
	}
	credential, ok := h.currentCredential(r)
	if !ok || credential.Authoring != nil || credential.Token.ID == "" || credential.Token.Name != access.APITokenNameInitialProjectClaim ||
		credential.Principal.ID != principal.ID || credential.Token.PrincipalID != principal.ID || len(credential.Token.Capabilities) != 0 ||
		credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil || h.DurableGrantInstanceID == "" {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return Principal{}, access.APICredential{}, false
	}
	want, err := access.InitialProjectClaimPermissions(h.DurableGrantInstanceID)
	if err != nil || len(want) != 1 || len(credential.Token.Permissions) != 1 || credential.Token.Permissions[0].Key() != want[0].Key() {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return Principal{}, access.APICredential{}, false
	}
	return principal, credential, true
}

func (h Handler) currentPublisherCredential(w http.ResponseWriter, r *http.Request) (Principal, access.APICredential, bool) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
		return Principal{}, access.APICredential{}, false
	}
	credential, ok := h.currentCredential(r)
	if !ok || credential.Authoring != nil || credential.Token.ID == "" ||
		credential.Principal.ID != principal.ID || credential.Token.PrincipalID != principal.ID || len(credential.Token.Capabilities) != 0 ||
		credential.Token.PermissionProfile != access.PermissionCatalogProfile || h.DurableGrantInstanceID == "" {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return Principal{}, access.APICredential{}, false
	}
	return principal, credential, true
}

func (h Handler) resolveProjectClaim(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if h.ProjectClaim == nil {
		writeProjectClaimPublisherUnavailable(w)
		return "", "", false
	}
	projectID, claimedBy, err := h.ProjectClaim(r.Context())
	if err != nil {
		writeProjectClaimPublisherUnavailable(w)
		return "", "", false
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(projectID) != projectID || strings.TrimSpace(claimedBy) == "" || strings.TrimSpace(claimedBy) != claimedBy {
		writeProjectClaimPublisherError(w, access.ErrForbidden)
		return "", "", false
	}
	return projectID, claimedBy, true
}

func writeProjectClaimPublisherError(w http.ResponseWriter, err error) {
	if errors.Is(err, access.ErrForbidden) {
		writeJSONError(w, errForbidden, http.StatusForbidden)
		return
	}
	writeProjectClaimPublisherUnavailable(w)
}

func writeProjectClaimPublisherUnavailable(w http.ResponseWriter) {
	writeJSONError(w, errors.New("project claim publisher operation is unavailable"), http.StatusServiceUnavailable)
}
