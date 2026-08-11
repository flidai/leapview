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

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	accessapi "github.com/flidai/leapview/internal/access/api"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	httpmodel "github.com/flidai/leapview/internal/platform/http/model"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/go-chi/chi/v5"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
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
type WorkspaceIDNormalizer func(string) string

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
	Repository          RepositoryProvider
	RoleBindingCommands access.RoleBindingCommander
	GrantCommands       access.GrantCommander
	CurrentPrincipal    PrincipalProvider
	CurrentCredential   CredentialProvider
	WorkspaceID         WorkspaceIDNormalizer
	AuthoringAuth       AuthoringAuthentication
}

func (h Handler) GetCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if h.Repository != nil {
		if repo, err := h.repository(); err == nil {
			if stored, err := repo.PrincipalByID(r.Context(), principal.ID); err == nil {
				writeJSON(w, stdhttp.StatusOK, principalDTO(stored))
				return
			}
		}
	}
	writeJSON(w, stdhttp.StatusOK, currentPrincipalDTO(principal))
}

func (h Handler) ListCurrentEffectivePrivileges(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	workspaceID := h.workspaceID(r.URL.Query().Get("workspace"))
	privileges, err := repo.EffectivePrivileges(r.Context(), principal.ID, access.WorkspaceObject(workspaceID))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	allowed := make([]string, 0, len(privileges))
	for _, privilege := range privileges {
		if credential, ok := h.currentCredential(r); ok && !apiTokenAllows(credential.Token, workspaceID, privilege) {
			continue
		}
		allowed = append(allowed, string(privilege))
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"workspaceId": workspaceID, "privileges": allowed})
}

func (h Handler) ListCurrentAPITokens(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListAPITokens(r.Context(), principal.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, apiTokenDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) CreateCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateCurrentAPIToken(), fmt.Errorf("authenticated principal is required"))
		return
	}
	var input struct {
		Name        string   `json:"name"`
		WorkspaceID string   `json:"workspaceId"`
		Privileges  []string `json:"privileges"`
		ExpiresAt   string   `json:"expiresAt"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateCurrentAPIToken(), apigenfailure.Wrap("invalid", err))
		return
	}
	var expiresAt time.Time
	if strings.TrimSpace(input.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, input.ExpiresAt)
		if err != nil {
			writeCommandFailure(w, r, accessgen.GenCommandOperationCreateCurrentAPIToken(), apigenfailure.Wrap("invalid", err))
			return
		}
		expiresAt = parsed
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateCurrentAPIToken(), err)
		return
	}
	var token string
	var row access.APIToken
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		token, row, mutationErr = txRepo.CreateAPITokenWithMetadata(r.Context(), access.APITokenInput{
			PrincipalID: principal.ID, WorkspaceID: input.WorkspaceID, Name: input.Name,
			Privileges: privilegesFromStrings(input.Privileges), ExpiresAt: expiresAt,
		})
		return accessAuditInput(r, "api_token.created", principal.ID, row.WorkspaceID, "api_token", row.ID, access.PrivilegeManageGrants, "success", map[string]any{"name": row.Name, "privileges": row.Privileges}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateCurrentAPIToken(), err, stdhttp.StatusBadRequest)
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"token": token, "apiToken": apiTokenDTO(row)})
}

func (h Handler) RevokeCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRevokeCurrentAPIToken(), fmt.Errorf("authenticated principal is required"))
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRevokeCurrentAPIToken(), err)
		return
	}
	tokenID := chi.URLParam(r, "token")
	var revoked access.APIToken
	if rows, err := repo.ListAPITokens(r.Context(), principal.ID); err == nil {
		for _, row := range rows {
			if row.ID == tokenID {
				revoked = row
				break
			}
		}
	}
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.RevokeAPITokenForPrincipal(r.Context(), principal.ID, tokenID)
		return accessAuditInput(r, "api_token.revoked", principal.ID, revoked.WorkspaceID, "api_token", tokenID, access.PrivilegeManageGrants, "success", map[string]any{"name": revoked.Name, "privileges": revoked.Privileges}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRevokeCurrentAPIToken(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListCurrentSessions(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	h.listPrincipalSessions(w, r, principal.ID)
}

func (h Handler) ListPrincipalSessions(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.listPrincipalSessions(w, r, chi.URLParam(r, "principal"))
}

func (h Handler) listPrincipalSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, principalID string) {
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
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) RevokeCurrentSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRevokeCurrentSession(), fmt.Errorf("authenticated principal is required"))
		return
	}
	h.revokePrincipalSession(w, r, accessgen.GenCommandOperationRevokeCurrentSession(), principal.ID, principal.ID, access.PrivilegeUseWorkspace, nil)
}

func (h Handler) RevokePrincipalSession(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	targetPrincipalID := chi.URLParam(r, "principal")
	h.revokePrincipalSession(
		w,
		r,
		accessgen.GenCommandOperationRevokePrincipalSession(),
		h.currentPrincipalID(r),
		targetPrincipalID,
		access.PrivilegeManageGrants,
		map[string]any{"targetPrincipalId": targetPrincipalID},
	)
}

func (h Handler) revokePrincipalSession(
	w stdhttp.ResponseWriter,
	r *stdhttp.Request,
	operationID accessgen.GenCommandOperationID,
	actorPrincipalID string,
	targetPrincipalID string,
	privilege access.Privilege,
	metadata map[string]any,
) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, operationID, err)
		return
	}
	sessionID := chi.URLParam(r, "session")
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.RevokeSessionForPrincipal(r.Context(), targetPrincipalID, sessionID)
		return accessAuditInput(r, "session.revoked", actorPrincipalID, "", "session", sessionID, privilege, "success", metadata), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, operationID, err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListPrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if _, ok := apiLimitForRequest(w, r); !ok {
		return
	}
	if _, ok := apiCursorKeyForRequest(w, r); !ok {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if repo == nil {
		_ = writePagedJSON(w, r, []map[string]any{})
		return
	}
	rows, err := repo.ListPrincipals(r.Context(), access.PrincipalFilter{
		Email: r.URL.Query().Get("email"),
		Query: r.URL.Query().Get("q"),
	})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, principalDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) CreatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := decodeStrictJSON(r, &input); err != nil {
			writeCommandFailure(w, r, accessgen.GenCommandOperationCreatePrincipal(), err)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeCommandFailure(w, r, accessgen.GenCommandOperationCreatePrincipal(), err)
			return
		}
		input.Email = r.Form.Get("email")
		input.DisplayName = r.Form.Get("displayName")
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreatePrincipal(), err)
		return
	}
	var created access.LocalPasswordReset
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		created, mutationErr = txRepo.CreateLocalUser(r.Context(), access.LocalUserInput{Email: input.Email, DisplayName: input.DisplayName, MustChange: true})
		return accessAuditInput(r, "principal.local_user.created", h.currentPrincipalID(r), "", "principal", created.Principal.ID, access.PrivilegeManageGrants, "success", map[string]any{"email": created.Principal.Email}), mutationErr
	})
	if err != nil {
		if errors.Is(err, access.ErrPrincipalAlreadyExists) {
			email := access.NormalizeEmail(input.Email)
			audit := accessAuditInput(
				r,
				"principal.local_user.create_rejected",
				h.currentPrincipalID(r),
				"",
				"principal",
				access.PrincipalIDForEmail(email),
				access.PrivilegeManageGrants,
				"conflict",
				map[string]any{"email": email, "reason": "duplicate"},
			)
			if auditErr := access.PersistAuditEvent(r.Context(), repo, audit); auditErr != nil {
				writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreatePrincipal(), fmt.Errorf("%w: %v", access.ErrAuditTransaction, auditErr), stdhttp.StatusInternalServerError)
				return
			}
			writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreatePrincipal(), err, stdhttp.StatusConflict)
			return
		}
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreatePrincipal(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, localPasswordResetDTO(created))
}

func (h Handler) GetPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	principal, err := repo.PrincipalByID(r.Context(), chi.URLParam(r, "principal"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(principal))
}

func (h Handler) DeletePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), err)
		return
	}
	id := chi.URLParam(r, "principal")
	existing, err := repo.PrincipalByID(r.Context(), id)
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), err)
		return
	}
	if !principalKindAllowsGenericMutation(existing.Kind) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), apigenfailure.New("invalid", fmt.Sprintf("principal kind %q is managed by its owning subsystem", existing.Kind)))
		return
	}
	if _, ok := repo.(interface {
		DeletePrincipal(context.Context, string) error
	}); !ok {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeletePrincipal(), fmt.Errorf("principal deletion is unavailable"))
		return
	}
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		txDeleter, ok := txRepo.(interface {
			DeletePrincipal(context.Context, string) error
		})
		if !ok {
			return access.AuditEventInput{}, fmt.Errorf("principal deletion is unavailable")
		}
		mutationErr := txDeleter.DeletePrincipal(r.Context(), id)
		return accessAuditInput(r, "principal.deleted", h.currentPrincipalID(r), "", "principal", id, access.PrivilegeManageGrants, "success", map[string]any{
			"email": existing.Email, "kind": string(existing.Kind), "displayName": existing.DisplayName,
		}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeletePrincipal(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ResetPrincipalPassword(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), err)
		return
	}
	var reset access.LocalPasswordReset
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		reset, mutationErr = txRepo.ResetLocalPassword(r.Context(), chi.URLParam(r, "principal"))
		return accessAuditInput(r, "principal.local_password.reset", h.currentPrincipalID(r), "", "principal", reset.Principal.ID, access.PrivilegeManageGrants, "success", map[string]any{"email": reset.Principal.Email}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationResetPrincipalPassword(), err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, localPasswordResetDTO(reset))
}

func (h Handler) UpdatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err)
		return
	}
	existing, err := repo.PrincipalByID(r.Context(), chi.URLParam(r, "principal"))
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err)
		return
	}
	if !principalKindAllowsGenericMutation(existing.Kind) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdatePrincipal(), apigenfailure.New("invalid", fmt.Sprintf("principal kind %q is managed by its owning subsystem", existing.Kind)))
		return
	}
	if !requireIfMatch(w, r, resourceETag(principalDTO(existing))) {
		return
	}
	if strings.TrimSpace(input.DisplayName) != "" {
		existing.DisplayName = input.DisplayName
	}
	var principal access.Principal
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		principal, mutationErr = txRepo.UpsertPrincipal(r.Context(), access.PrincipalInput{ID: existing.ID, Kind: existing.Kind, Email: existing.Email, DisplayName: existing.DisplayName})
		return accessAuditInput(r, "principal.updated", h.currentPrincipalID(r), "", "principal", principal.ID, access.PrivilegeManageGrants, "success", map[string]any{
			"email": principal.Email, "kind": string(principal.Kind), "displayName": principal.DisplayName,
		}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdatePrincipal(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(principal))
}

// OAuthToken issues the existing REST API credential used by service-principal
// automation. MCP tokens share the public token endpoint but are routed to the
// MCP authorization server before this handler is called.
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
		WorkspaceID  string `json:"workspace_id"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := decodeStrictJSON(r, &input); err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, err, stdhttp.StatusBadRequest)
			return
		}
		input.GrantType = r.Form.Get("grant_type")
		input.ClientID = r.Form.Get("client_id")
		input.ClientSecret = r.Form.Get("client_secret")
		input.Scope = r.Form.Get("scope")
		input.WorkspaceID = r.Form.Get("workspace_id")
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
	privileges, err := privilegesFromOAuthScope(input.Scope)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	var token string
	var row access.APIToken
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		token, row, mutationErr = txRepo.CreateAPITokenWithMetadata(r.Context(), access.APITokenInput{
			PrincipalID: principal.ID, WorkspaceID: input.WorkspaceID, Name: "oauth-client-credentials",
			Privileges: privileges, ExpiresAt: time.Now().Add(ttl),
		})
		return accessAuditInput(r, "oauth.token.created", principal.ID, input.WorkspaceID, "api_token", row.ID, "", "success", map[string]any{"grantType": "client_credentials"}), mutationErr
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
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, principalDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) GetServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
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
	principal, _ := h.currentPrincipal(r)
	var input struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateServicePrincipal(), err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateServicePrincipal(), err)
		return
	}
	var row access.Principal
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		row, mutationErr = txRepo.CreateServicePrincipal(r.Context(), access.ServicePrincipalInput{ID: input.ID, DisplayName: input.DisplayName})
		return accessAuditInput(r, "service_principal.created", principal.ID, "", "service_principal", row.ID, access.PrivilegeManagePlatform, "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateServicePrincipal(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, principalDTO(row))
}

