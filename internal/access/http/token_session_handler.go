package http

import (
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListCurrentAPITokens(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	if !principalKindAllowsGenericMutation(principal.Kind) {
		writeJSONError(w, fmt.Errorf("personal API tokens are only available to user principals"), stdhttp.StatusForbidden)
		return
	}
	h.listAPITokens(w, r, principal.ID)
}
func (h Handler) listAPITokens(w stdhttp.ResponseWriter, r *stdhttp.Request, principalID string) {
	if principalID == "" {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListAPITokens(r.Context(), principalID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, apiTokenDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}

func (h Handler) CreateCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateCurrentAPIToken(), errUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	if !principalKindAllowsGenericMutation(principal.Kind) {
		writeJSONError(w, fmt.Errorf("personal API tokens are only available to user principals"), stdhttp.StatusForbidden)
		return
	}
	var input struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Permissions json.RawMessage `json:"permissions"`
		ExpiresAt   string          `json:"expiresAt"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	var expires time.Time
	var err error
	if input.ExpiresAt != "" {
		expires, err = time.Parse(time.RFC3339, input.ExpiresAt)
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	// New credentials always use typed action-target pairs. Legacy capability
	// rows remain readable for migration/bootstrap only; public issuance cannot
	// create a second unscoped credential population after the migration.
	if input.Permissions == nil || string(input.Permissions) == "null" {
		writeJSONError(w, access.ErrTokenPermissionsNeeded, stdhttp.StatusBadRequest)
		return
	}
	{
		permissions, err := access.DecodePermissionPairs(input.Permissions)
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
		credential, credentialOK := h.currentCredential(r)
		if credentialOK && strings.TrimSpace(credential.Token.ID) != "" {
			if err := access.ValidateTokenPermissionAttenuation(credential.Token, permissions); err != nil {
				writeJSONError(w, err, stdhttp.StatusForbidden)
				return
			}
		} else if len(permissions) > 0 {
			if h.CurrentEffectivePermissionOptions == nil {
				writeJSONError(w, fmt.Errorf("effective typed permission authority is unavailable"), stdhttp.StatusServiceUnavailable)
				return
			}
			authority, authorityErr := h.CurrentEffectivePermissionOptions(r.Context(), principal.ID)
			if authorityErr != nil {
				writeJSONError(w, authorityErr, stdhttp.StatusServiceUnavailable)
				return
			}
			if authorityErr := access.ValidatePermissionPairsAgainstAuthority(authority, permissions); authorityErr != nil {
				writeJSONError(w, authorityErr, stdhttp.StatusForbidden)
				return
			}
		}
		var secret string
		var token access.APIToken
		err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationCreateCurrentAPIToken(), func(tx access.Repository) (access.AuditEventInput, error) {
			scoped, ok := tx.(access.ScopedAPITokenRepository)
			if !ok {
				return auditInput(r, "api_token.created", principal.ID, "api_token", "", "", "failure", nil), fmt.Errorf("typed API token repository is unavailable")
			}
			var mutationErr error
			secret, token, mutationErr = scoped.CreateScopedAPITokenWithMetadata(r.Context(), access.ScopedAPITokenInput{
				PrincipalID: principal.ID, Name: input.Name, Description: input.Description, Permissions: permissions, ExpiresAt: expires,
			})
			return auditInput(r, "api_token.created", principal.ID, "api_token", token.ID, "", "success", nil), mutationErr
		})
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
		writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"token": secret, "apiToken": apiTokenDTO(token)})
	}
}

func (h Handler) RevokeCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRevokeCurrentAPIToken(), errUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	if !principalKindAllowsGenericMutation(principal.Kind) {
		writeJSONError(w, fmt.Errorf("personal API tokens are only available to user principals"), stdhttp.StatusForbidden)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "token")
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationRevokeCurrentAPIToken(), func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.RevokeAPITokenForPrincipal(r.Context(), principal.ID, id)
		return auditInput(r, "api_token.revoked", principal.ID, "api_token", id, "", "success", nil), mutationErr
	})
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListCurrentSessions(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	h.listSessions(w, r, principal.ID)
}
func (h Handler) ListPrincipalSessions(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	target := chi.URLParam(r, "principal")
	if target == "" || target != h.currentPrincipalID(r) {
		if !h.requirePlatformAdmin(w, r) {
			return
		}
	}
	h.listSessions(w, r, target)
}
func (h Handler) listSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, principalID string) {
	if principalID == "" {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListSessions(r.Context(), principalID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	current := ""
	if currentPrincipal, ok := h.currentPrincipal(r); ok && currentPrincipal.ID == principalID && h.CurrentSession != nil {
		current, _ = h.CurrentSession(r)
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, sessionDTOFor(row, current))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) RevokeCurrentSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRevokeCurrentSession(), errUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	h.revokeSession(w, r, principal.ID, principal.ID, accessgen.GenCommandOperationRevokeCurrentSession())
}
func (h Handler) RevokePrincipalSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	target := chi.URLParam(r, "principal")
	if target == "" || target != h.currentPrincipalID(r) {
		if !h.requirePlatformAdmin(w, r) {
			return
		}
	}
	h.revokeSession(w, r, h.currentPrincipalID(r), target, accessgen.GenCommandOperationRevokePrincipalSession())
}
func (h Handler) revokeSession(w stdhttp.ResponseWriter, r *stdhttp.Request, actor, target string, operation accessgen.GenCommandOperationID) {
	if actor == "" || target == "" {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "session")
	auditPayload, err := revokeSessionAuditPayload(operation, id, target)
	if err != nil {
		writeCommandFailure(w, r, operation, err)
		return
	}
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.RevokeSessionForPrincipal(r.Context(), target, id)
		audit := auditInput(r, "session.revoked", actor, "session", id, "", "success", nil)
		audit.MetadataJSON = auditPayload
		return audit, mutationErr
	})
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func revokeSessionAuditPayload(operation accessgen.GenCommandOperationID, sessionID, targetPrincipalID string) (string, error) {
	switch operation.APIGenOperationID() {
	case accessgen.GenCommandOperationRevokeCurrentSession().APIGenOperationID():
		return accessgen.EncodeGenRevokeCurrentSessionAuditPayload(accessgen.GenSchemaCurrentSessionRevokedAuditPayload{SessionId: sessionID})
	case accessgen.GenCommandOperationRevokePrincipalSession().APIGenOperationID():
		return accessgen.EncodeGenRevokePrincipalSessionAuditPayload(accessgen.GenSchemaPrincipalSessionRevokedAuditPayload{TargetPrincipalId: targetPrincipalID})
	default:
		return "", fmt.Errorf("unsupported session revoke operation %q", operation.APIGenOperationID())
	}
}
