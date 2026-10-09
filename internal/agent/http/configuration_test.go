package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type adminConfigurationStore struct{ current agent.ConfigurationRevision }

func (s *adminConfigurationStore) CurrentConfiguration(context.Context) (agent.ConfigurationRevision, error) {
	if s.current.Revision == 0 {
		return s.current, agent.ErrConfigurationNotFound
	}
	return s.current, nil
}
func (s *adminConfigurationStore) ConfigurationByRevision(context.Context, int64) (agent.ConfigurationRevision, error) {
	return s.current, nil
}
func (s *adminConfigurationStore) SaveConfiguration(_ context.Context, expected int64, r agent.ConfigurationRevision) (agent.ConfigurationRevision, error) {
	if expected != s.current.Revision {
		return r, agent.ErrConfigurationConflict
	}
	r.Revision = expected + 1
	s.current = r
	return r, nil
}

type adminConfigurationCredentials struct {
	store        agent.ConfigurationStore
	probes       int
	aborts       int
	actor, token string
	expected     int64
	input        agent.ConfigurationInput
	versions     map[string]string
	install      func(context.Context, int64) error
}

func (c *adminConfigurationCredentials) AbortConfiguration(_ context.Context, actor string) error {
	if actor != "admin" {
		return errors.New("wrong actor")
	}
	c.aborts++
	return nil
}

func (c *adminConfigurationCredentials) BindRuntime(install func(context.Context, int64) error, _ func(context.Context) error) error {
	c.install = install
	return nil
}
func (c *adminConfigurationCredentials) Test(_ context.Context, actor string, expected int64, input agent.ConfigurationInput) (string, error) {
	c.probes++
	c.actor, c.expected, c.input = actor, expected, input
	c.token = fmt.Sprintf("validation:%d", c.probes)
	return c.token, nil
}
func (c *adminConfigurationCredentials) Activate(ctx context.Context, actor string, expected int64, input agent.ConfigurationInput, token string) (agent.ConfigurationRevision, error) {
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
func (c *adminConfigurationCredentials) UseConfiguration(_ context.Context, r agent.ConfigurationRevision, use func(agent.Config) error) error {
	key, ok := c.versions[r.CredentialVersionID]
	if !ok {
		return errors.New("credential version missing")
	}
	config := r.Config
	config.APIKey = key
	return use(config)
}

func TestAdminProviderConfigurationAuthorizationAndSecretRedaction(t *testing.T) {
	service := agent.NewService(nil, agent.Config{})
	service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			return agentcore.ModelResponse{}, nil
		})
	})
	store := &adminConfigurationStore{}
	credentials := &adminConfigurationCredentials{store: store}
	manager, err := agent.NewConfigurationManager(store, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	service.SetConfigurationManager(manager)
	isAdmin := false
	handler := NewHandler(Options{Service: service, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "admin"}, true }, PlatformAdmin: func(context.Context, string) (bool, error) { return isAdmin, nil }, RecordCommandAudit: func(context.Context, CommandAuditInput) error { return nil }})
	provider := map[string]any{"enabled": true, "model": "model", "baseUrl": "https://provider.example/v1", "apiMode": "responses", "reasoningEffort": "", "apiKey": "private-provider-key"}
	expectedRevision, restoreRevision := int64(0), int64(0)
	request := func(action, token string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"action": action, "provider": provider, "testToken": token, "expectedRevision": expectedRevision, "restoreRevision": restoreRevision})
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/agent/config", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		details, err := handler.AdminDetails(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		revision := agentResourceETag(details)
		req.Header.Set("If-Match", revision)
		ctx, guard, err := agentgen.BeginGenUpdateAgentConfigCommand(req.Context(), agentgen.GenUpdateAgentConfigCommandInvocation{Surface: apigencommand.SurfaceAPI, ConcurrencyToken: revision})
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.UpdateAgentConfig(rec, req)
		if rec.Code == http.StatusOK && !guard.Completed() {
			t.Fatal("generated command guard did not complete")
		}
		return rec
	}
	if rec := request("test", ""); rec.Code != http.StatusForbidden || credentials.probes != 0 {
		t.Fatalf("non-admin reached provider: status=%d probes=%d", rec.Code, credentials.probes)
	}
	// The UI command independently enforces the same permission.
	uiRequest := httptest.NewRequest(http.MethodPost, "/admin/agent/config", strings.NewReader(`{}`))
	uiDenied := httptest.NewRecorder()
	handler.UpdateAdminConfig(uiDenied, uiRequest)
	if uiDenied.Code != http.StatusForbidden {
		t.Fatalf("UI status=%d", uiDenied.Code)
	}
	isAdmin = true
	tested := request("test", "")
	if tested.Code != http.StatusOK {
		t.Fatalf("test: %d %s", tested.Code, tested.Body.String())
	}
	var result struct {
		TestToken string `json:"testToken"`
	}
	if err = json.Unmarshal(tested.Body.Bytes(), &result); err != nil || result.TestToken == "" {
		t.Fatalf("test token missing: %v", err)
	}
	if service.Enabled() {
		t.Fatal("connection test changed active runtime")
	}
	saved := request("save", result.TestToken)
	if saved.Code != http.StatusOK {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	read := httptest.NewRecorder()
	handler.GetAgentConfig(read, httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil))
	for _, rec := range []*httptest.ResponseRecorder{tested, saved, read} {
		if strings.Contains(rec.Body.String(), "private-provider-key") {
			t.Fatal("provider key leaked into response")
		}
	}
	if stale := request("save", result.TestToken); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale configuration status=%d", stale.Code)
	}
	expectedRevision, restoreRevision = 1, 1
	provider = nil
	restoredTest := request("test", "")
	if restoredTest.Code != http.StatusOK {
		t.Fatalf("restore-only test: %d %s", restoredTest.Code, restoredTest.Body.String())
	}
	if err := json.Unmarshal(restoredTest.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	restoredSave := request("save", result.TestToken)
	if restoredSave.Code != http.StatusOK || store.current.Revision != 2 {
		t.Fatalf("restore-only save: %d %s", restoredSave.Code, restoredSave.Body.String())
	}
	if store.current.CredentialVersionID != "immutable:2" || store.current.Config.APIKey != "" || len(store.current.Credential) != 0 {
		t.Fatal("route did not persist an opaque immutable credential reference")
	}
	if !service.Enabled() || store.current.ActorID != "admin" {
		t.Fatal("admin save did not persist/activate")
	}
	provider, restoreRevision = nil, 0
	if canceled := request("abort", ""); canceled.Code != http.StatusOK || credentials.aborts != 1 || store.current.Revision != 2 {
		t.Fatalf("cancel without provider changed configuration: %d %s", canceled.Code, canceled.Body.String())
	}
	if missing := request("test", ""); missing.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing provider: %d %s", missing.Code, missing.Body.String())
	}
	isAdmin = false
	if denied := request("abort", ""); denied.Code != http.StatusForbidden || credentials.aborts != 1 {
		t.Fatalf("unauthorized cancel reached lifecycle: %d", denied.Code)
	}
}
