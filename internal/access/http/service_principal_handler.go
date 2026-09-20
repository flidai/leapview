package http

import (
	"context"
	"database/sql"
	"fmt"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

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
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
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
	var row access.Principal
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationCreateServicePrincipal(), func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		row, mutationErr = tx.CreateServicePrincipal(r.Context(), input)
		return auditInput(r, "service_principal.created", h.currentPrincipalID(r), "service_principal", row.ID, "", "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateServicePrincipal(), err, stdhttp.StatusBadRequest)
		return
	}
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
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
	id := chi.URLParam(r, "servicePrincipal")
	existing, err := repo.PrincipalByID(r.Context(), id)
	if err != nil || existing.Kind != access.PrincipalKindServicePrincipal {
		writeJSONError(w, sql.ErrNoRows, stdhttp.StatusNotFound)
		return
	}
	if revision, revisionErr := access.PrincipalRevision(existing); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	var row access.Principal
	err = runAuditedMutationWithRevision(r, repo, func(tx access.Repository) (string, error) {
		current, err := tx.PrincipalByID(r.Context(), id)
		if err != nil {
			return "", err
		}
		if current.Kind != access.PrincipalKindServicePrincipal {
			return "", sql.ErrNoRows
		}
		return access.PrincipalRevision(current)
	}, func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		row, mutationErr = tx.UpdateServicePrincipal(r.Context(), id, input)
		return auditInput(r, "service_principal.updated", h.currentPrincipalID(r), "service_principal", id, "", "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateServicePrincipal(), err, statusForNotFound(err))
		return
	}
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
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
	id := chi.URLParam(r, "servicePrincipal")
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationDeleteServicePrincipal(), func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.DeleteServicePrincipal(r.Context(), id)
		return auditInput(r, "service_principal.deleted", h.currentPrincipalID(r), "service_principal", id, "", "success", nil), mutationErr
	})
	if err != nil {
		if writeOffboardingError(w, err, "SERVICE_PRINCIPAL_OWNS_OBJECTS") {
			return
		}
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteServicePrincipal(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) DisableServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "servicePrincipal")
	var row access.Principal
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationDisableServicePrincipal(), func(tx access.Repository) (access.AuditEventInput, error) {
		lifecycle, ok := tx.(access.ServicePrincipalCredentialRepository)
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("service principal lifecycle is unavailable")
		}
		var mutationErr error
		row, mutationErr = lifecycle.DisableServicePrincipal(r.Context(), id)
		return auditInput(r, "service_principal.disabled", h.currentPrincipalID(r), "service_principal", id, "", "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDisableServicePrincipal(), err, statusForNotFound(err))
		return
	}
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(row))
}

func (h Handler) EnableServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "servicePrincipal")
	var row access.Principal
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationEnableServicePrincipal(), func(tx access.Repository) (access.AuditEventInput, error) {
		lifecycle, ok := tx.(access.ServicePrincipalCredentialRepository)
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("service principal lifecycle is unavailable")
		}
		var mutationErr error
		row, mutationErr = lifecycle.EnableServicePrincipal(r.Context(), id)
		return auditInput(r, "service_principal.enabled", h.currentPrincipalID(r), "service_principal", id, "", "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationEnableServicePrincipal(), err, statusForNotFound(err))
		return
	}
	if revision, revisionErr := access.PrincipalRevision(row); revisionErr == nil {
		w.Header().Set("ETag", revision)
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(row))
}

