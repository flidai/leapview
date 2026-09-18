package module

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type sessionCredentialEvidenceContextKey struct{}

type sessionCredentialResolver interface {
	CredentialForSessionToken(context.Context, string) (access.Session, error)
}

func (a *Auth) sessionEvidence(r *http.Request, principalID string) (access.CredentialEvidence, bool) {
	if a == nil || r == nil || strings.TrimSpace(principalID) == "" {
		return access.CredentialEvidence{}, false
	}
	cookie, err := r.Cookie(a.SessionCookieName())
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return access.CredentialEvidence{}, false
	}
	resolver, supported := a.sessions.(sessionCredentialResolver)
	if !supported {
		return access.CredentialEvidence{}, false
	}
	session, err := resolver.CredentialForSessionToken(r.Context(), cookie.Value)
	if err != nil || session.Kind != access.SessionKindBrowser || session.PrincipalID != principalID || session.ID == "" || session.TokenFingerprint == "" {
		return access.CredentialEvidence{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil || !expiresAt.After(authNow().UTC()) {
		return access.CredentialEvidence{}, false
	}
	return access.CredentialEvidence{Class: "session", ID: session.ID, Fingerprint: session.TokenFingerprint, PrincipalID: principalID, ExpiresAt: expiresAt.UTC()}, true
}

// SessionCredentialEvidenceFromContext returns non-secret browser-session
// evidence. Browser sessions deliberately do not populate APICredential,
// because API-token attenuation must not classify a session as a zero-scope
// bearer token.
func SessionCredentialEvidenceFromContext(ctx context.Context) (access.CredentialEvidence, bool) {
	evidence, ok := ctx.Value(sessionCredentialEvidenceContextKey{}).(access.CredentialEvidence)
	return evidence, ok
}

func withSessionCredentialEvidence(ctx context.Context, evidence access.CredentialEvidence) context.Context {
	return context.WithValue(ctx, sessionCredentialEvidenceContextKey{}, evidence)
}
