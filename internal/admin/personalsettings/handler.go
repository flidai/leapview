package personalsettings

import (
	"context"
	"errors"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
)

type PrincipalProvider func(*http.Request) (string, bool)
type SessionProvider func(*http.Request) (string, bool)

type commandSignals struct {
	Profile          ProfileCommand          `json:"personalProfileCommand"`
	Theme            ThemeCommand            `json:"personalThemeCommand"`
	Password         PasswordCommand         `json:"personalPasswordCommand"`
	Session          SessionCommand          `json:"personalSessionCommand"`
	AuthoringSession AuthoringSessionCommand `json:"personalAuthoringSessionCommand"`
	Token            TokenCommand            `json:"personalTokenCommand"`
}

type Handler struct {
	Service          *Service
	CurrentPrincipal PrincipalProvider
	CurrentSession   SessionProvider
}

// Bootstrap emits the settings state as a normal Datastar signal patch. The
// admin page may call it from its canonical updates stream; it does not make a
// browser fetch to load personal settings.
func (h Handler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	principalID, ok := h.currentPrincipal(r)
	if !ok {
		h.writeError(w, r, ErrPrincipalRequired, http.StatusUnauthorized)
		return
	}
	state, err := h.load(r, principalID)
	if err != nil {
		h.writeError(w, r, err, http.StatusInternalServerError)
		return
	}
	_ = pagestream.PatchResponse(w, r, map[string]any{"personalSettings": state})
}

// Command applies one typed settings command and returns a complete state
// patch. Sending the resulting state keeps the browser component declarative
// and avoids ad hoc GET requests after mutations.
func (h Handler) Command(w http.ResponseWriter, r *http.Request) {
	principalID, ok := h.currentPrincipal(r)
	if !ok {
		h.writeError(w, r, ErrPrincipalRequired, http.StatusUnauthorized)
		return
	}
	if h.Service == nil {
		h.writeError(w, r, errors.New("personal settings are unavailable"), http.StatusServiceUnavailable)
		return
	}
	var signals commandSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		h.writeError(w, r, err, http.StatusBadRequest)
		return
	}
	started, err := beginPersonalSettingsInvocation(r, signals)
	if err != nil {
		h.writeError(w, r, err, http.StatusBadRequest)
		return
	}
	r = started
	var commandErr error
	var newToken *string
	switch {
	case signals.Profile.Action == "refresh":
		// The avatar upload is the binary API exception; refresh only reloads signals.
	case signals.Profile.Action != "":
		commandErr = h.Service.ApplyProfile(r.Context(), principalID, signals.Profile)
	case signals.Theme.Action != "":
		commandErr = h.Service.ApplyTheme(r.Context(), principalID, signals.Theme)
	case signals.Password.CurrentPassword != "" || signals.Password.NewPassword != "":
		commandErr = h.Service.ApplyPassword(r.Context(), principalID, signals.Password)
	case signals.Session.Action != "":
		commandErr = h.Service.RevokeSession(r.Context(), principalID, signals.Session)
	case signals.AuthoringSession.Action != "":
		commandErr = h.Service.RevokeAuthoringSession(r.Context(), principalID, signals.AuthoringSession)
	case signals.Token.Action != "":
		newToken, commandErr = h.Service.ApplyToken(r.Context(), principalID, signals.Token)
	default:
		commandErr = ErrCommandInvalid
	}
	if commandErr != nil {
		status := http.StatusBadRequest
		if errors.Is(commandErr, ErrPrincipalRequired) {
			status = http.StatusUnauthorized
		}
		h.writeError(w, r, commandErr, status)
		return
	}
	state, err := h.load(r, principalID)
	if err != nil {
		h.writeError(w, r, err, http.StatusInternalServerError)
		return
	}
	if newToken != nil {
		state.Tokens.NewToken = newToken
	}
	_ = pagestream.PatchResponse(w, r, BootstrapSignals(state))
}

