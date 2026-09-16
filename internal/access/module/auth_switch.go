package module

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// SwitchAccount ends the current browser session before returning to the
// authentication surface. The signed return cookie carries only a validated
// same-origin browser route so either local or OIDC login can resume the page
// that produced the authorization denial.
func (a *Auth) SwitchAccount(w http.ResponseWriter, r *http.Request) {
	if err := parseLocalAuthForm(w, r); err != nil {
		writeLocalAuthFormError(w, err)
		return
	}
	target, err := validatedAuthenticationReturnTarget(r.Form.Get("return_to"))
	if err != nil {
		http.Error(w, "invalid authentication return target", http.StatusBadRequest)
		return
	}
	if err := a.revokeCurrentBrowserSession(w, r); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.authReturnCookie(target))
	http.Redirect(w, r, "/login?error=forbidden&switch=1", http.StatusSeeOther)
}

func (a *Auth) revokeCurrentBrowserSession(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(a.SessionCookieName()); err == nil {
		principal, _ := a.sessions.PrincipalForToken(r.Context(), cookie.Value)
		if err := runAuthAuditedMutation(r, a.repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
			mutationErr := txRepo.DeleteSession(r.Context(), cookie.Value)
			return authAuditInput(r, "session.revoked", principal.ID, "session", "", "", "success", nil), mutationErr
		}); err != nil {
			return err
		}
		recordAccessAudit(r, a.repo, "sign_out", principal.ID, "principal", principal.ID, "", "success", nil)
	}
	http.SetCookie(w, a.expiredSessionCookie())
	return nil
}

func validatedAuthenticationReturnTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" || len(target) > maxAuthReturnTargetBytes {
		return "", errors.New("invalid authentication return target")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !isAuthenticationReturnPath(parsed.Path) {
		return "", errors.New("invalid authentication return target")
	}
	return parsed.RequestURI(), nil
}

func oidcAuthorizationURL(raw string, selectAccount bool) (string, error) {
	if !selectAccount {
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	values := parsed.Query()
	values.Set("prompt", "select_account")
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}
