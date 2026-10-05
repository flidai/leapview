package agent

import (
	"context"
	"errors"
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
func TestAdminConfigurationTestSaveAndRestart(t *testing.T) {
	ctx := context.Background()
	repo := &configurationMemory{}
	service := NewService(nil, Config{})
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	calls := 0
	probe := func(context.Context, Config) error { calls++; return nil }
	manager, err := NewConfigurationManager(repo, service, strings.Repeat("ab", 32), probe)
	if err != nil {
		t.Fatal(err)
	}
	candidate := ConfigurationInput{Enabled: true, Model: "my-model", BaseURL: "https://example.com/v1", APIMode: "responses", APIKey: "private-secret"}
	token, err := manager.Test(ctx, "admin", 0, candidate)
	if err != nil || calls != 1 {
		t.Fatalf("test: %v calls=%d", err, calls)
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
	if repo.rows[0].Config.APIKey != "" || strings.Contains(string(repo.rows[0].Credential), "private-secret") {
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
	manager, err = NewConfigurationManager(repo, restarted, strings.Repeat("ab", 32), probe)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Refresh(ctx); err != nil {
		t.Fatal(err)
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

func TestAdminConfigurationRejectsUnsafeTransitionsAndBadKeys(t *testing.T) {
	ctx := context.Background()
	repo := &configurationMemory{}
	service := NewService(nil, Config{APIKey: "legacy-secret", Model: "legacy", BaseURL: "https://old.example/v1"})
	service.ConfigureDefaultModel(func(Config) agentcore.Model { return newRecordingAgentModel() })
	manager, err := NewConfigurationManager(repo, service, strings.Repeat("cd", 32), func(context.Context, Config) error { return nil })
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
	wrong, _ := NewConfigurationManager(repo, service, strings.Repeat("ef", 32), func(context.Context, Config) error { return nil })
	if _, err = wrong.RestoreInput(ctx, 1); err == nil {
		t.Fatal("wrong encryption key decrypted saved credential")
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
	manager, err := NewConfigurationManager(repo, service, strings.Repeat("ab", 32), func(context.Context, Config) error { return nil })
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
	if len(repo.rows) != 0 || service.Enabled() {
		t.Fatal("failed preparation changed durable or active configuration")
	}
}
