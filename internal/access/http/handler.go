package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/go-chi/chi/v5"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = access.ErrForbidden
)

type Principal struct {
	ID          string
	Kind        access.PrincipalKind
	Email       string
	DisplayName string
	CreatedAt   string
	UpdatedAt   string
}

type RepositoryProvider func() (access.Repository, error)
type PrincipalProvider func(*stdhttp.Request) (Principal, bool)
type CredentialProvider func(*stdhttp.Request) (access.APICredential, bool)
type SessionProvider func(*stdhttp.Request) (string, bool)

type AuthoringAuthentication interface {
	InstanceID() string
	BeginDeviceAuthorization(context.Context, access.AuthoringScope) (access.DeviceAuthorizationResponse, error)
	ApproveDeviceAuthorization(context.Context, access.Principal, string) error
	DenyDeviceAuthorization(context.Context, access.Principal, string) error
	ExchangeDeviceCode(context.Context, string) (access.AuthoringTokenSet, error)
	Refresh(context.Context, string) (access.AuthoringTokenSet, error)
	ExchangeWorkloadIdentity(context.Context, access.WorkloadIdentityInput) (access.AuthoringTokenSet, error)
	RevokeAccessToken(context.Context, string) error
	ListSessions(context.Context, string) ([]access.AuthoringSession, error)
	RevokeSession(context.Context, string, string) error
}

