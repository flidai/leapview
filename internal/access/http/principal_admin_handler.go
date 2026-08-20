package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

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
		dto, dtoErr := h.principalAdministrationDTO(r.Context(), repo, row, h.currentPrincipalID(r))
		if dtoErr != nil {
			writeJSONError(w, dtoErr, stdhttp.StatusInternalServerError)
			return
		}
		items = append(items, dto)
	}
	_ = writePagedJSON(w, r, items)
}
func (h Handler) CreatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := decodeStrictJSON(r, &input); err != nil {
			writeCommandFailure(w, r, accessgen.GenCommandOperationCreatePrincipal(), err)
			return
		}
	} else if err := r.ParseForm(); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreatePrincipal(), err)
		return
	} else {
		input.Email = r.Form.Get("email")
		input.DisplayName = r.Form.Get("displayName")
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	var created access.LocalPasswordReset
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationCreatePrincipal(), func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		created, mutationErr = tx.CreateLocalUser(r.Context(), access.LocalUserInput{Email: input.Email, DisplayName: input.DisplayName, MustChange: true})
		return auditInput(r, "principal.local_user.created", h.currentPrincipalID(r), "principal", created.Principal.ID, "", "success", map[string]any{"email": created.Principal.Email}), mutationErr
	})
	if err != nil {
		if errors.Is(err, access.ErrPrincipalAlreadyExists) {
			// Duplicate creation is rejected, but retain the security audit trail.
			_ = repo.RecordAuditEvent(r.Context(), auditInput(r, "principal.local_user.create_rejected", h.currentPrincipalID(r), "principal", access.PrincipalIDForEmail(access.NormalizeEmail(input.Email)), "", "conflict", map[string]any{"email": access.NormalizeEmail(input.Email), "reason": "duplicate"}))
			writeCommandFailure(w, r, accessgen.GenCommandOperationCreatePrincipal(), err)
			return
		}
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreatePrincipal(), err, stdhttp.StatusBadRequest)
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
		writeJSONError(w, fmt.Errorf("principal kind %q is managed by its owning subsystem", existing.Kind), stdhttp.StatusUnprocessableEntity)
		return
	}
	management, err := principalIdentityManagement(r.Context(), repo, id)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), err)
		return
	}
	if management.Source != access.IdentityManagementLocal {
		writeJSONError(w, principalManagedExternallyError(management), stdhttp.StatusUnprocessableEntity)
		return
	}
	if id == h.currentPrincipalID(r) {
		writeJSONError(w, fmt.Errorf("the current principal cannot delete itself"), stdhttp.StatusUnprocessableEntity)
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
	principal, err := repo.PrincipalByID(r.Context(), principalID)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), err)
		return
	}
	if !principalKindAllowsGenericMutation(principal.Kind) {
		writeJSONError(w, fmt.Errorf("principal kind %q is managed by its owning subsystem", principal.Kind), stdhttp.StatusUnprocessableEntity)
		return
	}
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
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationResetPrincipalPassword(), func(tx access.Repository) (access.AuditEventInput, error) {
		current, currentErr := tx.PrincipalByID(r.Context(), principalID)
		if currentErr != nil {
			return access.AuditEventInput{}, currentErr
		}
		if !principalKindAllowsGenericMutation(current.Kind) {
			return access.AuditEventInput{}, fmt.Errorf("principal kind %q is managed by its owning subsystem", current.Kind)
		}
		currentManagement, managementErr := principalIdentityManagement(r.Context(), tx, principalID)
		if managementErr != nil {
			return access.AuditEventInput{}, managementErr
		}
		if !currentManagement.HasLocalPassword {
			return access.AuditEventInput{}, fmt.Errorf("this principal does not have a local password")
		}
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
		writeJSONError(w, fmt.Errorf("principal kind %q is managed by its owning subsystem", row.Kind), stdhttp.StatusUnprocessableEntity)
		return
	}
	management, err := principalIdentityManagement(r.Context(), repo, id)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err)
		return
	}
	if management.Source != access.IdentityManagementLocal {
		writeJSONError(w, principalManagedExternallyError(management), stdhttp.StatusUnprocessableEntity)
		return
	}
	revision, revisionErr := access.PrincipalRevision(row)
	if revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	var updated access.Principal
	err = runAuditedMutationWithRevision(r, repo, func(tx access.Repository) (string, error) {
		current, err := tx.PrincipalByID(r.Context(), id)
		if err != nil {
			return "", err
		}
		return access.PrincipalRevision(current)
	}, func(tx access.Repository) (access.AuditEventInput, error) {
		current, currentErr := tx.PrincipalByID(r.Context(), id)
		if currentErr != nil {
			return access.AuditEventInput{}, currentErr
		}
		currentManagement, managementErr := principalIdentityManagement(r.Context(), tx, id)
		if managementErr != nil {
			return access.AuditEventInput{}, managementErr
		}
		if currentManagement.Source != access.IdentityManagementLocal {
			return access.AuditEventInput{}, principalManagedExternallyError(currentManagement)
		}
		var mutationErr error
		updated, mutationErr = tx.UpsertPrincipal(r.Context(), access.PrincipalInput{ID: id, Kind: current.Kind, Email: current.Email, DisplayName: strings.TrimSpace(input.DisplayName)})
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
