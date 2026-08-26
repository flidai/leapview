package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/flidai/leapview/internal/app/config/spec"
	"github.com/flidai/leapview/internal/workload"
)

type Profile string

const DefaultDeliveryRollbackRetentionWindow = 24 * time.Hour

const (
	ProfileServe Profile = "serve"
)

// ListenAddress is the validated HTTP listen endpoint used consistently by
// configuration, serving, and health checks.
type ListenAddress struct {
	Host string
	Port int
}

func (a ListenAddress) String() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
}

// ParseListenAddr accepts host:port, :port, and bracketed IPv6 host:port.
// An omitted value selects the default endpoint.
func ParseListenAddr(raw string) (ListenAddress, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = ":8080"
	}
	if strings.ContainsAny(raw, "/?#") || strings.Contains(raw, "://") {
		return ListenAddress{}, fmt.Errorf("invalid listen address %q: schemes and paths are not allowed", raw)
	}
	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		return ListenAddress{}, fmt.Errorf("invalid listen address %q (must be host:port): %w", raw, err)
	}
	if strings.TrimSpace(portText) == "" {
		return ListenAddress{}, fmt.Errorf("invalid listen address %q: port is required", raw)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return ListenAddress{}, fmt.Errorf("invalid listen address %q: port must be 1-65535", raw)
	}
	return ListenAddress{Host: host, Port: port}, nil
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, configurationError(err)
	}
	return finishLoad(cfg), nil
}

// LoadEnvironment parses an explicit, already-decoded environment without
// consulting process-global variables. Host lifecycle controllers use it to
// consume the exact environment installed for the managed container.
func LoadEnvironment(values map[string]string) (Config, error) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: values}); err != nil {
		return Config{}, configurationError(err)
	}
	return finishLoad(cfg), nil
}

func finishLoad(cfg Config) Config {
	cfg.workloadLoaded = true
	if strings.TrimSpace(cfg.ManagedDataDir) == "" {
		cfg.ManagedDataDir = filepath.Join(cfg.HomeDir, "managed-data")
	}
	return cfg
}

func MustLoad() Config {
	cfg, err := Load()
	if err != nil {
		panic(err)
	}
	return cfg
}

func (c Config) ListenAddr() string {
	if value := strings.TrimSpace(c.Addr); value != "" {
		return value
	}
	return ":8080"
}

func (c Config) DBPath() string {
	return filepath.Join(c.HomeDir, "leapview.db")
}

func (c Config) ArtifactDir() string {
	return filepath.Join(c.HomeDir, "artifacts")
}

func (c Config) RuntimeDir() string {
	return filepath.Join(c.HomeDir, "runtime")
}

func (c Config) DuckLakeDataDir() string {
	return filepath.Join(c.HomeDir, "data")
}

func (c Config) DeliveryRollbackRetention() time.Duration {
	if c.DeliveryRollbackRetentionWindow == 0 {
		return DefaultDeliveryRollbackRetentionWindow
	}
	return c.DeliveryRollbackRetentionWindow
}

func (c Config) DuckLakeCatalogPath() string {
	if c.DuckLakeCatalog != "" {
		return c.DuckLakeCatalog
	}
	return filepath.Join(c.HomeDir, "ducklake", "catalog.duckdb")
}

func (c Config) DuckDBDirPath() string {
	if c.DuckDBDir != "" {
		return c.DuckDBDir
	}
	return filepath.Join(c.HomeDir, "duckdb")
}

func (c Config) ClientConfigPath() string {
	if c.CLIConfig != "" {
		return c.CLIConfig
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(c.HomeDir, "cli.json")
	}
	return filepath.Join(dir, "leapview", "cli.json")
}

func (c Config) AzureConfigured() bool {
	return c.AzureClientID != "" && c.AzureSecret != "" && c.AzureCallbackURL != ""
}

func (c Config) AzurePartiallyConfigured() bool {
	return c.AzureClientID != "" || c.AzureSecret != "" || c.AzureCallbackURL != "" || c.AzureTenant != ""
}

func (c Config) OIDCConfigured() bool {
	return c.OIDCIssuerURL != "" && c.OIDCClientID != "" && c.OIDCSecret != "" && c.OIDCCallbackURL != ""
}

func (c Config) OIDCPartiallyConfigured() bool {
	return c.OIDCIssuerURL != "" || c.OIDCClientID != "" || c.OIDCSecret != "" || c.OIDCCallbackURL != "" || c.OIDCScopes != ""
}

func (c Config) OIDCScopesList() []string {
	fields := strings.FieldsFunc(c.OIDCScopes, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, field)
		}
	}
	return out
}

func (c Config) AllowedHostList() ([]string, error) {
	return parseAllowedHosts(c.AllowedHosts)
}