type Handler struct {
	Repository           RepositoryProvider
	CurrentPrincipal     PrincipalProvider
	CurrentCredential    CredentialProvider
	CurrentSession       SessionProvider
	AuthoringAuth        AuthoringAuthentication
	Avatar               AvatarService
	LocalPasswordEnabled bool
}

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
	currentResponse, err := h.currentPrincipalResponseFor(r.Context(), existing, management, repo, true)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if !requireIfMatch(w, r, resourceETag(currentResponse)) {
		return
	}
	var updated access.Principal
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		updated, mutationErr = tx.UpsertPrincipal(r.Context(), access.PrincipalInput{ID: existing.ID, Kind: existing.Kind, Email: existing.Email, DisplayName: displayName})
		return auditInput(r, "principal.profile.updated", principal.ID, "principal", existing.ID, "", "success", map[string]any{"email": existing.Email, "displayName": displayName}), mutationErr
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
		Name      string `json:"name"`
		ExpiresAt string `json:"expiresAt"`
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
	var secret string
	var token access.APIToken
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		secret, token, mutationErr = tx.CreateAPITokenWithMetadata(r.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: input.Name, ExpiresAt: expires})
		return auditInput(r, "api_token.created", principal.ID, "api_token", token.ID, "", "success", nil), mutationErr
	})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"token": secret, "apiToken": apiTokenDTO(token)})
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
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
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
	h.listSessions(w, r, h.currentPrincipalID(r))
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
	if h.CurrentSession != nil {
		current, _ = h.CurrentSession(r)
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, sessionDTOFor(row, current))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) RevokeCurrentSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.revokeSession(w, r, h.currentPrincipalID(r), h.currentPrincipalID(r))
}
func (h Handler) RevokePrincipalSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	target := chi.URLParam(r, "principal")
	if target == "" || target != h.currentPrincipalID(r) {
		if !h.requirePlatformAdmin(w, r) {
			return
		}
	}
	h.revokeSession(w, r, h.currentPrincipalID(r), target)
}
func (h Handler) revokeSession(w stdhttp.ResponseWriter, r *stdhttp.Request, actor, target string) {
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
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.RevokeSessionForPrincipal(r.Context(), target, id)
		return auditInput(r, "session.revoked", actor, "session", id, "", "success", nil), mutationErr
	})
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListPrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListPrincipals(r.Context(), access.PrincipalFilter{Email: r.URL.Query().Get("email"), Query: r.URL.Query().Get("q")})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, principalDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) CreatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct{ Email, DisplayName string }
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	created, err := repo.CreateLocalUser(r.Context(), access.LocalUserInput{Email: input.Email, DisplayName: input.DisplayName, MustChange: true})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, localPasswordResetDTO(created))
}
func (h Handler) GetPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.PrincipalByID(r.Context(), chi.URLParam(r, "principal"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	dto, err := h.principalAdministrationDTO(r.Context(), repo, row, h.currentPrincipalID(r))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	writeJSON(w, stdhttp.StatusOK, dto)
}
func (h Handler) DeletePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "principal")
	existing, err := repo.PrincipalByID(r.Context(), id)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), err)
		return
	}
	if !principalKindAllowsGenericMutation(existing.Kind) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), fmt.Errorf("principal kind %q is managed by its owning subsystem", existing.Kind))
		return
	}
	management, err := principalIdentityManagement(r.Context(), repo, id)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), err)
		return
	}
	if management.Source != access.IdentityManagementLocal {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), principalManagedExternallyError(management))
		return
	}
	if id == h.currentPrincipalID(r) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), fmt.Errorf("the current principal cannot delete itself"))
		return
	}
	_, ok := repo.(interface {
		DeletePrincipal(context.Context, string) error
	})
	if !ok {
		writeJSONError(w, fmt.Errorf("principal deletion is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		txDeleter, ok := tx.(interface {
			DeletePrincipal(context.Context, string) error
		})
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("principal deletion is unavailable")
		}
		mutationErr := txDeleter.DeletePrincipal(r.Context(), id)
		return auditInput(r, "principal.deleted", h.currentPrincipalID(r), "principal", id, "", "success", map[string]any{"email": existing.Email, "kind": string(existing.Kind), "displayName": existing.DisplayName}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeletePrincipal(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
func (h Handler) DisablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	h.setPrincipalDisabled(w, r, true)
}
func (h Handler) EnablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	h.setPrincipalDisabled(w, r, false)
}
func (h Handler) setPrincipalDisabled(w stdhttp.ResponseWriter, r *stdhttp.Request, disabled bool) {
	operationID := accessgen.GenCommandOperationDisablePrincipal()
	if !disabled {
		operationID = accessgen.GenCommandOperationEnablePrincipal()
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "principal")
	existing, err := repo.PrincipalByID(r.Context(), id)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !principalKindAllowsGenericMutation(existing.Kind) {
		writeJSONError(w, fmt.Errorf("principal kind %q is managed by its owning subsystem", existing.Kind), stdhttp.StatusUnprocessableEntity)
		return
	}
	if disabled && id == h.currentPrincipalID(r) {
		writeJSONError(w, fmt.Errorf("the current principal cannot disable itself"), stdhttp.StatusUnprocessableEntity)
		return
	}
	type principalStatusWriter interface {
		DisablePrincipal(context.Context, string) (access.Principal, error)
		EnablePrincipal(context.Context, string) (access.Principal, error)
	}
	if _, ok := repo.(principalStatusWriter); !ok {
		writeJSONError(w, fmt.Errorf("principal status changes are unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	var updated access.Principal
	action := "principal.blocked"
	if !disabled {
		action = "principal.unblocked"
	}
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		writer, ok := tx.(principalStatusWriter)
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("principal status changes are unavailable")
		}
		var mutationErr error
		if disabled {
			updated, mutationErr = writer.DisablePrincipal(r.Context(), id)
		} else {
			updated, mutationErr = writer.EnablePrincipal(r.Context(), id)
		}
		return auditInput(r, action, h.currentPrincipalID(r), "principal", id, "", "success", map[string]any{"email": existing.Email, "kind": string(existing.Kind), "displayName": existing.DisplayName}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, operationID, err, statusForNotFound(err))
		return
	}
	dto, dtoErr := h.principalAdministrationDTO(r.Context(), repo, updated, h.currentPrincipalID(r))
	if dtoErr != nil {
		writeJSONError(w, dtoErr, stdhttp.StatusInternalServerError)
		return
	}
	if revision, revisionErr := access.PrincipalRevision(updated); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	writeJSON(w, stdhttp.StatusOK, dto)
}
func (h Handler) ResetPrincipalPassword(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), err)
		return
	}
	principalID := chi.URLParam(r, "principal")
	management, err := principalIdentityManagement(r.Context(), repo, principalID)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), err)
		return
	}
	if !management.HasLocalPassword {
		writeCommandFailure(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), fmt.Errorf("this principal does not have a local password"))
		return
	}
	var reset access.LocalPasswordReset
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		reset, mutationErr = tx.ResetLocalPassword(r.Context(), principalID)
		return auditInput(r, "principal.local_password.reset", h.currentPrincipalID(r), "principal", principalID, "", "success", map[string]any{"email": reset.Principal.Email}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), err, statusForNotFound(err))
		return
	}
	writeSecretJSON(w, stdhttp.StatusOK, localPasswordResetDTO(reset))
}
func (h Handler) UpdatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "principal")
	row, err := repo.PrincipalByID(r.Context(), id)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !principalKindAllowsGenericMutation(row.Kind) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), fmt.Errorf("principal kind %q is managed by its owning subsystem", row.Kind))
		return
	}
	management, err := principalIdentityManagement(r.Context(), repo, id)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err)
		return
	}
	if management.Source != access.IdentityManagementLocal {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), principalManagedExternallyError(management))
		return
	}
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
		if !requireIfMatch(w, r, revision) {
			return
		}
	}
	var updated access.Principal
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		updated, mutationErr = tx.UpsertPrincipal(r.Context(), access.PrincipalInput{ID: id, Kind: row.Kind, Email: row.Email, DisplayName: strings.TrimSpace(input.DisplayName)})
		return auditInput(r, "principal.updated", h.currentPrincipalID(r), "principal", id, "", "success", map[string]any{"email": row.Email, "displayName": strings.TrimSpace(input.DisplayName)}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err, stdhttp.StatusBadRequest)
		return
	}
	if revision, revisionErr := access.PrincipalRevision(updated); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	dto, err := h.principalAdministrationDTO(r.Context(), repo, updated, h.currentPrincipalID(r))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	writeJSON(w, stdhttp.StatusOK, dto)
}