func (h Handler) UpdateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateServicePrincipal(), err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateServicePrincipal(), err)
		return
	}
	existing, err := repo.PrincipalByID(r.Context(), chi.URLParam(r, "servicePrincipal"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateServicePrincipal(), err)
		return
	}
	if existing.Kind != access.PrincipalKindServicePrincipal {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateServicePrincipal(), apigenfailure.Wrap("not_found", sql.ErrNoRows))
		return
	}
	if !requireIfMatch(w, r, resourceETag(principalDTO(existing))) {
		return
	}
	var row access.Principal
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		row, mutationErr = txRepo.UpdateServicePrincipal(r.Context(), chi.URLParam(r, "servicePrincipal"), access.ServicePrincipalInput{DisplayName: input.DisplayName})
		return accessAuditInput(r, "service_principal.updated", principal.ID, "", "service_principal", row.ID, access.PrivilegeManagePlatform, "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateServicePrincipal(), err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, principalDTO(row))
}

func (h Handler) DeleteServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteServicePrincipal(), err)
		return
	}
	id := chi.URLParam(r, "servicePrincipal")
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.DeleteServicePrincipal(r.Context(), id)
		return accessAuditInput(r, "service_principal.deleted", principal.ID, "", "service_principal", id, access.PrivilegeManagePlatform, "success", nil), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteServicePrincipal(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) CreateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	var input struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateServicePrincipalSecret(), err)
		return
	}
	var expiresAt time.Time
	if strings.TrimSpace(input.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, input.ExpiresAt)
		if err != nil {
			writeCommandFailure(w, r, accessgen.GenCommandOperationCreateServicePrincipalSecret(), err)
			return
		}
		expiresAt = parsed
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateServicePrincipalSecret(), err)
		return
	}
	var rawSecret string
	var row access.ServicePrincipalSecret
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		rawSecret, row, mutationErr = txRepo.CreateServicePrincipalSecret(r.Context(), chi.URLParam(r, "servicePrincipal"), access.ServicePrincipalSecretInput{Name: input.Name, ExpiresAt: expiresAt})
		return accessAuditInput(r, "service_principal_secret.created", principal.ID, "", "service_principal", row.ServicePrincipalID, access.PrivilegeManagePlatform, "success", map[string]any{"secretId": row.ID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateServicePrincipalSecret(), err, stdhttp.StatusBadRequest)
		return
	}
	writeSecretJSON(w, stdhttp.StatusCreated, map[string]any{"secret": rawSecret, "clientSecret": servicePrincipalSecretDTO(row, "")})
}

