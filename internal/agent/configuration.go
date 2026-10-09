package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform/typednil"
)

var (
	ErrConfigurationNotFound        = errors.New("agent configuration not found")
	ErrConfigurationConflict        = errors.New("agent configuration changed; reload settings and test again")
	ErrConfigurationReentryRequired = errors.New("agent credentials must be explicitly re-entered")
)

// ConfigurationRevision is an immutable administrator-owned revision. Config
// never contains a plaintext key at the persistence boundary.
type ConfigurationRevision struct {
	Revision   int64
	Enabled    bool
	Config     Config
	Credential []byte
	// CredentialVersionID pins an immutable version in the customer credential store.
	CredentialVersionID string
	ActorID             string
	CreatedAt           time.Time
}
type ConfigurationStore interface {
	CurrentConfiguration(context.Context) (ConfigurationRevision, error)
	ConfigurationByRevision(context.Context, int64) (ConfigurationRevision, error)
	SaveConfiguration(context.Context, int64, ConfigurationRevision) (ConfigurationRevision, error)
}

// ConfigurationInput deliberately uses a pointer-free credential operation:
// an empty APIKey keeps the saved key, RemoveKey explicitly removes it.
type ConfigurationInput struct {
	Enabled         bool   `json:"enabled"`
	Model           string `json:"model"`
	BaseURL         string `json:"baseUrl"`
	APIMode         string `json:"apiMode"`
	ReasoningEffort string `json:"reasoningEffort"`
	APIKey          string `json:"apiKey,omitempty"`
	RemoveKey       bool   `json:"removeKey,omitempty"`
}

// ConfigurationCredentials is the shared customer credential lifecycle port.
// Test persists a draft and isolated validation receipt; Activate uses the
// durable activation coordinator and returns only after the exact runtime is
// ready. BindRuntime is called once during process construction.
type ConfigurationCredentials interface {
	Test(context.Context, string, int64, ConfigurationInput) (string, error)
	Activate(context.Context, string, int64, ConfigurationInput, string) (ConfigurationRevision, error)
	UseConfiguration(context.Context, ConfigurationRevision, func(Config) error) error
	BindRuntime(func(context.Context, int64) error, func(context.Context) error) error
}

type ConfigurationManager struct {
	mu          sync.Mutex
	store       ConfigurationStore
	service     *Service
	credentials ConfigurationCredentials
}

func NewConfigurationManager(store ConfigurationStore, service *Service, credentials ConfigurationCredentials) (*ConfigurationManager, error) {
	if typednil.IsNil(store) || service == nil || typednil.IsNil(credentials) {
		return nil, fmt.Errorf("agent configuration requires storage, runtime, and customer credential setup")
	}
	m := &ConfigurationManager{store: store, service: service, credentials: credentials}
	if err := credentials.BindRuntime(m.installRevision, m.Refresh); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *ConfigurationManager) readConfiguration(ctx context.Context, r ConfigurationRevision) (Config, error) {
	if len(r.Credential) != 0 {
		return Config{}, fmt.Errorf("%w: legacy agent credential format is unsupported; retain the old deployment for recovery and configure customer credential storage before explicitly re-entering the provider key", ErrConfigurationReentryRequired)
	}
	if r.CredentialVersionID == "" {
		return Config{}, fmt.Errorf("%w: agent configuration has no customer credential version; complete credential setup and explicitly re-enter the provider settings", ErrConfigurationReentryRequired)
	}
	var result Config
	err := m.credentials.UseConfiguration(ctx, r, func(c Config) error { result = c; result.Revision = r.Revision; return nil })
	return result, err
}

func (m *ConfigurationManager) installRevision(ctx context.Context, revision int64) error {
	r, err := m.store.CurrentConfiguration(ctx)
	if err != nil {
		return err
	}
	if r.Revision != revision {
		return ErrConfigurationConflict
	}
	c, err := m.readConfiguration(ctx, r)
	if err != nil {
		return err
	}
	return m.service.ApplyRuntimeConfig(c, r.Enabled)
}

