package module

import (
	"context"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// Authenticate establishes the canonical browser principal and rejects
// requests without one. Unlike the login-oriented Auth middleware, this seam
// is deliberately status based: callers are already on a protected browser
// route and an absent or invalid credential is a 401.
//
// The local development principal is returned by CurrentPrincipal when the
// access module has no configured Auth surface, and Auth's explicit dev
// bypass is also accepted. Neither path skips request-specific resource
// validation performed by project guards.
func (m *Module) Authenticate(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || r == nil {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		principal, ok := m.CurrentPrincipal(r)
		var credential *access.APICredential
		if m.auth != nil {
			if current, found := m.auth.APICredential(r); found {
				credential = &current
			}
		}
		if !ok && m.auth != nil {
			principal, credential, ok = m.auth.Authenticate(r)
			if ok {
				ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
				if credential != nil {
					ctx = context.WithValue(ctx, apiCredentialContextKey{}, *credential)
				}
				r = r.WithContext(ctx)
			}
		}
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if m.auth != nil && m.auth.mustChangeLocalPassword(r, principal.ID, credential) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePlatformAdmin authorizes instance-wide administration using the
// canonical PlatformRoleReader. Missing repositories, missing role readers,
// and checker errors all fail closed with service-unavailable. A non-admin is
// forbidden. Development bypass is the one intentional exception and remains
// an authenticated principal.
func (m *Module) RequirePlatformAdmin(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return m.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := m.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if principal.DevBypass {
			next.ServeHTTP(w, r)
			return
		}
		repository := m.repositoryValue()
		if repository == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		checker, ok := any(repository).(access.PlatformRoleReader)
		if !ok || checker == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		admin, err := checker.IsPlatformAdmin(r.Context(), principal.ID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !admin {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