func (h Handler) ListServicePrincipalSecrets(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(interface {
		ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error)
	})
	if !ok {
		writeJSONError(w, fmt.Errorf("secret metadata is unavailable"), stdhttp.StatusServiceUnavailable)
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
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(interface {
		GetServicePrincipalSecret(context.Context, string, string) (access.ServicePrincipalSecret, error)
	})
	if !ok {
		writeJSONError(w, fmt.Errorf("secret metadata is unavailable"), stdhttp.StatusServiceUnavailable)
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
	principal, _ := h.currentPrincipal(r)
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRevokeServicePrincipalSecret(), err)
		return
	}
	servicePrincipalID := chi.URLParam(r, "servicePrincipal")
	secretID := chi.URLParam(r, "secret")
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.RevokeServicePrincipalSecret(r.Context(), servicePrincipalID, secretID)
		return accessAuditInput(r, "service_principal_secret.revoked", principal.ID, "", "service_principal", servicePrincipalID, access.PrivilegeManagePlatform, "success", map[string]any{"secretId": secretID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRevokeServicePrincipalSecret(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListGroups(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGroups(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) CreateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateGroup(), err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateGroup(), err)
		return
	}
	name := firstNonEmpty(input.DisplayName, input.Name)
	var group access.Group
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		group, mutationErr = txRepo.UpsertGroup(r.Context(), access.GroupInput{WorkspaceID: h.workspaceID(chi.URLParam(r, "workspace")), Provider: "local", ExternalID: input.Name, Name: name})
		return accessAuditInput(r, "group.created", h.currentPrincipalID(r), group.WorkspaceID, "group", group.ID, access.PrivilegeManageGrants, "success", groupAuditMetadata(group)), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateGroup(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, groupDTO(group))
}

func (h Handler) GetGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	group, ok := h.groupByID(w, r, nil)
	if !ok {
		return
	}
	writeJSON(w, stdhttp.StatusOK, groupDTO(group))
}

func (h Handler) UpdateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGroup(), err)
		return
	}
	group, ok := h.groupByID(w, r, commandOperation(accessgen.GenCommandOperationUpdateGroup()))
	if !ok {
		return
	}
	if !requireIfMatch(w, r, resourceETag(groupDTO(group))) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGroup(), err)
		return
	}
	var updated access.Group
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		updated, mutationErr = txRepo.UpsertGroup(r.Context(), access.GroupInput{ID: group.ID, WorkspaceID: group.WorkspaceID, Provider: group.Provider, ExternalID: group.ExternalID, Name: firstNonEmpty(input.DisplayName, group.Name)})
		return accessAuditInput(r, "group.updated", h.currentPrincipalID(r), updated.WorkspaceID, "group", updated.ID, access.PrivilegeManageGrants, "success", groupAuditMetadata(updated)), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateGroup(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusOK, groupDTO(updated))
}

