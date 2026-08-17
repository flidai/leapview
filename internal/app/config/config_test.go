package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configspec "github.com/flidai/leapview/internal/app/config/spec"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

func TestParseListenAddrGrammar(t *testing.T) {
	tests := []struct {
		name, raw, want string
		bad             bool
	}{
		{"default", "", ":8080", false}, {"wildcard", ":8081", ":8081", false},
		{"hostname", "localhost:8080", "localhost:8080", false}, {"ipv4", "127.0.0.1:9", "127.0.0.1:9", false},
		{"ipv6", "[::1]:443", "[::1]:443", false}, {"scheme", "http://localhost:8080", "", true},
		{"path", "localhost:8080/x", "", true}, {"missing-port", "localhost", "", true},
		{"range", ":65536", "", true}, {"zero", ":0", "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseListenAddr(test.raw)
			if test.bad {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil || got.String() != test.want {
				t.Fatalf("got %q, err %v", got.String(), err)
			}
		})
	}
}

func TestLoadRejectsMalformedWorkloadConfiguration(t *testing.T) {
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "many")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted malformed workload concurrency")
	} else if !strings.Contains(err.Error(), "LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING") {
		t.Fatalf("Load() error does not name the environment variable: %v", err)
	}

	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "4")
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_EXECUTION_TIMEOUT", "eventually")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted malformed workload timeout")
	}
}

func TestLoadRejectsMalformedTypedValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "LEAPVIEW_PRODUCTION", value: "sometimes"},
		{name: "LEAPVIEW_WORKLOAD_REFRESH_MAX_QUEUED", value: "several"},
		{name: "LEAPVIEW_REFRESH_JOB_LEASE_TIMEOUT", value: "later"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.name, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %s=%q", test.name, test.value)
			}
		})
	}
}

func TestInfisicalRuntimeConfigurationIsAllOrNoneAndHTTPS(t *testing.T) {
	partial := map[string]any{
		"LEAPVIEW_INFISICAL_BASE_URL": "https://infisical.example.com",
	}
	if err := configspec.Validate(partial); err == nil {
		t.Fatal("partial Infisical configuration was accepted")
	}
	complete := map[string]any{
		"LEAPVIEW_INFISICAL_BASE_URL":                "https://infisical.example.com",
		"LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_ID":     "machine-client",
		"LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_SECRET": "bootstrap-secret",
		"LEAPVIEW_INFISICAL_ALLOWED_SCOPES":          `[{"projectId":"project-1","environment":"prod","secretPathPrefix":"/leapview"}]`,
	}
	if err := configspec.Validate(complete); err != nil {
		t.Fatalf("complete Infisical configuration rejected: %v", err)
	}
	complete["LEAPVIEW_INFISICAL_BASE_URL"] = "http://infisical.example.com"
	if err := configspec.Validate(complete); err == nil {
		t.Fatal("plain HTTP Infisical origin was accepted")
	}
}

func TestListenAddressUsesExplicitLeapViewSetting(t *testing.T) {
	t.Setenv("ADDR", "127.0.0.1:9002")
	t.Setenv("PORT", "9003")
	cfg, err := Load()
	require.NoError(t, err)
	if got := cfg.ListenAddr(); got != ":8080" {
		t.Fatalf("ListenAddr() with only legacy aliases = %q, want default", got)
	}

	t.Setenv("LEAPVIEW_ADDR", "127.0.0.1:9001")
	cfg, err = Load()
	require.NoError(t, err)
	if got := cfg.ListenAddr(); got != "127.0.0.1:9001" {
		t.Fatalf("ListenAddr() = %q", got)
	}
}

func TestManagedDataDefaultsUnderConfiguredHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_MANAGED_DATA_DIR", "")
	cfg, err := Load()
	require.NoError(t, err)
	if want := filepath.Join(home, "managed-data"); cfg.ManagedDataDir != want {
		t.Fatalf("ManagedDataDir = %q, want %q", cfg.ManagedDataDir, want)
	}
}

