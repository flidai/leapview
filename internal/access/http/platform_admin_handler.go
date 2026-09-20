package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	transport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/go-chi/chi/v5"
)

func platformAdministratorDTO(item access.PlatformAdministrator) map[string]any {
	return map[string]any{
		"bindingId": item.BindingID, "principalId": item.Principal.ID,
		"email": item.Principal.Email, "displayName": item.Principal.DisplayName,
		"role": string(item.Role), "grantedAt": item.CreatedAt,
	}
}

func platformAdministratorRevisionHeader(w stdhttp.ResponseWriter, revision string) {
	if strings.TrimSpace(revision) != "" {
		w.Header().Set("ETag", strconv.Quote(revision))
	}
}

func platformAdministratorCurrentRevision(ctx context.Context, repo access.Repository) (string, error) {
	lister, ok := repo.(access.PlatformAdminLister)
	if !ok {
		return "", errors.New("platform administrator listing is unavailable")
	}
	state, err := lister.ListPlatformAdministrators(ctx)
	if err != nil {
		return "", err
	}
	// PlatformAdministratorState keeps the opaque digest unquoted, while the
	// HTTP ETag (and generated If-Match contract) carries the strong value in
	// quotes. Keep the transaction-time comparison in that transport form.
	return strconv.Quote(state.Revision), nil
}

func (h Handler) platformAdminLister(w stdhttp.ResponseWriter, r *stdhttp.Request) (access.PlatformAdministratorState, bool) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return access.PlatformAdministratorState{}, false
	}
	lister, ok := repo.(access.PlatformAdminLister)
	if !ok {
		writeJSONError(w, errors.New("platform administrator listing is unavailable"), stdhttp.StatusServiceUnavailable)
		return access.PlatformAdministratorState{}, false
	}
	state, err := lister.ListPlatformAdministrators(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return access.PlatformAdministratorState{}, false
	}
	return state, true
}

func (h Handler) ListPlatformAdministrators(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	state, ok := h.platformAdminLister(w, r)
	if !ok {
		return
	}
	platformAdministratorRevisionHeader(w, state.Revision)
	items := make([]map[string]any, 0, len(state.Administrators))
	for _, item := range state.Administrators {
		items = append(items, platformAdministratorDTO(item))
	}
	page, next, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": page, "page": map[string]any{"nextCursor": next}})
}