func (h Handler) DeleteGroup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	group, ok := h.groupByID(w, r, commandOperation(accessgen.GenCommandOperationDeleteGroup()))
	if !ok {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteGroup(), err)
		return
	}
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.DeleteGroup(r.Context(), group.WorkspaceID, group.ID)
		return accessAuditInput(r, "group.deleted", h.currentPrincipalID(r), group.WorkspaceID, "group", group.ID, access.PrivilegeManageGrants, "success", groupAuditMetadata(group)), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteGroup(), err, stdhttp.StatusBadRequest)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListGroupMembers(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGroupMembers(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")), chi.URLParam(r, "group"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupMemberPrincipalDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) AddGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationAddGroupMember(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	groupID := chi.URLParam(r, "group")
	principalID := chi.URLParam(r, "principal")
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.AddGroupMember(r.Context(), workspaceID, groupID, principalID)
		return accessAuditInput(r, "group.member_added", h.currentPrincipalID(r), workspaceID, "group_member", groupID+":"+principalID, access.PrivilegeManageGrants, "success", map[string]any{"groupId": groupID, "memberPrincipalId": principalID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationAddGroupMember(), err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "added"})
}

func (h Handler) RemoveGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationRemoveGroupMember(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	groupID := chi.URLParam(r, "group")
	principalID := chi.URLParam(r, "principal")
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.RemoveGroupMember(r.Context(), workspaceID, groupID, principalID)
		return accessAuditInput(r, "group.member_removed", h.currentPrincipalID(r), workspaceID, "group_member", groupID+":"+principalID, access.PrivilegeManageGrants, "success", map[string]any{"groupId": groupID, "memberPrincipalId": principalID}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationRemoveGroupMember(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListWorkspaceRoles(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	roles, err := repo.ListRoles(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]accessapi.RoleResponse, 0, len(roles))
	for _, role := range roles {
		out = append(out, accessapi.RoleResponse{Name: role.Name, Privileges: privilegeStrings(role.Privileges)})
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) ListRoleBindings(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if repo == nil {
		_ = writePagedJSON(w, r, []map[string]any{})
		return
	}
	bindings, err := repo.ListRoleBindings(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, apiRoleBindingDTO(binding))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) GetRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.GetRoleBinding(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")), chi.URLParam(r, "binding"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, apiRoleBindingDTO(row))
}

func (h Handler) CreateRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	input, ok := decodeRoleBindingInput(w, r, accessgen.GenCommandOperationCreateRoleBinding())
	if !ok {
		return
	}
	commands := h.RoleBindingCommands
	if commands == nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateRoleBinding(), fmt.Errorf("role binding command service is not configured"))
		return
	}
	row, err := commands.CreateRoleBinding(r.Context(), h.roleBindingInvocation(r), input)
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateRoleBinding(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, apiRoleBindingDTO(row))
}

func (h Handler) ListEffectivePrivileges(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	object, ok := objectRefFromRequest(w, r)
	if !ok {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	effective, err := repo.EffectiveAccess(r.Context(), principal.ID, object)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	allowed := make([]string, 0, len(effective))
	explanations := make([]map[string]any, 0, len(effective))
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	for _, decision := range effective {
		if credential, ok := h.currentCredential(r); ok && !apiTokenAllows(credential.Token, workspaceID, decision.Privilege) {
			continue
		}
		allowed = append(allowed, string(decision.Privilege))
		explanations = append(explanations, authorizationDecisionDTO(decision))
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{
		"workspaceId":     object.WorkspaceID,
		"objectType":      string(object.Type),
		"objectId":        emptyToNil(object.ObjectID),
		"privileges":      allowed,
		"effectiveGrants": explanations,
	})
}

func (h Handler) ListGrants(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	object, ok := objectRefFromRequest(w, r)
	if !ok {
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, object) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListGrantsWithOptions(r.Context(), object, parseBoolQuery(r.URL.Query().Get("includeInherited")))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, grantViewDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) CreateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var input struct {
		ObjectType  string `json:"objectType"`
		ObjectID    string `json:"objectId"`
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Privilege   string `json:"privilege"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateGrant(), err)
		return
	}
	object, ok := objectRefFromValues(w, r, commandOperation(accessgen.GenCommandOperationCreateGrant()), input.ObjectType, input.ObjectID)
	if !ok {
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, object) {
		return
	}
	subjectType := access.SubjectType(strings.TrimSpace(input.SubjectType))
	if !knownGrantSubjectType(subjectType) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateGrant(), apigenfailure.New("invalid", fmt.Sprintf("unsupported subject type %q", input.SubjectType)))
		return
	}
	privilege := access.Privilege(strings.TrimSpace(input.Privilege))
	if !knownPrivilege(privilege) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateGrant(), apigenfailure.New("invalid", fmt.Sprintf("unsupported privilege %q", input.Privilege)))
		return
	}
	commands := h.GrantCommands
	if commands == nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateGrant(), fmt.Errorf("grant command service is not configured"))
		return
	}
	grant, err := commands.CreateGrant(r.Context(), h.grantInvocation(r), access.GrantInput{
		Object: object, SubjectType: subjectType, SubjectID: input.SubjectID, Privilege: privilege,
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateGrant(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, grantDTO(grant))
}

func (h Handler) GetGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.GetGrant(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")), chi.URLParam(r, "grant"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, objectRefFromGrant(row)) {
		return
	}
	dto := grantDTO(row)
	w.Header().Set("ETag", resourceETag(dto))
	writeJSON(w, stdhttp.StatusOK, dto)
}

func (h Handler) UpdateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGrant(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	id := chi.URLParam(r, "grant")
	current, err := repo.GetGrant(r.Context(), workspaceID, id)
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGrant(), err)
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, objectRefFromGrant(current)) {
		return
	}
	if !requireIfMatch(w, r, resourceETag(grantDTO(current))) {
		return
	}
	var input struct {
		ObjectType, ObjectID, SubjectType, SubjectID, Privilege string
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGrant(), err)
		return
	}
	object, ok := objectRefFromValues(w, r, commandOperation(accessgen.GenCommandOperationUpdateGrant()), input.ObjectType, input.ObjectID)
	if !ok {
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, object) {
		return
	}
	subjectType := access.SubjectType(strings.TrimSpace(input.SubjectType))
	privilege := access.Privilege(strings.TrimSpace(input.Privilege))
	if !knownGrantSubjectType(subjectType) || !knownPrivilege(privilege) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGrant(), apigenfailure.New("invalid", "unsupported grant subject or privilege"))
		return
	}
	commands := h.GrantCommands
	if commands == nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateGrant(), fmt.Errorf("grant command service is not configured"))
		return
	}
	updated, err := commands.UpdateGrant(r.Context(), h.grantInvocation(r), workspaceID, id, access.GrantInput{
		Object: object, SubjectType: subjectType, SubjectID: input.SubjectID, Privilege: privilege,
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateGrant(), err, stdhttp.StatusUnprocessableEntity)
		return
	}
	dto := grantDTO(updated)
	w.Header().Set("ETag", resourceETag(dto))
	writeJSON(w, stdhttp.StatusOK, dto)
}

func (h Handler) DeleteGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteGrant(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	grant, err := repo.GetGrant(r.Context(), workspaceID, chi.URLParam(r, "grant"))
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteGrant(), err)
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, objectRefFromGrant(grant)) {
		return
	}
	commands := h.GrantCommands
	if commands == nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteGrant(), fmt.Errorf("grant command service is not configured"))
		return
	}
	_, err = commands.DeleteGrant(r.Context(), h.grantInvocation(r), workspaceID, grant.ID)
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteGrant(), err, stdhttp.StatusBadRequest)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) ListDataPolicies(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	object, ok := objectRefFromRequest(w, r)
	if !ok {
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, object) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	rows, err := repo.ListDataPoliciesWithOptions(r.Context(), object, parseBoolQuery(r.URL.Query().Get("includeInherited")))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, dataPolicyDTO(row))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) CreateDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	var input struct {
		ObjectType  string         `json:"objectType"`
		ObjectID    string         `json:"objectId"`
		SubjectType string         `json:"subjectType"`
		SubjectID   string         `json:"subjectId"`
		PolicyType  string         `json:"policyType"`
		Expression  map[string]any `json:"expression"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), err)
		return
	}
	object, ok := objectRefFromValues(w, r, commandOperation(accessgen.GenCommandOperationCreateDataPolicy()), input.ObjectType, input.ObjectID)
	if !ok {
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, object) {
		return
	}
	if !knownDataPolicyType(input.PolicyType) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), apigenfailure.New("invalid", fmt.Sprintf("unsupported policyType %q", input.PolicyType)))
		return
	}
	subjectType := access.SubjectType(strings.TrimSpace(input.SubjectType))
	subjectID := strings.TrimSpace(input.SubjectID)
	if subjectType != "" && !knownDataPolicySubjectType(subjectType) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), apigenfailure.New("invalid", fmt.Sprintf("unsupported subjectType %q", input.SubjectType)))
		return
	}
	if subjectType != "" && subjectID == "" {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), apigenfailure.New("invalid", "subjectId is required when subjectType is set"))
		return
	}
	if subjectType == "" && subjectID != "" {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), apigenfailure.New("invalid", "subjectType is required when subjectId is set"))
		return
	}
	expression, err := json.Marshal(input.Expression)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), err)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationCreateDataPolicy(), err)
		return
	}
	var row access.DataPolicy
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		row, mutationErr = txRepo.UpsertDataPolicy(r.Context(), access.DataPolicyInput{Object: object, SubjectType: subjectType, SubjectID: subjectID, PolicyType: input.PolicyType, ExpressionJSON: string(expression)})
		return accessAuditInput(r, "data_policy.created", principal.ID, row.WorkspaceID, "data_policy", row.ID, access.PrivilegeManageGrants, "success", map[string]any{"objectId": row.ObjectID, "policyType": row.PolicyType}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationCreateDataPolicy(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, dataPolicyDTO(row))
}

func (h Handler) GetDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	row, err := repo.GetDataPolicy(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")), chi.URLParam(r, "policy"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, objectRefFromCanonical(row.WorkspaceID, row.ObjectID)) {
		return
	}
	dto := dataPolicyDTO(row)
	w.Header().Set("ETag", resourceETag(dto))
	writeJSON(w, stdhttp.StatusOK, dto)
}