func TestGeneratedEnvironmentExampleValidates(t *testing.T) {
	for _, setting := range configspec.Settings() {
		if setting.Runtime {
			t.Setenv(setting.Name, "")
		}
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".env.example"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid environment example line %q", line)
		}
		t.Setenv(name, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load generated environment example: %v", err)
	}
	if err := cfg.Validate(ProfileServe); err != nil {
		t.Fatalf("validate generated environment example: %v", err)
	}
}

func TestLoadIncludesWorkloadConfiguration(t *testing.T) {
	t.Setenv("LEAPVIEW_WORKLOAD_MAX_RUNNING", "7")
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "7")
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_QUEUED", "9")
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_QUEUE_TIMEOUT", "11s")
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_EXECUTION_TIMEOUT", "13s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	workloads := cfg.WorkloadConfig()
	interactive := workloads.Classes[workload.Interactive]
	if workloads.MaxRunning != 7 || interactive.MaximumRunning != 7 || interactive.MaximumQueued != 9 {
		t.Fatalf("workload concurrency = %#v", workloads)
	}
	if interactive.QueueTimeout != 11*time.Second || interactive.ExecutionTimeout != 13*time.Second {
		t.Fatalf("workload timeouts = %#v", workloads)
	}
}

func TestDuckLakeCatalogPathDefaultsOutsidePlatformDB(t *testing.T) {
	cfg := Config{HomeDir: "/var/lib/leapview"}
	if got, want := cfg.DBPath(), "/var/lib/leapview/leapview.db"; got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	if got, want := cfg.DuckLakeCatalogPath(), "/var/lib/leapview/ducklake/catalog.duckdb"; got != want {
		t.Fatalf("DuckLakeCatalogPath = %q, want %q", got, want)
	}
	if cfg.DuckLakeCatalogPath() == cfg.DBPath() {
		t.Fatal("DuckLake catalog must not default to the platform database")
	}
}

func TestDuckLakeCatalogPathHonorsExplicitPath(t *testing.T) {
	cfg := Config{HomeDir: "/var/lib/leapview", DuckLakeCatalog: "/mnt/catalog.duckdb"}
	if got, want := cfg.DuckLakeCatalogPath(), "/mnt/catalog.duckdb"; got != want {
		t.Fatalf("DuckLakeCatalogPath = %q, want %q", got, want)
	}
}

func TestValidateProductionAuthRequiresCSRFKey(t *testing.T) {
	cfg := Config{Production: true, APITokenOnlyAuth: true}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected missing CSRF key to fail production auth validation")
	}
}

func TestValidateProductionAuthRequiresMetricsBearerToken(t *testing.T) {
	cfg := Config{
		Production:       true,
		APITokenOnlyAuth: true,
		CSRFKey:          "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected missing metrics bearer token to fail production auth validation")
	}
}

func TestValidateProductionAuthRejectsDevBypass(t *testing.T) {
	cfg := Config{
		Production:         true,
		DevAuthBypass:      true,
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected production dev auth bypass to fail validation")
	}
}

func TestValidateProductionAuthAllowsGenericOIDC(t *testing.T) {
	cfg := Config{
		Production:         true,
		PublicURL:          "https://app.example",
		OIDCIssuerURL:      "https://issuer.example",
		OIDCClientID:       "client-id",
		OIDCSecret:         "client-secret",
		OIDCCallbackURL:    "https://app.example/auth/oidc/callback",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("validate production auth: %v", err)
	}
}

