package transport

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

const ClientIDCookieName = "pagestream_client_id"

var readClientIDRandom = rand.Read

// EnsureClientID returns the valid page-stream client ID from the request or
// creates a new session-scoped ID. Cookie policy belongs to the product HTTP
// transport rather than the Pagestream framework.
func EnsureClientID(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie(ClientIDCookieName); err == nil && validClientID(cookie.Value) {
		return cookie.Value, nil
	}
	clientID, err := newClientID()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     ClientIDCookieName,
		Value:    clientID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	return clientID, nil
}

// RequireClientID ensures a client ID or writes a service-unavailable response.
func RequireClientID(w http.ResponseWriter, r *http.Request) (string, bool) {
	clientID, err := EnsureClientID(w, r)
	if err != nil {
		http.Error(w, "page-stream client identity is unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	return clientID, true
}

// ClientIDFromRequest resolves an explicitly supplied ID before the product
// cookie. Invalid or missing identity fails closed with an empty result.
func ClientIDFromRequest(r *http.Request, supplied string) string {
	if supplied = strings.TrimSpace(supplied); validClientID(supplied) {
		return supplied
	}
	if r == nil {
		return ""
	}
	cookie, err := r.Cookie(ClientIDCookieName)
	if err == nil && validClientID(cookie.Value) {
		return cookie.Value
	}
	return ""
}

func validClientID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

func newClientID() (string, error) {
	var value [16]byte
	if _, err := readClientIDRandom(value[:]); err != nil {
		return "", fmt.Errorf("generate page-stream client identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
