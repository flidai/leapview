package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestAgentEditingRequiresPlatformAdminRatherThanProjectAdmin(t *testing.T) {
	for _, isPlatformAdmin := range []bool{false, true} {
		model := ReadModel{AuthConfigured: true, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal"}, true }, CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return []access.Capability{access.CapabilityProjectAdmin}, nil
		}, PlatformAdmin: func(context.Context, string) (bool, error) { return isPlatformAdmin, nil }}
		data, err := model.agentData(httptest.NewRequest(http.MethodGet, "/admin/agent", nil))
		if err != nil {
			t.Fatal(err)
		}
		if data.CanWrite != isPlatformAdmin {
			t.Fatalf("platformAdmin=%t canWrite=%t", isPlatformAdmin, data.CanWrite)
		}
	}
}