// OAuthToken issues an identity-only REST API credential for service-principal
// automation. Authoring and MCP grants are dispatched by the access module;
// this handler owns the legacy service-principal client-credentials exchange.
func (h Handler) OAuthToken(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	var input struct {
		GrantType    string `json:"grant_type"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Scope        string `json:"scope"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := decodeStrictJSON(r, &input); err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
	} else if err := r.ParseForm(); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	} else {
		input.GrantType = r.Form.Get("grant_type")
		input.ClientID = r.Form.Get("client_id")
		input.ClientSecret = r.Form.Get("client_secret")
		input.Scope = r.Form.Get("scope")
	}
	if strings.TrimSpace(input.GrantType) != "client_credentials" {
		writeJSONError(w, fmt.Errorf("unsupported grant_type %q", input.GrantType), stdhttp.StatusBadRequest)
		return
	}
	principal, err := repo.PrincipalForServicePrincipalSecret(r.Context(), input.ClientID, input.ClientSecret)
	if err != nil {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	ttl := time.Hour
	var token string
	err = runAuditedMutation(r, repo, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		token, _, mutationErr = tx.CreateAPITokenWithMetadata(r.Context(), access.APITokenInput{
			PrincipalID: principal.ID,
			Name:        "oauth-client-credentials",
			ExpiresAt:   time.Now().Add(ttl),
		})
		return auditInput(r, "oauth.token.created", principal.ID, "api_token", "", "", "success", map[string]any{"grantType": "client_credentials"}), mutationErr
	})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeSecretJSON(w, stdhttp.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(ttl.Seconds()),
		"scope":        input.Scope,
	})
}

