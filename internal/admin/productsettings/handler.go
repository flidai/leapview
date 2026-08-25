package productsettings

import (
	"context"
	"errors"
	"net/http"
	"strings"

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
	ReadModel        ReadModel
	CanManage        func(*http.Request) bool
	CurrentPrincipal func(*http.Request) (product.Principal, bool)
	Commands         CommandContract
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
		transport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "platform administrator access is required", nil)
		return
	}
	var request CommandSignals
	if err := pagestream.ReadSignals(r, &request); err != nil {
		h.writeProblem(w, r, http.StatusBadRequest, "INVALID_COMMAND", "The product settings command is invalid")
		return
	}
	command := request.ProductSettingsCommand
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
