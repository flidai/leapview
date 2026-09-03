// Package configspec defines LeapView's application-wide environment contract.
// It intentionally contains data only so runtime configuration and generators
// consume the same source of truth.
package configspec

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

const SecretPlaceholder = "<secret>"

type ValueType string

const (
	TypeString   ValueType = "string"
	TypeBool     ValueType = "boolean"
	TypeInt      ValueType = "integer"
	TypeInt64    ValueType = "integer64"
	TypeDuration ValueType = "duration"
)

type Setting struct {
	Name        string
	Field       string
	Type        ValueType
	DecodeType  ValueType
	Default     string
	Category    string
	Scope       string
	Description string
	Example     string
	Secret      bool
	Runtime     bool
	Lifecycle   string
	AliasFor    string
	EnvExample  string
	Commented   bool
}

// Settings returns a stable, name-sorted copy of the global catalog.
func Settings() []Setting {
	settings := append([]Setting(nil), settings...)
	sort.Slice(settings, func(i, j int) bool { return settings[i].Name < settings[j].Name })
	return settings
}

// DynamicEnvironmentPrefixes catalogs deliberately namespaced variable
// families whose suffix is a target-owned logical identifier rather than a
// fixed application setting.
func DynamicEnvironmentPrefixes() []string {
	return []string{"LEAPVIEW_DEV_CONNECTION_"}
}

