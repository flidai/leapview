package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type routeConfigurationCredentials struct {
	store        agent.ConfigurationStore
	probes       int
	actor, token string
	expected     int64
	input        agent.ConfigurationInput
	versions     map[string]string
	install      func(context.Context, int64) error
}

func (c *routeConfigurationCredentials) BindRuntime(install func(context.Context, int64) error, _ func(context.Context) error) error {
	c.install = install
	return nil
}
func (c *routeConfigurationCredentials) Test(_ context.Context, actor string, expected int64, input agent.ConfigurationInput) (string, error) {
	c.probes++
	c.actor, c.expected, c.input = actor, expected, input
	c.token = fmt.Sprintf("validation:%d", c.probes)
	return c.token, nil
}
func (c *routeConfigurationCredentials) Activate(ctx context.Context, actor string, expected int64, input agent.ConfigurationInput, token string) (agent.ConfigurationRevision, error) {
	if token == "" || token != c.token || actor != c.actor || expected != c.expected || input != c.input {
		return agent.ConfigurationRevision{}, errors.New("validation receipt mismatch")
	}
	version := fmt.Sprintf("immutable:%d", expected+1)
	saved, err := c.store.SaveConfiguration(ctx, expected, agent.ConfigurationRevision{Enabled: input.Enabled, ActorID: actor, CredentialVersionID: version, Config: agent.Config{Model: input.Model, BaseURL: input.BaseURL, APIMode: input.APIMode, ReasoningEffort: input.ReasoningEffort}})
	if err != nil {
		return agent.ConfigurationRevision{}, err
	}
	if c.versions == nil {
		c.versions = map[string]string{}
	}
	c.versions[version] = input.APIKey
	return saved, c.install(ctx, saved.Revision)
}
func (c *routeConfigurationCredentials) UseConfiguration(_ context.Context, r agent.ConfigurationRevision, use func(agent.Config) error) error {
	key, ok := c.versions[r.CredentialVersionID]
	if !ok {
		return errors.New("credential version missing")
	}
	config := r.Config
	config.APIKey = key
	return use(config)
}

func TestMountedAdminConfigurationTestAndSave(t *testing.T) {
	service := agent.NewService(nil, agent.Config{})
	service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			return agentcore.ModelResponse{}, nil
		})
	})
	store := &routeConfigurationStore{}
	credentials := &routeConfigurationCredentials{store: store}
	manager, err := agent.NewConfigurationManager(store, service, credentials)
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
	if response.TestToken == "" || credentials.probes != 1 || service.Enabled() {
		t.Fatal("test must probe without activation")
	}
	saved := request("save", response.TestToken)
	if saved.Code != http.StatusOK || !service.Enabled() || store.current.Revision != 1 {
		t.Fatalf("mounted save: %d %s", saved.Code, saved.Body.String())
	}
	if store.current.CredentialVersionID != "immutable:1" || store.current.Config.APIKey != "" || len(store.current.Credential) != 0 {
		t.Fatal("mounted route persisted credentials instead of an immutable reference")
	}
	for _, response := range []*httptest.ResponseRecorder{tested, saved} {
		if strings.Contains(response.Body.String(), `"apiKey":"secret"`) {
			t.Fatal("mounted route returned provider credentials")
		}
	}
}
