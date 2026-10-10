package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/api"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type adminConfigurationStore struct {
	current agent.ConfigurationRevision
	history map[int64]agent.ConfigurationRevision
}

func (s *adminConfigurationStore) CurrentConfiguration(context.Context) (agent.ConfigurationRevision, error) {
	if s.current.Revision == 0 {
		return s.current, agent.ErrConfigurationNotFound
	}
	return s.current, nil
}
func (s *adminConfigurationStore) ConfigurationByRevision(_ context.Context, revision int64) (agent.ConfigurationRevision, error) {
	if revision == s.current.Revision {
		return s.current, nil
	}
	if record, ok := s.history[revision]; ok {
		return record, nil
	}
	return agent.ConfigurationRevision{}, agent.ErrConfigurationNotFound
}
func (s *adminConfigurationStore) SaveConfiguration(_ context.Context, expected int64, r agent.ConfigurationRevision) (agent.ConfigurationRevision, error) {
	if expected != s.current.Revision {
		return r, agent.ErrConfigurationConflict
	}
	r.Revision = expected + 1
	if s.current.Revision > 0 {
		if s.history == nil {
			s.history = map[int64]agent.ConfigurationRevision{}
		}
		s.history[s.current.Revision] = s.current
	}
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
	used         []int64
	activations  int
	useError     error
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
	c.activations++
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
	c.used = append(c.used, r.Revision)
	if c.useError != nil {
		return c.useError
	}
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

func TestAdminProviderConfigurationRecoversUnsupportedStoredCredentials(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy ciphertext", false: "missing customer version"}[legacy], func(t *testing.T) {
			original := agent.ConfigurationRevision{Revision: 1, Enabled: true, ActorID: "original-admin", Config: agent.Config{Model: "saved-model", BaseURL: "https://provider.example/v1", APIMode: "responses", ReasoningEffort: "high", APIKey: "must-not-project-config-key"}}
			if legacy {
				original.Credential = []byte("must-not-project-legacy-ciphertext")
				// Legacy bytes remain unsupported even beside a customer reference.
				original.CredentialVersionID = "unusable-version"
			}
			store := &adminConfigurationStore{current: original}
			credentials := &adminConfigurationCredentials{store: store}
			service := agent.NewService(nil, agent.Config{})
			service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
				return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
					return agentcore.ModelResponse{}, nil
				})
			})
			manager, err := agent.NewConfigurationManager(store, service, credentials)
			if err != nil {
				t.Fatal(err)
			}
			service.SetConfigurationManager(manager)
			authenticated, isAdmin := false, false
			handler := NewHandler(Options{Service: service, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "admin"}, authenticated }, PlatformAdmin: func(context.Context, string) (bool, error) { return isAdmin, nil }, RecordCommandAudit: func(context.Context, CommandAuditInput) error { return nil }})
			read := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				handler.GetAgentConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil))
				return rec
			}
			candidate := agent.ConfigurationInput{Enabled: true, Model: original.Config.Model, BaseURL: original.Config.BaseURL, APIMode: original.Config.APIMode, ReasoningEffort: original.Config.ReasoningEffort, APIKey: "explicit-new-key"}
			request := func(action, token, revision string, input agent.ConfigurationInput, ui bool, restore int64) *httptest.ResponseRecorder {
				command := api.AdminAgentConfigPatchRequest{Action: action, ExpectedRevision: 1, Provider: &input, TestToken: token, RestoreRevision: restore}
				var body []byte
				var req *http.Request
				if ui {
					body, _ = json.Marshal(map[string]any{"adminAgentCommand": command})
					req = httptest.NewRequest(http.MethodPatch, "/admin/agent/config", strings.NewReader(string(body)))
					req.Header.Set("Accept", "text/event-stream")
					req.Header.Set("X-LeapView-Operation-ID", "updateAgentConfig")
				} else {
					body, _ = json.Marshal(command)
					req = httptest.NewRequest(http.MethodPatch, "/api/v1/agent/config", strings.NewReader(string(body)))
					ctx, _, err := agentgen.BeginGenUpdateAgentConfigCommand(req.Context(), agentgen.GenUpdateAgentConfigCommandInvocation{Surface: apigencommand.SurfaceAPI, ConcurrencyToken: revision})
					if err != nil {
						t.Fatal(err)
					}
					req = req.WithContext(ctx)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("If-Match", revision)
				rec := httptest.NewRecorder()
				if ui {
					handler.UpdateAdminConfig(rec, req)
				} else {
					handler.UpdateAgentConfig(rec, req)
				}
				return rec
			}
			for _, wantStatus := range []int{http.StatusUnauthorized, http.StatusForbidden} {
				if rec := read(); rec.Code != wantStatus {
					t.Fatalf("unauthorized read: %d", rec.Code)
				}
				for _, ui := range []bool{false, true} {
					for _, action := range []string{"test", "save"} {
						if rec := request(action, "", `"ignored"`, candidate, ui, 0); rec.Code != wantStatus {
							t.Fatalf("unauthorized %s (ui=%t): %d", action, ui, rec.Code)
						}
					}
				}
				authenticated = true
			}
			isAdmin = true
			loaded := read()
			if loaded.Code != http.StatusOK {
				t.Fatalf("recovery settings read: %d %s", loaded.Code, loaded.Body.String())
			}
			var details api.AdminAgentResponse
			if err := json.Unmarshal(loaded.Body.Bytes(), &details); err != nil {
				t.Fatal(err)
			}
			if !details.Enabled || !details.AdminManaged || !details.ConfigurationAvailable || details.Configured || details.CredentialConfigured || details.ConfigurationRevision != 1 || details.Model != original.Config.Model || details.BaseURL != original.Config.BaseURL || details.APIMode != original.Config.APIMode || details.ReasoningEffort != original.Config.ReasoningEffort || details.Status != string(agent.AgentRuntimeDegraded) || !strings.Contains(details.StatusDetail, "re-enter") {
				t.Fatalf("recovery metadata did not preserve settings and describe unavailable credential: %+v", details)
			}
			revision := loaded.Header().Get("ETag")
			if revision == "" || revision != agentResourceETag(details) {
				t.Fatal("read did not expose the exact settings ETag")
			}
			if rec := request("test", "", `"stale"`, candidate, false, 0); rec.Code != http.StatusPreconditionFailed {
				t.Fatalf("stale ETag reached validation: %d", rec.Code)
			}
			omitted := candidate
			omitted.APIKey = ""
			for _, action := range []string{"test", "save"} {
				if rec := request(action, "", revision, omitted, false, 0); rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s with omitted key: %d", action, rec.Code)
				}
			}
			if rec := request("test", "", revision, candidate, false, 1); rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("unsupported history restored: %d", rec.Code)
			}
			if credentials.probes != 0 || credentials.activations != 0 || len(credentials.used) != 0 || service.Enabled() {
				t.Fatal("unsupported credential or invalid command reached the lifecycle/runtime")
			}
			tested := request("test", "", revision, candidate, false, 0)
			if tested.Code != http.StatusOK {
				t.Fatalf("explicit re-entry test: %d %s", tested.Code, tested.Body.String())
			}
			if err := json.Unmarshal(tested.Body.Bytes(), &details); err != nil {
				t.Fatal(err)
			}
			if details.TestToken == "" || !details.Enabled || details.Model != original.Config.Model || details.ConfigurationRevision != 1 || tested.Header().Get("ETag") != revision || service.Enabled() || credentials.activations != 0 || len(credentials.used) != 0 {
				t.Fatal("test changed the saved settings, settings ETag, or active runtime")
			}
			if rec := request("save", "invalid-receipt", revision, candidate, false, 0); rec.Code != http.StatusUnprocessableEntity || store.current.Revision != 1 || service.Enabled() {
				t.Fatal("save bypassed the validation receipt")
			}
			saved := request("save", details.TestToken, revision, candidate, true, 0)
			if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "datastar-patch-signals") || !strings.Contains(saved.Body.String(), `"configurationRevision":2`) || saved.Header().Get("ETag") == revision || !service.Enabled() || store.current.CredentialVersionID != "immutable:2" || len(store.current.Credential) != 0 || store.current.Config.APIKey != "" {
				t.Fatalf("validated UI activation failed: %d %s", saved.Code, saved.Body.String())
			}
			retained, err := store.ConfigurationByRevision(t.Context(), 1)
			if err != nil || !reflect.DeepEqual(retained, original) {
				t.Fatal("recovery changed the retained original revision")
			}
			if _, err = manager.RestoreInput(t.Context(), 1); err == nil {
				t.Fatal("unsupported retained history became restorable")
			}
			if !reflect.DeepEqual(credentials.used, []int64{2}) {
				t.Fatal("recovery accessed a credential outside the activated customer revision")
			}
			for _, rec := range []*httptest.ResponseRecorder{loaded, tested, saved, read()} {
				for _, secret := range []string{original.Config.APIKey, "must-not-project-legacy-ciphertext", candidate.APIKey, "unusable-version"} {
					if strings.Contains(rec.Body.String(), secret) {
						t.Fatal("secret or unusable credential reference leaked into response")
					}
				}
			}
		})
	}
}