func (h Handler) ListServicePrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListServicePrincipals(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, principalDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) GetServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.PrincipalByID(r.Context(), chi.URLParam(r, "servicePrincipal"))
	if err != nil || row.Kind != access.PrincipalKindServicePrincipal {
		writeJSONError(w, sql.ErrNoRows, stdhttp.StatusNotFound)
		return
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(row))
}
func (h Handler) CreateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input access.ServicePrincipalInput
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.CreateServicePrincipal(r.Context(), input)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, principalDTO(row))
}
func (h Handler) UpdateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input access.ServicePrincipalInput
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.UpdateServicePrincipal(r.Context(), chi.URLParam(r, "servicePrincipal"), input)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(row))
}
func (h Handler) DeleteServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if err := repo.DeleteServicePrincipal(r.Context(), chi.URLParam(r, "servicePrincipal")); err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
func (h Handler) CreateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input access.ServicePrincipalSecretInput
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	secret, row, err := repo.CreateServicePrincipalSecret(r.Context(), chi.URLParam(r, "servicePrincipal"), input)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"secret": secret, "clientSecret": servicePrincipalSecretDTO(row, "")})
}
func (h Handler) ListServicePrincipalSecrets(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(interface {
		ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error)
	})
	if !ok {
		writeJSONError(w, fmt.Errorf("secret metadata unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	rows, err := reader.ListServicePrincipalSecrets(r.Context(), chi.URLParam(r, "servicePrincipal"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, servicePrincipalSecretDTO(row, ""))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) GetServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(interface {
		GetServicePrincipalSecret(context.Context, string, string) (access.ServicePrincipalSecret, error)
	})
	if !ok {
		writeJSONError(w, fmt.Errorf("secret metadata unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	row, err := reader.GetServicePrincipalSecret(r.Context(), chi.URLParam(r, "servicePrincipal"), chi.URLParam(r, "secret"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, servicePrincipalSecretDTO(row, ""))
}
func (h Handler) RevokeServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if err := repo.RevokeServicePrincipalSecret(r.Context(), chi.URLParam(r, "servicePrincipal"), chi.URLParam(r, "secret")); err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListGroups(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGroups(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, groupDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) CreateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct{ Name, DisplayName string }
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.UpsertGroup(r.Context(), access.GroupInput{Provider: "local", ExternalID: input.Name, Name: firstNonEmpty(input.DisplayName, input.Name)})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, groupDTO(row))
}
func (h Handler) GetGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, groupDTO(row))
}
func (h Handler) UpdateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(row) {
		writeJSONError(w, groupManagedExternallyError(row), stdhttp.StatusUnprocessableEntity)
		return
	}
	row, err = repo.UpsertGroup(r.Context(), access.GroupInput{ID: row.ID, Provider: row.Provider, ExternalID: row.ExternalID, Name: input.DisplayName})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusOK, groupDTO(row))
}
func (h Handler) DeleteGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	group, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(group) {
		writeJSONError(w, groupManagedExternallyError(group), stdhttp.StatusUnprocessableEntity)
		return
	}
	if err := repo.DeleteGroup(r.Context(), group.ID); err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
func (h Handler) ListGroupMembers(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGroupMembers(r.Context(), chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, groupMemberPrincipalDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) AddGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	group, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(group) {
		writeJSONError(w, groupManagedExternallyError(group), stdhttp.StatusUnprocessableEntity)
		return
	}
	if err := repo.AddGroupMember(r.Context(), group.ID, chi.URLParam(r, "principal")); err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "added"})
}
func (h Handler) RemoveGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	group, err := findGroup(r.Context(), repo, chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !groupIsLocallyManaged(group) {
		writeJSONError(w, groupManagedExternallyError(group), stdhttp.StatusUnprocessableEntity)
		return
	}
	if err := repo.RemoveGroupMember(r.Context(), group.ID, chi.URLParam(r, "principal")); err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListAuditEvents(r.Context(), access.AuditEventFilter{PrincipalID: r.URL.Query().Get("principalId"), Action: r.URL.Query().Get("action"), ResourceKind: r.URL.Query().Get("resourceKind"), ResourceID: r.URL.Query().Get("resourceId"), Limit: 100})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, auditEventDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) ListPlatformAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.ListAuditEvents(w, r)
}

func (h Handler) repository() (access.Repository, error) {
	if h.Repository == nil {
		return nil, errors.New("access repository is unavailable")
	}
	return h.Repository()
}
func (h Handler) currentPrincipal(r *stdhttp.Request) (Principal, bool) {
	if h.CurrentPrincipal == nil {
		return Principal{}, false
	}
	return h.CurrentPrincipal(r)
}
func (h Handler) currentPrincipalID(r *stdhttp.Request) string {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		return ""
	}
	return principal.ID
}
func (h Handler) currentCredential(r *stdhttp.Request) (access.APICredential, bool) {
	if h.CurrentCredential == nil {
		return access.APICredential{}, false
	}
	return h.CurrentCredential(r)
}

