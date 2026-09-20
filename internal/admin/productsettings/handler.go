package productsettings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/admin/product"
	signals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
)

// CommandSignals is the Datastar request envelope. Keeping the root key
// explicit prevents command payloads from becoming an ad hoc JSON API.
type CommandSignals struct {
	ProductSettingsCommand signals.ProductSettingsCommand `json:"productSettingsCommand"`
}

type HTTPConfig struct {
	ReadModel                   ReadModel
	CanManage                   func(*http.Request) bool
	CurrentPrincipal            func(*http.Request) (product.Principal, bool)
	Commands                    CommandContract
	PlatformCommands            map[string]uicommand.Binding
	PlatformAdministration      access.PlatformAdminAuthorityLister
	PlatformWriter              access.PlatformAdminWriter
	AuditRepository             access.AuditedMutationRepository
	RequirePlatformRoleApproval bool
	CurrentCredential           func(*http.Request) (access.APICredential, bool)
	InteractiveAuthentication   func(*http.Request) (time.Time, bool)
	Now                         func() time.Time
}

type Handler struct{ config HTTPConfig }

func NewHandler(config HTTPConfig) (*Handler, error) {
	if config.ReadModel.Service == nil {
		return nil, errors.New("product settings service is required")
	}
	return &Handler{config: config}, nil
}

// Bootstrap returns the complete signal subtree for the page stream. The
// parent admin handler can place it under the `productSettings` signal key.
func (h *Handler) Bootstrap(r *http.Request, active string) (signals.ProductSettingsSignal, error) {
	canManage := true
	if h.config.CanManage != nil {
		canManage = h.config.CanManage(r)
	}
	data, err := h.config.ReadModel.Data(r.Context(), active, canManage)
	if err != nil {
		return signals.ProductSettingsSignal{}, err
	}
	return Signal(data), nil
}

// Command handles the non-binary settings mutations through the page stream.
// Logo bytes use the browser-authenticated upload route because Datastar
// signal payloads are JSON; the Lit component preserves the returned ETag and
// CSRF token for that one unavoidable binary operation.
func (h *Handler) Command(w http.ResponseWriter, r *http.Request) {
	if h.config.CanManage != nil && !h.config.CanManage(r) {
		h.recordDeniedPlatformAttempt(r, "authorization.denied", "platform_authorization", "product-settings", access.AuditReasonAuthorizationDenied, nil)
		transport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "platform administrator access is required", nil)
		return
	}
	var request CommandSignals
	if err := pagestream.ReadSignals(r, &request); err != nil {
		h.writeProblem(w, r, http.StatusBadRequest, "INVALID_COMMAND", "The product settings command is invalid")
		return
	}
	command := request.ProductSettingsCommand
	if command.Action == "grant_platform_administrator" || command.Action == "revoke_platform_administrator" {
		h.platformAdministratorCommand(w, r, command)
		return
	}
	if command.Revision <= 0 && command.Action != "refresh" {
		h.writeProblem(w, r, http.StatusPreconditionFailed, "ETAG_MISMATCH", "The product settings revision is required")
		return
	}
	started, err := h.beginProductSettingsInvocation(r, command)
	if err != nil {
		h.writeProblem(w, r, http.StatusBadRequest, "INVALID_COMMAND_CONTRACT", "The product settings command contract is invalid")
		return
	}
	r = started
	err = nil
	switch strings.TrimSpace(command.Action) {
	case "save_display_name":
		if command.DisplayName == nil {
			h.writeProblem(w, r, http.StatusBadRequest, "INVALID_COMMAND", "displayName is required")
			return
		}
		_, err = h.config.ReadModel.Service.SetDisplayName(r.Context(), command.Revision, *command.DisplayName, h.mutation(r))
	case "remove_logo":
		_, err = h.config.ReadModel.Service.DeleteLogo(r.Context(), command.Revision, h.mutation(r))
	case "reset_identity":
		_, err = h.config.ReadModel.Service.ResetIdentity(r.Context(), command.Revision, h.mutation(r))
	case "refresh":
		// Read-only refresh is useful after the binary logo PUT response.
	default:
		h.writeProblem(w, r, http.StatusBadRequest, "UNKNOWN_COMMAND", "The product settings command is not supported")
		return
	}
	if err != nil {
		h.problem(w, r, err)
		return
	}
	active := strings.TrimSpace(r.URL.Query().Get("section"))
	if active == "" {
		active = "general"
	}
	canManage := true
	if h.config.CanManage != nil {
		canManage = h.config.CanManage(r)
	}
	data, err := h.config.ReadModel.Data(r.Context(), active, canManage)
	if err != nil {
		h.problem(w, r, err)
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"productSettings": Payload(Signal(data))})
}