func TestValidateProductionAuthRejectsInvalidOIDCProviderID(t *testing.T) {
	for _, providerID := range []string{"okta/prod", "../okta", "okta prod"} {
		cfg := Config{
			Production:         true,
			OIDCProviderID:     providerID,
			OIDCIssuerURL:      "https://issuer.example",
			OIDCClientID:       "client-id",
			OIDCSecret:         "client-secret",
			OIDCCallbackURL:    "https://app.example/auth/oidc/callback",
			CSRFKey:            "0123456789abcdef0123456789abcdef",
			MetricsBearerToken: "0123456789abcdef0123456789abcdef",
		}
		if err := cfg.ValidateProductionAuth(); err == nil {
			t.Fatalf("provider ID %q validated successfully, want error", providerID)
		}
	}
}

func TestValidateProductionAuthAllowsLocalAuth(t *testing.T) {
	cfg := Config{
		Production:         true,
		PublicURL:          "https://leapview.example.com",
		LocalAuth:          true,
		AllowedHosts:       "leapview.example.com",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("validate production auth: %v", err)
	}
}

func TestValidateProductionAuthRejectsInsecureBrowserAuthCookies(t *testing.T) {
	cfg := Config{
		Production:         true,
		OIDCIssuerURL:      "https://issuer.example",
		OIDCClientID:       "client-id",
		OIDCSecret:         "client-secret",
		OIDCCallbackURL:    "https://app.example/auth/oidc/callback",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
		CookieSecureRaw:    "false",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected insecure browser auth cookies to fail production validation")
	}

	cfg.APITokenOnlyAuth = true
	cfg.PublicURL = "https://app.example"
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("api-token-only production should allow explicitly insecure browser cookies: %v", err)
	}
}

func TestValidateProductionAuthRejectsPartialGenericOIDC(t *testing.T) {
	cfg := Config{
		Production:         true,
		APITokenOnlyAuth:   true,
		OIDCIssuerURL:      "https://issuer.example",
		OIDCClientID:       "client-id",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected partial OIDC config to fail production auth validation")
	}
}

func TestValidateProductionAuthRejectsInsecureOIDCURLs(t *testing.T) {
	cfg := Config{
		Production:         true,
		OIDCIssuerURL:      "http://issuer.example",
		OIDCClientID:       "client-id",
		OIDCSecret:         "client-secret",
		OIDCCallbackURL:    "https://app.example/auth/oidc/callback",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected insecure OIDC issuer URL to fail production auth validation")
	}

	cfg.OIDCIssuerURL = "https://issuer.example"
	cfg.OIDCCallbackURL = "http://app.example/auth/oidc/callback"
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected insecure OIDC callback URL to fail production auth validation")
	}
}

func TestValidateProductionAuthRejectsInsecureAzureCallbackURL(t *testing.T) {
	cfg := Config{
		Production:         true,
		AzureClientID:      "client-id",
		AzureSecret:        "client-secret",
		AzureCallbackURL:   "http://app.example/auth/azureadv2/callback",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected insecure Azure callback URL to fail production auth validation")
	}
}

func TestValidateProductionAuthRejectsPartialAzureConfig(t *testing.T) {
	cfg := Config{
		Production:         true,
		APITokenOnlyAuth:   true,
		AzureClientID:      "client-id",
		AzureCallbackURL:   "https://app.example/auth/azureadv2/callback",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected partial Azure config to fail production auth validation")
	}
}

func TestValidateProductionAuthRequiresStrongSCIMBearerWhenConfigured(t *testing.T) {
	cfg := Config{
		Production:         true,
		APITokenOnlyAuth:   true,
		AllowedHosts:       "leapview.example.com",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		SCIMBearerToken:    "short",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected weak SCIM bearer token to fail production auth validation")
	}

	cfg.SCIMBearerToken = "0123456789abcdef0123456789abcdef"
	cfg.PublicURL = "https://leapview.example.com"
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("strong SCIM bearer token rejected: %v", err)
	}
}

func TestValidateProductionAuthRequiresStrongMetricsBearerWhenConfigured(t *testing.T) {
	cfg := Config{
		Production:         true,
		APITokenOnlyAuth:   true,
		AllowedHosts:       "leapview.example.com",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "short",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected weak metrics bearer token to fail production auth validation")
	}

	cfg.MetricsBearerToken = "0123456789abcdef0123456789abcdef"
	cfg.PublicURL = "https://leapview.example.com"
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("strong metrics bearer token rejected: %v", err)
	}
}

