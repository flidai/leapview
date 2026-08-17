package module

import (
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/desktopauth"
	"github.com/go-chi/chi/v5"
)

const (
	DesktopAuthorizePath     = "/auth/desktop/authorize"
	DesktopRedeemPath        = "/auth/desktop/redeem"
	DesktopSessionStatusPath = "/auth/desktop/session"
	DesktopDisconnectPath    = "/auth/desktop/disconnect"
	DesktopAuthMaxFormBytes  = 8 * 1024
)

var desktopRedeemFields = map[string]struct{}{
	"client_id": {}, "code": {}, "code_verifier": {}, "instance_id": {},
	"profile_id": {}, "redirect_uri": {},
}

var desktopDisconnectFields = map[string]struct{}{
	"instance_id": {}, "profile_id": {},
}

var desktopAuthorizeFields = map[string]bool{
	"client_id": true, "response_type": true, "code_challenge": true,
	"code_challenge_method": true, "state": true, "instance_id": true,
	"profile_id": true, "redirect_uri": true, "return_path": false,
}

func (m *Module) MountDesktopAuth(router chi.Router) {
	if m == nil {
		return
	}
	authorize := http.Handler(http.HandlerFunc(m.DesktopAuthorize))
	if m.auth != nil {
		authorize = m.auth.Middleware(authorize)
	}
	router.Method(http.MethodGet, DesktopAuthorizePath, authorize)
	router.Get(DesktopSessionStatusPath, m.DesktopSessionStatus)
	router.Post(DesktopRedeemPath, m.DesktopRedeem)
	router.Post(DesktopDisconnectPath, m.DesktopDisconnect)
}

func (m *Module) DesktopSessionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m == nil || m.auth == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	cookie, err := r.Cookie("lv_session")
	if err != nil || cookie.Value == "" {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	repository, ok := m.repositoryValue().(access.DesktopSessionRepository)
	if !ok {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if _, err := repository.DesktopSessionForToken(r.Context(), cookie.Value); err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	if _, _, ok := m.auth.authenticate(r); !ok {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) DesktopAuthorize(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.desktopAuth == nil || m.auth == nil {
		http.Error(w, "Desktop authentication is unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || principal.ID == "" {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	query := r.URL.Query()
	if !hasExactDesktopAuthorizeQuery(query) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	result, err := m.desktopAuth.Issue(r.Context(), principal.ID, desktopauth.AuthorizeRequest{
		ClientID:            query.Get("client_id"),
		ResponseType:        query.Get("response_type"),
		CodeChallenge:       query.Get("code_challenge"),
		CodeChallengeMethod: query.Get("code_challenge_method"),
		State:               query.Get("state"),
		InstanceID:          query.Get("instance_id"),
		ProfileID:           query.Get("profile_id"),
		RedirectURI:         query.Get("redirect_uri"),
		ReturnPath:          query.Get("return_path"),
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, result.RedirectURL, http.StatusFound)
}

func hasExactDesktopAuthorizeQuery(query map[string][]string) bool {
	for field, required := range desktopAuthorizeFields {
		values, ok := query[field]
		if required && !ok {
			return false
		}
		if ok && (len(values) != 1 || values[0] == "") {
			return false
		}
	}
	for field := range query {
		if _, ok := desktopAuthorizeFields[field]; !ok {
			return false
		}
	}
	return true
}

func (m *Module) DesktopRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m == nil || m.desktopAuth == nil || m.auth == nil || m.repository == nil {
		http.Error(w, "Desktop authentication is unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.URL.RawQuery != "" || !isFormContentType(r.Header.Get("Content-Type")) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DesktopAuthMaxFormBytes)
	if err := r.ParseForm(); err != nil || !hasExactDesktopRedeemForm(r.Form) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	request := desktopauth.RedeemRequest{
		ClientID:     r.Form.Get("client_id"),
		Code:         r.Form.Get("code"),
		CodeVerifier: r.Form.Get("code_verifier"),
		InstanceID:   r.Form.Get("instance_id"),
		ProfileID:    r.Form.Get("profile_id"),
		RedirectURI:  r.Form.Get("redirect_uri"),
	}
	repository := m.repositoryValue()
	if repository == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	var token string
	err := runAuthAuditedMutation(r, repository, func(transaction access.Repository) (access.AuditEventInput, error) {
		desktopStore, ok := transaction.(desktopauth.Store)
		if !ok {
			return access.AuditEventInput{}, errors.New("desktop authorization store is unavailable")
		}
		desktopRepository, ok := transaction.(access.DesktopSessionRepository)
		if !ok {
			return access.AuditEventInput{}, errors.New("desktop session repository is unavailable")
		}
		principalID, redeemErr := m.desktopAuth.RedeemWithStore(
			r.Context(),
			desktopStore,
			request,
		)
		if redeemErr != nil {
			return access.AuditEventInput{}, redeemErr
		}
		var mutationErr error
		token, mutationErr = desktopRepository.CreateDesktopSession(
			r.Context(), principalID, request.InstanceID, request.ProfileID, access.DesktopSessionAbsoluteLifetime,
		)
		return authAuditInput(
			r, "desktop_session.created", principalID, "desktop_profile",
			request.ProfileID, "", "success",
			map[string]any{"client": desktopauth.DesktopClientID, "instance_id": request.InstanceID},
		), mutationErr
	})
	if err != nil {
		if errors.Is(err, desktopauth.ErrInvalidGrant) {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, m.auth.sessionCookie(token, authNow().Add(access.DesktopSessionAbsoluteLifetime)))
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) DesktopDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m == nil || m.auth == nil || m.repository == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if r.URL.RawQuery != "" || !isFormContentType(r.Header.Get("Content-Type")) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DesktopAuthMaxFormBytes)
	if err := r.ParseForm(); err != nil || !hasExactForm(r.Form, desktopDisconnectFields) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie("lv_session")
	if err != nil || cookie.Value == "" {
		clearDesktopSessionCookie(w, m.auth.cookieSecure)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	repository := m.repositoryValue()
	desktopRepository, ok := repository.(access.DesktopSessionRepository)
	if !ok {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	binding, err := desktopRepository.DesktopSessionForToken(r.Context(), cookie.Value)
	if err != nil {
		clearDesktopSessionCookie(w, m.auth.cookieSecure)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	instanceID := r.Form.Get("instance_id")
	profileID := r.Form.Get("profile_id")
	err = runAuthAuditedMutation(r, repository, func(transaction access.Repository) (access.AuditEventInput, error) {
		desktopTransaction, ok := transaction.(access.DesktopSessionRepository)
		if !ok {
			return access.AuditEventInput{}, errors.New("desktop session repository is unavailable")
		}
		mutationErr := desktopTransaction.RevokeDesktopSession(
			r.Context(), cookie.Value, instanceID, profileID,
		)
		return authAuditInput(
			r, "desktop_session.revoked", binding.PrincipalID, "desktop_profile",
			profileID, "", "success", map[string]any{"instance_id": instanceID},
		), mutationErr
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	clearDesktopSessionCookie(w, m.auth.cookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

func isFormContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && strings.EqualFold(mediaType, "application/x-www-form-urlencoded")
}

func hasExactDesktopRedeemForm(form map[string][]string) bool {
	return hasExactForm(form, desktopRedeemFields)
}

func hasExactForm(form map[string][]string, fields map[string]struct{}) bool {
	if len(form) != len(fields) {
		return false
	}
	for field := range fields {
		values, ok := form[field]
		if !ok || len(values) != 1 || values[0] == "" {
			return false
		}
	}
	return true
}

func clearDesktopSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: "lv_session", Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}