var settings = []Setting{
	{Name: "LEAPVIEW_ADDR", Field: "Addr", Type: TypeString, Category: "server", Scope: "serve,healthcheck", Description: "HTTP listen address.", Example: ":8080", Runtime: true, Lifecycle: "supported"},
	{Name: "LEAPVIEW_AGENT_API_KEY", Field: "AgentAPIKey", Type: TypeString, Category: "agent", Scope: "serve", Description: "API key for the configured agent model provider.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_AGENT_BASE_URL", Field: "AgentBaseURL", Type: TypeString, Default: "https://api.openai.com/v1", Category: "agent", Scope: "serve", Description: "OpenAI-compatible agent API base URL.", Example: "https://api.openai.com/v1", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_AGENT_MODEL", Field: "AgentModel", Type: TypeString, Category: "agent", Scope: "serve", Description: "Agent model identifier.", Example: "gpt-5", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_ALLOWED_HOSTS", Field: "AllowedHosts", Type: TypeString, Category: "security", Scope: "serve", Description: "Comma- or whitespace-separated exact hosts and wildcard suffixes accepted in production.", Example: "leapview.example.com", Runtime: true, Lifecycle: "supported", EnvExample: "leapview.example.com"},
	{Name: "LEAPVIEW_API_TOKEN", Field: "APIToken", Type: TypeString, Category: "client", Scope: "client commands", Description: "Compatibility API token for an ephemeral CLI invocation; prefer device login for people and workload identity for CI.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_API_TOKEN_ONLY_AUTH", Field: "APITokenOnlyAuth", Type: TypeBool, Category: "authentication", Scope: "serve", Description: "Disable browser authentication and accept API tokens only.", Example: "true", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_ASSET_VERSION", Field: "AssetVersion", Type: TypeString, Category: "assets", Scope: "serve", Description: "Optional browser asset cache-busting version override.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_AZURE_CALLBACK_URL", Field: "AzureCallbackURL", Type: TypeString, Category: "authentication", Scope: "serve", Description: "HTTPS callback URL registered with Azure AD or Entra ID.", Example: "https://leapview.example.com/auth/azureadv2/callback", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_AZURE_CLIENT_ID", Field: "AzureClientID", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Azure AD or Entra ID OAuth client identifier.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_AZURE_CLIENT_SECRET", Field: "AzureSecret", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Azure AD or Entra ID OAuth client secret.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_AZURE_TENANT", Field: "AzureTenant", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Optional Azure AD or Entra ID tenant identifier.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_BASE_URL", Type: TypeString, Category: "development", Scope: "ui-qa", Description: "Base URL used by browser QA tooling.", Default: "http://localhost:8195", Lifecycle: "development"},
	{Name: "LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL", Field: "BootstrapEmail", Type: TypeString, Category: "administration", Scope: "instance initialization", Description: "Email assigned to the initial production administrator.", Example: "admin@example.com", Runtime: true, Lifecycle: "supported", EnvExample: "admin@example.com"},
	{Name: "LEAPVIEW_BOOTSTRAP_CACHE_DIR", Type: TypeString, Category: "bootstrap", Scope: "bootstrap tools", Description: "Download cache directory used by dataset bootstrap tools.", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_BOOTSTRAP_FORCE", Type: TypeBool, Category: "bootstrap", Scope: "bootstrap tools", Description: "Force dataset bootstrap tools to refresh existing files.", Default: "false", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_BRIDGE_BENCH_ITERATIONS", Type: TypeInt, Default: "120", Category: "development", Scope: "browser benchmark", Description: "Measured Datastar bridge benchmark iterations.", Lifecycle: "development"},
	{Name: "LEAPVIEW_BRIDGE_BENCH_WARMUP", Type: TypeInt, Default: "20", Category: "development", Scope: "browser benchmark", Description: "Warm-up Datastar bridge benchmark iterations.", Lifecycle: "development"},
	{Name: "LEAPVIEW_CONFORMANCE_EVIDENCE_OUT", Type: TypeString, Category: "ci", Scope: "MinIO conformance gate", Description: "Path where the required object-backed physical-pool conformance evidence artifact is written.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_CLI_CONFIG", Field: "CLIConfig", Type: TypeString, Category: "client", Scope: "client commands", Description: "Path to the non-secret CLI target profile document.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_COOKIE_SECURE", Field: "CookieSecureRaw", Type: TypeBool, DecodeType: TypeString, Category: "security", Scope: "serve", Description: "Secure-cookie override; defaults to true for production browser authentication.", Example: "true", Runtime: true, Lifecycle: "supported", EnvExample: "true"},
	{Name: "LEAPVIEW_CSRF_KEY", Field: "CSRFKey", Type: TypeString, Category: "security", Scope: "serve", Description: "Key used for CSRF protection and OAuth state cookies; production requires at least 32 characters.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", EnvExample: "replace-with-at-least-32-characters"},
	{Name: "LEAPVIEW_DEV_AUTH_BYPASS", Field: "DevAuthBypass", Type: TypeBool, Category: "authentication", Scope: "serve", Description: "Bypass authentication in development; forbidden in production.", Default: "false", Runtime: true, Lifecycle: "development", Commented: true},
	{Name: "LEAPVIEW_DEV_API_TOKEN", Field: "DevAPIToken", Type: TypeString, Default: "dev", Category: "authentication", Scope: "serve", Description: "Static bearer credential accepted by the public API in development; replace the development default on shared machines.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "development", Commented: true},
	{Name: "LEAPVIEW_DEV_ASSET_CACHE_DIR", Type: TypeString, Category: "development", Scope: "development asset tools", Description: "User-level cache shared by worktrees for immutable development datasets and map assets.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_LOG_LINES", Type: TypeInt, Default: "120", Category: "development", Scope: "dev server", Description: "Number of log lines shown by the managed development server.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_PORT_COUNT", Type: TypeInt, Default: "100", Category: "development", Scope: "dev server", Description: "Number of ports scanned by the managed development server.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_PORT_START", Type: TypeInt, Default: "8100", Category: "development", Scope: "dev server", Description: "First port scanned by the managed development server.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_PROJECT", Type: TypeString, Default: "dashboards/leapview.yaml", Category: "development", Scope: "dev server", Description: "Project published by the managed development server.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_READY_ATTEMPTS", Type: TypeInt, Default: "150", Category: "development", Scope: "dev server", Description: "Readiness attempts made by the managed development server.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_READY_INTERVAL", Type: TypeDuration, Default: "200ms", Category: "development", Scope: "dev server", Description: "Delay between managed development server readiness attempts.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_MCP_ATTEMPTS", Type: TypeInt, Default: "20", Category: "development", Scope: "dev server", Description: "Attempts made by the development MCP smoke check while the active project converges.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_MCP_INTERVAL", Type: TypeDuration, Default: "500ms", Category: "development", Scope: "dev server", Description: "Delay between development MCP smoke-check attempts.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_RESTART", Type: TypeBool, Default: "false", Category: "development", Scope: "dev server", Description: "Force the managed development server to restart.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_SKIP_PUBLISH", Type: TypeBool, Default: "false", Category: "development", Scope: "dev server", Description: "Skip automatic project publishing in the managed development server.", Lifecycle: "development"},
	{Name: "LEAPVIEW_DEV_WORKTREE", Type: TypeString, Category: "development", Scope: "dev server", Description: "Worktree path exported by the managed development server.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_DUCKDB_DIR", Field: "DuckDBDir", Type: TypeString, Category: "storage", Scope: "serve", Description: "Directory containing node-local DuckDB runtime state.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH", Field: "DuckDBExtensionSupplyPath", Type: TypeString, Category: "storage", Scope: "serve", Description: "Required absolute path to the target-reviewed packaged DuckDB extension supply document.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_SHA256", Field: "DuckDBExtensionSupplySHA256", Type: TypeString, Category: "storage", Scope: "serve", Description: "Required lowercase SHA-256 digest anchoring the complete extension supply document.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_EXTENSION_CACHE_DIR", Field: "DuckDBExtensionCacheDir", Type: TypeString, Category: "storage", Scope: "serve", Description: "Private content-addressed cache for admitted DuckDB extensions.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKLAKE_CATALOG_PATH", Field: "DuckLakeCatalog", Type: TypeString, Category: "storage", Scope: "serve,admin", Description: "Path to the node's DuckDB-backed DuckLake catalog.", Example: "/var/lib/leapview/ducklake/catalog.duckdb", Runtime: true, Lifecycle: "supported", EnvExample: "/var/lib/leapview/ducklake/catalog.duckdb"},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_URL", Field: "PostgresControlURL", Type: TypeString, Category: "postgres", Scope: "serve", Description: "Explicit PostgreSQL control-plane connection URL (including credentials and TLS parameters).", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL", Field: "PostgresControlMigratorURL", Type: TypeString, Category: "postgres", Scope: "serve", Description: "Explicit PostgreSQL control-plane migration connection URL; used only before readiness.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE", Field: "PostgresControlMigratorRole", Type: TypeString, Default: "leapview_control_migrator", Category: "postgres", Scope: "serve", Description: "Configured PostgreSQL role expected by the control-plane migration pool.", Example: "leapview_control_migrator", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL", Field: "PostgresControlUpgradeCoordinatorURL", Type: TypeString, Category: "postgres", Scope: "upgrade operation", Description: "Explicit PostgreSQL control-plane upgrade coordinator URL; this role has no owner membership and only guarded DuckLake authority functions.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_ROLE", Field: "PostgresControlUpgradeCoordinatorRole", Type: TypeString, Default: "leapview_control_upgrade_coordinator", Category: "postgres", Scope: "upgrade operation", Description: "Configured PostgreSQL role expected by the DuckLake upgrade coordinator connection.", Example: "leapview_control_upgrade_coordinator", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_READONLY_URL", Field: "PostgresControlReadonlyURL", Type: TypeString, Category: "postgres", Scope: "serve", Description: "Optional PostgreSQL control-plane readonly connection URL for bounded reporting and backup reads.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE", Field: "PostgresControlReadonlyRole", Type: TypeString, Default: "leapview_control_readonly", Category: "postgres", Scope: "serve", Description: "Configured PostgreSQL role expected by the optional control-plane readonly pool.", Example: "leapview_control_readonly", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL", Field: "PostgresControlMaintenanceURL", Type: TypeString, Category: "postgres", Scope: "maintenance operation", Description: "Explicit PostgreSQL control-plane maintenance URL using the separately authenticated bounded maintenance role.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE", Field: "PostgresControlMaintenanceRole", Type: TypeString, Default: "leapview_control_maintenance", Category: "postgres", Scope: "maintenance operation", Description: "Configured PostgreSQL role expected by bounded control-plane maintenance operations.", Example: "leapview_control_maintenance", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_URL", Field: "PostgresDuckLakeURL", Type: TypeString, Category: "postgres", Scope: "serve", Description: "Explicit PostgreSQL DuckLake catalog connection URL; independent from the control-plane URL.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL", Field: "PostgresDuckLakeMigratorURL", Type: TypeString, Category: "postgres", Scope: "upgrade operation", Description: "Explicit PostgreSQL DuckLake catalog owner-capable migration URL; never used by ordinary runtime attachments.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE", Field: "PostgresDuckLakeMigratorRole", Type: TypeString, Default: "leapview_ducklake_migrator", Category: "postgres", Scope: "upgrade operation", Description: "Configured PostgreSQL role expected by the DuckLake catalog migration connection.", Example: "leapview_ducklake_migrator", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL", Field: "PostgresDuckLakeMaintenanceURL", Type: TypeString, Category: "postgres", Scope: "maintenance operation", Description: "Explicit PostgreSQL DuckLake catalog maintenance URL using the separately authenticated bounded cleanup role.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE", Field: "PostgresDuckLakeMaintenanceRole", Type: TypeString, Default: "leapview_ducklake_maintenance", Category: "postgres", Scope: "maintenance operation", Description: "Configured PostgreSQL role expected by bounded DuckLake catalog expiry and cleanup operations.", Example: "leapview_ducklake_maintenance", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_EXPECTED_MAJOR", Field: "PostgresExpectedMajor", Type: TypeInt, Default: "18", Category: "postgres", Scope: "serve", Description: "Required PostgreSQL server major version for runtime readiness.", Example: "18", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_POOL_MIN_CONNS", Field: "PostgresControlPoolMinConns", Type: TypeInt, Default: "1", Category: "postgres", Scope: "serve", Description: "Minimum connections reserved for the control-plane PostgreSQL pool.", Example: "1", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_POOL_MAX_CONNS", Field: "PostgresControlPoolMaxConns", Type: TypeInt, Default: "8", Category: "postgres", Scope: "serve", Description: "Maximum connections allowed in the control-plane PostgreSQL pool.", Example: "8", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE", Field: "PostgresControlRuntimeRole", Type: TypeString, Default: "leapview_control_runtime", Category: "postgres", Scope: "serve", Description: "Configured PostgreSQL runtime role expected by the control-plane pool.", Example: "leapview_control_runtime", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_INTENT", Field: "PostgresControlIntent", Type: TypeString, Default: "read-write", Category: "postgres", Scope: "serve", Description: "Read/write intent required for the control-plane PostgreSQL pool.", Example: "read-write", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_POOL_MIN_CONNS", Field: "PostgresDuckLakePoolMinConns", Type: TypeInt, Default: "1", Category: "postgres", Scope: "serve", Description: "Minimum connections reserved for the DuckLake catalog PostgreSQL pool.", Example: "1", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_POOL_MAX_CONNS", Field: "PostgresDuckLakePoolMaxConns", Type: TypeInt, Default: "4", Category: "postgres", Scope: "serve", Description: "Maximum connections allowed in the DuckLake catalog PostgreSQL pool.", Example: "4", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE", Field: "PostgresDuckLakeRuntimeRole", Type: TypeString, Default: "leapview_ducklake_runtime", Category: "postgres", Scope: "serve", Description: "Configured PostgreSQL runtime role expected by the DuckLake catalog pool.", Example: "leapview_ducklake_runtime", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_INTENT", Field: "PostgresDuckLakeIntent", Type: TypeString, Default: "read-write", Category: "postgres", Scope: "serve", Description: "Read/write intent required for the DuckLake catalog PostgreSQL pool.", Example: "read-write", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_ACQUIRE_TIMEOUT", Field: "PostgresControlAcquireTimeout", Type: TypeDuration, Default: "5s", Category: "postgres", Scope: "serve", Description: "Maximum time to wait when acquiring a control-plane PostgreSQL connection.", Example: "5s", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_STATEMENT_TIMEOUT", Field: "PostgresControlStatementTimeout", Type: TypeDuration, Default: "30s", Category: "postgres", Scope: "serve", Description: "Control-plane PostgreSQL session statement timeout.", Example: "30s", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_LOCK_TIMEOUT", Field: "PostgresControlLockTimeout", Type: TypeDuration, Default: "5s", Category: "postgres", Scope: "serve", Description: "Control-plane PostgreSQL session lock-wait timeout.", Example: "5s", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_IDLE_TX_TIMEOUT", Field: "PostgresControlIdleTransactionTimeout", Type: TypeDuration, Default: "1m", Category: "postgres", Scope: "serve", Description: "Control-plane PostgreSQL idle-in-transaction timeout.", Example: "1m", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_ACQUIRE_TIMEOUT", Field: "PostgresDuckLakeAcquireTimeout", Type: TypeDuration, Default: "5s", Category: "postgres", Scope: "serve", Description: "Maximum time to wait when acquiring a DuckLake catalog PostgreSQL connection.", Example: "5s", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_STATEMENT_TIMEOUT", Field: "PostgresDuckLakeStatementTimeout", Type: TypeDuration, Default: "30s", Category: "postgres", Scope: "serve", Description: "DuckLake catalog PostgreSQL session statement timeout.", Example: "30s", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_LOCK_TIMEOUT", Field: "PostgresDuckLakeLockTimeout", Type: TypeDuration, Default: "5s", Category: "postgres", Scope: "serve", Description: "DuckLake catalog PostgreSQL session lock-wait timeout.", Example: "5s", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_IDLE_TX_TIMEOUT", Field: "PostgresDuckLakeIdleTransactionTimeout", Type: TypeDuration, Default: "1m", Category: "postgres", Scope: "serve", Description: "DuckLake catalog PostgreSQL idle-in-transaction timeout.", Example: "1m", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKLAKE_RETENTION_INTERVAL", Field: "DuckLakeRetentionInterval", Type: TypeDuration, Default: "1h", Category: "ducklake retention", Scope: "serve", Description: "Interval between bounded DuckLake catalog retention passes.", Example: "1h", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKLAKE_RETENTION_FILE_GRACE_PERIOD", Field: "DuckLakeRetentionFileGracePeriod", Type: TypeDuration, Default: "24h", Category: "ducklake retention", Scope: "serve", Description: "Minimum age of DuckLake files before bounded retention cleanup.", Example: "24h", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_POSTGRES_REQUIRE_TLS", Field: "PostgresRequireTLS", Type: TypeBool, Default: "true", Category: "postgres", Scope: "serve", Description: "Require PostgreSQL URLs to use an encrypted sslmode.", Example: "true", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID", Field: "DeliveryPhysicalPoolID", Type: TypeString, Category: "delivery", Scope: "serve,admin", Description: "Exact admitted physical-pool identity used by plan-driven candidate builds; startup never synthesizes admission.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST", Field: "DeliveryPhysicalPoolCompatibilityDigest", Type: TypeString, Category: "delivery", Scope: "serve,admin", Description: "Exact immutable compatibility tuple digest required for the configured delivery pool admission.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DELIVERY_STAGING_DIR", Field: "DeliveryStagingDir", Type: TypeString, Category: "delivery", Scope: "serve,admin", Description: "Private staging directory for disposable plan-driven candidate catalogs and remote verification.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DELIVERY_ROLLBACK_RETENTION_WINDOW", Field: "DeliveryRollbackRetentionWindow", Type: TypeDuration, Default: "24h", Category: "delivery", Scope: "serve,admin", Description: "Target-owned duration for which immutable sealed generations remain eligible for rollback.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_REFRESH_JOB_LEASE_TIMEOUT", Field: "RefreshJobLeaseTimeout", Type: TypeDuration, Default: "2m", Category: "refresh", Scope: "serve", Description: "Lease duration before an abandoned refresh job may be reclaimed.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAX_QUEUED", Field: "WorkloadMaxQueued", Type: TypeInt, Default: "112", Category: "workload", Scope: "serve", Description: "Maximum queued operations across all workload classes.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAX_RUNNING", Field: "WorkloadMaxRunning", Type: TypeInt, Default: "5", Category: "workload", Scope: "serve", Description: "Maximum operations running concurrently on this node.", Runtime: true, Lifecycle: "supported", EnvExample: "5"},
	{Name: "LEAPVIEW_WORKLOAD_INTERACTIVE_RESERVED_RUNNING", Field: "WorkloadInteractiveReservedRunning", Type: TypeInt, Default: "3", Category: "workload", Scope: "serve", Description: "Capacity reserved for interactive work when interactive demand is queued.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CLIENT_ID", Field: "WorkloadClientID", Type: TypeString, Category: "client", Scope: "client commands", Description: "Service-principal identifier exchanged for an ephemeral, scoped CI credential.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CLIENT_SECRET", Field: "WorkloadClientSecret", Type: TypeString, Category: "client", Scope: "client commands", Description: "Service-principal secret injected by the CI secret manager for workload identity exchange.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_PROJECT", Field: "WorkloadProject", Type: TypeString, Category: "client", Scope: "client commands", Description: "Exact project scope requested by CI workload identity.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", Field: "WorkloadInteractiveMaxRunning", Type: TypeInt, Default: "4", Category: "workload", Scope: "serve", Description: "Maximum concurrently running interactive operations.", Runtime: true, Lifecycle: "supported", EnvExample: "4"},
	{Name: "LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_QUEUED", Field: "WorkloadInteractiveMaxQueued", Type: TypeInt, Default: "64", Category: "workload", Scope: "serve", Description: "Maximum queued interactive operations.", Runtime: true, Lifecycle: "supported", EnvExample: "64"},
	{Name: "LEAPVIEW_WORKLOAD_INTERACTIVE_QUEUE_TIMEOUT", Field: "WorkloadInteractiveQueueTimeout", Type: TypeDuration, Default: "30s", Category: "workload", Scope: "serve", Description: "Maximum time interactive work may wait for admission.", Runtime: true, Lifecycle: "supported", EnvExample: "30s"},
	{Name: "LEAPVIEW_WORKLOAD_INTERACTIVE_EXECUTION_TIMEOUT", Field: "WorkloadInteractiveExecutionTimeout", Type: TypeDuration, Default: "2m", Category: "workload", Scope: "serve", Description: "Maximum execution time for interactive work.", Runtime: true, Lifecycle: "supported", EnvExample: "2m"},
	{Name: "LEAPVIEW_WORKLOAD_BACKGROUND_RESERVED_RUNNING", Field: "WorkloadBackgroundReservedRunning", Type: TypeInt, Default: "0", Category: "workload", Scope: "serve", Description: "Capacity reserved for background work when background demand is queued.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_BACKGROUND_MAX_RUNNING", Field: "WorkloadBackgroundMaxRunning", Type: TypeInt, Default: "1", Category: "workload", Scope: "serve", Description: "Maximum concurrently running background operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_BACKGROUND_MAX_QUEUED", Field: "WorkloadBackgroundMaxQueued", Type: TypeInt, Default: "16", Category: "workload", Scope: "serve", Description: "Maximum queued background operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_BACKGROUND_QUEUE_TIMEOUT", Field: "WorkloadBackgroundQueueTimeout", Type: TypeDuration, Default: "2m", Category: "workload", Scope: "serve", Description: "Maximum time background work may wait for admission.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_BACKGROUND_EXECUTION_TIMEOUT", Field: "WorkloadBackgroundExecutionTimeout", Type: TypeDuration, Default: "15m", Category: "workload", Scope: "serve", Description: "Maximum execution time for background work.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_REFRESH_RESERVED_RUNNING", Field: "WorkloadRefreshReservedRunning", Type: TypeInt, Default: "1", Category: "workload", Scope: "serve", Description: "Capacity reserved for refresh work when refresh demand is queued.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_REFRESH_MAX_RUNNING", Field: "WorkloadRefreshMaxRunning", Type: TypeInt, Default: "1", Category: "workload", Scope: "serve", Description: "Maximum concurrently running refresh operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_REFRESH_MAX_QUEUED", Field: "WorkloadRefreshMaxQueued", Type: TypeInt, Default: "16", Category: "workload", Scope: "serve", Description: "Maximum queued refresh operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_REFRESH_QUEUE_TIMEOUT", Field: "WorkloadRefreshQueueTimeout", Type: TypeDuration, Default: "2m", Category: "workload", Scope: "serve", Description: "Maximum time refresh work may wait for admission.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_REFRESH_EXECUTION_TIMEOUT", Field: "WorkloadRefreshExecutionTimeout", Type: TypeDuration, Default: "0s", Category: "workload", Scope: "serve", Description: "Maximum execution time for refresh work; zero leaves the deadline to the workflow.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CONTROL_RESERVED_RUNNING", Field: "WorkloadControlReservedRunning", Type: TypeInt, Default: "1", Category: "workload", Scope: "serve", Description: "Capacity reserved for node-scoped control work when control demand is queued.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CONTROL_MAX_RUNNING", Field: "WorkloadControlMaxRunning", Type: TypeInt, Default: "1", Category: "workload", Scope: "serve", Description: "Maximum concurrently running control operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CONTROL_MAX_QUEUED", Field: "WorkloadControlMaxQueued", Type: TypeInt, Default: "16", Category: "workload", Scope: "serve", Description: "Maximum queued control operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CONTROL_QUEUE_TIMEOUT", Field: "WorkloadControlQueueTimeout", Type: TypeDuration, Default: "2m", Category: "workload", Scope: "serve", Description: "Maximum time control work may wait for admission.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_CONTROL_EXECUTION_TIMEOUT", Field: "WorkloadControlExecutionTimeout", Type: TypeDuration, Default: "15m", Category: "workload", Scope: "serve", Description: "Maximum execution time for control work.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAINTENANCE_RESERVED_RUNNING", Field: "WorkloadMaintenanceReservedRunning", Type: TypeInt, Default: "0", Category: "workload", Scope: "serve", Description: "Capacity reserved for maintenance work when maintenance demand is queued.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAINTENANCE_MAX_RUNNING", Field: "WorkloadMaintenanceMaxRunning", Type: TypeInt, Default: "1", Category: "workload", Scope: "serve", Description: "Maximum concurrently running maintenance operations.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAINTENANCE_MAX_QUEUED", Field: "WorkloadMaintenanceMaxQueued", Type: TypeInt, Default: "0", Category: "workload", Scope: "serve", Description: "Maximum queued maintenance operations; zero makes maintenance skip when saturated.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAINTENANCE_QUEUE_TIMEOUT", Field: "WorkloadMaintenanceQueueTimeout", Type: TypeDuration, Default: "0s", Category: "workload", Scope: "serve", Description: "Maximum time maintenance may wait; zero means no wait.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_WORKLOAD_MAINTENANCE_EXECUTION_TIMEOUT", Field: "WorkloadMaintenanceExecutionTimeout", Type: TypeDuration, Default: "30m", Category: "workload", Scope: "serve", Description: "Maximum execution time for maintenance work.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_ENVIRONMENT", Field: "Environment", Type: TypeString, Category: "server", Scope: "serve,admin", Description: "Single serving environment permanently bound to this LeapView instance.", Example: "prod", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_EVALUATION_MODE", Field: "EvaluationMode", Type: TypeBool, Default: "false", Category: "server", Scope: "evaluate,serve,admin", Description: "Enable the disposable loopback-only evaluation profile; never use for production deployment.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_NODE_MEMORY_MAX_BYTES", Field: "DuckDBNodeMemoryMaxBytes", Type: TypeInt64, Default: "2684354560", Category: "analytics", Scope: "serve", Description: "Memory limit for the process-owned DuckDB instance.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_NODE_TEMP_MAX_BYTES", Field: "DuckDBNodeTempMaxBytes", Type: TypeInt64, Default: "10737418240", Category: "analytics", Scope: "serve", Description: "Temporary-storage limit for the process-owned DuckDB instance.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_NODE_MAX_THREADS", Field: "DuckDBNodeMaxThreads", Type: TypeInt, Default: "5", Category: "analytics", Scope: "serve", Description: "Execution-thread limit shared by all work in the process-owned DuckDB instance.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_DUCKDB_TEMP_DIR", Field: "DuckDBTempDir", Type: TypeString, Category: "analytics", Scope: "serve", Description: "Private temporary-storage directory for the process-owned DuckDB instance; defaults beneath LEAPVIEW_HOME.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_RESULT_MAX_ROWS", Field: "QueryResultMaxRows", Type: TypeInt, Default: "10000", Category: "analytics", Scope: "serve", Description: "Maximum rows retained by one logical analytical operation.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_RESULT_MAX_BYTES", Field: "QueryResultMaxBytes", Type: TypeInt64, Default: "33554432", Category: "analytics", Scope: "serve", Description: "Maximum conservatively retained Arrow bytes for one logical analytical operation.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_CACHE_RUNTIME_MAX_ENTRIES", Field: "QueryCacheRuntimeMaxEntries", Type: TypeInt, Default: "256", Category: "analytics", Scope: "serve", Description: "Maximum cached result entries retained by one serving generation.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_CACHE_RUNTIME_MAX_BYTES", Field: "QueryCacheRuntimeMaxBytes", Type: TypeInt64, Default: "67108864", Category: "analytics", Scope: "serve", Description: "Maximum conservatively retained Arrow cache bytes for one serving generation.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_CACHE_NODE_MAX_ENTRIES", Field: "QueryCacheNodeMaxEntries", Type: TypeInt, Default: "2048", Category: "analytics", Scope: "serve", Description: "Maximum cached result entries retained by the node.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_CACHE_NODE_MAX_BYTES", Field: "QueryCacheNodeMaxBytes", Type: TypeInt64, Default: "536870912", Category: "analytics", Scope: "serve", Description: "Maximum conservatively retained Arrow cache bytes for the node.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_QUERY_CACHE_L3_ENABLED", Field: "QueryCacheL3Enabled", Type: TypeBool, Default: "false", Category: "analytics", Scope: "serve", Description: "Enable the optional target-scoped durable query-result object cache (requires an admitted S3 physical pool).", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_HEALTHCHECK_URL", Field: "HealthcheckURL", Type: TypeString, Category: "operations", Scope: "healthcheck", Description: "Explicit readiness URL used by the healthcheck command.", Example: "http://127.0.0.1:8080/readyz", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_HOME", Field: "HomeDir", Type: TypeString, Default: ".leapview", Category: "storage", Scope: "serve,admin,client", Description: "Instance state directory containing databases, artifacts, and runtime files.", Example: "/var/lib/leapview", Runtime: true, Lifecycle: "supported", EnvExample: "/var/lib/leapview"},
	{Name: "LEAPVIEW_IMAGE", Field: "Image", Type: TypeString, Category: "deployment", Scope: "serve,admin,Hetzner provisioner", Description: "Exact immutable LeapView OCI image identity used by release, backup, and qualification workflows.", Example: "ghcr.io/flidai/leapview@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_INFISICAL_ALLOWED_SCOPES", Field: "InfisicalAllowedScopes", Type: TypeString, Category: "connections", Scope: "serve", Description: "JSON array of exact Infisical project/environment/path-prefix scopes the target runtime may read.", Example: `[{"projectId":"project-id","environment":"prod","secretPathPrefix":"/leapview"}]`, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_INFISICAL_BASE_URL", Field: "InfisicalBaseURL", Type: TypeString, Category: "connections", Scope: "serve", Description: "HTTPS origin of the target's authoritative read-only Infisical backend.", Example: "https://app.infisical.com", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_ID", Field: "InfisicalUniversalClientID", Type: TypeString, Category: "connections", Scope: "serve", Description: "Infisical Universal Auth machine identity client identifier.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_SECRET", Field: "InfisicalUniversalClientSecret", Type: TypeString, Category: "connections", Scope: "serve", Description: "Infisical Universal Auth bootstrap secret supplied only to the target process.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_SITE_HOST", Type: TypeString, Default: "178.105.204.14", Category: "deployment", Scope: "public-site operator", Description: "Reserved production IPv4 contacted by the public-site deployment command.", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_SITE_IMAGE", Type: TypeString, Category: "deployment", Scope: "public-site provisioner", Description: "Immutable LeapView public-site OCI image reference consumed by deployment tooling.", Example: "ghcr.io/flidai/leapview-site@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_SITE_SSH_KEY", Type: TypeString, Category: "deployment", Scope: "public-site operator", Description: "Optional path to the dedicated production SSH identity used by the public-site deployment command.", Example: "~/.ssh/leapview-site-production", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_LOCAL_AUTH", Field: "LocalAuth", Type: TypeBool, Category: "authentication", Scope: "serve", Description: "Enable administrator-managed local browser authentication.", Example: "true", Runtime: true, Lifecycle: "supported", EnvExample: "true"},
	{Name: "LEAPVIEW_MAP_ASSET_DIR", Field: "MapAssetDir", Type: TypeString, Default: ".data/map-assets", Category: "assets", Scope: "serve", Description: "Local root containing the verified, content-addressed basemap package.", Example: "/var/lib/leapview/map-assets", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_BACKEND", Field: "ManagedDataBackend", Type: TypeString, Default: "local", Category: "managed data", Scope: "serve", Description: "Storage backend for project-global managed data; supported values are local and s3.", Runtime: true, Lifecycle: "supported", EnvExample: "local"},
	{Name: "LEAPVIEW_MANAGED_DATA_DIR", Field: "ManagedDataDir", Type: TypeString, Category: "managed data", Scope: "serve", Description: "Private local root for managed-data objects, upload staging, and verified runtime views; defaults beneath LEAPVIEW_HOME.", Runtime: true, Lifecycle: "supported", EnvExample: "/var/lib/leapview/managed-data", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_GC_GRACE_PERIOD", Field: "ManagedDataGCGracePeriod", Type: TypeDuration, Default: "24h", Category: "managed data", Scope: "serve", Description: "Minimum age of unreferenced managed-data objects before garbage collection.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_GC_INTERVAL", Field: "ManagedDataGCInterval", Type: TypeDuration, Default: "1h", Category: "managed data", Scope: "serve", Description: "Interval between managed-data garbage-collection passes.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_MAX_FILES", Field: "ManagedDataMaxFiles", Type: TypeInt, Default: "10000", Category: "managed data", Scope: "serve", Description: "Maximum number of files in one managed-data revision.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES", Field: "ManagedDataMaxFileBytes", Type: TypeInt64, Default: "1073741824", Category: "managed data", Scope: "serve", Description: "Maximum size in bytes of one managed-data file.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_MAX_REVISION_BYTES", Field: "ManagedDataMaxRevisionBytes", Type: TypeInt64, Default: "10737418240", Category: "managed data", Scope: "serve", Description: "Maximum total size in bytes of one managed-data revision.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES", Field: "ManagedDataMinFreeBytes", Type: TypeInt64, Default: "5368709120", Category: "managed data", Scope: "serve", Description: "Minimum free bytes required before accepting local managed-data uploads.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID", Field: "ManagedDataS3AccessKeyID", Type: TypeString, Category: "managed data", Scope: "serve", Description: "Optional S3 access-key identifier for managed-data storage.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_BUCKET", Field: "ManagedDataS3Bucket", Type: TypeString, Category: "managed data", Scope: "serve", Description: "S3 bucket used for managed-data objects and staging.", Example: "leapview-managed-data", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_ENDPOINT", Field: "ManagedDataS3Endpoint", Type: TypeString, Category: "managed data", Scope: "serve", Description: "Optional S3-compatible endpoint URL.", Example: "https://s3.example.com", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_PATH_STYLE", Field: "ManagedDataS3PathStyle", Type: TypeBool, Default: "false", Category: "managed data", Scope: "serve", Description: "Use path-style addressing for S3-compatible managed-data storage.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_PREFIX", Field: "ManagedDataS3Prefix", Type: TypeString, Default: "managed-data", Category: "managed data", Scope: "serve", Description: "Object-key prefix for managed data in the configured S3 bucket.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_REGION", Field: "ManagedDataS3Region", Type: TypeString, Category: "managed data", Scope: "serve", Description: "S3 region used for managed-data requests.", Example: "eu-west-1", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_SECRET_ACCESS_KEY", Field: "ManagedDataS3SecretAccessKey", Type: TypeString, Category: "managed data", Scope: "serve", Description: "Optional S3 secret access key for managed-data storage.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_S3_SESSION_TOKEN", Field: "ManagedDataS3SessionToken", Type: TypeString, Category: "managed data", Scope: "serve", Description: "Optional temporary S3 session token for managed-data storage.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MANAGED_DATA_UPLOAD_SESSION_TTL", Field: "ManagedDataUploadSessionTTL", Type: TypeDuration, Default: "24h", Category: "managed data", Scope: "serve", Description: "Lifetime of an incomplete managed-data upload session.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_BACKEND", Field: "ObjectStoreBackend", Type: TypeString, Default: "filesystem", Category: "object store", Scope: "serve", Description: "Immutable object-store backend for project sources and compiled serving artifacts; supported values are filesystem and s3.", Runtime: true, Lifecycle: "supported", EnvExample: "filesystem"},
	{Name: "LEAPVIEW_OBJECT_STORE_FILESYSTEM_ROOT", Field: "ObjectStoreFilesystemRoot", Type: TypeString, Category: "object store", Scope: "serve", Description: "Optional private, dedicated filesystem root for immutable project sources and compiled serving artifacts; defaults beneath LEAPVIEW_HOME/artifacts.", Example: "/var/lib/leapview/artifacts/object-store", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_ACCESS_KEY_ID", Field: "ObjectStoreS3AccessKeyID", Type: TypeString, Category: "object store", Scope: "serve", Description: "Optional static S3 access-key identifier for immutable project sources and compiled serving artifacts; omitted values use ambient AWS credentials.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_BUCKET", Field: "ObjectStoreS3Bucket", Type: TypeString, Category: "object store", Scope: "serve", Description: "S3 bucket for immutable project sources and compiled serving artifacts.", Example: "leapview-object-store", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_ENDPOINT", Field: "ObjectStoreS3Endpoint", Type: TypeString, Category: "object store", Scope: "serve", Description: "Optional S3-compatible endpoint URL for immutable project sources and compiled serving artifacts.", Example: "https://s3.example.com", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_EXPECTED_BUCKET_OWNER", Field: "ObjectStoreS3ExpectedBucketOwner", Type: TypeString, Category: "object store", Scope: "serve", Description: "Optional twelve-digit AWS account ID expected to own the immutable object-store bucket.", Example: "123456789012", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_MODE", Field: "ObjectStoreS3EncryptionMode", Type: TypeString, Default: "AES256", Category: "object store", Scope: "serve", Description: "Explicit server-side encryption mode for immutable object-store S3 objects: AES256 (SSE-S3) or aws:kms (SSE-KMS).", Example: "AES256", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_KEY_REF", Field: "ObjectStoreS3EncryptionKeyRef", Type: TypeString, Category: "object store", Scope: "serve", Description: "Opaque application reference for resolving the immutable object-store S3 KMS key; never sent to S3 as a key ID.", Example: "prod-object-store", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_PROVIDER_KEY", Field: "ObjectStoreS3EncryptionProviderKey", Type: TypeString, Category: "object store", Scope: "serve", Description: "Target-resolved provider KMS key identity used for immutable object-store S3 SSE-KMS requests.", Example: "arn:aws:kms:eu-west-1:123456789012:key/…", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_PATH_STYLE", Field: "ObjectStoreS3PathStyle", Type: TypeBool, Default: "false", Category: "object store", Scope: "serve", Description: "Use path-style addressing for S3-compatible immutable object storage.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_PREFIX", Field: "ObjectStoreS3Prefix", Type: TypeString, Default: "objects", Category: "object store", Scope: "serve", Description: "Object-key prefix for immutable project sources and compiled serving artifacts in the configured S3 bucket.", Example: "leapview/objects", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_REGION", Field: "ObjectStoreS3Region", Type: TypeString, Category: "object store", Scope: "serve", Description: "S3 region used for immutable project sources and compiled serving artifact requests.", Example: "eu-west-1", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_SECRET_ACCESS_KEY", Field: "ObjectStoreS3SecretAccessKey", Type: TypeString, Category: "object store", Scope: "serve", Description: "Optional static S3 secret access key for immutable project sources and compiled serving artifacts; omitted values use ambient AWS credentials.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OBJECT_STORE_S3_SESSION_TOKEN", Field: "ObjectStoreS3SessionToken", Type: TypeString, Category: "object store", Scope: "serve", Description: "Optional static S3 session token for immutable project sources and compiled serving artifacts.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_MINIO_CONFORMANCE_REQUIRED", Type: TypeBool, Category: "ci", Scope: "MinIO conformance gate", Description: "Require the real MinIO conformance lane to fail closed instead of skipping unavailable runtime or evidence checks.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", Type: TypeBool, Category: "ci", Scope: "PostgreSQL conformance gate", Description: "Require the real PostgreSQL 18 conformance lane to fail closed when Docker or the pinned image is unavailable.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONFORMANCE_SKIP", Type: TypeBool, Category: "ci", Scope: "PostgreSQL conformance routing", Description: "Skip PostgreSQL-backed packages in ordinary Go test lanes; the dedicated fail-closed conformance lane overrides this flag.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_BOOTSTRAP_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable bootstrap credential for the loopback-only development PostgreSQL container.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable control-runtime credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable control-readonly credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable control-migrator credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable control upgrade-coordinator credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable bounded control-maintenance credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable DuckLake-runtime credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable DuckLake-migrator credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Disposable DuckLake-maintenance credential generated by the local PostgreSQL harness.", Secret: true, Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_COMPOSE_PROJECT", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Explicit Docker Compose project identity used by the local PostgreSQL harness.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_DEV_ENV_FILE", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Path for the mode-0600 environment file emitted by the local PostgreSQL harness.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_DEV_PORT", Type: TypeInt, Category: "ci", Scope: "local PostgreSQL harness", Description: "Loopback port reserved for the worktree-local PostgreSQL container.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_PROJECT_SUFFIX", Type: TypeString, Category: "ci", Scope: "local PostgreSQL harness", Description: "Isolate the Docker Compose project name used by worktree-local PostgreSQL tests.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_POSTGRES_TEST_MODE", Type: TypeBool, Category: "ci", Scope: "local PostgreSQL harness", Description: "Enable the isolated disposable PostgreSQL test-harness mode.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_METRICS_BEARER_TOKEN", Field: "MetricsBearerToken", Type: TypeString, Category: "operations", Scope: "serve", Description: "Bearer token protecting the Prometheus metrics endpoint; production requires at least 32 characters.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", EnvExample: "replace-with-at-least-32-characters"},
	{Name: "LEAPVIEW_MCP_OAUTH_ISSUER_URL", Field: "MCPOAuthIssuerURL", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Optional external OAuth issuer for MCP JWT access tokens; when omitted, LeapView provides the MCP authorization server.", Example: "https://identity.example.com", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_PERF_ITERATIONS", Type: TypeInt, Default: "5", Category: "development", Scope: "dashboard performance QA", Description: "Measured interaction iterations run by the configured dashboard performance scenario.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_ENFORCE_THRESHOLDS", Type: TypeBool, Default: "false", Category: "development", Scope: "dashboard performance QA", Description: "Fail dashboard performance QA when phase latency or query-count thresholds are exceeded.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_LOG", Type: TypeString, Default: ".tmp/dev-server.log", Category: "development", Scope: "dashboard performance QA", Description: "Development server log consumed by dashboard performance QA.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_MAX_ALL_TARGET_P95_MS", Type: TypeInt, Default: "1000", Category: "development", Scope: "dashboard performance QA", Description: "Maximum all-target settlement p95 when performance thresholds are enforced.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_MAX_CRITICAL_KPI_P95_MS", Type: TypeInt, Default: "1000", Category: "development", Scope: "dashboard performance QA", Description: "Maximum critical-KPI settlement p95 when performance thresholds are enforced.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_MAX_FIRST_TARGET_PAINT_P95_MS", Type: TypeInt, Default: "500", Category: "development", Scope: "dashboard performance QA", Description: "Maximum first-target paint p95 when performance thresholds are enforced.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_MAX_OPTIMISTIC_FEEDBACK_P95_MS", Type: TypeInt, Default: "16", Category: "development", Scope: "dashboard performance QA", Description: "Maximum local optimistic-feedback p95 when performance thresholds are enforced.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_MAX_QUERIES", Type: TypeInt, Default: "4", Category: "development", Scope: "dashboard performance QA", Description: "Maximum physical queries per measured refresh when performance thresholds are enforced.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_OUTPUT", Type: TypeString, Category: "development", Scope: "dashboard performance QA", Description: "Optional JSON output path; defaults to a suite-specific file under .tmp.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PERF_SCENARIO", Type: TypeString, Default: "scripts/performance/movielens.json", Category: "development", Scope: "dashboard performance QA", Description: "Path to a dashboard performance scenario manifest.", Lifecycle: "development"},
	{Name: "LEAPVIEW_PLAYWRIGHT_READY", Type: TypeBool, Default: "false", Category: "ci", Scope: "browser test setup", Description: "Signals that CI already provisioned the pinned Playwright browser and dependencies.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_OIDC_CALLBACK_URL", Field: "OIDCCallbackURL", Type: TypeString, Category: "authentication", Scope: "serve", Description: "HTTPS callback URL registered with the generic OIDC provider.", Example: "https://leapview.example.com/auth/oidc/callback", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OIDC_CLIENT_ID", Field: "OIDCClientID", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Generic OIDC client identifier.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OIDC_CLIENT_SECRET", Field: "OIDCSecret", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Generic OIDC client secret.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OIDC_ISSUER_URL", Field: "OIDCIssuerURL", Type: TypeString, Category: "authentication", Scope: "serve", Description: "HTTPS issuer URL for the generic OIDC provider.", Example: "https://issuer.example.com", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OIDC_PROVIDER_ID", Field: "OIDCProviderID", Type: TypeString, Default: "oidc", Category: "authentication", Scope: "serve", Description: "Route-safe identifier for the generic OIDC provider.", Example: "oidc", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_OIDC_SCOPES", Field: "OIDCScopes", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Comma- or whitespace-separated additional OIDC scopes.", Example: "openid profile email", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_PRODUCTION", Field: "Production", Type: TypeBool, Category: "server", Scope: "serve,admin", Description: "Enable production serving and validation behavior.", Default: "false", Runtime: true, Lifecycle: "supported", EnvExample: "1"},
	{Name: "LEAPVIEW_PUBLIC_URL", Field: "PublicURL", Type: TypeString, Category: "server", Scope: "serve", Description: "Canonical externally visible LeapView origin used for MCP resource identity and OAuth discovery.", Example: "https://leapview.example.com", Runtime: true, Lifecycle: "supported", EnvExample: "https://leapview.example.com"},
	{Name: "LEAPVIEW_PUBLIC_RELEASE_MANIFEST", Type: TypeString, Default: "docs/public-release.json", Category: "release", Scope: "public site smoke", Description: "Release manifest compared with the public site's deployed release identity.", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_DESKTOP_DISTRIBUTION", Type: TypeString, Category: "release", Scope: "desktop packaging", Description: "Required desktop package identity; accepted values are preview and stable.", Example: "preview", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_DESKTOP_PACKAGED_PROOF_ORIGIN", Type: TypeString, Category: "release", Scope: "packaged desktop security proof", Description: "Exact loopback hostile-instance origin injected only into an isolated packaged-app security proof.", Example: "http://127.0.0.1:18080", Lifecycle: "internal"},
	{Name: "LEAPVIEW_DESKTOP_RELEASE_MANIFEST", Type: TypeString, Default: "docs/desktop-release.json", Category: "release", Scope: "public site smoke", Description: "Desktop release manifest compared with the public download page and deployed desktop release identity.", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_PACKAGED_APP", Type: TypeString, Category: "release", Scope: "packaged desktop security proof", Description: "Path to the exact packaged LeapView executable qualified by the hostile-instance proof.", Example: "desktop/out/LeapView-linux-x64/LeapView", Lifecycle: "internal"},
	{Name: "LEAPVIEW_PUBLIC_SITE_ALIASES", Type: TypeString, Default: "http://leapview.dev,https://www.leapview.dev", Category: "release", Scope: "public site smoke", Description: "Comma-separated public aliases that must redirect to the canonical site origin.", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_PUBLIC_SITE_URL", Type: TypeString, Default: "https://leapview.dev", Category: "release", Scope: "public site smoke", Description: "Canonical public site origin verified by the release adoption smoke test.", Lifecycle: "tooling"},
	{Name: "LEAPVIEW_ROUTE_QA_SCOPE", Type: TypeString, Default: "all", Category: "development", Scope: "ui-qa", Description: "Assembled browser route QA scope.", Lifecycle: "development"},
	{Name: "LEAPVIEW_SCIM_BEARER_TOKEN", Field: "SCIMBearerToken", Type: TypeString, Category: "authentication", Scope: "serve", Description: "Bearer token enabling SCIM provisioning; production requires at least 32 characters when set.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_SITE_BASE_URL", Type: TypeString, Category: "site", Scope: "public site", Description: "Externally visible HTTP(S) origin used for canonical URLs, discovery documents, and transport policy.", Example: "https://leapview.dev", Lifecycle: "supported"},
	{Name: "LEAPVIEW_SITE_SHOWCASE_EMBED_URL", Type: TypeString, Category: "site", Scope: "public site", Description: "Optional public dashboard embed URL that enables the live /showcase route and its exact frame-src policy.", Example: "https://app.leapview.dev/embed/dashboards/opaque-public-id", Lifecycle: "supported"},
	{Name: "LEAPVIEW_SMOKE_PORT", Type: TypeInt, Default: "18080", Category: "development", Scope: "production image smoke test", Description: "Host port used by the production image smoke test.", Lifecycle: "internal"},
	{Name: "LEAPVIEW_TARGET", Field: "Target", Type: TypeString, Category: "client", Scope: "client commands", Description: "Default LeapView API target URL.", Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_TOKEN_HASH_KEY", Field: "TokenHashKey", Type: TypeString, Category: "security", Scope: "serve", Description: "Optional dedicated key for deterministic API-token fingerprints; falls back to the CSRF key.", Example: SecretPlaceholder, Secret: true, Runtime: true, Lifecycle: "supported", Commented: true},
	{Name: "LEAPVIEW_TRUST_PROXY_HEADERS", Field: "TrustProxyHeaders", Type: TypeBool, Category: "security", Scope: "serve", Description: "Trust client-address headers only when a trusted proxy overwrites them.", Default: "false", Runtime: true, Lifecycle: "supported", EnvExample: "false"},
	{Name: "LEAPVIEW_UI_QA_SCOPE", Type: TypeString, Default: "all", Category: "development", Scope: "ui-qa", Description: "UI framework QA scope; all runs route and visual coverage, while visual runs only screenshot comparisons.", Lifecycle: "development"},
	{Name: "LEAPVIEW_UPDATE_VISUAL_BASELINES", Type: TypeBool, Default: "false", Category: "development", Scope: "ui-qa", Description: "Write reviewed UI visual-regression baselines before immediately comparing them again.", Lifecycle: "development"},
	{Name: "LEAPVIEW_VISUAL_ARTIFACT_DIR", Type: TypeString, Default: ".tmp/qa-ui-framework/visual-artifacts", Category: "development", Scope: "ui-qa", Description: "Directory where UI visual-regression reports and failure artifacts are written.", Lifecycle: "development"},
	{Name: "LEAPVIEW_WAREHOUSE_DSN", Type: TypeString, Category: "connection", Scope: "example connection", Description: "Example externally supplied warehouse connection credential.", Example: SecretPlaceholder, Secret: true, Lifecycle: "external"},
}

type PredicateKind string

const (
	PredicateAll                PredicateKind = "all"
	PredicateAny                PredicateKind = "any"
	PredicateNot                PredicateKind = "not"
	PredicatePresent            PredicateKind = "present"
	PredicateTrue               PredicateKind = "true"
	PredicateMinLength          PredicateKind = "min_length"
	PredicateHTTPSURL           PredicateKind = "https_url"
	PredicateHTTPSOrigin        PredicateKind = "https_origin"
	PredicateLoopbackHTTPOrigin PredicateKind = "loopback_http_origin"
	PredicateSlug               PredicateKind = "route_slug"
	PredicateEquals             PredicateKind = "equals"
	PredicateOneOf              PredicateKind = "one_of"
	PredicatePositive           PredicateKind = "positive"
	PredicateAtLeast            PredicateKind = "at_least_setting"
)

type Predicate struct {
	Kind      PredicateKind `json:"kind"`
	Name      string        `json:"name,omitempty"`
	Minimum   int           `json:"minimum,omitempty"`
	Value     string        `json:"value,omitempty"`
	Values    []string      `json:"values,omitempty"`
	OtherName string        `json:"otherName,omitempty"`
	Children  []Predicate   `json:"children,omitempty"`
}

func All(children ...Predicate) Predicate { return Predicate{Kind: PredicateAll, Children: children} }
func Any(children ...Predicate) Predicate { return Predicate{Kind: PredicateAny, Children: children} }
func Not(child Predicate) Predicate {
	return Predicate{Kind: PredicateNot, Children: []Predicate{child}}
}
func Present(name string) Predicate { return Predicate{Kind: PredicatePresent, Name: name} }
func True(name string) Predicate    { return Predicate{Kind: PredicateTrue, Name: name} }
func MinLength(name string, minimum int) Predicate {
	return Predicate{Kind: PredicateMinLength, Name: name, Minimum: minimum}
}
func HTTPSURL(name string) Predicate    { return Predicate{Kind: PredicateHTTPSURL, Name: name} }
func HTTPSOrigin(name string) Predicate { return Predicate{Kind: PredicateHTTPSOrigin, Name: name} }
func LoopbackHTTPOrigin(name string) Predicate {
	return Predicate{Kind: PredicateLoopbackHTTPOrigin, Name: name}
}
func RouteSlug(name string) Predicate { return Predicate{Kind: PredicateSlug, Name: name} }
func Equals(name, value string) Predicate {
	return Predicate{Kind: PredicateEquals, Name: name, Value: value}
}
func OneOf(name string, values ...string) Predicate {
	return Predicate{Kind: PredicateOneOf, Name: name, Values: append([]string(nil), values...)}
}
func Positive(name string) Predicate { return Predicate{Kind: PredicatePositive, Name: name} }
func AtLeast(name, otherName string) Predicate {
	return Predicate{Kind: PredicateAtLeast, Name: name, OtherName: otherName}
}

type Rule struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	When        Predicate `json:"when,omitempty"`
	Assert      Predicate `json:"assert"`
	Message     string    `json:"message"`
}

func Rules() []Rule { return append([]Rule(nil), rules...) }

var (
	production        = True("LEAPVIEW_PRODUCTION")
	evaluation        = True("LEAPVIEW_EVALUATION_MODE")
	oidcAny           = Any(Present("LEAPVIEW_OIDC_ISSUER_URL"), Present("LEAPVIEW_OIDC_CLIENT_ID"), Present("LEAPVIEW_OIDC_CLIENT_SECRET"), Present("LEAPVIEW_OIDC_CALLBACK_URL"), Present("LEAPVIEW_OIDC_SCOPES"))
	oidcComplete      = All(Present("LEAPVIEW_OIDC_ISSUER_URL"), Present("LEAPVIEW_OIDC_CLIENT_ID"), Present("LEAPVIEW_OIDC_CLIENT_SECRET"), Present("LEAPVIEW_OIDC_CALLBACK_URL"))
	azureAny          = Any(Present("LEAPVIEW_AZURE_CLIENT_ID"), Present("LEAPVIEW_AZURE_CLIENT_SECRET"), Present("LEAPVIEW_AZURE_CALLBACK_URL"), Present("LEAPVIEW_AZURE_TENANT"))
	azureComplete     = All(Present("LEAPVIEW_AZURE_CLIENT_ID"), Present("LEAPVIEW_AZURE_CLIENT_SECRET"), Present("LEAPVIEW_AZURE_CALLBACK_URL"))
	browserAuth       = Any(True("LEAPVIEW_LOCAL_AUTH"), oidcComplete, azureComplete)
	managedData       = Present("LEAPVIEW_MANAGED_DATA_BACKEND")
	managedS3         = Equals("LEAPVIEW_MANAGED_DATA_BACKEND", "s3")
	infisicalAny      = Any(Present("LEAPVIEW_INFISICAL_BASE_URL"), Present("LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_ID"), Present("LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_SECRET"), Present("LEAPVIEW_INFISICAL_ALLOWED_SCOPES"))
	infisicalComplete = All(Present("LEAPVIEW_INFISICAL_BASE_URL"), Present("LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_ID"), Present("LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_SECRET"), Present("LEAPVIEW_INFISICAL_ALLOWED_SCOPES"))
	objectStore       = Present("LEAPVIEW_OBJECT_STORE_BACKEND")
	objectStoreS3     = Equals("LEAPVIEW_OBJECT_STORE_BACKEND", "s3")
	objectStoreKMS    = Equals("LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_MODE", "aws:kms")
	objectStoreSSES3  = Equals("LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_MODE", "AES256")
)

var rules = []Rule{
	{ID: "production-dev-bypass", Description: "Production cannot bypass authentication.", When: production, Assert: Not(True("LEAPVIEW_DEV_AUTH_BYPASS")), Message: "production serve must not enable LEAPVIEW_DEV_AUTH_BYPASS"},
	{ID: "production-oidc-complete", Description: "OIDC settings are all-or-none in production.", When: All(production, oidcAny), Assert: oidcComplete, Message: "production OIDC auth requires LEAPVIEW_OIDC_ISSUER_URL, LEAPVIEW_OIDC_CLIENT_ID, LEAPVIEW_OIDC_CLIENT_SECRET, and LEAPVIEW_OIDC_CALLBACK_URL"},
	{ID: "production-azure-complete", Description: "Azure settings are all-or-none in production.", When: All(production, azureAny), Assert: azureComplete, Message: "production Azure auth requires LEAPVIEW_AZURE_CLIENT_ID, LEAPVIEW_AZURE_CLIENT_SECRET, and LEAPVIEW_AZURE_CALLBACK_URL"},
	{ID: "production-auth-mode", Description: "Production requires local, API-token-only, OIDC, or Azure authentication.", When: production, Assert: Any(True("LEAPVIEW_LOCAL_AUTH"), True("LEAPVIEW_API_TOKEN_ONLY_AUTH"), oidcComplete, azureComplete), Message: "production serve requires OIDC auth env vars, Azure auth env vars, LEAPVIEW_LOCAL_AUTH, or LEAPVIEW_API_TOKEN_ONLY_AUTH"},
	{ID: "production-csrf-key", Description: "Production requires a CSRF key with at least 32 characters.", When: production, Assert: MinLength("LEAPVIEW_CSRF_KEY", 32), Message: "production serve requires LEAPVIEW_CSRF_KEY with at least 32 characters"},
	{ID: "production-metrics-token", Description: "Production requires a metrics bearer token with at least 32 characters.", When: production, Assert: MinLength("LEAPVIEW_METRICS_BEARER_TOKEN", 32), Message: "production metrics scraping requires LEAPVIEW_METRICS_BEARER_TOKEN with at least 32 characters"},
	{ID: "production-public-url", Description: "Production requires a canonical public URL.", When: production, Assert: Present("LEAPVIEW_PUBLIC_URL"), Message: "production serve requires LEAPVIEW_PUBLIC_URL"},
	{ID: "production-public-url-https", Description: "The production public URL must be an HTTPS origin without a path, query, fragment, or credentials.", When: All(production, Not(evaluation)), Assert: HTTPSOrigin("LEAPVIEW_PUBLIC_URL"), Message: "production serve requires LEAPVIEW_PUBLIC_URL to be an https origin"},
	{ID: "production-mcp-oauth-issuer-https", Description: "An external production MCP OAuth issuer must use HTTPS.", When: All(production, Present("LEAPVIEW_MCP_OAUTH_ISSUER_URL")), Assert: HTTPSURL("LEAPVIEW_MCP_OAUTH_ISSUER_URL"), Message: "production serve requires LEAPVIEW_MCP_OAUTH_ISSUER_URL to be an https URL"},
	{ID: "production-allowed-host", Description: "Production derives an allowed host from its public URL, explicit hosts, or a browser-auth callback host.", When: production, Assert: Any(Present("LEAPVIEW_PUBLIC_URL"), Present("LEAPVIEW_ALLOWED_HOSTS"), Present("LEAPVIEW_OIDC_CALLBACK_URL"), Present("LEAPVIEW_AZURE_CALLBACK_URL")), Message: "production serve requires LEAPVIEW_PUBLIC_URL, LEAPVIEW_ALLOWED_HOSTS, or an OIDC/Azure callback URL host"},
	{ID: "production-secure-cookie", Description: "Production browser authentication requires secure cookies unless API-token-only mode is also enabled.", When: All(production, browserAuth, Not(True("LEAPVIEW_API_TOKEN_ONLY_AUTH")), Not(evaluation)), Assert: True("LEAPVIEW_COOKIE_SECURE"), Message: "production browser auth requires LEAPVIEW_COOKIE_SECURE=true"},
	{ID: "evaluation-production", Description: "Evaluation mode uses the production serving-state runtime.", When: evaluation, Assert: production, Message: "LEAPVIEW_EVALUATION_MODE requires LEAPVIEW_PRODUCTION=true"},
	{ID: "evaluation-environment", Description: "Evaluation state is isolated in the evaluation environment.", When: evaluation, Assert: Equals("LEAPVIEW_ENVIRONMENT", "evaluation"), Message: "LEAPVIEW_EVALUATION_MODE requires LEAPVIEW_ENVIRONMENT=evaluation"},
	{ID: "evaluation-local-auth", Description: "Evaluation mode requires an unavoidable local sign-in.", When: evaluation, Assert: True("LEAPVIEW_LOCAL_AUTH"), Message: "LEAPVIEW_EVALUATION_MODE requires LEAPVIEW_LOCAL_AUTH=true"},
	{ID: "evaluation-loopback-origin", Description: "Evaluation mode permits plain HTTP only for a loopback origin.", When: evaluation, Assert: LoopbackHTTPOrigin("LEAPVIEW_PUBLIC_URL"), Message: "LEAPVIEW_EVALUATION_MODE requires a loopback http LEAPVIEW_PUBLIC_URL origin"},
	{ID: "evaluation-insecure-cookie", Description: "Evaluation mode uses a localhost HTTP session cookie.", When: evaluation, Assert: Not(True("LEAPVIEW_COOKIE_SECURE")), Message: "LEAPVIEW_EVALUATION_MODE requires LEAPVIEW_COOKIE_SECURE=false"},
	{ID: "evaluation-no-proxy", Description: "Evaluation mode must not trust forwarding headers.", When: evaluation, Assert: Not(True("LEAPVIEW_TRUST_PROXY_HEADERS")), Message: "LEAPVIEW_EVALUATION_MODE requires LEAPVIEW_TRUST_PROXY_HEADERS=false"},
	{ID: "production-oidc-issuer-https", Description: "The production OIDC issuer must use HTTPS.", When: All(production, oidcComplete), Assert: HTTPSURL("LEAPVIEW_OIDC_ISSUER_URL"), Message: "production serve requires LEAPVIEW_OIDC_ISSUER_URL to be an https URL"},
	{ID: "production-oidc-callback-https", Description: "The production OIDC callback must use HTTPS.", When: All(production, oidcComplete), Assert: HTTPSURL("LEAPVIEW_OIDC_CALLBACK_URL"), Message: "production serve requires LEAPVIEW_OIDC_CALLBACK_URL to be an https URL"},
	{ID: "production-oidc-provider-slug", Description: "The OIDC provider identifier must be route-safe.", When: All(production, oidcComplete), Assert: RouteSlug("LEAPVIEW_OIDC_PROVIDER_ID"), Message: "LEAPVIEW_OIDC_PROVIDER_ID must be a route-safe slug containing only letters, numbers, dots, underscores, or dashes"},
	{ID: "production-azure-callback-https", Description: "The production Azure callback must use HTTPS.", When: All(production, azureComplete), Assert: HTTPSURL("LEAPVIEW_AZURE_CALLBACK_URL"), Message: "production serve requires LEAPVIEW_AZURE_CALLBACK_URL to be an https URL"},
	{ID: "infisical-complete", Description: "The read-only Infisical target resolver is configured as one complete tuple.", When: infisicalAny, Assert: infisicalComplete, Message: "Infisical resolution requires LEAPVIEW_INFISICAL_BASE_URL, LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_ID, LEAPVIEW_INFISICAL_UNIVERSAL_CLIENT_SECRET, and LEAPVIEW_INFISICAL_ALLOWED_SCOPES"},
	{ID: "infisical-https", Description: "The Infisical backend is an HTTPS origin.", When: infisicalComplete, Assert: HTTPSOrigin("LEAPVIEW_INFISICAL_BASE_URL"), Message: "LEAPVIEW_INFISICAL_BASE_URL must be an https origin"},
	{ID: "production-scim-token", Description: "A configured production SCIM token must contain at least 32 characters.", When: All(production, Present("LEAPVIEW_SCIM_BEARER_TOKEN")), Assert: MinLength("LEAPVIEW_SCIM_BEARER_TOKEN", 32), Message: "production SCIM provisioning requires LEAPVIEW_SCIM_BEARER_TOKEN with at least 32 characters"},
	{ID: "managed-data-backend", Description: "Managed data uses a supported storage backend.", When: managedData, Assert: OneOf("LEAPVIEW_MANAGED_DATA_BACKEND", "local", "s3"), Message: "LEAPVIEW_MANAGED_DATA_BACKEND must be local or s3"},
	{ID: "managed-data-runtime-dir", Description: "Every managed-data backend requires a private local runtime and staging directory.", When: managedData, Assert: Present("LEAPVIEW_MANAGED_DATA_DIR"), Message: "managed-data storage requires LEAPVIEW_MANAGED_DATA_DIR"},
	{ID: "managed-data-s3-location", Description: "The S3 managed-data backend requires a bucket and region.", When: managedS3, Assert: All(Present("LEAPVIEW_MANAGED_DATA_S3_BUCKET"), Present("LEAPVIEW_MANAGED_DATA_S3_REGION")), Message: "S3 managed-data storage requires LEAPVIEW_MANAGED_DATA_S3_BUCKET and LEAPVIEW_MANAGED_DATA_S3_REGION"},
	{ID: "managed-data-s3-credentials", Description: "Managed-data S3 credentials are either omitted or configured as a complete key pair.", When: managedS3, Assert: Any(All(Not(Present("LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID")), Not(Present("LEAPVIEW_MANAGED_DATA_S3_SECRET_ACCESS_KEY")), Not(Present("LEAPVIEW_MANAGED_DATA_S3_SESSION_TOKEN"))), All(Present("LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID"), Present("LEAPVIEW_MANAGED_DATA_S3_SECRET_ACCESS_KEY"))), Message: "managed-data S3 credentials require both LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID and LEAPVIEW_MANAGED_DATA_S3_SECRET_ACCESS_KEY; a session token also requires that pair"},
	{ID: "managed-data-positive-limits", Description: "Managed-data upload, session, garbage-collection, and free-space limits are positive.", When: managedData, Assert: All(Positive("LEAPVIEW_MANAGED_DATA_MAX_FILES"), Positive("LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES"), Positive("LEAPVIEW_MANAGED_DATA_MAX_REVISION_BYTES"), Positive("LEAPVIEW_MANAGED_DATA_UPLOAD_SESSION_TTL"), Positive("LEAPVIEW_MANAGED_DATA_GC_INTERVAL"), Positive("LEAPVIEW_MANAGED_DATA_GC_GRACE_PERIOD"), Positive("LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES")), Message: "managed-data limits, durations, and free-space thresholds must be positive"},
	{ID: "ducklake-retention-positive-limits", Description: "DuckLake catalog retention interval and file grace period are positive.", When: production, Assert: All(Positive("LEAPVIEW_DUCKLAKE_RETENTION_INTERVAL"), Positive("LEAPVIEW_DUCKLAKE_RETENTION_FILE_GRACE_PERIOD")), Message: "production DuckLake retention interval and file grace period must be positive"},
	{ID: "managed-data-revision-limit", Description: "The managed-data revision limit is at least the per-file limit.", When: managedData, Assert: AtLeast("LEAPVIEW_MANAGED_DATA_MAX_REVISION_BYTES", "LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES"), Message: "LEAPVIEW_MANAGED_DATA_MAX_REVISION_BYTES must be at least LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES"},
	{ID: "object-store-backend", Description: "Immutable project sources and serving artifacts use a supported object-store backend.", When: objectStore, Assert: OneOf("LEAPVIEW_OBJECT_STORE_BACKEND", "filesystem", "s3"), Message: "LEAPVIEW_OBJECT_STORE_BACKEND must be filesystem or s3"},
	{ID: "object-store-s3-location", Description: "The immutable S3 object store requires a bucket and region.", When: objectStoreS3, Assert: All(Present("LEAPVIEW_OBJECT_STORE_S3_BUCKET"), Present("LEAPVIEW_OBJECT_STORE_S3_REGION")), Message: "S3 object storage requires LEAPVIEW_OBJECT_STORE_S3_BUCKET and LEAPVIEW_OBJECT_STORE_S3_REGION"},
	{ID: "object-store-s3-credentials", Description: "Immutable object-store S3 credentials are omitted for ambient AWS credentials or configured as a complete static pair.", When: objectStoreS3, Assert: Any(All(Not(Present("LEAPVIEW_OBJECT_STORE_S3_ACCESS_KEY_ID")), Not(Present("LEAPVIEW_OBJECT_STORE_S3_SECRET_ACCESS_KEY")), Not(Present("LEAPVIEW_OBJECT_STORE_S3_SESSION_TOKEN"))), All(Present("LEAPVIEW_OBJECT_STORE_S3_ACCESS_KEY_ID"), Present("LEAPVIEW_OBJECT_STORE_S3_SECRET_ACCESS_KEY"))), Message: "S3 object-store credentials require both LEAPVIEW_OBJECT_STORE_S3_ACCESS_KEY_ID and LEAPVIEW_OBJECT_STORE_S3_SECRET_ACCESS_KEY; a session token also requires that pair"},
	{ID: "object-store-s3-encryption-mode", Description: "Immutable object-store S3 encryption is explicit and supported.", When: objectStoreS3, Assert: Any(objectStoreSSES3, objectStoreKMS), Message: "LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_MODE must be AES256 (SSE-S3) or aws:kms (SSE-KMS)"},
	{ID: "object-store-s3-sse-s3-keys", Description: "SSE-S3 does not accept KMS key identities.", When: All(objectStoreS3, objectStoreSSES3), Assert: All(Not(Present("LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_KEY_REF")), Not(Present("LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_PROVIDER_KEY"))), Message: "SSE-S3 object storage must not set LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_KEY_REF or LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_PROVIDER_KEY"},
	{ID: "object-store-s3-sse-kms-keys", Description: "SSE-KMS requires both opaque and resolved provider key identities.", When: All(objectStoreS3, objectStoreKMS), Assert: All(Present("LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_KEY_REF"), Present("LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_PROVIDER_KEY")), Message: "SSE-KMS object storage requires LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_KEY_REF and LEAPVIEW_OBJECT_STORE_S3_ENCRYPTION_PROVIDER_KEY"},
}

func (r Rule) References() []string {
	seen := map[string]struct{}{}
	collectReferences(r.When, seen)
	collectReferences(r.Assert, seen)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func collectReferences(predicate Predicate, seen map[string]struct{}) {
	if predicate.Name != "" {
		seen[predicate.Name] = struct{}{}
	}
	if predicate.OtherName != "" {
		seen[predicate.OtherName] = struct{}{}
	}
	for _, child := range predicate.Children {
		collectReferences(child, seen)
	}
}

func Validate(values map[string]any) error {
	for _, rule := range rules {
		if rule.When.Kind != "" && !rule.When.Evaluate(values) {
			continue
		}
		if !rule.Assert.Evaluate(values) {
			return fmt.Errorf("%s", rule.Message)
		}
	}
	return nil
}

func (p Predicate) Evaluate(values map[string]any) bool {
	switch p.Kind {
	case "":
		return true
	case PredicateAll:
		for _, child := range p.Children {
			if !child.Evaluate(values) {
				return false
			}
		}
		return true
	case PredicateAny:
		for _, child := range p.Children {
			if child.Evaluate(values) {
				return true
			}
		}
		return false
	case PredicateNot:
		return len(p.Children) == 1 && !p.Children[0].Evaluate(values)
	case PredicatePresent:
		return present(values[p.Name])
	case PredicateTrue:
		value, ok := values[p.Name].(bool)
		return ok && value
	case PredicateMinLength:
		value, _ := values[p.Name].(string)
		return len(strings.TrimSpace(value)) >= p.Minimum
	case PredicateHTTPSURL:
		value, _ := values[p.Name].(string)
		parsed, err := url.Parse(strings.TrimSpace(value))
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
			parsed.RawQuery == "" && parsed.Fragment == ""
	case PredicateHTTPSOrigin:
		value, _ := values[p.Name].(string)
		parsed, err := url.Parse(strings.TrimSpace(value))
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
			(parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == ""
	case PredicateLoopbackHTTPOrigin:
		value, _ := values[p.Name].(string)
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false
		}
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	case PredicateSlug:
		return routeSlug(values[p.Name])
	case PredicateEquals:
		value, _ := values[p.Name].(string)
		return strings.TrimSpace(value) == p.Value
	case PredicateOneOf:
		value, _ := values[p.Name].(string)
		value = strings.TrimSpace(value)
		for _, allowed := range p.Values {
			if value == allowed {
				return true
			}
		}
		return false
	case PredicatePositive:
		value, ok := numericValue(values[p.Name])
		return ok && value > 0
	case PredicateAtLeast:
		value, valueOK := numericValue(values[p.Name])
		other, otherOK := numericValue(values[p.OtherName])
		return valueOK && otherOK && value >= other
	default:
		return false
	}
}

func numericValue(value any) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case time.Duration:
		return int64(value), true
	default:
		return 0, false
	}
}

func present(value any) bool {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case bool:
		return value
	case int:
		return value != 0
	default:
		return value != nil
	}
}

func routeSlug(value any) bool {
	id, _ := value.(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	if len(id) > 64 {
		return false
	}
	for index, char := range []byte(id) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		if index > 0 && (char == '-' || char == '_' || char == '.') {
			continue
		}
		return false
	}
	return true
}