func (h Handler) requirePlatformAdmin(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return false
	}
	if h.Repository == nil {
		writeJSONError(w, errors.New("access repository is unavailable"), stdhttp.StatusInternalServerError)
		return false
	}
	repo, err := h.Repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return false
	}
	checker, ok := repo.(access.PlatformRoleReader)
	if !ok {
		writeJSONError(w, errors.New("platform role checker is unavailable"), stdhttp.StatusInternalServerError)
		return false
	}
	admin, err := checker.IsPlatformAdmin(r.Context(), principal.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return false
	}
	if admin {
		return true
	}
	writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
	return false
}
func (h Handler) rejectAuthoringCredential(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	if credential, ok := h.currentCredential(r); ok && credential.Authoring != nil {
		writeJSONError(w, errors.New("authoring credentials cannot perform this mutation"), stdhttp.StatusForbidden)
		return true
	}
	return false
}

func principalDTO(row access.Principal) map[string]any {
	return map[string]any{"id": row.ID, "kind": row.Kind, "email": row.Email, "displayName": row.DisplayName, "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt, "disabledAt": emptyToNil(row.DisabledAt), "blockedAt": emptyToNil(row.BlockedAt)}
}

func (h Handler) currentPrincipalResponse(r *stdhttp.Request, current Principal) (map[string]any, error) {
	accountManagement := true
	if credential, ok := h.currentCredential(r); ok && credential.Authoring != nil {
		accountManagement = false
	}
	if h.Repository == nil {
		return h.currentPrincipalResponseFor(r.Context(), access.Principal{
			ID: current.ID, Kind: current.Kind, Email: current.Email, DisplayName: current.DisplayName,
			CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
		}, access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem}, nil, accountManagement)
	}
	repo, err := h.repository()
	if err != nil {
		return nil, err
	}
	stored, err := repo.PrincipalByID(r.Context(), current.ID)
	if err != nil {
		return nil, err
	}
	management, err := principalIdentityManagement(r.Context(), repo, stored.ID)
	if err != nil {
		return nil, err
	}
	return h.currentPrincipalResponseFor(r.Context(), stored, management, repo, accountManagement)
}

func (h Handler) currentPrincipalResponseFor(ctx context.Context, principal access.Principal, management access.PrincipalIdentityManagement, repo access.Repository, accountManagement bool) (map[string]any, error) {
	response := principalDTO(principal)
	identity := map[string]any{"source": management.Source}
	if management.Provider != "" {
		identity["provider"] = management.Provider
	}
	response["identityManagement"] = identity
	isUser := principal.Kind == "" || principal.Kind == access.PrincipalKindUser
	response["capabilities"] = map[string]bool{
		"canUpdateDisplayName":       accountManagement && isUser && management.Source == access.IdentityManagementLocal,
		"canChangePassword":          accountManagement && isUser && h.LocalPasswordEnabled && management.HasLocalPassword,
		"canManageAvatar":            accountManagement && isUser && h.Avatar != nil,
		"canManageSessions":          accountManagement && isUser && repo != nil,
		"canManageAuthoringSessions": isUser && h.AuthoringAuth != nil,
		"canManageApiTokens":         accountManagement && isUser && repo != nil,
	}
	if h.Avatar != nil && isUser {
		metadata, err := h.Avatar.Current(ctx, principal.ID)
		switch {
		case err == nil:
			response["avatar"] = avatarResponse(metadata)
		case errors.Is(err, avatar.ErrNotFound):
		default:
			return nil, err
		}
	}
	return response, nil
}

func (h Handler) principalAdministrationDTO(ctx context.Context, repo access.Repository, principal access.Principal, actorID string) (map[string]any, error) {
	management, err := principalIdentityManagement(ctx, repo, principal.ID)
	if err != nil {
		return nil, err
	}
	response := principalDTO(principal)
	identity := map[string]any{"source": management.Source}
	if management.Provider != "" {
		identity["provider"] = management.Provider
	}
	response["identityManagement"] = identity
	isUser := principalKindAllowsGenericMutation(principal.Kind)
	isSelf := strings.TrimSpace(actorID) != "" && actorID == principal.ID
	response["capabilities"] = map[string]bool{
		"canUpdateProfile":       isUser && management.Source == access.IdentityManagementLocal,
		"canResetPassword":       isUser && management.HasLocalPassword,
		"canBlock":               isUser && !isSelf && principal.BlockedAt == "" && principal.DisabledAt == "",
		"canUnblock":             isUser && principal.BlockedAt != "",
		"canDelete":              isUser && !isSelf && management.Source == access.IdentityManagementLocal,
		"canManageSessions":      isUser,
		"canManageAuthorization": isUser,
	}
	return response, nil
}