func (m *ConfigurationManager) resolve(ctx context.Context, expected int64, in ConfigurationInput) (Config, error) {
	current, err := m.store.CurrentConfiguration(ctx)
	if err != nil && !errors.Is(err, ErrConfigurationNotFound) {
		return Config{}, err
	}
	if current.Revision != expected {
		return Config{}, ErrConfigurationConflict
	}
	c := Config{Model: strings.TrimSpace(in.Model), BaseURL: strings.TrimSpace(in.BaseURL), APIMode: strings.TrimSpace(in.APIMode), ReasoningEffort: strings.ToLower(strings.TrimSpace(in.ReasoningEffort))}
	if in.RemoveKey && in.APIKey != "" {
		return Config{}, fmt.Errorf("choose either replace or remove credential")
	}
	if in.APIKey != "" {
		c.APIKey = strings.TrimSpace(in.APIKey)
	} else if !in.RemoveKey {
		if current.Revision > 0 {
			saved, e := m.readConfiguration(ctx, current)
			if e != nil {
				return Config{}, e
			}
			c.APIKey = saved.APIKey
		} else if in.Enabled {
			return Config{}, fmt.Errorf("enter a credential for the first customer-managed agent configuration")
		}
	}
	// A credential must never be implicitly forwarded to a different endpoint.
	if in.APIKey == "" && !in.RemoveKey && c.APIKey != "" {
		previous := current.Config
		if current.Revision == 0 {
			if live := m.service.runtimeSnapshot(); live != nil {
				previous = live.config
			}
		}
		if previous.NormalizedBaseURL() != c.NormalizedBaseURL() {
			return Config{}, fmt.Errorf("enter a credential when changing the provider endpoint")
		}
	}
	if in.Enabled && c.BaseURL == "" {
		return Config{}, fmt.Errorf("provider endpoint is required")
	}
	if in.Enabled && c.APIMode == "" {
		return Config{}, fmt.Errorf("select an API mode")
	}
	if err := c.Validate(in.Enabled); err != nil {
		return Config{}, err
	}
	if c.APIMode == "chat-completions" {
		if strings.HasPrefix(strings.ToLower(c.Model), "deepseek-v4") {
			if in.Enabled && c.ReasoningEffort != "none" {
				return Config{}, fmt.Errorf("DeepSeek V4 currently requires reasoning disabled (none)")
			}
		} else if c.ReasoningEffort != "" {
			return Config{}, fmt.Errorf("reasoning effort is not supported by this Chat Completions adapter")
		}
	}
	return c, nil
}
func (m *ConfigurationManager) Test(ctx context.Context, actor string, expected int64, in ConfigurationInput) (string, error) {
	if actor == "" {
		return "", fmt.Errorf("administrator identity is required")
	}
	c, err := m.resolve(ctx, expected, in)
	if err != nil {
		return "", err
	}
	return m.credentials.Test(ctx, actor, expected, configurationInput(c, in.Enabled))
}
func (m *ConfigurationManager) Save(ctx context.Context, actor string, expected int64, in ConfigurationInput, token string) (ConfigurationRevision, error) {
	if actor == "" {
		return ConfigurationRevision{}, fmt.Errorf("administrator identity is required")
	}
	c, err := m.resolve(ctx, expected, in)
	if errors.Is(err, ErrConfigurationConflict) {
		current, readErr := m.store.CurrentConfiguration(ctx)
		if readErr != nil || expected < 0 || current.Revision != expected+1 {
			return ConfigurationRevision{}, ErrConfigurationConflict
		}
		// The prior attempt may have committed before its acknowledgment was
		// lost. The lifecycle backend must match the same immutable operation
		// before recovering its exact committed runtime.
		in.Model = strings.TrimSpace(in.Model)
		in.BaseURL = strings.TrimSpace(in.BaseURL)
		in.APIMode = strings.TrimSpace(in.APIMode)
		in.ReasoningEffort = strings.ToLower(strings.TrimSpace(in.ReasoningEffort))
		in.APIKey = strings.TrimSpace(in.APIKey)
		return m.credentials.Activate(ctx, actor, expected, in, token)
	}
	if err != nil {
		return ConfigurationRevision{}, err
	}
	// Fail before durable mutation if this process cannot construct the model.
	if _, err := m.service.prepareRuntimeConfig(c, in.Enabled); err != nil {
		return ConfigurationRevision{}, err
	}
	return m.credentials.Activate(ctx, actor, expected, configurationInput(c, in.Enabled), token)
}
func configurationInput(c Config, enabled bool) ConfigurationInput {
	return ConfigurationInput{Enabled: enabled, Model: c.Model, BaseURL: c.BaseURL, APIMode: c.APIMode, ReasoningEffort: c.ReasoningEffort, APIKey: c.APIKey, RemoveKey: c.APIKey == ""}
}
func (m *ConfigurationManager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.store.CurrentConfiguration(ctx)
	if errors.Is(err, ErrConfigurationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if live := m.service.runtimeSnapshot(); live != nil && live.config.Revision == r.Revision {
		return nil
	}
	c, err := m.readConfiguration(ctx, r)
	if err != nil {
		return err
	}
	return m.service.ApplyRuntimeConfig(c, r.Enabled)
}

// CurrentMetadata exposes retained settings for administrator recovery without
// decrypting, installing, or returning historical credentials.
func (m *ConfigurationManager) CurrentMetadata(ctx context.Context) (ConfigurationRevision, error) {
	r, err := m.store.CurrentConfiguration(ctx)
	if err != nil {
		return ConfigurationRevision{}, err
	}
	r.Credential = nil
	r.Config.APIKey = ""
	return r, nil
}
func (m *ConfigurationManager) runtimeForRevision(ctx context.Context, revision int64) (*agentRuntime, error) {
	r, err := m.store.ConfigurationByRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	c, err := m.readConfiguration(ctx, r)
	if err != nil {
		return nil, err
	}
	return m.service.prepareRuntimeConfig(c, r.Enabled)
}

func (s *Service) SetConfigurationManager(m *ConfigurationManager) { s.configuration = m }
func (s *Service) ConfigurationManager() *ConfigurationManager     { return s.configuration }
func (s *Service) AdminManaged() bool {
	r := s.runtimeSnapshot()
	return r != nil && r.config.Revision > 0
}
func (s *Service) DeploymentConfig() Config {
	r := s.runtimeSnapshot()
	if r == nil {
		return Config{}
	}
	c := r.config
	c.APIKey = ""
	return c
}

// RestoreInput stays server-side: credentials from history never return to the browser.
func (m *ConfigurationManager) RestoreInput(ctx context.Context, revision int64) (ConfigurationInput, error) {
	r, err := m.store.ConfigurationByRevision(ctx, revision)
	if err != nil {
		return ConfigurationInput{}, err
	}
	c, err := m.readConfiguration(ctx, r)
	if err != nil {
		return ConfigurationInput{}, err
	}
	return ConfigurationInput{Enabled: r.Enabled, Model: c.Model, BaseURL: c.BaseURL, APIMode: c.APIMode, ReasoningEffort: c.ReasoningEffort, APIKey: c.APIKey, RemoveKey: c.APIKey == ""}, nil
}
func (s *Service) HasProviderCredential() bool {
	r := s.runtimeSnapshot()
	return r != nil && strings.TrimSpace(r.config.APIKey) != ""
}

// ReportDeploymentConfigError is fenced with takeover so a stale file watcher
// cannot mark an administrator-owned runtime degraded after an admin save.
func (s *Service) ReportDeploymentConfigError() {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if !s.AdminManaged() {
		s.ReportRuntimeConfigError()
	}
}

// Abort cancels the current precommit provider change through the same durable
// lifecycle; cancellation after commit is refused by its authority.
func (m *ConfigurationManager) Abort(ctx context.Context, actor string) error {
	lifecycle, ok := m.credentials.(interface {
		AbortConfiguration(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("agent credential activation recovery is unavailable")
	}
	return lifecycle.AbortConfiguration(ctx, actor)
}

// CredentialVersion identifies the exact immutable version of a retained
// configuration without decrypting or returning its provider key.
func (m *ConfigurationManager) CredentialVersion(ctx context.Context, revision int64) (string, error) {
	if revision == 0 {
		return "", nil
	}
	record, err := m.store.ConfigurationByRevision(ctx, revision)
	if err != nil {
		return "", err
	}
	return record.CredentialVersionID, nil
}
