package module

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
)

const testCSRFKey = "0123456789abcdef0123456789abcdef"

func mustNewAuth(t *testing.T, repository access.Repository, config AuthConfig) *Auth {
	t.Helper()
	if config.CSRFKey == "" {
		config.CSRFKey = testCSRFKey
	}
	auth, err := NewAuth(repository, config)
	if err != nil {
		t.Fatalf("construct auth: %v", err)
	}
	return auth
}

func TestNewAuthRejectsMissingOrShortCSRFKey(t *testing.T) {
	for _, key := range []string{"", "short", "0123456789abcdef0123456789abcde"} {
		if auth, err := NewAuth(nil, AuthConfig{CSRFKey: key}); err == nil || auth != nil {
			t.Fatalf("NewAuth(%q) = %#v, %v; want nil and validation error", key, auth, err)
		}
	}
	if key, err := csrfKey("0123456789abcdef0123456789abcdef"); err != nil || len(key) != 32 {
		t.Fatalf("valid csrfKey = %d bytes, %v; want 32 bytes and nil", len(key), err)
	}
}