func (h Handler) UpdateDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	id := chi.URLParam(r, "policy")
	current, err := repo.GetDataPolicy(r.Context(), workspaceID, id)
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), err)
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, objectRefFromCanonical(current.WorkspaceID, current.ObjectID)) {
		return
	}
	if !requireIfMatch(w, r, resourceETag(dataPolicyDTO(current))) {
		return
	}
	var input struct {
		ObjectType  string         `json:"objectType"`
		ObjectID    string         `json:"objectId"`
		SubjectType string         `json:"subjectType"`
		SubjectID   string         `json:"subjectId"`
		PolicyType  string         `json:"policyType"`
		Expression  map[string]any `json:"expression"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), err)
		return
	}
	object, ok := objectRefFromValues(w, r, commandOperation(accessgen.GenCommandOperationUpdateDataPolicy()), input.ObjectType, input.ObjectID)
	if !ok {
		return
	}
	if !knownDataPolicyType(input.PolicyType) {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), apigenfailure.New("invalid", fmt.Sprintf("unsupported policyType %q", input.PolicyType)))
		return
	}
	subjectType := access.SubjectType(strings.TrimSpace(input.SubjectType))
	if subjectType != "" && (!knownDataPolicySubjectType(subjectType) || strings.TrimSpace(input.SubjectID) == "") {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), apigenfailure.New("invalid", "invalid data policy subject"))
		return
	}
	expression, err := json.Marshal(input.Expression)
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), err)
		return
	}
	var updated access.DataPolicy
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		updated, mutationErr = txRepo.UpsertDataPolicy(r.Context(), access.DataPolicyInput{ID: id, Object: object, SubjectType: subjectType, SubjectID: input.SubjectID, PolicyType: input.PolicyType, ExpressionJSON: string(expression)})
		return accessAuditInput(r, "data_policy.updated", principal.ID, updated.WorkspaceID, "data_policy", updated.ID, access.PrivilegeManageGrants, "success", map[string]any{"objectId": updated.ObjectID, "policyType": updated.PolicyType}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateDataPolicy(), err, stdhttp.StatusUnprocessableEntity)
		return
	}
	dto := dataPolicyDTO(updated)
	w.Header().Set("ETag", resourceETag(dto))
	writeJSON(w, stdhttp.StatusOK, dto)
}

func (h Handler) CheckAuthorizationBatch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authentication required"), stdhttp.StatusUnauthorized)
		return
	}
	var input struct {
		Checks []struct{ Privilege, ObjectType, ObjectID string } `json:"checks"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if len(input.Checks) == 0 || len(input.Checks) > 200 {
		writeJSONError(w, fmt.Errorf("checks must contain 1 to 200 items"), stdhttp.StatusUnprocessableEntity)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	checks := make([]access.AuthorizationCheck, 0, len(input.Checks))
	for _, item := range input.Checks {
		privilege := access.Privilege(strings.TrimSpace(item.Privilege))
		if !knownPrivilege(privilege) {
			writeJSONError(w, fmt.Errorf("unsupported privilege %q", item.Privilege), stdhttp.StatusUnprocessableEntity)
			return
		}
		object, valid := objectRefFromValues(w, r, nil, item.ObjectType, item.ObjectID)
		if !valid {
			return
		}
		object.WorkspaceID = workspaceID
		checks = append(checks, access.AuthorizationCheck{Privilege: privilege, Object: object})
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	decisions, err := repo.AuthorizeBatch(r.Context(), principal.ID, checks)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(decisions))
	for _, decision := range decisions {
		item := authorizationDecisionDTO(decision)
		item["allowed"] = decision.Allowed
		out = append(out, item)
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"decisions": out})
}

