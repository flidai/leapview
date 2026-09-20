package http

import (
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
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Capabilities []string `json:"capabilities"`
		ExpiresAt    string   `json:"expiresAt"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.Capabilities == nil || len(input.Capabilities) == 0 {
		writeJSONError(w, fmt.Errorf("at least one explicit API token capability is required"), stdhttp.StatusBadRequest)
		return
	}
	var expires time.Time
	var err error
	if strings.TrimSpace(input.ExpiresAt) != "" {
		expires, err = time.Parse(time.RFC3339, strings.TrimSpace(input.ExpiresAt))
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
	}
	expires, err = access.ResolveAPITokenExpiry(expires, time.Now().UTC())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	var capabilities []access.Capability
	if input.Capabilities != nil {
		capabilities = make([]access.Capability, 0, len(input.Capabilities))
		for _, raw := range input.Capabilities {
			capability, parseErr := access.ParseCapability(strings.TrimSpace(raw))
			if parseErr != nil {
				writeJSONError(w, parseErr, stdhttp.StatusBadRequest)
				return
			}
			capabilities = append(capabilities, capability)
		}
		if h.RequestEffectiveCapabilities == nil {
			writeJSONError(w, fmt.Errorf("request-effective project capabilities are unavailable"), stdhttp.StatusBadRequest)
			return
		}
		effective, effectiveErr := h.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
		if effectiveErr != nil {
			writeJSONError(w, effectiveErr, stdhttp.StatusBadRequest)
			return
		}
		if validateErr := access.ValidateTokenCapabilities(capabilities, effective); validateErr != nil {
			writeJSONError(w, validateErr, stdhttp.StatusBadRequest)
			return
		}
	}
	var secret string
	var token access.APIToken
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationCreateCurrentAPIToken(), func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		secret, token, mutationErr = tx.CreateAPITokenWithMetadata(r.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: input.Name, Description: input.Description, Capabilities: capabilities, ExpiresAt: expires})
		return auditInput(r, "api_token.created", principal.ID, "api_token", token.ID, "", "success", nil), mutationErr
	})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"token": secret, "apiToken": apiTokenDTO(token)})
}

func (h Handler) RotateCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRotateCurrentAPIToken(), errUnauthorized)
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
		Name           string   `json:"name"`
		Description    string   `json:"description"`
		Capabilities   []string `json:"capabilities"`
		ExpiresAt      string   `json:"expiresAt"`
		RevokePrevious bool     `json:"revokePrevious"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if len(input.Capabilities) == 0 {
		writeJSONError(w, fmt.Errorf("at least one explicit API token capability is required"), stdhttp.StatusBadRequest)
		return
	}
	var expires time.Time
	var err error
	if strings.TrimSpace(input.ExpiresAt) != "" {
		expires, err = time.Parse(time.RFC3339, strings.TrimSpace(input.ExpiresAt))
		if err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
	}
	expires, err = access.ResolveAPITokenExpiry(expires, time.Now().UTC())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	capabilities := make([]access.Capability, 0, len(input.Capabilities))
	for _, raw := range input.Capabilities {
		capability, parseErr := access.ParseCapability(strings.TrimSpace(raw))
		if parseErr != nil {
			writeJSONError(w, parseErr, stdhttp.StatusBadRequest)
			return
		}
		capabilities = append(capabilities, capability)
	}
	if h.RequestEffectiveCapabilities == nil {
		writeJSONError(w, fmt.Errorf("request-effective project capabilities are unavailable"), stdhttp.StatusBadRequest)
		return
	}
	effective, err := h.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if err := access.ValidateTokenCapabilities(capabilities, effective); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	var rotation access.APITokenRotation
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationRotateCurrentAPIToken(), func(tx access.Repository) (access.AuditEventInput, error) {
		lifecycle, ok := tx.(access.APITokenCredentialRepository)
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("personal API token rotation is unavailable")
		}
		var mutationErr error
		rotation, mutationErr = lifecycle.RotateAPIToken(r.Context(), access.APITokenRotationInput{
			PrincipalID: principal.ID, PreviousTokenID: chi.URLParam(r, "token"),
			Token:          access.APITokenInput{PrincipalID: principal.ID, Name: input.Name, Description: input.Description, Capabilities: capabilities, ExpiresAt: expires},
			RevokePrevious: input.RevokePrevious,
		})
		return auditInput(r, "api_token.rotated", principal.ID, "api_token", rotation.Created.ID, "", "success", map[string]any{
			"name": rotation.Created.Name, "capabilities": rotation.Created.Capabilities,
			"previousTokenId": chi.URLParam(r, "token"), "revokePrevious": input.RevokePrevious,
		}), mutationErr
	})
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"token": rotation.Secret, "apiToken": apiTokenDTO(rotation.Created)})
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
