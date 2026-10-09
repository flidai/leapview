package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

type configurationMemory struct{ rows []ConfigurationRevision }

func (m *configurationMemory) CurrentConfiguration(context.Context) (ConfigurationRevision, error) {
	if len(m.rows) == 0 {
		return ConfigurationRevision{}, ErrConfigurationNotFound
	}
	return m.rows[len(m.rows)-1], nil
}
func (m *configurationMemory) ConfigurationByRevision(_ context.Context, revision int64) (ConfigurationRevision, error) {
	for _, r := range m.rows {
		if r.Revision == revision {
			return r, nil
		}
	}
	return ConfigurationRevision{}, ErrConfigurationNotFound
}
func (m *configurationMemory) SaveConfiguration(_ context.Context, expected int64, r ConfigurationRevision) (ConfigurationRevision, error) {
	if int64(len(m.rows)) != expected {
		return ConfigurationRevision{}, ErrConfigurationConflict
	}
	r.Revision = expected + 1
	m.rows = append(m.rows, r)
	return r, nil
}

// configurationLifecycleStub models the shared port; credential cryptography and
// durable receipt validation belong to the lifecycle backend's own tests.
type configurationLifecycleStub struct {
	store                    ConfigurationStore
	versions                 map[string]string
	drafts                   map[string]configurationDraft
	used                     []string
	testCalls, activateCalls int
	install                  func(context.Context, int64) error
	bindError                error
}
type configurationDraft struct {
	actor    string
	expected int64
	input    ConfigurationInput
}

func newConfigurationLifecycleStub(store ConfigurationStore) *configurationLifecycleStub {
	return &configurationLifecycleStub{store: store, versions: map[string]string{}, drafts: map[string]configurationDraft{}}
}
func (s *configurationLifecycleStub) BindRuntime(install func(context.Context, int64) error, refresh func(context.Context) error) error {
	s.install = install
	if refresh == nil {
		return errors.New("missing runtime refresh callback")
	}
	return s.bindError
}
func (s *configurationLifecycleStub) Test(_ context.Context, actor string, expected int64, input ConfigurationInput) (string, error) {
	s.testCalls++
	token := fmt.Sprintf("validation:%d", s.testCalls)
	s.drafts[token] = configurationDraft{actor: actor, expected: expected, input: input}
	return token, nil
}
func (s *configurationLifecycleStub) Activate(ctx context.Context, actor string, expected int64, input ConfigurationInput, token string) (ConfigurationRevision, error) {
	s.activateCalls++
	draft, ok := s.drafts[token]
	if !ok || draft.actor != actor || draft.expected != expected || draft.input != input {
		return ConfigurationRevision{}, errors.New("validation receipt does not match actor, revision and input")
	}
	version := fmt.Sprintf("credential-version:%d", expected+1)
	config := Config{Model: input.Model, BaseURL: input.BaseURL, APIMode: input.APIMode, ReasoningEffort: input.ReasoningEffort}
	saved, err := s.store.SaveConfiguration(ctx, expected, ConfigurationRevision{Enabled: input.Enabled, Config: config, CredentialVersionID: version, ActorID: actor})
	if err != nil {
		return ConfigurationRevision{}, err
	}
	s.versions[version] = input.APIKey
	if err = s.install(ctx, saved.Revision); err != nil {
		return ConfigurationRevision{}, err
	}
	return saved, nil
}
func (s *configurationLifecycleStub) UseConfiguration(_ context.Context, revision ConfigurationRevision, use func(Config) error) error {
	s.used = append(s.used, revision.CredentialVersionID)
	secret, ok := s.versions[revision.CredentialVersionID]
	if !ok {
		return errors.New("customer credential version is unavailable")
	}
	config := revision.Config
	config.APIKey = secret
	return use(config)
}