func beginPersonalSettingsInvocation(r *http.Request, signals commandSignals) (*http.Request, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	idempotencyKey := "ui:" + requestID
	claims := uicommand.OperationClaims(r)
	begin := func(binding uicommand.Binding, start func() (context.Context, error)) (*http.Request, error) {
		if err := uicommand.VerifyClaim(claims, binding.OperationID()); err != nil {
			return r, err
		}
		ctx, err := start()
		if err != nil {
			return r, err
		}
		return r.WithContext(ctx), nil
	}
	switch {
	case signals.Profile.Action == "refresh":
		return r, nil
	case signals.Profile.Action != "":
		return begin(accessgen.GenUIActionUpdateCurrentPrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenUpdateCurrentPrincipalCommand(r.Context(), accessgen.GenUpdateCurrentPrincipalCommandInvocation{
				Surface: apigencommand.SurfaceUI, ConcurrencyToken: "*", RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case signals.Theme.Action != "":
		return begin(accessgen.GenUIActionUpdateCurrentTheme(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenUpdateCurrentThemeCommand(r.Context(), accessgen.GenUpdateCurrentThemeCommandInvocation{
				Surface: apigencommand.SurfaceUI, RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case signals.Password.CurrentPassword != "" || signals.Password.NewPassword != "":
		return begin(accessgen.GenUIActionChangeCurrentPassword(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenChangeCurrentPasswordCommand(r.Context(), accessgen.GenChangeCurrentPasswordCommandInvocation{
				Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case signals.Session.Action != "":
		return begin(accessgen.GenUIActionRevokeCurrentSession(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRevokeCurrentSessionCommand(r.Context(), accessgen.GenRevokeCurrentSessionCommandInvocation{
				Surface: apigencommand.SurfaceUI, Session: strings.TrimSpace(signals.Session.SessionID), RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case signals.AuthoringSession.Action != "":
		return begin(accessgen.GenUIActionRevokeCurrentAuthoringSession(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRevokeCurrentAuthoringSessionCommand(r.Context(), accessgen.GenRevokeCurrentAuthoringSessionCommandInvocation{
				Surface: apigencommand.SurfaceUI, Session: strings.TrimSpace(signals.AuthoringSession.SessionID), RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case signals.Token.Action == "create":
		return begin(accessgen.GenUIActionCreateCurrentAPIToken(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenCreateCurrentAPITokenCommand(r.Context(), accessgen.GenCreateCurrentAPITokenCommandInvocation{
				Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case signals.Token.Action == "revoke":
		return begin(accessgen.GenUIActionRevokeCurrentAPIToken(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRevokeCurrentAPITokenCommand(r.Context(), accessgen.GenRevokeCurrentAPITokenCommandInvocation{
				Surface: apigencommand.SurfaceUI, Token: strings.TrimSpace(signals.Token.TokenID), RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	default:
		return r, ErrCommandInvalid
	}
}

func (h Handler) currentPrincipal(r *http.Request) (string, bool) {
	if h.CurrentPrincipal == nil {
		return "", false
	}
	id, ok := h.CurrentPrincipal(r)
	return strings.TrimSpace(id), ok && strings.TrimSpace(id) != ""
}

func (h Handler) load(r *http.Request, principalID string) (Signal, error) {
	currentSessionID := ""
	if h.CurrentSession != nil {
		currentSessionID, _ = h.CurrentSession(r)
	}
	active := strings.TrimSpace(r.URL.Query().Get("section"))
	state, err := h.Service.Load(r.Context(), principalID, currentSessionID, active == "api-tokens")
	state.Active = active
	return state, err
}

func (h Handler) State(r *http.Request) (Signal, error) {
	principalID, ok := h.currentPrincipal(r)
	if !ok {
		return Signal{}, ErrPrincipalRequired
	}
	return h.load(r, principalID)
}

func (h Handler) writeError(w http.ResponseWriter, r *http.Request, err error, status int) {
	http.Error(w, err.Error(), status)
}