func (h *Handler) platformAdministratorCommand(w http.ResponseWriter, r *http.Request, command signals.ProductSettingsCommand) {
	if binding, ok := h.config.PlatformCommands[command.Action]; !ok || uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()) != nil {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonInvalidRequest, nil)
		h.writeProblem(w, r, http.StatusBadRequest, "INVALID_COMMAND_CONTRACT", "The platform administrator command contract is invalid")
		return
	}
	if h.config.PlatformWriter == nil || h.config.PlatformAdministration == nil || h.config.AuditRepository == nil {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonConfigurationUnavailable, nil)
		h.writeProblem(w, r, http.StatusServiceUnavailable, "PLATFORM_ADMIN_UNAVAILABLE", "Platform authority administration is unavailable")
		return
	}
	if h.config.CurrentCredential != nil {
		if credential, ok := h.config.CurrentCredential(r); ok && (credential.Authoring != nil || credential.Token.ID != "") {
			h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonCredentialAttenuated, nil)
			transport.WriteProblem(w, r, http.StatusForbidden, "INTERACTIVE_AUTHENTICATION_REQUIRED", "Platform-role changes require a browser session with recent interactive authentication.", nil)
			return
		}
	}
	if h.config.InteractiveAuthentication == nil {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonConfigurationUnavailable, nil)
		transport.WriteProblem(w, r, http.StatusUnauthorized, "RECENT_AUTHENTICATION_REQUIRED", "Recent interactive authentication is required before changing platform authority.", nil)
		return
	}
	authenticatedAt, ok := h.config.InteractiveAuthentication(r)
	if !ok || authenticatedAt.IsZero() {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonRecentAuthentication, nil)
		transport.WriteProblem(w, r, http.StatusUnauthorized, "RECENT_AUTHENTICATION_REQUIRED", "Recent interactive authentication is required before changing platform authority.", nil)
		return
	}
	now := time.Now
	if h.config.Now != nil {
		now = h.config.Now
	}
	age := now().UTC().Sub(authenticatedAt.UTC())
	if age < 0 || age > access.RecentInteractiveAuthenticationWindow {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonRecentAuthentication, nil)
		transport.WriteProblem(w, r, http.StatusUnauthorized, "RECENT_AUTHENTICATION_REQUIRED", "Recent interactive authentication is required before changing platform authority.", nil)
		return
	}
	if h.config.RequirePlatformRoleApproval {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonApprovalRequired, nil)
		transport.WriteProblem(w, r, http.StatusConflict, "PLATFORM_ADMIN_APPROVAL_REQUIRED", access.ErrPlatformAdminApprovalRequired.Error(), nil)
		return
	}
	expected := strings.TrimSpace(pointerString(command.ExpectedRevision))
	if expected == "" {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonPreconditionFailed, nil)
		transport.WriteProblem(w, r, http.StatusPreconditionFailed, "PLATFORM_ADMIN_STALE", "The platform authority changed; reload the latest state before retrying.", nil)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", pointerString(command.PrincipalID), access.AuditReasonInvalidRequest, nil)
		transport.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required.", nil)
		return
	}
	principalID := strings.TrimSpace(pointerString(command.PrincipalID))
	if principalID == "" {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", "unknown", access.AuditReasonInvalidRequest, map[string]any{"targetPrincipalId": ""})
		transport.WriteProblem(w, r, http.StatusBadRequest, "PLATFORM_ADMIN_INVALID", "principalId is required.", nil)
		return
	}
	var state access.PlatformAdministratorState
	err := h.config.AuditRepository.RunAuditedMutation(r.Context(), func(repository access.Repository) (access.AuditEventInput, error) {
		writer, ok := repository.(access.PlatformAdminWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform administrator writer is unavailable")
		}
		actorID := ""
		if h.config.CurrentPrincipal != nil {
			if principal, found := h.config.CurrentPrincipal(r); found {
				actorID = principal.ID
			}
		}
		var bindingID, role string
		switch command.Action {
		case "grant_platform_administrator":
			result, mutationErr := writer.GrantPlatformAdmin(r.Context(), access.PlatformAdminGrantInput{PrincipalID: principalID, ExpectedRevision: expected, IdempotencyKey: idempotencyKey})
			if mutationErr != nil {
				return access.AuditEventInput{}, mutationErr
			}
			state, bindingID, role = result.State, result.Administrator.BindingID, string(result.Administrator.Role)
		case "revoke_platform_administrator":
			before, listErr := repository.(access.PlatformAdminLister).ListPlatformAdministrators(r.Context())
			if listErr != nil {
				return access.AuditEventInput{}, listErr
			}
			for _, item := range before.Administrators {
				if item.Principal.ID == principalID {
					bindingID, role = item.BindingID, string(item.Role)
					break
				}
			}
			var mutationErr error
			state, mutationErr = writer.RevokePlatformAdmin(r.Context(), access.PlatformAdminRevokeInput{PrincipalID: principalID, ExpectedRevision: expected, IdempotencyKey: idempotencyKey})
			if mutationErr != nil {
				return access.AuditEventInput{}, mutationErr
			}
		default:
			return access.AuditEventInput{}, errors.New("unknown platform administrator command")
		}
		var metadata string
		var encodeErr error
		payload := accessgen.GenSchemaPlatformAdministratorAuditPayload{PrincipalId: principalID, BindingId: bindingID, Role: role, Revision: state.Revision}
		if command.Action == "grant_platform_administrator" {
			metadata, encodeErr = accessgen.EncodeGenGrantPlatformAdministratorAuditPayload(payload)
		} else {
			metadata, encodeErr = accessgen.EncodeGenRevokePlatformAdministratorAuditPayload(payload)
		}
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		action := "platform_admin.granted"
		if command.Action == "revoke_platform_administrator" {
			action = "platform_admin.revoked"
		}
		return access.AuditEventInput{PrincipalID: actorID, Action: action, ResourceKind: "platform_role_binding", ResourceID: bindingID, Status: "success", RequestID: r.Header.Get("X-Request-ID"), CorrelationID: r.Header.Get("X-Correlation-ID"), MetadataJSON: metadata}, nil
	})
	if err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_admin.denied", "platform_role_binding", principalID, productSettingsAuditDenialReason(err), nil)
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, access.ErrPlatformAdminStaleRevision):
			status = http.StatusPreconditionFailed
		case errors.Is(err, access.ErrPlatformAdminInvalid):
			status = http.StatusBadRequest
		case errors.Is(err, access.ErrPlatformAdminNotFound):
			status = http.StatusNotFound
		case errors.Is(err, access.ErrPlatformAdminConflict), errors.Is(err, access.ErrPlatformAdminLastAdmin), errors.Is(err, access.ErrPlatformAdminIdempotency):
			status = http.StatusConflict
		}
		if status == http.StatusPreconditionFailed {
			transport.WriteProblem(w, r, status, "PLATFORM_ADMIN_STALE", "The platform authority changed; reload the latest state before retrying.", nil)
		} else {
			transport.WriteProblem(w, r, status, "PLATFORM_ADMIN_MUTATION_FAILED", err.Error(), nil)
		}
		return
	}
	state, err = h.config.PlatformAdministration.ListPlatformAdminAuthorities(r.Context())
	if err != nil {
		h.writeProblem(w, r, http.StatusServiceUnavailable, "PLATFORM_ADMIN_UNAVAILABLE", "Platform authority administration is unavailable")
		return
	}
	active := strings.TrimSpace(r.URL.Query().Get("section"))
	if active == "" {
		active = "authentication"
	}
	data, err := h.config.ReadModel.Data(r.Context(), active, true)
	if err != nil {
		h.writeProblem(w, r, http.StatusServiceUnavailable, "PLATFORM_ADMIN_UNAVAILABLE", "Platform authority administration is unavailable")
		return
	}
	data.PlatformAdministration = state
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"productSettings": Payload(Signal(data))})
}