func (h Handler) CreateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var request struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeStrictJSON(r, &request); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	var expiresAt time.Time
	if strings.TrimSpace(request.ExpiresAt) != "" {
		parsed, parseErr := time.Parse(time.RFC3339, request.ExpiresAt)
		if parseErr != nil {
			writeJSONError(w, parseErr, stdhttp.StatusBadRequest)
			return
		}
		expiresAt = parsed
	}
	var err error
	expiresAt, err = access.ResolveServicePrincipalSecretExpiry(expiresAt, time.Now().UTC())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	var secret string
	var row access.ServicePrincipalSecret
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationCreateServicePrincipalSecret(), func(tx access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		secret, row, mutationErr = tx.CreateServicePrincipalSecret(r.Context(), chi.URLParam(r, "servicePrincipal"), access.ServicePrincipalSecretInput{Name: request.Name, ExpiresAt: expiresAt})
		return auditInput(r, "service_principal_secret.created", h.currentPrincipalID(r), "service_principal", row.ServicePrincipalID, "", "success", map[string]any{"secretId": row.ID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateServicePrincipalSecret(), err, stdhttp.StatusBadRequest)
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
	servicePrincipalID := chi.URLParam(r, "servicePrincipal")
	secretID := chi.URLParam(r, "secret")
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationRevokeServicePrincipalSecret(), func(tx access.Repository) (access.AuditEventInput, error) {
		mutationErr := tx.RevokeServicePrincipalSecret(r.Context(), servicePrincipalID, secretID)
		return auditInput(r, "service_principal_secret.revoked", h.currentPrincipalID(r), "service_principal", servicePrincipalID, "", "success", map[string]any{"secretId": secretID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRevokeServicePrincipalSecret(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) RotateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var request struct {
		Name           string `json:"name"`
		ExpiresAt      string `json:"expiresAt"`
		RevokePrevious bool   `json:"revokePrevious"`
	}
	if err := decodeStrictJSON(r, &request); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	var expiresAt time.Time
	if strings.TrimSpace(request.ExpiresAt) != "" {
		parsed, parseErr := time.Parse(time.RFC3339, request.ExpiresAt)
		if parseErr != nil {
			writeJSONError(w, parseErr, stdhttp.StatusBadRequest)
			return
		}
		expiresAt = parsed
	}
	var err error
	expiresAt, err = access.ResolveServicePrincipalSecretExpiry(expiresAt, time.Now().UTC())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	principalID := chi.URLParam(r, "servicePrincipal")
	previousID := chi.URLParam(r, "secret")
	var rotation access.ServicePrincipalSecretRotation
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationRotateServicePrincipalSecret(), func(tx access.Repository) (access.AuditEventInput, error) {
		lifecycle, ok := tx.(interface {
			RotateServicePrincipalSecret(context.Context, access.ServicePrincipalSecretRotationInput) (access.ServicePrincipalSecretRotation, error)
		})
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("service principal secret rotation is unavailable")
		}
		var mutationErr error
		rotation, mutationErr = lifecycle.RotateServicePrincipalSecret(r.Context(), access.ServicePrincipalSecretRotationInput{
			ServicePrincipalID: principalID,
			PreviousSecretID:   previousID,
			Secret:             access.ServicePrincipalSecretInput{Name: request.Name, ExpiresAt: expiresAt},
			RevokePrevious:     request.RevokePrevious,
		})
		return auditInput(r, "service_principal_secret.rotated", h.currentPrincipalID(r), "service_principal", principalID, "", "success", map[string]any{"secretId": rotation.Created.ID, "previousSecretId": previousID, "revokePrevious": request.RevokePrevious}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRotateServicePrincipalSecret(), err, statusForNotFound(err))
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"secret": rotation.Secret, "clientSecret": servicePrincipalSecretDTO(rotation.Created, "")})
}

func (h Handler) RevokeAllServicePrincipalCredentials(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if err := decodeStrictJSON(r, &request); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if len(strings.TrimSpace(request.Reason)) > 512 {
		writeJSONError(w, fmt.Errorf("reason is too long"), stdhttp.StatusBadRequest)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "servicePrincipal")
	err = executeAuditedMutation(r, repo, accessgen.GenCommandOperationRevokeAllServicePrincipalCredentials(), func(tx access.Repository) (access.AuditEventInput, error) {
		lifecycle, ok := tx.(access.ServicePrincipalCredentialRepository)
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("service principal credential revocation is unavailable")
		}
		mutationErr := lifecycle.RevokeAllServicePrincipalCredentials(r.Context(), id)
		return auditInput(r, "service_principal_credentials.revoked_all", h.currentPrincipalID(r), "service_principal", id, "", "success", map[string]any{"reason": strings.TrimSpace(request.Reason), "credentialClasses": []string{"sessions", "api_tokens", "service_secrets", "authoring_sessions", "oauth_sessions"}}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRevokeAllServicePrincipalCredentials(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