func TestValidateProductionAuthRequiresAllowedHostForAPITokenOnly(t *testing.T) {
	cfg := Config{
		Production:         true,
		APITokenOnlyAuth:   true,
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected API-token-only production to require LEAPVIEW_ALLOWED_HOSTS")
	}

	cfg.PublicURL = "https://leapview.example.com"
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("production public URL rejected: %v", err)
	}
}

func TestValidateProductionAuthRejectsMissingOrInsecurePublicURL(t *testing.T) {
	cfg := Config{
		Production: true, APITokenOnlyAuth: true, AllowedHosts: "leapview.example.com",
		CSRFKey: "0123456789abcdef0123456789abcdef", MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected missing public URL to fail production validation")
	}
	cfg.PublicURL = "http://leapview.example.com"
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected insecure public URL to fail production validation")
	}
	for _, invalid := range []string{
		"https://user@leapview.example.com", "https://leapview.example.com/base",
		"https://leapview.example.com?tenant=one", "https://leapview.example.com/#fragment",
	} {
		cfg.PublicURL = invalid
		if err := cfg.ValidateProductionAuth(); err == nil {
			t.Fatalf("expected non-origin public URL %q to fail production validation", invalid)
		}
	}
}

func TestValidateProductionEvaluationModeAllowsOnlyLoopbackHTTP(t *testing.T) {
	cfg := Config{
		Production:         true,
		EvaluationMode:     true,
		Environment:        "evaluation",
		LocalAuth:          true,
		CookieSecureRaw:    "false",
		PublicURL:          "http://localhost:8080",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("loopback evaluation configuration rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"external HTTP origin": func(c *Config) { c.PublicURL = "http://evaluation.example.com" },
		"wrong environment":    func(c *Config) { c.Environment = "prod" },
		"missing local auth":   func(c *Config) { c.LocalAuth = false },
		"secure cookie":        func(c *Config) { c.CookieSecureRaw = "true" },
		"trusted proxy":        func(c *Config) { c.TrustProxyHeaders = true },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := cfg
			mutate(&invalid)
			if err := invalid.ValidateProductionAuth(); err == nil {
				t.Fatalf("invalid evaluation configuration accepted: %#v", invalid)
			}
		})
	}
}

func TestValidateProductionAuthRejectsInsecureExternalMCPOAuthIssuer(t *testing.T) {
	cfg := Config{
		Production: true, APITokenOnlyAuth: true, PublicURL: "https://leapview.example.com",
		MCPOAuthIssuerURL: "http://identity.example.com", CSRFKey: "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected insecure external MCP OAuth issuer to fail production validation")
	}
	cfg.MCPOAuthIssuerURL = "https://identity.example.com"
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("secure external MCP OAuth issuer rejected: %v", err)
	}
}