type unavailableAdminConfigurationStore struct {
	agent.ConfigurationStore
	err error
}

func (s unavailableAdminConfigurationStore) CurrentConfiguration(context.Context) (agent.ConfigurationRevision, error) {
	return agent.ConfigurationRevision{}, s.err
}

func TestAdminProviderConfigurationDoesNotRecoverUnknownErrors(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "credential access", true: "configuration storage"}[storageFailure], func(t *testing.T) {
			unknown := errors.New("storage unavailable")
			var store agent.ConfigurationStore = &adminConfigurationStore{current: agent.ConfigurationRevision{Revision: 1, CredentialVersionID: "customer-version"}}
			if storageFailure {
				store = unavailableAdminConfigurationStore{ConfigurationStore: store, err: unknown}
			}
			credentials := &adminConfigurationCredentials{store: store, useError: unknown}
			service := agent.NewService(nil, agent.Config{})
			manager, err := agent.NewConfigurationManager(store, service, credentials)
			if err != nil {
				t.Fatal(err)
			}
			service.SetConfigurationManager(manager)
			handler := NewHandler(Options{Service: service})
			if _, err := handler.AdminDetails(t.Context()); !errors.Is(err, unknown) {
				t.Fatalf("unknown failure was swallowed: %v", err)
			}
		})
	}
}
