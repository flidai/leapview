package agent

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrConfigurationNotFound = errors.New("agent configuration not found")
	ErrConfigurationConflict = errors.New("agent configuration changed; reload settings and test again")
)

// ConfigurationRevision is an immutable administrator-owned revision. Config
// never contains a plaintext key at the persistence boundary.
type ConfigurationRevision struct {
	Revision   int64
	Enabled    bool
	Config     Config
	Credential []byte
	ActorID    string
	CreatedAt  time.Time
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
type ConfigurationManager struct {
	mu      sync.Mutex
	store   ConfigurationStore
	service *Service
	aead    cipher.AEAD
	key     []byte
	probe   func(context.Context, Config) error
}

func NewConfigurationManager(store ConfigurationStore, service *Service, key string, probe func(context.Context, Config) error) (*ConfigurationManager, error) {
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("LEAPVIEW_AGENT_CREDENTIAL_KEY must be a 64-character hexadecimal encryption key")
	}
	block, err := aes.NewCipher(decoded)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if store == nil || service == nil || probe == nil {
		return nil, fmt.Errorf("agent configuration requires storage, runtime, and connection tester")
	}
	return &ConfigurationManager{store: store, service: service, aead: aead, key: decoded, probe: probe}, nil
}
func (m *ConfigurationManager) decrypt(r ConfigurationRevision) (Config, error) {
	c := r.Config
	c.Revision = r.Revision
	if len(r.Credential) == 0 {
		return c, nil
	}
	if len(r.Credential) < m.aead.NonceSize() {
		return Config{}, fmt.Errorf("agent credential cannot be decrypted")
	}
	nonce := r.Credential[:m.aead.NonceSize()]
	clear, err := m.aead.Open(nil, nonce, r.Credential[m.aead.NonceSize():], []byte("leapview.agent.credential.v1"))
	if err != nil {
		return Config{}, fmt.Errorf("agent credential cannot be decrypted; check the deployment encryption key")
	}
	c.APIKey = string(clear)
	return c, nil
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
			saved, e := m.decrypt(current)
			if e != nil {
				return Config{}, e
			}
			c.APIKey = saved.APIKey
		} else if live := m.service.runtimeSnapshot(); live != nil {
			c.APIKey = live.config.APIKey
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
func (m *ConfigurationManager) signature(actor string, expected int64, in ConfigurationInput, c Config, expires int64, nonce string) string {
	raw, _ := json.Marshal(struct {
		Actor    string
		Expected int64
		Input    ConfigurationInput
		Config   Config
		Nonce    string
		Expires  int64
	}{actor, expected, in, c, nonce, expires})
	mac := hmac.New(sha256.New, m.key)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (m *ConfigurationManager) Test(ctx context.Context, actor string, expected int64, in ConfigurationInput) (string, error) {
	if actor == "" {
		return "", fmt.Errorf("administrator identity is required")
	}
	c, err := m.resolve(ctx, expected, in)
	if err != nil {
		return "", err
	}
	if in.Enabled {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err = m.probe(ctx, c); err != nil {
			return "", fmt.Errorf("connection test failed; check provider, model, credentials, and supported API mode")
		}
	}
	expires := time.Now().Add(5 * time.Minute).Unix()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	return strconv.FormatInt(expires, 10) + "." + nonceText + "." + m.signature(actor, expected, in, c, expires, nonceText), nil
}
func (m *ConfigurationManager) Save(ctx context.Context, actor string, expected int64, in ConfigurationInput, token string) (ConfigurationRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.resolve(ctx, expected, in)
	if err != nil {
		return ConfigurationRevision{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ConfigurationRevision{}, fmt.Errorf("test this configuration before activating")
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expires || !hmac.Equal([]byte(parts[2]), []byte(m.signature(actor, expected, in, c, expires, parts[1]))) {
		return ConfigurationRevision{}, fmt.Errorf("connection test expired or settings changed; test again")
	}
	prepared, err := m.service.prepareRuntimeConfig(c, in.Enabled)
	if err != nil {
		return ConfigurationRevision{}, err
	}
	r := ConfigurationRevision{Enabled: in.Enabled, Config: c, ActorID: actor}
	if c.APIKey != "" {
		nonce := make([]byte, m.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return r, err
		}
		r.Credential = m.aead.Seal(nonce, nonce, []byte(c.APIKey), []byte("leapview.agent.credential.v1"))
	}
	r.Config.APIKey = ""
	r, err = m.store.SaveConfiguration(ctx, expected, r)
	if err != nil {
		return ConfigurationRevision{}, err
	}
	prepared.config.Revision = r.Revision
	m.service.installRuntimeConfig(prepared)
	return r, nil
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
	c, err := m.decrypt(r)
	if err != nil {
		return err
	}
	return m.service.ApplyRuntimeConfig(c, r.Enabled)
}
func (m *ConfigurationManager) runtimeForRevision(ctx context.Context, revision int64) (*agentRuntime, error) {
	r, err := m.store.ConfigurationByRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	c, err := m.decrypt(r)
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
	c, err := m.decrypt(r)
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
