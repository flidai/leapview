package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	"github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// This fixture substitutes only provider I/O and credential persistence. Session
// authentication, authorization, bootstrap, CSRF, generated command dispatch,
// concurrency, configuration manager and the Node driver are production code.
type sessionTransitionCredentials struct {
	mu                  sync.Mutex
	current             agent.ConfigurationRevision
	actor, token        string
	input               agent.ConfigurationInput
	install             func(context.Context, int64) error
	probes, activations int
}

func (s *sessionTransitionCredentials) CurrentConfiguration(context.Context) (agent.ConfigurationRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}
func (s *sessionTransitionCredentials) ConfigurationByRevision(ctx context.Context, r int64) (agent.ConfigurationRevision, error) {
	v, _ := s.CurrentConfiguration(ctx)
	if v.Revision != r {
		return v, agent.ErrConfigurationNotFound
	}
	return v, nil
}
func (s *sessionTransitionCredentials) SaveConfiguration(_ context.Context, r int64, v agent.ConfigurationRevision) (agent.ConfigurationRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.Revision != r {
		return v, agent.ErrConfigurationConflict
	}
	v.Revision = r + 1
	s.current = v
	return v, nil
}
func (s *sessionTransitionCredentials) BindRuntime(install func(context.Context, int64) error, _ func(context.Context) error) error {
	s.install = install
	return nil
}
func (s *sessionTransitionCredentials) Test(_ context.Context, actor string, r int64, input agent.ConfigurationInput) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r != 2 {
		return "", errors.New("unexpected revision")
	}
	s.probes++
	s.actor = actor
	s.input = input
	s.token = "synthetic-bound-validation"
	return s.token, nil
}
func (s *sessionTransitionCredentials) Activate(ctx context.Context, actor string, r int64, input agent.ConfigurationInput, token string) (agent.ConfigurationRevision, error) {
	s.mu.Lock()
	valid := actor == s.actor && r == 2 && input == s.input && token == s.token && token != ""
	if valid {
		s.activations++
	}
	s.mu.Unlock()
	if !valid {
		return agent.ConfigurationRevision{}, errors.New("invalid synthetic receipt")
	}
	v, err := s.SaveConfiguration(ctx, r, agent.ConfigurationRevision{Enabled: input.Enabled, ActorID: actor, CredentialVersionID: "synthetic-immutable-version", Config: agent.Config{Model: input.Model, BaseURL: input.BaseURL, APIMode: input.APIMode, ReasoningEffort: input.ReasoningEffort}})
	if err != nil {
		return v, err
	}
	return v, s.install(ctx, v.Revision)
}
func (s *sessionTransitionCredentials) UseConfiguration(_ context.Context, v agent.ConfigurationRevision, use func(agent.Config) error) error {
	if v.CredentialVersionID != "synthetic-immutable-version" {
		return errors.New("missing synthetic version")
	}
	v.Config.APIKey = "synthetic-provider-key"
	return use(v.Config)
}

func TestAgentCredentialTransitionUsesComposedSession(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the composed session regression")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(root, "scripts/demo_agent_credential_transition.mjs")
	for _, mode := range []string{"profile", "transition", "csrf", "claim", "etag", "auth"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			store := testStore(t)
			repo := testAccessRepository(store)
			local, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "session-admin@example.test", DisplayName: "Session admin"})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: local.Principal.ID, Email: local.Principal.Email, Role: access.PlatformRoleAdmin})
			if err != nil {
				t.Fatal(err)
			}
			session := testAdminBrowserSession(t, ctx, store, principal)
			config := agent.Config{Model: "model", BaseURL: "https://provider.example/v1", APIMode: "responses", ReasoningEffort: "high"}
			credentials := &sessionTransitionCredentials{current: agent.ConfigurationRevision{Revision: 2, Enabled: true, Config: config, Credential: []byte("synthetic-legacy")}}
			service := agent.NewService(testAgentRepository(store), agent.Config{})
			service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
				return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
					return agentcore.ModelResponse{}, nil
				})
			})
			manager, err := agent.NewConfigurationManager(credentials, service, credentials)
			if err != nil {
				t.Fatal(err)
			}
			service.SetConfigurationManager(manager)
			productService, err := product.NewWithStorage(newProductAuthorizationStorage(), productAuthorizationBlobs{})
			if err != nil {
				t.Fatal(err)
			}
			revision := strings.Repeat("a", 40)
			server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: session.auth, Agent: service, Product: productService, ProductStatus: adminmodule.ProductStatus{System: adminmodule.ProductSystemStatus{InstanceID: "synthetic-instance", CanonicalOrigin: "https://demo.leapview.dev", Build: buildinfo.Identity{Revision: revision, Development: true}}}}))
			httpServer := httptest.NewServer(server.Routes())
			defer httpServer.Close()
			cookies := session.sessionCookie.String() + "; " + session.csrfCookie.String() + "; pagestream_client_id=session-regression"
			if mode == "auth" {
				cookies = session.csrfCookie.String() + "; pagestream_client_id=session-regression"
			}
			provider := map[string]any{"enabled": true, "model": config.Model, "baseUrl": config.BaseURL, "apiMode": config.APIMode, "reasoningEffort": config.ReasoningEffort}
			input := map[string]any{"candidateRevision": revision, "operationDigest": "sha256:" + strings.Repeat("b", 64), "loginEmail": principal.Email, "adminPassword": "synthetic-password", "apiKey": "synthetic-provider-key", "intent": map[string]any{"reference": "session-regression", "transitionVersion": "8a22935d-54ab-4b99-90ef-72cc4130be2a", "fileDigest": "sha256:" + strings.Repeat("c", 64), "installationId": "test-installation", "instanceId": "synthetic-instance", "customerOwnerId": "test-customer", "expectedRevision": 2, "actorId": principal.ID, "providerAddress": "93.184.215.14", "provider": provider}}
			failure := ""
			if mode != "profile" && mode != "transition" {
				failure = mode
			}
			payload, _ := json.Marshal(map[string]any{"driver": driver, "origin": httpServer.URL, "cookies": cookies, "csrf": session.csrfToken, "input": input, "mode": mode, "failure": failure})
			bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			command := exec.CommandContext(bounded, node, filepath.Join(root, "scripts/tests/demo_agent_session_composed.mjs"))
			command.Stdin = bytes.NewReader(payload)
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "LANG=C.UTF-8"}
			output, err := command.CombinedOutput()
			if strings.Contains(string(output), "COOKIE_PROFILE_BEARER_REQUIRED\n") {
				t.Fatal("production cookie identity transport returned 401 BEARER_REQUIRED")
			}
			if err != nil || string(output) != "COMPOSED_SESSION_PASSED\n" {
				t.Fatalf("production session driver failed (%s); response/input output deliberately withheld", mode)
			}
			credentials.mu.Lock()
			defer credentials.mu.Unlock()
			if mode == "transition" {
				if credentials.probes != 1 || credentials.activations != 1 || credentials.current.Revision != 3 {
					t.Fatal("supported Test/Save did not activate exactly one immutable revision")
				}
			} else if credentials.probes != 0 || credentials.activations != 0 {
				t.Fatal("negative/session-read probe reached provider validation or activation")
			}
		})
	}
}