func (c Config) ProductionAllowedHosts() ([]string, error) {
	hosts, err := c.AllowedHostList()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(hosts)+3)
	add := func(host string) {
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	for _, host := range hosts {
		add(host)
	}
	for _, raw := range []string{c.PublicURL, c.OIDCCallbackURL, c.AzureCallbackURL} {
		host, err := callbackAllowedHost(raw)
		if err != nil {
			return nil, err
		}
		add(host)
	}
	return out, nil
}

func (c Config) CookieSecure() (bool, error) {
	value := strings.TrimSpace(c.CookieSecureRaw)
	if value == "" {
		return c.Production && !c.DevAuthBypass, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("LEAPVIEW_COOKIE_SECURE must be a boolean: %w", err)
	}
	return parsed, nil
}

func (c Config) Validate(profile Profile) error {
	if profile != ProfileServe {
		return fmt.Errorf("unsupported configuration profile %q", profile)
	}
	if _, err := ParseListenAddr(c.ListenAddr()); err != nil {
		return err
	}
	if _, err := c.AllowedHostList(); err != nil {
		return err
	}
	cookieSecure, err := c.CookieSecure()
	if err != nil {
		return err
	}
	values := c.catalogValues()
	values[configspec.EnvLEAPVIEW_COOKIE_SECURE] = cookieSecure
	if err := configspec.Validate(values); err != nil {
		return err
	}
	if err := c.WorkloadConfig().Validate(); err != nil {
		return fmt.Errorf("invalid workload configuration: %w", err)
	}
	if err := c.validateAnalyticalResources(); err != nil {
		return fmt.Errorf("invalid analytical resource configuration: %w", err)
	}
	if c.DeliveryRollbackRetentionWindow < 0 {
		return fmt.Errorf("LEAPVIEW_DELIVERY_ROLLBACK_RETENTION_WINDOW must not be negative")
	}
	return nil
}