func TestAdminConfigurationTestSaveAndRestart(t *testing.T) {
	ctx := context.Background()
	repo := &configurationMemory{}
	service := NewService(nil, Config{})
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	credentials := newConfigurationLifecycleStub(repo)
	manager, err := NewConfigurationManager(repo, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	candidate := ConfigurationInput{Enabled: true, Model: "my-model", BaseURL: "https://example.com/v1", APIMode: "responses", APIKey: "private-secret"}
	token, err := manager.Test(ctx, "admin", 0, candidate)
	if err != nil || credentials.testCalls != 1 {
		t.Fatalf("test: %v calls=%d", err, credentials.testCalls)
	}
	if service.Enabled() {
		t.Fatal("testing activated configuration")
	}
	if _, err = manager.Save(ctx, "different-admin", 0, candidate, token); err == nil {
		t.Fatal("accepted another admin's test")
	}
	changed := candidate
	changed.Model = "other"
	if _, err = manager.Save(ctx, "admin", 0, changed, token); err == nil {
		t.Fatal("accepted changed candidate")
	}
	saved, err := manager.Save(ctx, "admin", 0, candidate, token)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || !service.Enabled() {
		t.Fatal("not activated")
	}
	if repo.rows[0].Config.APIKey != "" || len(repo.rows[0].Credential) != 0 || repo.rows[0].CredentialVersionID != "credential-version:1" {
		t.Fatal("stored plaintext credential")
	}
	if _, err = manager.Save(ctx, "admin", 0, candidate, token); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if err = service.ApplyRuntimeConfig(Config{APIKey: "env-key", Model: "env-model"}, true); err != nil {
		t.Fatal(err)
	}
	if service.RuntimeStatus().Model != "my-model" {
		t.Fatal("deployment overrode admin")
	}
	restarted := NewService(nil, Config{})
	restarted.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	restartedCredentials := newConfigurationLifecycleStub(repo)
	restartedCredentials.versions = credentials.versions
	manager, err = NewConfigurationManager(repo, restarted, restartedCredentials)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(restartedCredentials.used) != 1 || restartedCredentials.used[0] != saved.CredentialVersionID {
		t.Fatal("restart did not resolve the exact saved credential version")
	}
	if restarted.RuntimeStatus().Model != "my-model" {
		t.Fatal("restart lost config")
	}
}

func TestUnspecifiedReasoningRemainsUnspecified(t *testing.T) {
	c := Config{APIKey: "secret", Model: "model", APIMode: "responses"}
	if c.NormalizedReasoningEffort() != "" {
		t.Fatal("unset reasoning has a hidden default")
	}
	if err := c.Validate(true); err != nil {
		t.Fatal(err)
	}
}

func TestAdminConfigurationRejectsUnsafeTransitionsAndMissingVersions(t *testing.T) {
	ctx := context.Background()
	repo := &configurationMemory{}
	service := NewService(nil, Config{APIKey: "legacy-secret", Model: "legacy", BaseURL: "https://old.example/v1"})
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	credentials := newConfigurationLifecycleStub(repo)
	manager, err := NewConfigurationManager(repo, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	input := ConfigurationInput{Enabled: true, Model: "new", BaseURL: "https://new.example/v1", APIMode: "responses"}
	if _, err := manager.Test(ctx, "admin", 0, input); err == nil {
		t.Fatal("forwarded legacy key to a new endpoint")
	}
	input.APIKey = "new-secret"
	token, err := manager.Test(ctx, "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Test(ctx, "admin", 0, input)
	if err != nil || second == token {
		t.Fatal("test tokens must identify separate tests")
	}
	if _, err = manager.Save(ctx, "admin", 0, input, token); err != nil {
		t.Fatal(err)
	}
	unavailable, _ := NewConfigurationManager(repo, service, newConfigurationLifecycleStub(repo))
	if _, err = unavailable.RestoreInput(ctx, 1); err == nil || !strings.Contains(err.Error(), "credential version is unavailable") {
		t.Fatalf("missing immutable credential version accepted: %v", err)
	}
	disable := ConfigurationInput{Enabled: false, Model: "new", BaseURL: input.BaseURL, APIMode: "responses"}
	token, err = manager.Test(ctx, "admin", 1, disable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Save(ctx, "admin", 1, disable, token); err != nil {
		t.Fatal(err)
	}
	if service.Enabled() {
		t.Fatal("disabled configuration stayed enabled")
	}
	if err = service.ApplyRuntimeConfig(Config{APIKey: "env", Model: "env"}, true); err != nil {
		t.Fatal(err)
	}
	if service.Enabled() {
		t.Fatal("environment resurrected disabled agent")
	}
	restore, err := manager.RestoreInput(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	token, err = manager.Test(ctx, "admin", 2, restore)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Save(ctx, "admin", 2, restore, token); err != nil {
		t.Fatal(err)
	}
	if !service.Enabled() || service.RuntimeStatus().Model != "new" {
		t.Fatal("restore did not activate")
	}
}

func TestAdminConfigurationPreparationFailureDoesNotPersist(t *testing.T) {
	service := NewService(nil, Config{})
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return nil })
	repo := &configurationMemory{}
	credentials := newConfigurationLifecycleStub(repo)
	manager, err := NewConfigurationManager(repo, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	input := ConfigurationInput{Enabled: true, Model: "model", BaseURL: "https://provider.example", APIMode: "responses", APIKey: "secret"}
	token, err := manager.Test(t.Context(), "admin", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Save(t.Context(), "admin", 0, input, token); err == nil {
		t.Fatal("accepted a runtime that cannot activate")
	}
	if len(repo.rows) != 0 || service.Enabled() || credentials.activateCalls != 0 {
		t.Fatal("failed preparation changed durable or active configuration")
	}
}

func TestAdminConfigurationRejectsLegacyCiphertextWithoutCredentialFallback(t *testing.T) {
	for _, row := range []ConfigurationRevision{
		{Revision: 1, Credential: []byte("legacy ciphertext"), CredentialVersionID: "credential-version:1"},
		{Revision: 1},
	} {
		repo := &configurationMemory{rows: []ConfigurationRevision{row}}
		credentials := newConfigurationLifecycleStub(repo)
		service := NewService(nil, Config{})
		manager, err := NewConfigurationManager(repo, service, credentials)
		if err != nil {
			t.Fatal(err)
		}
		err = manager.Refresh(t.Context())
		expected := "complete credential setup"
		if len(row.Credential) > 0 {
			expected = "legacy agent credential format is unsupported"
		}
		if err == nil || !strings.Contains(err.Error(), expected) {
			t.Fatalf("unsupported revision: %v", err)
		}
		if _, err = manager.RestoreInput(t.Context(), 1); err == nil {
			t.Fatal("unsupported history was restored")
		}
		if len(credentials.used) != 0 || service.Enabled() {
			t.Fatal("unsupported storage reached credentials/runtime")
		}
	}
}

func TestAdminConfigurationRequiresActorAndExactRevisionBeforeCredentialAccess(t *testing.T) {
	repo := &configurationMemory{}
	credentials := newConfigurationLifecycleStub(repo)
	manager, err := NewConfigurationManager(repo, NewService(nil, Config{}), credentials)
	if err != nil {
		t.Fatal(err)
	}
	input := ConfigurationInput{Enabled: true, Model: "model", BaseURL: "https://provider.example", APIMode: "responses", APIKey: "secret"}
	for _, actor := range []string{"", "admin"} {
		expected := int64(0)
		if actor != "" {
			expected = 3
		}
		if _, err = manager.Test(t.Context(), actor, expected, input); err == nil {
			t.Fatal("invalid actor/revision reached test")
		}
		if _, err = manager.Save(t.Context(), actor, expected, input, "receipt"); err == nil {
			t.Fatal("invalid actor/revision reached activation")
		}
	}
	if credentials.testCalls != 0 || credentials.activateCalls != 0 || len(credentials.used) != 0 {
		t.Fatal("invalid actor/revision reached lifecycle backend")
	}
}

func TestAdminConfigurationRequiresCredentialBackendAndRuntimeBinding(t *testing.T) {
	repo := &configurationMemory{}
	service := NewService(nil, Config{})
	var absent *configurationLifecycleStub
	for _, backend := range []ConfigurationCredentials{nil, absent} {
		if _, err := NewConfigurationManager(repo, service, backend); err == nil {
			t.Fatal("missing credential backend accepted")
		}
	}
	backend := newConfigurationLifecycleStub(repo)
	backend.bindError = errors.New("runtime binding failed")
	if _, err := NewConfigurationManager(repo, service, backend); !errors.Is(err, backend.bindError) {
		t.Fatalf("runtime binding error lost: %v", err)
	}
}

func TestAdminConfigurationHistoricalRuntimeResolvesPinnedCredentialVersion(t *testing.T) {
	repo := &configurationMemory{rows: []ConfigurationRevision{
		{Revision: 1, Enabled: true, Config: Config{Model: "old", BaseURL: "https://provider.example", APIMode: "responses"}, CredentialVersionID: "immutable:first"},
		{Revision: 2, Enabled: true, Config: Config{Model: "new", BaseURL: "https://provider.example", APIMode: "responses"}, CredentialVersionID: "immutable:second"},
	}}
	credentials := newConfigurationLifecycleStub(repo)
	credentials.versions = map[string]string{"immutable:first": "old-secret", "immutable:second": "new-secret"}
	service := NewService(nil, Config{})
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	manager, err := NewConfigurationManager(repo, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	historical, err := manager.runtimeForRevision(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if historical.config.Revision != 1 || historical.config.APIKey != "old-secret" || historical.config.Model != "old" {
		t.Fatal("historical runtime substituted the current credential")
	}
	if service.runtimeSnapshot().config.APIKey != "new-secret" {
		t.Fatal("historical resolution changed active credentials")
	}
	if len(credentials.used) != 2 || credentials.used[0] != "immutable:second" || credentials.used[1] != "immutable:first" {
		t.Fatalf("resolved wrong immutable versions: %v", credentials.used)
	}
	if err = credentials.install(t.Context(), 1); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatalf("stale install callback accepted: %v", err)
	}
}

func TestConfigurationVersionMetadataDoesNotResolveProviderSecret(t *testing.T) {
	repo := &configurationMemory{rows: []ConfigurationRevision{{Revision: 1, CredentialVersionID: "immutable:first"}, {Revision: 2, CredentialVersionID: "immutable:second"}}}
	credentials := newConfigurationLifecycleStub(repo)
	service := NewService(nil, Config{})
	manager, err := NewConfigurationManager(repo, service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	version, err := manager.CredentialVersion(t.Context(), 1)
	if err != nil || version != "immutable:first" || len(credentials.used) != 0 {
		t.Fatalf("metadata resolved wrong version or consumed provider secret: %s %v", version, err)
	}
	if _, err = manager.CredentialVersion(t.Context(), 3); !errors.Is(err, ErrConfigurationNotFound) {
		t.Fatal("unknown configuration metadata was accepted")
	}
}