func TestProductionAllowedHostsDerivesBrowserAuthCallbackHosts(t *testing.T) {
	cfg := Config{
		Production:         true,
		PublicURL:          "https://public.example.com",
		OIDCIssuerURL:      "https://issuer.example",
		OIDCClientID:       "client-id",
		OIDCSecret:         "client-secret",
		OIDCCallbackURL:    "https://app.example.com/auth/oidc/callback",
		AzureClientID:      "azure-client-id",
		AzureSecret:        "azure-client-secret",
		AzureCallbackURL:   "https://tenant.example.com/auth/azureadv2/callback",
		AllowedHosts:       "ops.example.com, *.trusted.example.com",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	hosts, err := cfg.ProductionAllowedHosts()
	if err != nil {
		t.Fatalf("production allowed hosts: %v", err)
	}
	for _, want := range []string{"ops.example.com", "*.trusted.example.com", "public.example.com", "app.example.com", "tenant.example.com"} {
		if !containsString(hosts, want) {
			t.Fatalf("allowed hosts = %#v, missing %q", hosts, want)
		}
	}
	cfg = withAnalyticalTestDefaults(cfg)
	if err := cfg.ValidateProductionAuth(); err != nil {
		t.Fatalf("derived callback hosts should satisfy production validation: %v", err)
	}
}

func withAnalyticalTestDefaults(cfg Config) Config {
	cfg.DuckDBNodeMemoryMaxBytes = 256 << 20
	cfg.DuckDBNodeTempMaxBytes = 1 << 30
	cfg.DuckDBNodeMaxThreads = 2
	cfg.QueryResultMaxRows = 10_000
	cfg.QueryResultMaxBytes = 32 << 20
	cfg.QueryCacheRuntimeMaxEntries = 16
	cfg.QueryCacheRuntimeMaxBytes = 4 << 20
	cfg.QueryCacheNodeMaxEntries = 64
	cfg.QueryCacheNodeMaxBytes = 16 << 20
	return cfg
}

func TestProductionAllowedHostsRejectsInvalidEntries(t *testing.T) {
	cfg := Config{
		Production:         true,
		APITokenOnlyAuth:   true,
		AllowedHosts:       "https://app.example.com",
		CSRFKey:            "0123456789abcdef0123456789abcdef",
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}
	if _, err := cfg.ProductionAllowedHosts(); err == nil {
		t.Fatal("expected URL-shaped allowed host to fail validation")
	}
}

func TestOIDCScopesListSplitsWhitespaceAndCommas(t *testing.T) {
	cfg := Config{OIDCScopes: "openid profile,email\ngroups"}
	got := cfg.OIDCScopesList()
	want := []string{"openid", "profile", "email", "groups"}
	if len(got) != len(want) {
		t.Fatalf("scopes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scopes = %#v, want %#v", got, want)
		}
	}
}

func TestCookieSecureDefaultsToProduction(t *testing.T) {
	secure, err := (Config{Production: true}).CookieSecure()
	if err != nil {
		t.Fatalf("cookie secure: %v", err)
	}
	if !secure {
		t.Fatal("production cookie secure default = false, want true")
	}
}

func TestProductionMiddlewareDefaults(t *testing.T) {
	cfg := Config{Production: true}
	if !cfg.RequestLoggingEnabled() {
		t.Fatal("production request logging = false, want true")
	}
	if !cfg.RateLimitingEnabled() {
		t.Fatal("production rate limiting = false, want true")
	}
	if cfg.RateLimitingUsesRealIP() {
		t.Fatal("production rate limiting trusts proxy headers by default")
	}
	if !cfg.HSTSEnabled(true) {
		t.Fatal("production HSTS with secure cookies = false, want true")
	}
	if cfg.HSTSEnabled(false) {
		t.Fatal("production HSTS without secure cookies = true, want false")
	}
}

func TestDevelopmentMiddlewareDefaults(t *testing.T) {
	cfg := Config{}
	if cfg.RequestLoggingEnabled() {
		t.Fatal("development request logging = true, want false")
	}
	if cfg.RateLimitingEnabled() {
		t.Fatal("development rate limiting = true, want false")
	}
	if cfg.RateLimitingUsesRealIP() {
		t.Fatal("development rate limiting trusts proxy headers")
	}
	if cfg.HSTSEnabled(true) {
		t.Fatal("development HSTS = true, want false")
	}
}

func TestRateLimitingUsesRealIPRequiresProductionAndTrustProxyHeaders(t *testing.T) {
	if !(Config{Production: true, TrustProxyHeaders: true}).RateLimitingUsesRealIP() {
		t.Fatal("trusted production proxy headers should enable real IP rate limit keys")
	}
	if (Config{TrustProxyHeaders: true}).RateLimitingUsesRealIP() {
		t.Fatal("development proxy header trust should not enable real IP rate limit keys")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