func principalIdentityManagement(ctx context.Context, repo access.Repository, principalID string) (access.PrincipalIdentityManagement, error) {
	resolver, ok := repo.(access.PrincipalIdentityManagementRepository)
	if !ok {
		return access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem}, nil
	}
	return resolver.PrincipalIdentityManagement(ctx, principalID)
}

func principalKindAllowsGenericMutation(kind access.PrincipalKind) bool {
	return kind == "" || kind == access.PrincipalKindUser
}

func principalManagedExternallyError(management access.PrincipalIdentityManagement) error {
	provider := strings.TrimSpace(management.Provider)
	if provider == "" {
		provider = "the owning identity subsystem"
	}
	return fmt.Errorf("principal profile is managed by %s", provider)
}

func localPasswordResetDTO(row access.LocalPasswordReset) map[string]any {
	out := principalDTO(row.Principal)
	out["temporaryPassword"] = row.Password
	return out
}
func groupDTO(row access.Group) map[string]any {
	local := groupIsLocallyManaged(row)
	return map[string]any{
		"id": row.ID, "provider": row.Provider, "externalId": row.ExternalID,
		"name": row.Name, "createdAt": row.CreatedAt,
		"capabilities": map[string]bool{
			"canUpdate": local, "canDelete": local, "canManageMembers": local,
		},
	}
}
func groupMemberPrincipalDTO(row access.GroupMember) map[string]any {
	return map[string]any{"groupId": row.GroupID, "principalId": row.PrincipalID, "kind": row.Kind, "email": row.Email, "displayName": row.DisplayName, "createdAt": row.CreatedAt}
}
func apiTokenDTO(row access.APIToken) map[string]any {
	return map[string]any{"id": row.ID, "principalId": row.PrincipalID, "name": row.Name, "expiresAt": emptyToNil(row.ExpiresAt), "createdAt": row.CreatedAt, "lastUsedAt": emptyToNil(row.LastUsedAt), "revokedAt": emptyToNil(row.RevokedAt)}
}
func servicePrincipalSecretDTO(row access.ServicePrincipalSecret, raw string) map[string]any {
	out := map[string]any{"id": row.ID, "servicePrincipalId": row.ServicePrincipalID, "name": row.Name, "expiresAt": row.ExpiresAt, "createdAt": row.CreatedAt, "revokedAt": emptyToNil(row.RevokedAt)}
	if raw != "" {
		out["secret"] = raw
	}
	return out
}
func sessionDTOFor(row access.Session, current string) map[string]any {
	return map[string]any{"id": row.ID, "principalId": row.PrincipalID, "kind": row.Kind, "instanceId": row.InstanceID, "profileId": row.ProfileID, "clientId": row.ClientID, "expiresAt": row.ExpiresAt, "absoluteExpiresAt": row.AbsoluteExpiresAt, "createdAt": row.CreatedAt, "lastSeenAt": row.LastSeenAt, "revokedAt": emptyToNil(row.RevokedAt), "current": row.ID == current}
}
func sessionDTO(row access.Session) map[string]any { return sessionDTOFor(row, "") }
func auditEventDTO(row access.AuditEvent) map[string]any {
	return map[string]any{"id": row.ID, "principalId": emptyToNil(row.PrincipalID), "action": row.Action, "resourceKind": row.ResourceKind, "resourceId": row.ResourceID, "capability": emptyToNil(string(row.Capability)), "status": row.Status, "requestId": emptyToNil(row.RequestID), "correlationId": emptyToNil(row.CorrelationID), "metadata": json.RawMessage(row.MetadataJSON), "createdAt": row.CreatedAt}
}

func auditInput(r *stdhttp.Request, action, principalID, resourceKind, resourceID string, capability access.Capability, status string, metadata map[string]any) access.AuditEventInput {
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, _ := json.Marshal(metadata)
	return access.AuditEventInput{PrincipalID: principalID, Action: action, ResourceKind: resourceKind, ResourceID: resourceID, Capability: capability, Status: status, RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r), MetadataJSON: string(encoded)}
}
func runAuditedMutation(r *stdhttp.Request, repo access.Repository, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	transactional, ok := repo.(access.AuditedMutationRepository)
	if !ok {
		return errors.New("transactional access repository is required")
	}
	return transactional.RunAuditedMutation(r.Context(), mutation)
}