func (h Handler) DeleteDataPolicy(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteDataPolicy(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	row, err := repo.GetDataPolicy(r.Context(), workspaceID, chi.URLParam(r, "policy"))
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteDataPolicy(), err)
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageGrants, objectRefFromCanonical(row.WorkspaceID, row.ObjectID)) {
		return
	}
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		mutationErr := txRepo.DeleteDataPolicy(r.Context(), workspaceID, row.ID)
		return accessAuditInput(r, "data_policy.deleted", principal.ID, row.WorkspaceID, "data_policy", row.ID, access.PrivilegeManageGrants, "success", map[string]any{"objectId": row.ObjectID, "policyType": row.PolicyType}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteDataPolicy(), err, stdhttp.StatusBadRequest)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) TransferOwnership(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, _ := h.currentPrincipal(r)
	var input struct {
		ObjectType       string `json:"objectType"`
		ObjectID         string `json:"objectId"`
		OwnerPrincipalID string `json:"ownerPrincipalId"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationTransferOwnership(), err)
		return
	}
	object, ok := objectRefFromValues(w, r, commandOperation(accessgen.GenCommandOperationTransferOwnership()), input.ObjectType, input.ObjectID)
	if !ok {
		return
	}
	if !h.authorizeCurrentObject(w, r, access.PrivilegeManageItem, object) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationTransferOwnership(), err)
		return
	}
	var updated access.SecurableObject
	err = runAuditedMutation(r, repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		updated, mutationErr = txRepo.SetObjectOwner(r.Context(), object, input.OwnerPrincipalID)
		return accessAuditInput(r, "ownership.transferred", principal.ID, updated.WorkspaceID, "securable_object", updated.ID, access.PrivilegeManageItem, "success", map[string]any{"ownerPrincipalId": updated.OwnerPrincipalID, "objectType": string(updated.Type)}), mutationErr
	})
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationTransferOwnership(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusOK, securableObjectDTO(updated))
}

func (h Handler) UpdateRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateRoleBinding(), err)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	current, err := repo.GetRoleBinding(r.Context(), workspaceID, chi.URLParam(r, "binding"))
	if err != nil {
		status := statusForNotFound(err)
		if status == stdhttp.StatusNotFound {
			err = apigenfailure.Wrap("not_found", err)
		}
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateRoleBinding(), err)
		return
	}
	if !requireIfMatch(w, r, resourceETag(apiRoleBindingDTO(current))) {
		return
	}
	input, ok := decodeRoleBindingInput(w, r, accessgen.GenCommandOperationUpdateRoleBinding())
	if !ok {
		return
	}
	commands := h.RoleBindingCommands
	if commands == nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUpdateRoleBinding(), fmt.Errorf("role binding command service is not configured"))
		return
	}
	row, err := commands.UpdateRoleBinding(r.Context(), h.roleBindingInvocation(r), input.WorkspaceID, chi.URLParam(r, "binding"), input)
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateRoleBinding(), err, stdhttp.StatusBadRequest)
		return
	}
	writeJSON(w, stdhttp.StatusOK, apiRoleBindingDTO(row))
}

func (h Handler) DeleteRoleBinding(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	bindingID := chi.URLParam(r, "binding")
	commands := h.RoleBindingCommands
	if commands == nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteRoleBinding(), fmt.Errorf("role binding command service is not configured"))
		return
	}
	_, err := commands.DeleteRoleBinding(r.Context(), h.roleBindingInvocation(r), workspaceID, bindingID)
	if err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationDeleteRoleBinding(), err, statusForNotFound(err))
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) roleBindingInvocation(r *stdhttp.Request) access.RoleBindingInvocation {
	// The client surface is audit attribution only. Authorization is enforced
	// from the generated operation contract before this handler is dispatched.
	surface := access.OperationSurfaceAPI
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Invocation-Surface")), string(access.OperationSurfaceCLI)) ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Client")), string(access.OperationSurfaceCLI)) {
		surface = access.OperationSurfaceCLI
	}
	return access.RoleBindingInvocation{
		PrincipalID: h.currentPrincipalID(r), Surface: surface,
		RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r),
	}
}

func (h Handler) grantInvocation(r *stdhttp.Request) access.GrantInvocation {
	return h.roleBindingInvocation(r)
}

func (h Handler) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return
	}
	cursorTime, cursorID := decodeCursor(r.URL.Query().Get("pageToken"))
	rows, err := repo.ListAuditEvents(r.Context(), access.AuditEventFilter{
		WorkspaceID: h.workspaceID(chi.URLParam(r, "workspace")),
		PrincipalID: r.URL.Query().Get("actor"),
		Action:      r.URL.Query().Get("action"),
		TargetType:  r.URL.Query().Get("targetType"),
		TargetID:    r.URL.Query().Get("targetId"),
		From:        r.URL.Query().Get("from"),
		To:          r.URL.Query().Get("to"),
		CursorTime:  cursorTime,
		CursorID:    cursorID,
		Limit:       limit + 1,
	})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	nextCursor := ""
	if len(rows) > limit {
		last := rows[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, auditEventDTO(row))
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(out, nextCursor))
}

func (h Handler) repository() (access.Repository, error) {
	if h.Repository == nil {
		return nil, nil
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

func (h Handler) workspaceID(value string) string {
	if h.WorkspaceID == nil {
		return strings.TrimSpace(value)
	}
	return h.WorkspaceID(value)
}

func principalDTO(row access.Principal) map[string]any {
	return map[string]any{"id": row.ID, "kind": string(row.Kind), "email": row.Email, "displayName": row.DisplayName, "createdAt": normalizeTimestamp(row.CreatedAt), "updatedAt": normalizeTimestamp(row.UpdatedAt)}
}

func localPasswordResetDTO(row access.LocalPasswordReset) map[string]any {
	return map[string]any{"principal": principalDTO(row.Principal), "temporaryPassword": row.Password}
}

func currentPrincipalDTO(row Principal) map[string]any {
	kind := row.Kind
	if kind == "" {
		kind = access.PrincipalKindUser
	}
	return map[string]any{"id": row.ID, "kind": string(kind), "email": row.Email, "displayName": row.DisplayName, "createdAt": normalizeTimestamp(row.CreatedAt), "updatedAt": normalizeTimestamp(row.UpdatedAt)}
}

func normalizeTimestamp(value string) string {
	if normalized, err := apitransport.NormalizeTimestamp(value); err == nil {
		return normalized
	}
	return ""
}

func groupDTO(row access.Group) map[string]any {
	return map[string]any{"id": row.ID, "name": row.ExternalID, "displayName": row.Name, "createdAt": row.CreatedAt, "updatedAt": row.CreatedAt}
}

func groupMemberPrincipalDTO(row access.GroupMember) map[string]any {
	return map[string]any{"id": row.PrincipalID, "kind": string(row.Kind), "email": row.Email, "displayName": row.DisplayName, "createdAt": normalizeTimestamp(row.CreatedAt), "updatedAt": normalizeTimestamp(row.CreatedAt)}
}

func apiRoleBindingDTO(row access.RoleBinding) map[string]any {
	return map[string]any{"id": row.ID, "workspaceId": row.WorkspaceID, "subjectType": string(row.SubjectType), "subjectId": row.SubjectID, "email": row.Email, "displayName": firstNonEmpty(row.DisplayName, row.GroupName), "role": row.Role, "createdAt": row.CreatedAt}
}

func groupAuditMetadata(row access.Group) map[string]any {
	return map[string]any{"provider": row.Provider, "externalId": row.ExternalID, "displayName": row.Name}
}

func grantDTO(row access.Grant) map[string]any {
	return map[string]any{
		"id":          row.ID,
		"objectId":    row.ObjectID,
		"objectType":  string(row.ObjectType),
		"workspaceId": row.WorkspaceID,
		"subjectType": string(row.SubjectType),
		"subjectId":   row.SubjectID,
		"privilege":   string(row.Privilege),
		"createdAt":   row.CreatedAt,
	}
}

func grantViewDTO(row access.GrantView) map[string]any {
	out := grantDTO(row.Grant)
	out["inherited"] = row.Inherited
	out["parentId"] = emptyToNil(row.ParentID)
	out["parentType"] = emptyToNil(string(row.ParentType))
	out["parentObject"] = emptyToNil(row.ParentObject)
	return out
}

func dataPolicyDTO(row access.DataPolicy) map[string]any {
	var expression map[string]any
	if err := json.Unmarshal([]byte(row.ExpressionJSON), &expression); err != nil || expression == nil {
		expression = map[string]any{}
	}
	return map[string]any{
		"id":          row.ID,
		"workspaceId": row.WorkspaceID,
		"objectId":    row.ObjectID,
		"subjectType": emptyToNil(string(row.SubjectType)),
		"subjectId":   emptyToNil(row.SubjectID),
		"policyType":  row.PolicyType,
		"expression":  expression,
		"createdAt":   row.CreatedAt,
		"updatedAt":   row.UpdatedAt,
	}
}

func securableObjectDTO(row access.SecurableObject) map[string]any {
	return map[string]any{
		"id":               row.ID,
		"type":             string(row.Type),
		"workspaceId":      row.WorkspaceID,
		"parentId":         emptyToNil(row.ParentID),
		"ownerPrincipalId": emptyToNil(row.OwnerPrincipalID),
		"displayName":      emptyToNil(row.DisplayName),
		"createdAt":        row.CreatedAt,
		"updatedAt":        row.UpdatedAt,
	}
}

func authorizationDecisionDTO(row access.AuthorizationDecision) map[string]any {
	return map[string]any{
		"privilege":     string(row.Privilege),
		"reason":        string(row.Reason),
		"objectType":    string(row.Object.Type),
		"objectId":      emptyToNil(row.Object.ObjectID),
		"grantId":       emptyToNil(row.GrantID),
		"grantObjectId": emptyToNil(row.GrantObjectID),
		"subjectType":   emptyToNil(string(row.SubjectType)),
		"subjectId":     emptyToNil(row.SubjectID),
		"inherited":     row.Inherited,
		"owner":         row.Owner,
		"platform":      row.Platform,
	}
}

func apiTokenDTO(row access.APIToken) map[string]any {
	return map[string]any{"id": row.ID, "name": row.Name, "workspaceId": row.WorkspaceID, "privileges": row.Privileges, "expiresAt": emptyToNil(row.ExpiresAt), "revokedAt": emptyToNil(row.RevokedAt), "createdAt": row.CreatedAt, "lastUsedAt": emptyToNil(row.LastUsedAt)}
}

func servicePrincipalSecretDTO(row access.ServicePrincipalSecret, rawSecret string) map[string]any {
	out := map[string]any{
		"id":                 row.ID,
		"servicePrincipalId": row.ServicePrincipalID,
		"name":               row.Name,
		"expiresAt":          emptyToNil(row.ExpiresAt),
		"createdAt":          emptyToNil(row.CreatedAt),
		"revokedAt":          emptyToNil(row.RevokedAt),
	}
	if strings.TrimSpace(rawSecret) != "" {
		out["secret"] = rawSecret
	}
	return out
}

func sessionDTO(row access.Session) map[string]any {
	return map[string]any{
		"id":                row.ID,
		"kind":              row.Kind,
		"instanceId":        emptyToNil(row.InstanceID),
		"profileId":         emptyToNil(row.ProfileID),
		"clientId":          emptyToNil(row.ClientID),
		"createdAt":         row.CreatedAt,
		"expiresAt":         row.ExpiresAt,
		"absoluteExpiresAt": emptyToNil(row.AbsoluteExpiresAt),
		"lastSeenAt":        emptyToNil(row.LastSeenAt),
		"revokedAt":         emptyToNil(row.RevokedAt),
	}
}

func accessAuditInput(r *stdhttp.Request, action, principalID, workspaceID, targetType, targetID string, privilege access.Privilege, status string, metadata map[string]any) access.AuditEventInput {
	if metadata == nil {
		metadata = map[string]any{}
	}
	bytes, _ := json.Marshal(metadata)
	return access.AuditEventInput{
		WorkspaceID:   workspaceID,
		PrincipalID:   principalID,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		Privilege:     privilege,
		Status:        status,
		RequestID:     requestIDFromRequest(r),
		CorrelationID: correlationIDFromRequest(r),
		MetadataJSON:  string(bytes),
	}
}

func runAuditedMutation(r *stdhttp.Request, repo access.Repository, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	transactional, ok := repo.(access.AuditedMutationRepository)
	if !ok {
		return fmt.Errorf("%w: access repository does not support transactional auditing", access.ErrAuditTransaction)
	}
	operationID, generatedCommand := apigencommand.OperationID(r.Context())
	if !generatedCommand {
		// Non-generated internal callers retain the same atomic repository path.
		return transactional.RunAuditedMutation(r.Context(), mutation)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operationID, apigencommand.Execution{
		Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
			return transactional.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
				input, mutationErr := mutation(txRepo)
				if mutationErr == nil && input.Action != contract.AuditAction {
					return access.AuditEventInput{}, fmt.Errorf("generated audit action %q does not match mutation action %q", contract.AuditAction, input.Action)
				}
				return input, mutationErr
			})
		},
	})
}

func writeCommandFailure(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID accessgen.GenCommandOperationID, err error) {
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, accessgen.GetAPIGenCommandFailureContracts, err)
}

func writeAuditedMutationError(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID accessgen.GenCommandOperationID, err error, mutationStatus int) {
	if mutationStatus == stdhttp.StatusNotFound && errors.Is(err, sql.ErrNoRows) {
		err = apigenfailure.Wrap("not_found", err)
	}
	if _, classified := apigenfailure.KindOf(err); !classified {
		switch operationID.APIGenOperationID() {
		case "createCurrentAPIToken", "createServicePrincipal", "createServicePrincipalSecret", "createGroup", "createRoleBinding", "createGrant", "transferOwnership", "createDataPolicy":
			err = apigenfailure.Wrap("invalid", err)
		case "updateGrant", "updateDataPolicy":
			if mutationStatus == stdhttp.StatusUnprocessableEntity {
				err = apigenfailure.Wrap("invalid", err)
			}
		}
	}
	writeCommandFailure(w, r, operationID, err)
}

func auditEventDTO(row access.AuditEvent) map[string]any {
	var metadata map[string]any
	if strings.TrimSpace(row.MetadataJSON) != "" {
		_ = json.Unmarshal([]byte(row.MetadataJSON), &metadata)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	return map[string]any{
		"id":            row.ID,
		"workspaceId":   row.WorkspaceID,
		"principalId":   row.PrincipalID,
		"action":        row.Action,
		"targetType":    row.TargetType,
		"targetId":      row.TargetID,
		"privilege":     row.Privilege,
		"status":        row.Status,
		"requestId":     row.RequestID,
		"correlationId": row.CorrelationID,
		"metadata":      metadata,
		"createdAt":     row.CreatedAt,
	}
}

func emptyToNil(value string) any {
	if value == "" {
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

func knownPrivileges() []string {
	known := access.KnownPrivileges()
	out := make([]string, len(known))
	for i, privilege := range known {
		out[i] = string(privilege)
	}
	return out
}

func privilegesFromStrings(values []string) []access.Privilege {
	privileges := make([]access.Privilege, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		privileges = append(privileges, access.Privilege(value))
	}
	return privileges
}

func privilegesFromOAuthScope(scope string) ([]access.Privilege, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil, fmt.Errorf("OAuth client credentials require at least one privilege scope")
	}
	values := strings.FieldsFunc(scope, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\n' || r == '\t'
	})
	privileges := make([]access.Privilege, 0, len(values))
	for _, value := range values {
		privilege := access.Privilege(strings.TrimSpace(value))
		if privilege == "" {
			continue
		}
		if !knownPrivilege(privilege) {
			return nil, fmt.Errorf("unsupported OAuth scope privilege %q", value)
		}
		privileges = append(privileges, privilege)
	}
	if len(privileges) == 0 {
		return nil, fmt.Errorf("OAuth client credentials require at least one privilege scope")
	}
	return privileges, nil
}

func knownPrivilege(value access.Privilege) bool {
	parsed, ok := access.ParsePrivilege(string(value))
	return ok && parsed == value
}

func knownGrantSubjectType(value access.SubjectType) bool {
	switch value {
	case access.SubjectPrincipal, access.SubjectGroup, access.SubjectServicePrincipal:
		return true
	default:
		return false
	}
}

func knownDataPolicySubjectType(value access.SubjectType) bool {
	return knownGrantSubjectType(value) || value == access.SubjectDashboardPublication
}

func principalKindAllowsGenericMutation(kind access.PrincipalKind) bool {
	return kind == access.PrincipalKindUser
}

func knownDataPolicyType(value string) bool {
	switch strings.TrimSpace(value) {
	case "row_filter", "column_mask":
		return true
	default:
		return false
	}
}

func objectRefFromRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (access.ObjectRef, bool) {
	return objectRefFromValues(w, r, nil, r.URL.Query().Get("objectType"), r.URL.Query().Get("objectId"))
}

func commandOperation(operationID accessgen.GenCommandOperationID) *accessgen.GenCommandOperationID {
	return &operationID
}

func objectRefFromValues(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID *accessgen.GenCommandOperationID, objectType, objectID string) (access.ObjectRef, bool) {
	writeInvalid := func(err error) {
		if operationID != nil {
			writeCommandFailure(w, r, *operationID, apigenfailure.Wrap("invalid", err))
			return
		}
		writeJSONError(w, err, stdhttp.StatusBadRequest)
	}
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspace"))
	if workspaceID == "" {
		writeInvalid(fmt.Errorf("workspace is required"))
		return access.ObjectRef{}, false
	}
	objectType = strings.TrimSpace(objectType)
	objectID = strings.TrimSpace(objectID)
	if objectType == "" || objectType == string(access.SecurableWorkspace) {
		if objectID != "" {
			writeInvalid(fmt.Errorf("workspace objectId must be empty"))
			return access.ObjectRef{}, false
		}
		return access.WorkspaceObject(workspaceID), true
	}
	if access.SecurableType(objectType) == access.SecurablePlatform {
		if objectID != "" {
			writeInvalid(fmt.Errorf("platform objectId must be empty"))
			return access.ObjectRef{}, false
		}
		return access.PlatformObject(), true
	}
	typ := access.SecurableType(objectType)
	if !validWorkspaceSecurableType(typ) {
		writeInvalid(fmt.Errorf("unsupported securable object type %q", objectType))
		return access.ObjectRef{}, false
	}
	if objectID == "" {
		writeInvalid(fmt.Errorf("objectId is required for %s grants", objectType))
		return access.ObjectRef{}, false
	}
	if typ == access.SecurableProjectEnvironment {
		return access.ProjectEnvironmentObject(workspaceID, objectID), true
	}
	return objectWithInferredParent(typ, workspaceID, objectID), true
}

func objectWithInferredParent(typ access.SecurableType, workspaceID, objectID string) access.ObjectRef {
	parts := strings.Split(objectID, "/")
	switch typ {
	case access.SecurableDataset, access.SecurableTable:
		if len(parts) >= 2 && strings.TrimSpace(parts[0]) != "" {
			return access.ItemObjectWithParent(typ, workspaceID, objectID, access.ItemObject(access.SecurableSemanticModel, workspaceID, parts[0]))
		}
	case access.SecurableColumn:
		if len(parts) >= 3 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			parent := access.ItemObjectWithParent(access.SecurableDataset, workspaceID, parts[0]+"/"+parts[1], access.ItemObject(access.SecurableSemanticModel, workspaceID, parts[0]))
			return access.ItemObjectWithParent(typ, workspaceID, objectID, parent)
		}
	case access.SecurableSemanticField:
		if len(parts) >= 2 && strings.TrimSpace(parts[0]) != "" {
			return access.ItemObjectWithParent(typ, workspaceID, objectID, access.ItemObject(access.SecurableSemanticModel, workspaceID, parts[0]))
		}
	}
	return access.ItemObject(typ, workspaceID, objectID)
}

func objectRefFromCanonical(workspaceID, canonicalID string) access.ObjectRef {
	canonicalID = strings.TrimSpace(canonicalID)
	if canonicalID == access.PlatformObject().CanonicalID() {
		return access.PlatformObject()
	}
	if canonicalID == access.WorkspaceObject(workspaceID).CanonicalID() {
		return access.WorkspaceObject(workspaceID)
	}
	prefix, rest, ok := strings.Cut(canonicalID, ":")
	if !ok {
		return access.WorkspaceObject(workspaceID)
	}
	objectWorkspace, objectID, ok := strings.Cut(rest, ":")
	if !ok {
		return access.WorkspaceObject(workspaceID)
	}
	return objectWithInferredParent(access.SecurableType(prefix), objectWorkspace, objectID)
}

func objectRefFromGrant(grant access.Grant) access.ObjectRef {
	switch grant.ObjectType {
	case access.SecurablePlatform:
		return access.PlatformObject()
	case access.SecurableWorkspace:
		return access.WorkspaceObject(grant.WorkspaceID)
	default:
		prefix := string(grant.ObjectType) + ":" + grant.WorkspaceID + ":"
		objectID := strings.TrimPrefix(grant.ObjectID, prefix)
		return objectWithInferredParent(grant.ObjectType, grant.WorkspaceID, objectID)
	}
}

func validWorkspaceSecurableType(typ access.SecurableType) bool {
	switch typ {
	case access.SecurableDashboard,
		access.SecurableProjectEnvironment,
		access.SecurableSemanticModel,
		access.SecurableSemanticField,
		access.SecurableSource,
		access.SecurableModelTable,
		access.SecurableDataset,
		access.SecurableTable,
		access.SecurableColumn:
		return true
	default:
		return false
	}
}

func (h Handler) authorizeCurrentObject(w stdhttp.ResponseWriter, r *stdhttp.Request, privilege access.Privilege, object access.ObjectRef) bool {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return false
	}
	if credential, ok := h.currentCredential(r); ok && !apiTokenAllows(credential.Token, object.WorkspaceID, privilege) {
		writeJSONError(w, errForbidden, objectAuthorizationDenialStatus(r))
		return false
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return false
	}
	decision, err := repo.Authorize(r.Context(), principal.ID, privilege, object)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return false
	}
	if !decision.Allowed {
		writeJSONError(w, errForbidden, objectAuthorizationDenialStatus(r))
		return false
	}
	return true
}

func objectAuthorizationDenialStatus(r *stdhttp.Request) int {
	// Only routes whose path identifies a concrete access resource conceal its
	// existence. Collection commands describe their target in the request body
	// and therefore report an ordinary authorization failure.
	if chi.URLParam(r, "grant") != "" || chi.URLParam(r, "policy") != "" {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusForbidden
}

func (h Handler) groupByID(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID *accessgen.GenCommandOperationID) (access.Group, bool) {
	writeFailure := func(err error) {
		if operationID != nil {
			writeCommandFailure(w, r, *operationID, err)
			return
		}
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
	}
	repo, err := h.repository()
	if err != nil {
		writeFailure(err)
		return access.Group{}, false
	}
	rows, err := repo.ListGroups(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")))
	if err != nil {
		writeFailure(err)
		return access.Group{}, false
	}
	for _, row := range rows {
		if row.ID == chi.URLParam(r, "group") {
			return row, true
		}
	}
	if operationID != nil {
		writeCommandFailure(w, r, *operationID, apigenfailure.Wrap("not_found", sql.ErrNoRows))
	} else {
		writeJSONError(w, sql.ErrNoRows, stdhttp.StatusNotFound)
	}
	return access.Group{}, false
}

func decodeRoleBindingInput(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID accessgen.GenCommandOperationID) (access.RoleBindingInput, bool) {
	var input struct {
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Role        string `json:"role"`
	}
	if err := decodeStrictJSON(r, &input); err != nil {
		writeCommandFailure(w, r, operationID, apigenfailure.Wrap("invalid", err))
		return access.RoleBindingInput{}, false
	}
	return access.RoleBindingInput{
		WorkspaceID: chi.URLParam(r, "workspace"),
		SubjectType: access.SubjectType(input.SubjectType),
		SubjectID:   input.SubjectID,
		Role:        input.Role,
	}, true
}

func apiTokenAllows(token access.APIToken, workspaceID string, privilege access.Privilege) bool {
	if token.WorkspaceID != "" && token.WorkspaceID != workspaceID {
		return false
	}
	if token.Privileges == nil {
		return true
	}
	for _, allowed := range token.Privileges {
		if allowed == privilege {
			return true
		}
	}
	return false
}

type pageResponse struct {
	NextCursor string `json:"nextCursor"`
}

func pagedResponseWithCursor(items any, nextCursor string) map[string]any {
	return map[string]any{"items": items, "page": pageResponse{NextCursor: nextCursor}}
}

func writePagedJSON[T any](w stdhttp.ResponseWriter, r *stdhttp.Request, items []T) bool {
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return false
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(page, nextCursor))
	return true
}

func pageSliceForRequest[T any](w stdhttp.ResponseWriter, r *stdhttp.Request, items []T) ([]T, string, bool) {
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return nil, "", false
	}
	cursorKey, ok := apiCursorKeyForRequest(w, r)
	if !ok {
		return nil, "", false
	}
	start := 0
	if cursorKey != "" {
		start = -1
		for index, item := range items {
			if apiItemPageKey(item) == cursorKey {
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
	nextCursor := ""
	if end < len(items) {
		nextCursor = encodeKeyCursor(apiItemPageKey(items[end-1]))
	}
	return append(make([]T, 0, end-start), items[start:end]...), nextCursor, true
}

const (
	defaultAPILimit = 50
	maxAPILimit     = 200
)

func apiLimitForRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (int, bool) {
	limit, err := parseAPILimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func parseAPILimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultAPILimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if value <= 0 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if value > maxAPILimit {
		return 0, fmt.Errorf("limit must not exceed 200")
	}
	return value, nil
}

func parseBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func privilegeStrings(values []access.Privilege) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func apiCursorKeyForRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (string, bool) {
	key, err := decodeKeyCursor(r.URL.Query().Get("pageToken"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return "", false
	}
	return key, true
}

func encodeKeyCursor(key string) string {
	if key == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("key:" + key))
}

func decodeKeyCursor(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("pageToken is invalid")
	}
	text := string(raw)
	if !strings.HasPrefix(text, "key:") || strings.TrimPrefix(text, "key:") == "" {
		return "", fmt.Errorf("pageToken is invalid")
	}
	return strings.TrimPrefix(text, "key:"), nil
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

func cleanQueryValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func encodeCursor(createdAt, id string) string {
	if createdAt == "" || id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}

func decodeCursor(token string) (string, string) {
	if strings.TrimSpace(token) == "" {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", ""
	}
	createdAt, id, ok := strings.Cut(string(raw), "\x00")
	if !ok {
		return "", ""
	}
	return createdAt, id
}

func writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	apitransport.WriteJSON(w, status, value)
}

func writeSecretJSON(w stdhttp.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, value)
}

func writeJSONError(w stdhttp.ResponseWriter, err error, status int) {
	writeJSON(w, status, httpmodel.ErrorResponse{
		Code:      status,
		Message:   err.Error(),
		Details:   map[string]any{},
		RequestID: "",
	})
}

func decodeStrictJSON(r *stdhttp.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func resourceETag(value any) string {
	encoded, _ := apitransport.CanonicalJSON(value)
	return apitransport.StrongETag(string(encoded))
}

func requireIfMatch(w stdhttp.ResponseWriter, r *stdhttp.Request, current string) bool {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if value == "*" || value == current {
		return true
	}
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSONError(w, fmt.Errorf("If-Match does not match the current resource"), stdhttp.StatusPreconditionFailed)
	return false
}

func statusForNotFound(err error) int {
	if err == sql.ErrNoRows {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusInternalServerError
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
