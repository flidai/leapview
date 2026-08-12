package productsettings

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/admin/product"
	signals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/platform/http/transport"
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
		transport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "MANAGE_PLATFORM is required", nil)
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
	var err error
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
	switch {
	case errors.Is(err, product.ErrInvalid):
		h.writeProblem(w, r, http.StatusUnprocessableEntity, "INVALID_PRODUCT_IDENTITY", err.Error())
	case errors.Is(err, product.ErrPrecondition):
		h.writeProblem(w, r, http.StatusPreconditionFailed, "ETAG_MISMATCH", "The product settings revision is stale")
	default:
		h.writeProblem(w, r, http.StatusInternalServerError, "PRODUCT_SETTINGS_FAILED", "The product settings command could not be completed")
	}
}

func (h *Handler) writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	transport.WriteProblem(w, r, status, code, detail, nil)
}
