package personalsettings

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPersonalSettingsCommandRejectsBearerCredential(t *testing.T) {
	handler := Handler{
		Service:          &Service{},
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
		CurrentCredential: func(*http.Request) (access.APICredential, bool) {
			return access.APICredential{Token: access.APIToken{ID: "narrow-token", PrincipalID: "principal-1"}}, true
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/personal-settings/command", nil)
	response := httptest.NewRecorder()
	handler.Command(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}