func (c Config) validateAnalyticalResources() error {
	positive := map[string]int64{
		"LEAPVIEW_DUCKDB_NODE_MEMORY_MAX_BYTES":    c.DuckDBNodeMemoryMaxBytes,
		"LEAPVIEW_DUCKDB_NODE_TEMP_MAX_BYTES":      c.DuckDBNodeTempMaxBytes,
		"LEAPVIEW_DUCKDB_NODE_MAX_THREADS":         int64(c.DuckDBNodeMaxThreads),
		"LEAPVIEW_QUERY_RESULT_MAX_ROWS":           int64(c.QueryResultMaxRows),
		"LEAPVIEW_QUERY_RESULT_MAX_BYTES":          c.QueryResultMaxBytes,
		"LEAPVIEW_QUERY_CACHE_RUNTIME_MAX_ENTRIES": int64(c.QueryCacheRuntimeMaxEntries),
		"LEAPVIEW_QUERY_CACHE_RUNTIME_MAX_BYTES":   c.QueryCacheRuntimeMaxBytes,
		"LEAPVIEW_QUERY_CACHE_NODE_MAX_ENTRIES":    int64(c.QueryCacheNodeMaxEntries),
		"LEAPVIEW_QUERY_CACHE_NODE_MAX_BYTES":      c.QueryCacheNodeMaxBytes,
	}
	for name, value := range positive {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.QueryCacheRuntimeMaxEntries > c.QueryCacheNodeMaxEntries {
		return fmt.Errorf("query cache entry limits must satisfy runtime <= node")
	}
	if c.QueryCacheRuntimeMaxBytes > c.QueryCacheNodeMaxBytes {
		return fmt.Errorf("query cache byte limits must satisfy runtime <= node")
	}
	return nil
}

func (c Config) DuckDBTempDirPath() string {
	if value := strings.TrimSpace(c.DuckDBTempDir); value != "" {
		return value
	}
	return filepath.Join(c.HomeDir, "tmp", "duckdb")
}

func (c Config) ValidateProductionAuth() error {
	return c.Validate(ProfileServe)
}

func (c Config) WorkloadConfig() workload.Config {
	configured := workload.Config{
		MaxRunning:    c.WorkloadMaxRunning,
		MaximumQueued: c.WorkloadMaxQueued,
		Classes: map[workload.Class]workload.Policy{
			workload.Interactive: workloadPolicy(c.WorkloadInteractiveReservedRunning, c.WorkloadInteractiveMaxRunning, c.WorkloadInteractiveMaxQueued, c.WorkloadInteractiveQueueTimeout, c.WorkloadInteractiveExecutionTimeout),
			workload.Background:  workloadPolicy(c.WorkloadBackgroundReservedRunning, c.WorkloadBackgroundMaxRunning, c.WorkloadBackgroundMaxQueued, c.WorkloadBackgroundQueueTimeout, c.WorkloadBackgroundExecutionTimeout),
			workload.Refresh:     workloadPolicy(c.WorkloadRefreshReservedRunning, c.WorkloadRefreshMaxRunning, c.WorkloadRefreshMaxQueued, c.WorkloadRefreshQueueTimeout, c.WorkloadRefreshExecutionTimeout),
			workload.Control:     workloadPolicy(c.WorkloadControlReservedRunning, c.WorkloadControlMaxRunning, c.WorkloadControlMaxQueued, c.WorkloadControlQueueTimeout, c.WorkloadControlExecutionTimeout),
			workload.Maintenance: workloadPolicy(c.WorkloadMaintenanceReservedRunning, c.WorkloadMaintenanceMaxRunning, c.WorkloadMaintenanceMaxQueued, c.WorkloadMaintenanceQueueTimeout, c.WorkloadMaintenanceExecutionTimeout),
		},
	}
	if c.workloadLoaded {
		return mergeWorkloadDefaults(configured)
	}
	if configured.MaxRunning == 0 && configured.MaximumQueued == 0 {
		unset := true
		for _, policy := range configured.Classes {
			if policy != (workload.Policy{}) {
				unset = false
				break
			}
		}
		if unset {
			return workload.DefaultConfig()
		}
	}
	return configured
}

// mergeWorkloadDefaults applies the environment-visible workload overrides to
// LeapView's complete application policy. Memory and actor limits are not
// process-global environment settings, so they remain at their reviewed
// application defaults when Config was loaded from the environment.
func mergeWorkloadDefaults(configured workload.Config) workload.Config {
	defaults := workload.DefaultConfig()
	defaults.MaxRunning = configured.MaxRunning
	defaults.MaximumQueued = configured.MaximumQueued
	for class, policy := range configured.Classes {
		defaultPolicy := defaults.Classes[class]
		defaultPolicy.ReservedRunning = policy.ReservedRunning
		defaultPolicy.MaximumRunning = policy.MaximumRunning
		defaultPolicy.MaximumQueued = policy.MaximumQueued
		defaultPolicy.QueueTimeout = policy.QueueTimeout
		defaultPolicy.ExecutionTimeout = policy.ExecutionTimeout
		defaults.Classes[class] = defaultPolicy
	}
	return defaults
}

func workloadPolicy(reserved, running, queued int, queueTimeout, executionTimeout time.Duration) workload.Policy {
	return workload.Policy{ReservedRunning: reserved, MaximumRunning: running, MaximumQueued: queued, QueueTimeout: queueTimeout, ExecutionTimeout: executionTimeout}
}

func redactSecrets(err error) error {
	message := err.Error()
	for _, setting := range configspec.Settings() {
		if !setting.Secret {
			continue
		}
		if value := os.Getenv(setting.Name); len(value) >= 8 {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
	}
	return fmt.Errorf("%s", message)
}

func configurationError(err error) error {
	var parseErr env.ParseError
	if errors.As(err, &parseErr) {
		for _, setting := range configspec.Settings() {
			if setting.Runtime && setting.Field == parseErr.Name {
				err = fmt.Errorf("%s must be a valid %s: %w", setting.Name, setting.Type, parseErr.Err)
				break
			}
		}
	}
	return redactSecrets(err)
}

func parseAllowedHosts(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		host, err := normalizeAllowedHost(field)
		if err != nil {
			return nil, err
		}
		if host != "" {
			out = append(out, host)
		}
	}
	return out, nil
}

func callbackAllowedHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid callback URL host %q: %w", raw, err)
	}
	return normalizeAllowedHost(parsed.Host)
}

func normalizeAllowedHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", nil
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/\\") {
		return "", fmt.Errorf("LEAPVIEW_ALLOWED_HOSTS entries must be hostnames, not URLs: %q", raw)
	}
	if host == "*" {
		return "", fmt.Errorf("LEAPVIEW_ALLOWED_HOSTS must not allow every host in production")
	}
	if strings.HasPrefix(host, "[") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	} else if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	if strings.HasPrefix(host, "*.") {
		suffix := strings.TrimPrefix(host, "*.")
		if suffix == "" || strings.Contains(suffix, "*") {
			return "", fmt.Errorf("invalid LEAPVIEW_ALLOWED_HOSTS wildcard entry: %q", raw)
		}
		return "*." + suffix, nil
	}
	if strings.Contains(host, "*") || strings.ContainsAny(host, " \r\n\t") {
		return "", fmt.Errorf("invalid LEAPVIEW_ALLOWED_HOSTS entry: %q", raw)
	}
	return host, nil
}

func (c Config) RequestLoggingEnabled() bool {
	return c.Production
}

func (c Config) RateLimitingEnabled() bool {
	return c.Production
}

func (c Config) RateLimitingUsesRealIP() bool {
	return c.Production && c.TrustProxyHeaders
}

func (c Config) HSTSEnabled(cookieSecure bool) bool {
	return c.Production && cookieSecure
}