func (h Handler) GrantPlatformAdministrator(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	if !h.requireRecentInteractiveBrowserAuth(w, r) {
		return
	}
	if h.RequirePlatformRoleApproval {
		h.recordDeniedPlatformAttempt(r, "platform_admin.granted", "platform_role_binding", chi.URLParam(r, "principal"), access.AuditReasonApprovalRequired, nil)
		writePlatformAdminError(w, access.ErrPlatformAdminApprovalRequired)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	_, ok := repo.(access.PlatformAdminWriter)
	if !ok {
		h.recordDeniedPlatformAttempt(r, "platform_admin.granted", "platform_role_binding", chi.URLParam(r, "principal"), access.AuditReasonConfigurationUnavailable, nil)
		writeJSONError(w, errors.New("platform administrator writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	principalID := chi.URLParam(r, "principal")
	if strings.TrimSpace(principalID) == "" {
		h.recordDeniedPlatformAttempt(r, "platform_admin.granted", "platform_role_binding", "unknown", access.AuditReasonInvalidRequest, map[string]any{"targetPrincipalId": ""})
		writePlatformAdminError(w, access.ErrPlatformAdminInvalid)
		return
	}
	var result access.PlatformAdminGrantResult
	operation := accessgen.GenCommandOperationGrantPlatformAdministrator()
	err = executeAuditedMutationWithRevision(r, repo, operation, func(tx access.Repository) (string, error) {
		return platformAdministratorCurrentRevision(r.Context(), tx)
	}, func(tx access.Repository) (access.AuditEventInput, error) {
		txWriter, ok := tx.(access.PlatformAdminWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator writer is unavailable")
		}
		result, err = txWriter.GrantPlatformAdmin(r.Context(), access.PlatformAdminGrantInput{
			PrincipalID: principalID, ExpectedRevision: r.Header.Get("If-Match"), IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, encodeErr := accessgen.EncodeGenGrantPlatformAdministratorAuditPayload(accessgen.GenSchemaPlatformAdministratorAuditPayload{
			PrincipalId: result.Administrator.Principal.ID, BindingId: result.Administrator.BindingID,
			Role: string(result.Administrator.Role), Revision: result.State.Revision,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "platform_admin.granted", h.currentPrincipalID(r), "platform_role_binding", result.Administrator.BindingID, "", "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_admin.granted", "platform_role_binding", principalID, auditDenialReason(err), nil)
		writePlatformAdminError(w, err)
		return
	}
	platformAdministratorRevisionHeader(w, result.State.Revision)
	writeJSON(w, stdhttp.StatusOK, platformAdministratorDTO(result.Administrator))
}

func (h Handler) RevokePlatformAdministrator(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	if !h.requireRecentInteractiveBrowserAuth(w, r) {
		return
	}
	if h.RequirePlatformRoleApproval {
		h.recordDeniedPlatformAttempt(r, "platform_admin.revoked", "platform_role_binding", chi.URLParam(r, "principal"), access.AuditReasonApprovalRequired, nil)
		writePlatformAdminError(w, access.ErrPlatformAdminApprovalRequired)
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	_, ok := repo.(access.PlatformAdminWriter)
	if !ok {
		h.recordDeniedPlatformAttempt(r, "platform_admin.revoked", "platform_role_binding", chi.URLParam(r, "principal"), access.AuditReasonConfigurationUnavailable, nil)
		writeJSONError(w, errors.New("platform administrator writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	principalID := chi.URLParam(r, "principal")
	if strings.TrimSpace(principalID) == "" {
		h.recordDeniedPlatformAttempt(r, "platform_admin.revoked", "platform_role_binding", "unknown", access.AuditReasonInvalidRequest, map[string]any{"targetPrincipalId": ""})
		writePlatformAdminError(w, access.ErrPlatformAdminInvalid)
		return
	}
	var result access.PlatformAdministratorState
	var bindingID string
	operation := accessgen.GenCommandOperationRevokePlatformAdministrator()
	err = executeAuditedMutationWithRevision(r, repo, operation, func(tx access.Repository) (string, error) {
		return platformAdministratorCurrentRevision(r.Context(), tx)
	}, func(tx access.Repository) (access.AuditEventInput, error) {
		if lister, ok := tx.(access.PlatformAdminLister); ok {
			if before, listErr := lister.ListPlatformAdministrators(r.Context()); listErr == nil {
				for _, item := range before.Administrators {
					if item.Principal.ID == principalID {
						bindingID = item.BindingID
						break
					}
				}
			}
		}
		txWriter, ok := tx.(access.PlatformAdminWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator writer is unavailable")
		}
		result, err = txWriter.RevokePlatformAdmin(r.Context(), access.PlatformAdminRevokeInput{
			PrincipalID: principalID, ExpectedRevision: r.Header.Get("If-Match"), IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, encodeErr := accessgen.EncodeGenRevokePlatformAdministratorAuditPayload(accessgen.GenSchemaPlatformAdministratorAuditPayload{
			PrincipalId: principalID, BindingId: bindingID, Role: string(access.PlatformRoleAdmin), Revision: result.Revision,
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "platform_admin.revoked", h.currentPrincipalID(r), "platform_role_binding", bindingID, "", "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_admin.revoked", "platform_role_binding", principalID, auditDenialReason(err), nil)
		writePlatformAdminError(w, err)
		return
	}
	platformAdministratorRevisionHeader(w, result.Revision)
	w.WriteHeader(stdhttp.StatusNoContent)
}

// requireRecentInteractiveBrowserAuth gates browser-session mutations while
// preserving the existing bearer API contract. API/service credentials are
// not browser sessions and are evaluated by the normal platform-role policy;
// the Settings command has an additional explicit rejection for them.
func (h Handler) requireRecentInteractiveBrowserAuth(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	if h.InteractiveAuthentication == nil {
		return true
	}
	if credential, ok := h.currentCredential(r); ok && (credential.Authoring != nil || credential.Token.ID != "") {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", platformAdminRequestTarget(r), access.AuditReasonCredentialAttenuated, nil)
		return true
	}
	authenticatedAt, ok := h.InteractiveAuthentication(r)
	if !ok || authenticatedAt.IsZero() {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", platformAdminRequestTarget(r), access.AuditReasonRecentAuthentication, nil)
		transport.WriteProblem(w, r, stdhttp.StatusUnauthorized, "RECENT_AUTHENTICATION_REQUIRED", "Recent interactive authentication is required before changing platform authority.", nil)
		return false
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	age := now().UTC().Sub(authenticatedAt.UTC())
	if age < 0 || age > access.RecentInteractiveAuthenticationWindow {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", platformAdminRequestTarget(r), access.AuditReasonRecentAuthentication, nil)
		transport.WriteProblem(w, r, stdhttp.StatusUnauthorized, "RECENT_AUTHENTICATION_REQUIRED", "Recent interactive authentication is required before changing platform authority.", nil)
		return false
	}
	return true
}

func platformAdminRequestTarget(r *stdhttp.Request) string {
	if r == nil {
		return "platform"
	}
	if target := strings.TrimSpace(chi.URLParam(r, "principal")); target != "" {
		return target
	}
	if r.URL != nil && strings.TrimSpace(r.URL.Path) != "" {
		return r.URL.Path
	}
	return "platform"
}

func writePlatformAdminError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, errIfMatchRequired), errors.Is(err, errIfMatchFailed):
		status = stdhttp.StatusPreconditionFailed
	case errors.Is(err, access.ErrPlatformAdminInvalid):
		status = stdhttp.StatusBadRequest
	case errors.Is(err, access.ErrPlatformAdminNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrPlatformAdminConflict), errors.Is(err, access.ErrPlatformAdminLastAdmin), errors.Is(err, access.ErrPlatformAdminIdempotency), errors.Is(err, access.ErrPlatformAdminApprovalRequired):
		status = stdhttp.StatusConflict
	case errors.Is(err, access.ErrPlatformAdminStaleRevision):
		status = stdhttp.StatusPreconditionFailed
	}
	writeJSONError(w, fmt.Errorf("%w", err), status)
}
