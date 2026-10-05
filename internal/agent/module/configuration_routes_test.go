package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	agentapi "github.com/flidai/leapview/internal/agent/api"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/go-chi/chi/v5"
)

type routeConfigurationStore struct{ current agent.ConfigurationRevision }

func (s *routeConfigurationStore) CurrentConfiguration(context.Context) (agent.ConfigurationRevision, error) {
	if s.current.Revision == 0 {
		return s.current, agent.ErrConfigurationNotFound
	}
	return s.current, nil
}
func (s *routeConfigurationStore) ConfigurationByRevision(context.Context, int64) (agent.ConfigurationRevision, error) {
	return s.current, nil
}
func (s *routeConfigurationStore) SaveConfiguration(_ context.Context, expected int64, r agent.ConfigurationRevision) (agent.ConfigurationRevision, error) {
	if expected != s.current.Revision {
		return r, agent.ErrConfigurationConflict
	}
	r.Revision = expected + 1
	s.current = r
	return r, nil
}

func TestMountedAdminConfigurationTestAndSave(t *testing.T) {
	service := agent.NewService(nil, agent.Config{})
	service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			return agentcore.ModelResponse{}, nil
		})
	})
	store := &routeConfigurationStore{}
	probes := 0
	manager, err := agent.NewConfigurationManager(store, service, strings.Repeat("12", 32), func(context.Context, agent.Config) error { probes++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	service.SetConfigurationManager(manager)
	handler := agenthttp.NewHandler(agenthttp.Options{Service: service, CurrentPrincipal: func(*http.Request) (agenthttp.Principal, bool) { return agenthttp.Principal{ID: "admin"}, true }, PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil }, RecordCommandAudit: func(context.Context, agenthttp.CommandAuditInput) error { return nil }})
	router := chi.NewRouter()
	(&Module{handler: handler}).MountAuthenticated(router, RouteGuard{RequirePlatformAdmin: func(next http.Handler) http.Handler { return next }})
	request := func(action, token string) *httptest.ResponseRecorder {
		details, err := handler.AdminDetails(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"adminAgentCommand": map[string]any{"action": action, "expectedRevision": 0, "testToken": token, "provider": map[string]any{"enabled": true, "model": "test-model", "baseUrl": "https://provider.example/v1", "apiMode": "responses", "apiKey": "secret"}}})
		req := httptest.NewRequest(http.MethodPatch, "/admin/agent/config", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LeapView-Operation-ID", "updateAgentConfig")
		revision, err := agentapi.AgentConfigRevision(details)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("If-Match", revision)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	tested := request("test", "")
	if tested.Code != http.StatusOK {
		t.Fatalf("mounted test: %d %s", tested.Code, tested.Body.String())
	}
	var response struct {
		TestToken string `json:"testToken"`
	}
	if err := json.Unmarshal(tested.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TestToken == "" || probes != 1 || service.Enabled() {
		t.Fatal("test must probe without activation")
	}
	saved := request("save", response.TestToken)
	if saved.Code != http.StatusOK || !service.Enabled() || store.current.Revision != 1 {
		t.Fatalf("mounted save: %d %s", saved.Code, saved.Body.String())
	}
}
