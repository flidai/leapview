package http

import (
	"database/sql"
	"errors"
	"fmt"
	stdhttp "net/http"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

func (h Handler) DecideDeviceAuthorization(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, ok := h.authoringAuthentication(w)
	if !ok {
		return
	}
	principal, authenticated := h.currentPrincipal(r)
	if !authenticated {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if _, bearerCredential := h.currentCredential(r); bearerCredential {
		writeJSONError(w, fmt.Errorf("device authorization approval requires a browser session"), stdhttp.StatusForbidden)
		return
	}
	var input struct {
		UserCode string `json:"userCode"`
		Approved bool   `json:"approved"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	actor := access.Principal{
		ID: principal.ID, Kind: access.PrincipalKindUser, Email: principal.Email, DisplayName: principal.DisplayName,
	}
	var err error
	status := "approved"
	if input.Approved {
		err = service.ApproveDeviceAuthorization(r.Context(), actor, input.UserCode)
	} else {
		status = "denied"
		err = service.DenyDeviceAuthorization(r.Context(), actor, input.UserCode)
	}
	if err != nil {
		writeAuthoringCommandError(w, r, accessgen.GenCommandOperationDecideDeviceAuthorization(), err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": status})
}

func (h Handler) ListCurrentAuthoringSessions(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, ok := h.authoringAuthentication(w)
	if !ok {
		return
	}
	principal, authenticated := h.currentPrincipal(r)
	if !authenticated {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	sessions, err := service.ListSessions(r.Context(), principal.ID)
	if err != nil {
		writeAuthoringAuthError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	currentSessionID := ""
	if credential, found := h.currentCredential(r); found && credential.Authoring != nil {
		currentSessionID = credential.Authoring.ID
	}
	for _, session := range sessions {
		out = append(out, authoringSessionDTO(session, session.ID != "" && session.ID == currentSessionID))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) RevokeCurrentAuthoringSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, ok := h.authoringAuthentication(w)
	if !ok {
		return
	}
	principal, authenticated := h.currentPrincipal(r)
	if !authenticated {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "session")
	if err := service.RevokeSession(r.Context(), principal.ID, sessionID); err != nil {
		if errors.Is(err, access.ErrInvalidAuthoringCredential) {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeAuthoringCommandError(w, r, accessgen.GenCommandOperationRevokeCurrentAuthoringSession(), err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "revoked"})
}

func (h Handler) authoringAuthentication(w stdhttp.ResponseWriter) (AuthoringAuthentication, bool) {
	if h.AuthoringAuth == nil {
		writeJSONError(w, fmt.Errorf("authoring authentication is unavailable"), stdhttp.StatusServiceUnavailable)
		return nil, false
	}
	return h.AuthoringAuth, true
}

func authoringTokenDTO(tokens access.AuthoringTokenSet) map[string]any {
	response := map[string]any{
		"accessToken": tokens.AccessToken, "tokenType": tokens.TokenType,
		"expiresIn": tokens.ExpiresIn, "session": authoringSessionDTO(tokens.Session, true),
	}
	if tokens.RefreshToken != "" {
		response["refreshToken"] = tokens.RefreshToken
	}
	return response
}

func authoringSessionDTO(session access.AuthoringSession, current bool) map[string]any {
	privileges := make([]string, len(session.Scope.Privileges))
	for index, privilege := range session.Scope.Privileges {
		privileges[index] = string(privilege)
	}
	response := map[string]any{
		"id": session.ID, "kind": session.Kind, "current": current, "clientId": session.ClientID,
		"targetId": session.Scope.TargetID, "projectId": session.Scope.ProjectID,
		"privileges": privileges, "createdAt": session.CreatedAt.UTC().Format(time.RFC3339),
		"expiresAt": session.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if !session.LastUsedAt.IsZero() {
		response["lastUsedAt"] = session.LastUsedAt.UTC().Format(time.RFC3339)
	}
	if !session.RevokedAt.IsZero() {
		response["revokedAt"] = session.RevokedAt.UTC().Format(time.RFC3339)
	}
	return response
}

func writeAuthoringAuthError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusBadRequest
	switch {
	case errors.Is(err, access.ErrDeviceAuthorizationPending),
		errors.Is(err, access.ErrAuthoringRefreshReplay):
		status = stdhttp.StatusConflict
	case errors.Is(err, access.ErrDeviceAuthorizationSlowDown):
		status = stdhttp.StatusTooManyRequests
	case errors.Is(err, access.ErrAuthoringScopeDenied):
		status = stdhttp.StatusUnprocessableEntity
	}
	writeJSONError(w, err, status)
}

func writeAuthoringCommandError(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID accessgen.GenCommandOperationID, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		err = apigenfailure.Wrap("not_found", err)
	}
	writeCommandFailure(w, r, operationID, err)
}