func decodeStrictJSON(r *stdhttp.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
func writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeSecretJSON(w stdhttp.ResponseWriter, status int, value any) { writeJSON(w, status, value) }
func writeJSONError(w stdhttp.ResponseWriter, err error, status int) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
func writePagedJSON[T any](w stdhttp.ResponseWriter, r *stdhttp.Request, items []T) bool {
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return false
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": page, "page": map[string]any{"nextCursor": nextCursor}})
	return true
}

func pageSliceForRequest[T any](w stdhttp.ResponseWriter, r *stdhttp.Request, items []T) ([]T, string, bool) {
	limit, err := parseAPILimitQuery(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return nil, "", false
	}
	cursor, err := decodeKeyCursor(r.URL.Query().Get("pageToken"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return nil, "", false
	}
	start := 0
	if cursor != "" {
		start = -1
		for index, item := range items {
			if apiItemPageKey(item) == cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			writeJSONError(w, fmt.Errorf("pageToken key is unavailable"), stdhttp.StatusBadRequest)
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) && end > start {
		next = encodeKeyCursor(apiItemPageKey(items[end-1]))
	}
	page := make([]T, end-start)
	copy(page, items[start:end])
	return page, next, true
}
func writeCommandFailure(w stdhttp.ResponseWriter, _ *stdhttp.Request, _ accessgen.GenCommandOperationID, err error) {
	writeJSONError(w, err, stdhttp.StatusBadRequest)
}
func writeAuditedMutationError(w stdhttp.ResponseWriter, _ *stdhttp.Request, _ accessgen.GenCommandOperationID, err error, status int) {
	writeJSONError(w, err, status)
}
func statusForNotFound(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusInternalServerError
}
func emptyToNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func requestIDFromRequest(r *stdhttp.Request) string {
	return firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID"))
}
func correlationIDFromRequest(r *stdhttp.Request) string {
	return firstNonEmpty(r.Header.Get("X-Correlation-Id"), r.Header.Get("X-Correlation-ID"), requestIDFromRequest(r))
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func servicePrincipalSecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func resourceETag(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
func requireIfMatch(w stdhttp.ResponseWriter, r *stdhttp.Request, current string) bool {
	if value := strings.TrimSpace(r.Header.Get("If-Match")); value != "" && value != current {
		writeJSONError(w, errors.New("resource changed"), stdhttp.StatusPreconditionFailed)
		return false
	}
	return true
}
func writeAPIProblem(w stdhttp.ResponseWriter, _ *stdhttp.Request, status int, code, detail string, _ any) {
	writeJSON(w, status, map[string]any{"code": code, "detail": detail})
}
func apiTokenID(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
func parseAPILimitQuery(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 50, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if parsed > 200 {
		return 0, fmt.Errorf("limit must not exceed 200")
	}
	return parsed, nil
}

func apiItemPageKey(value any) string {
	encoded, _ := json.Marshal(value)
	var object map[string]any
	if json.Unmarshal(encoded, &object) == nil {
		id, _ := object["id"].(string)
		created, _ := object["createdAt"].(string)
		if id != "" {
			return created + "\x00" + id
		}
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func encodeKeyCursor(key string) string {
	if key == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("key:" + key))
}

func decodeKeyCursor(token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || !strings.HasPrefix(string(raw), "key:") || strings.TrimPrefix(string(raw), "key:") == "" {
		return "", fmt.Errorf("pageToken is invalid")
	}
	return strings.TrimPrefix(string(raw), "key:"), nil
}
func normalizeTimestamp(value string) string { return strings.TrimSpace(value) }
func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func formatAuditTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func groupManagedExternallyError(group access.Group) error {
	return fmt.Errorf("group %q is managed by provider %q", group.ID, group.Provider)
}
func groupIsLocallyManaged(group access.Group) bool {
	return strings.TrimSpace(group.Provider) == "local"
}

func findGroup(ctx context.Context, repo access.Repository, id string) (access.Group, error) {
	rows, err := repo.ListGroups(ctx)
	if err != nil {
		return access.Group{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return access.Group{}, sql.ErrNoRows
}