func productSettingsAuditDenialReason(err error) access.AuditDenialReason {
	switch {
	case errors.Is(err, access.ErrPlatformAdminStaleRevision):
		return access.AuditReasonPreconditionFailed
	case errors.Is(err, access.ErrPlatformAdminIdempotency):
		return access.AuditReasonIdempotencyConflict
	case errors.Is(err, access.ErrPlatformAdminInvalid):
		return access.AuditReasonInvalidRequest
	case errors.Is(err, access.ErrPlatformAdminNotFound):
		return access.AuditReasonNotFound
	case errors.Is(err, access.ErrPlatformAdminConflict), errors.Is(err, access.ErrPlatformAdminLastAdmin):
		return access.AuditReasonConflict
	default:
		return access.AuditReasonInternalFailure
	}
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type platformAdministratorDeniedPayload struct {
	Outcome        string                   `json:"outcome"`
	Reason         access.AuditDenialReason `json:"reason"`
	TargetID       string                   `json:"targetId"`
	IdempotencyKey string                   `json:"idempotencyKey"`
}

func (h *Handler) recordDeniedPlatformAttempt(r *http.Request, action, resourceKind, resourceID string, reason access.AuditDenialReason, metadata map[string]any) {
	if h == nil || r == nil || h.config.AuditRepository == nil || h.config.CurrentPrincipal == nil {
		return
	}
	actor, ok := h.config.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(actor.ID) == "" {
		return
	}
	recorder, ok := h.config.AuditRepository.(access.AuditEventRecorder)
	if !ok || recorder == nil {
		return
	}
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		resourceID = "unknown"
	}
	payload := platformAdministratorDeniedPayload{Outcome: "denied", Reason: reason, TargetID: resourceID, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}
	if metadata != nil {
		// The denial payload is deliberately closed. Additional caller context is
		// ignored rather than allowing an unvalidated value or secret into audit.
		if target, ok := metadata["targetPrincipalId"].(string); ok && strings.TrimSpace(target) != "" {
			payload.TargetID = strings.TrimSpace(target)
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	input := access.AuditEventInput{PrincipalID: actor.ID, Action: action, ResourceKind: resourceKind, ResourceID: resourceID, Capability: access.CapabilityProjectAdmin, Status: "denied", RequestID: r.Header.Get("X-Request-ID"), CorrelationID: r.Header.Get("X-Correlation-ID"), MetadataJSON: string(encoded)}
	defer func() { _ = recover() }()
	_ = access.PersistAuditEvent(r.Context(), recorder, input)
}

func (h *Handler) beginProductSettingsInvocation(r *http.Request, command signals.ProductSettingsCommand) (*http.Request, error) {
	action := strings.TrimSpace(command.Action)
	if action == "refresh" {
		return r, nil
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	concurrencyToken := strings.TrimSpace(r.Header.Get("If-Match"))
	begin := func(binding uicommand.Binding, start func() (context.Context, error)) (*http.Request, error) {
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
			return r, err
		}
		ctx, err := start()
		if err != nil {
			return r, err
		}
		return r.WithContext(ctx), nil
	}
	switch action {
	case "save_display_name":
		binding, err := h.config.Commands.Binding(CommandUpdateIdentity)
		if err != nil {
			return r, err
		}
		return begin(binding, func() (context.Context, error) {
			return h.config.Commands.BeginInvocation(r.Context(), CommandUpdateIdentity, CommandInvocation{
				ConcurrencyToken: concurrencyToken, RequestID: requestID, CorrelationID: correlationID,
			})
		})
	case "remove_logo":
		binding, err := h.config.Commands.Binding(CommandDeleteLogo)
		if err != nil {
			return r, err
		}
		return begin(binding, func() (context.Context, error) {
			return h.config.Commands.BeginInvocation(r.Context(), CommandDeleteLogo, CommandInvocation{
				ConcurrencyToken: concurrencyToken, RequestID: requestID, CorrelationID: correlationID,
			})
		})
	case "reset_identity":
		binding, err := h.config.Commands.Binding(CommandResetIdentity)
		if err != nil {
			return r, err
		}
		return begin(binding, func() (context.Context, error) {
			return h.config.Commands.BeginInvocation(r.Context(), CommandResetIdentity, CommandInvocation{
				IdempotencyKey: "ui:" + requestID, ConcurrencyToken: concurrencyToken, RequestID: requestID, CorrelationID: correlationID,
			})
		})
	default:
		return r, errors.New("unknown product settings command")
	}
}

func (h *Handler) mutation(r *http.Request) product.Mutation {
	mutation := product.Mutation{
		RequestID:        r.Header.Get("X-Request-ID"),
		CorrelationID:    r.Header.Get("X-Correlation-ID"),
		ConcurrencyToken: r.Header.Get("If-Match"),
	}
	if h.config.CurrentPrincipal != nil {
		if principal, ok := h.config.CurrentPrincipal(r); ok {
			mutation.PrincipalID = principal.ID
		}
	}
	return mutation
}

func (h *Handler) problem(w http.ResponseWriter, r *http.Request, err error) {
	if h.patchError(w, r, err) {
		return
	}
	switch {
	case errors.Is(err, product.ErrInvalid):
		h.writeProblem(w, r, http.StatusUnprocessableEntity, "INVALID_PRODUCT_IDENTITY", err.Error())
	case errors.Is(err, product.ErrPrecondition):
		h.writeProblem(w, r, http.StatusPreconditionFailed, "ETAG_MISMATCH", "The product settings revision is stale")
	default:
		h.writeProblem(w, r, http.StatusInternalServerError, "PRODUCT_SETTINGS_FAILED", "The product settings command could not be completed")
	}
}

// patchError follows the access-administration command contract: expected
// domain failures are returned as a complete typed signal patch so the page
// keeps its draft values and controls can leave their busy state. Transport
// failures are retained for cases where the read model itself is unavailable.
func (h *Handler) patchError(w http.ResponseWriter, r *http.Request, err error) bool {
	if h == nil || h.config.ReadModel.Service == nil || r == nil {
		return false
	}
	active := strings.TrimSpace(r.URL.Query().Get("section"))
	if active == "" {
		active = "general"
	}
	canManage := true
	if h.config.CanManage != nil {
		canManage = h.config.CanManage(r)
	}
	data, loadErr := h.config.ReadModel.Data(r.Context(), active, canManage)
	if loadErr != nil {
		return false
	}
	state := Signal(data)
	detail := "The product settings command could not be completed. Your previous state was kept; retry."
	switch {
	case errors.Is(err, product.ErrInvalid):
		detail = err.Error()
	case errors.Is(err, product.ErrPrecondition):
		detail = "The product settings changed elsewhere. Reload the latest state before retrying."
	}
	state.Error = &detail
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"productSettings": Payload(state)})
	return true
}

func (h *Handler) writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	transport.WriteProblem(w, r, status, code, detail, nil)
}
