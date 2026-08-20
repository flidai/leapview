package http

import (
	"database/sql"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

func (h Handler) GetCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	response, err := h.currentPrincipalResponse(r, principal)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", resourceETag(response))
	writeJSON(w, stdhttp.StatusOK, response)
}

// ListCurrentEffectiveCapabilities projects the active immutable authorization
// snapshot for the authenticated principal. The callback is deliberately
// required: falling back to a mutable repository or a hard-coded role list
// would make token capability decisions stale or bypass generation binding.
func (h Handler) ListCurrentEffectiveCapabilities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	if h.RequestEffectiveCapabilities == nil {
		writeJSONError(w, errors.New("active authorization snapshot is unavailable"), stdhttp.StatusInternalServerError)
		return
	}
	capabilities, err := h.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
	if err != nil {
		if errors.Is(err, access.ErrForbidden) {
			writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
			return
		}
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if capabilities == nil {
		capabilities = []access.Capability{}
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"capabilities": capabilities})
}

func (h Handler) UpdateCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	var input struct {
		DisplayName *string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.DisplayName == nil {
		writeJSONError(w, fmt.Errorf("displayName is required"), stdhttp.StatusBadRequest)
		return
	}
	displayName := strings.TrimSpace(*input.DisplayName)
	if displayName == "" || len(displayName) > 200 {
		writeJSONError(w, fmt.Errorf("displayName must contain between 1 and 200 bytes"), stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	existing, err := repo.PrincipalByID(r.Context(), principal.ID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	management, err := principalIdentityManagement(r.Context(), repo, existing.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if management.Source != access.IdentityManagementLocal {
		writeJSONError(w, fmt.Errorf("display name is managed by the identity provider"), stdhttp.StatusUnprocessableEntity)
		return
	}
	var updated access.Principal
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		current, currentErr := tx.PrincipalByID(r.Context(), principal.ID)
		if currentErr != nil {
			return access.AuditEventInput{}, currentErr
		}
		currentManagement, managementErr := principalIdentityManagement(r.Context(), tx, current.ID)
		if managementErr != nil {
			return access.AuditEventInput{}, managementErr
		}
		if currentManagement.Source != access.IdentityManagementLocal {
			return access.AuditEventInput{}, fmt.Errorf("display name is managed by the identity provider")
		}
		currentResponse, responseErr := h.currentPrincipalResponseFor(r.Context(), current, currentManagement, tx, true)
		if responseErr != nil {
			return access.AuditEventInput{}, responseErr
		}
		if matchErr := checkIfMatch(r.Header.Get("If-Match"), resourceETag(currentResponse)); matchErr != nil {
			return access.AuditEventInput{}, matchErr
		}
		var mutationErr error
		updated, mutationErr = tx.UpsertPrincipal(r.Context(), access.PrincipalInput{ID: current.ID, Kind: current.Kind, Email: current.Email, DisplayName: displayName})
		return auditInput(r, "principal.profile.updated", principal.ID, "principal", current.ID, "", "success", map[string]any{"email": current.Email, "displayName": displayName}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateCurrentPrincipal(), err, stdhttp.StatusBadRequest)
		return
	}
	response, err := h.currentPrincipalResponseFor(r.Context(), updated, management, repo, true)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", resourceETag(response))
	writeJSON(w, stdhttp.StatusOK, response)
}

func (h Handler) ChangeCurrentPassword(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	var input struct{ CurrentPassword, NewPassword string }
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.CurrentPassword == "" || input.NewPassword == "" {
		writeJSONError(w, fmt.Errorf("currentPassword and newPassword are required"), stdhttp.StatusBadRequest)
		return
	}
	if input.CurrentPassword == input.NewPassword {
		writeJSONError(w, fmt.Errorf("newPassword must differ from currentPassword"), stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	management, err := principalIdentityManagement(r.Context(), repo, principal.ID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !h.LocalPasswordEnabled || !management.HasLocalPassword {
		writeJSONError(w, fmt.Errorf("local password changes are unavailable for this principal"), stdhttp.StatusUnprocessableEntity)
		return
	}
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		_, mutationErr := tx.ChangeLocalPassword(r.Context(), principal.ID, input.CurrentPassword, input.NewPassword)
		return auditInput(r, "password.changed", principal.ID, "principal", principal.ID, "", "success", map[string]any{"email": principal.Email, "provider": "local"}), mutationErr
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
			return
		}
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationChangeCurrentPassword(), err, stdhttp.StatusBadRequest)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) UpdateCurrentTheme(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	var input struct {
		Theme string `json:"theme"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateCurrentTheme(), err)
		return
	}
	theme, valid := access.ParseThemeMode(input.Theme)
	if !valid {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateCurrentTheme(), fmt.Errorf("unsupported theme %q", input.Theme))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateCurrentTheme(), err)
		return
	}
	writer, ok := repo.(access.AuditedPrincipalPreferences)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateCurrentTheme(), fmt.Errorf("principal preferences are unavailable"))
		return
	}
	if err := writer.SetPrincipalThemeAudited(r.Context(), principal.ID, theme); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateCurrentTheme(), err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
