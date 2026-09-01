package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

const postgresControlMaintenanceRole = "leapview_control_maintenance"

// postgresCredentialAlias compares only the credential identity carried by a
// PostgreSQL URL. Query ordering, TLS options, and an omitted default port do
// not create a new credential, so they must not evade production role
// separation checks. It deliberately returns false for malformed URLs; the
// owning pool validator reports malformed connection details later without
// echoing the URL (or its password) in this package's diagnostics.
func postgresCredentialAlias(left, right string) bool {
	a, okA := postgresCredentialIdentity(left)
	b, okB := postgresCredentialIdentity(right)
	return okA && okB && a == b
}

func postgresCredentialIdentity(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.User == nil {
		return "", false
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username == "" || !hasPassword {
		return "", false
	}
	database, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil || database == "" || strings.Contains(database, "/") {
		return "", false
	}
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", false
		}
	}
	for _, value := range []string{parsed.Hostname(), database, username, password} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", false
		}
	}
	return strings.ToLower(parsed.Hostname()) + "|" + strconv.Itoa(port) + "|" + database + "|" + username + "|" + password, true
}

// PostgresRuntimeConfig maps the application environment contract to the
// capability-neutral PostgreSQL runtime policy. Control and DuckLake are
// intentionally mapped to independent Config values: callers must open and
// close their pools separately and must not infer a cross-database
// transaction from this value.
func (c Config) PostgresRuntimeConfig() platformpostgres.RuntimeConfig {
	return platformpostgres.RuntimeConfig{
		Control: platformpostgres.Config{
			URL:                    c.PostgresControlURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            c.PostgresControlRuntimeRole,
			Intent:                 platformpostgres.Intent(c.PostgresControlIntent),
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               int32(c.PostgresControlPoolMinConns),
			MaxConns:               int32(c.PostgresControlPoolMaxConns),
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		},
		DuckLake: platformpostgres.Config{
			URL:                    c.PostgresDuckLakeURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            c.PostgresDuckLakeRuntimeRole,
			Intent:                 platformpostgres.Intent(c.PostgresDuckLakeIntent),
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               int32(c.PostgresDuckLakePoolMinConns),
			MaxConns:               int32(c.PostgresDuckLakePoolMaxConns),
			AcquireTimeout:         c.PostgresDuckLakeAcquireTimeout,
			StatementTimeout:       c.PostgresDuckLakeStatementTimeout,
			LockTimeout:            c.PostgresDuckLakeLockTimeout,
			IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
		},
	}
}

// PostgresControlPlaneConfig maps the explicit control-plane role settings to
// the capability-neutral pool policy. Maintenance is always represented as a
// required one-connection read/write pool; production validation rejects an
// empty URL rather than synthesizing credentials from another role. The
// readonly pool is omitted when its URL is empty.
func (c Config) PostgresControlPlaneConfig() platformpostgres.ControlPlaneConfig {
	migratorRole := strings.TrimSpace(c.PostgresControlMigratorRole)
	if migratorRole == "" {
		migratorRole = "leapview_control_migrator"
	}
	runtimeRole := strings.TrimSpace(c.PostgresControlRuntimeRole)
	if runtimeRole == "" {
		runtimeRole = "leapview_control_runtime"
	}
	readonlyRole := strings.TrimSpace(c.PostgresControlReadonlyRole)
	if readonlyRole == "" {
		readonlyRole = "leapview_control_readonly"
	}
	config := platformpostgres.ControlPlaneConfig{
		Migrator: platformpostgres.Config{
			URL:                    c.PostgresControlMigratorURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            migratorRole,
			Intent:                 platformpostgres.IntentReadWrite,
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               1,
			MaxConns:               1,
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		},
		Runtime: platformpostgres.Config{
			URL:                    c.PostgresControlURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            runtimeRole,
			Intent:                 platformpostgres.IntentReadWrite,
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               int32(c.PostgresControlPoolMinConns),
			MaxConns:               int32(c.PostgresControlPoolMaxConns),
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		},
		Maintenance: c.PostgresControlMaintenanceConfig(),
	}
	if strings.TrimSpace(c.PostgresControlReadonlyURL) != "" {
		readonly := platformpostgres.Config{
			URL:                    c.PostgresControlReadonlyURL,
			ExpectedMajor:          c.PostgresExpectedMajor,
			RuntimeRole:            readonlyRole,
			Intent:                 platformpostgres.IntentReadOnly,
			RequireTLS:             c.PostgresRequireTLS,
			MinConns:               1,
			MaxConns:               4,
			AcquireTimeout:         c.PostgresControlAcquireTimeout,
			StatementTimeout:       c.PostgresControlStatementTimeout,
			LockTimeout:            c.PostgresControlLockTimeout,
			IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
		}
		config.Readonly = &readonly
	}
	return config
}

// PostgresDuckLakeRuntimeConfig maps only the ordinary catalog runtime role.
// Catalog migration credentials are deliberately absent from serve
// configuration: catalog upgrades run as a separately fenced operation.
func (c Config) PostgresDuckLakeRuntimeConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresDuckLakeRuntimeRole)
	if role == "" {
		role = "leapview_ducklake_runtime"
	}
	return platformpostgres.Config{
		URL:                    c.PostgresDuckLakeURL,
		ExpectedMajor:          c.PostgresExpectedMajor,
		RuntimeRole:            role,
		Intent:                 platformpostgres.IntentReadWrite,
		RequireTLS:             c.PostgresRequireTLS,
		MinConns:               int32(c.PostgresDuckLakePoolMinConns),
		MaxConns:               int32(c.PostgresDuckLakePoolMaxConns),
		AcquireTimeout:         c.PostgresDuckLakeAcquireTimeout,
		StatementTimeout:       c.PostgresDuckLakeStatementTimeout,
		LockTimeout:            c.PostgresDuckLakeLockTimeout,
		IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
}

// PostgresDuckLakeMaintenanceConfig describes the separately authenticated,
// one-connection read/write pool used only by bounded physical DuckLake
// expiry and cleanup. It is intentionally not part of PostgresRuntimeConfig:
// ordinary serving must never open or borrow this credential.
func (c Config) PostgresDuckLakeMaintenanceConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresDuckLakeMaintenanceRole)
	if role == "" {
		role = "leapview_ducklake_maintenance"
	}
	return platformpostgres.Config{
		URL: c.PostgresDuckLakeMaintenanceURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: role, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresDuckLakeAcquireTimeout, StatementTimeout: c.PostgresDuckLakeStatementTimeout,
		LockTimeout: c.PostgresDuckLakeLockTimeout, IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
}

// PostgresDuckLakeUpgradeConfig returns the two explicitly independent pools
// used by the catalog upgrade operation. They are intentionally not included
// in PostgresRuntimeConfig: serving never opens owner-capable catalog
// credentials or the guarded control coordinator pool.
func (c Config) PostgresDuckLakeUpgradeConfig() (platformpostgres.Config, platformpostgres.Config) {
	coordinatorRole := strings.TrimSpace(c.PostgresControlUpgradeCoordinatorRole)
	if coordinatorRole == "" {
		coordinatorRole = "leapview_control_upgrade_coordinator"
	}
	catalogRole := strings.TrimSpace(c.PostgresDuckLakeMigratorRole)
	if catalogRole == "" {
		catalogRole = "leapview_ducklake_migrator"
	}
	coordinator := platformpostgres.Config{
		URL: c.PostgresControlUpgradeCoordinatorURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: coordinatorRole, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresControlAcquireTimeout, StatementTimeout: c.PostgresControlStatementTimeout,
		LockTimeout: c.PostgresControlLockTimeout, IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
	catalog := platformpostgres.Config{
		URL: c.PostgresDuckLakeMigratorURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: catalogRole, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresDuckLakeAcquireTimeout, StatementTimeout: c.PostgresDuckLakeStatementTimeout,
		LockTimeout: c.PostgresDuckLakeLockTimeout, IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
	return coordinator, catalog
}

// PostgresControlMaintenanceConfig describes the separately authenticated,
// one-connection read/write maintenance pool. It is intentionally kept out of
// the ordinary runtime policy; composition retains this bounded pool so
// explicit maintenance operations can be wired without borrowing runtime
// credentials.
func (c Config) PostgresControlMaintenanceConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlMaintenanceRole)
	if role == "" {
		role = postgresControlMaintenanceRole
	}
	return platformpostgres.Config{
		URL: c.PostgresControlMaintenanceURL, ExpectedMajor: c.PostgresExpectedMajor,
		RuntimeRole: role, Intent: platformpostgres.IntentReadWrite,
		RequireTLS: c.PostgresRequireTLS, MinConns: 1, MaxConns: 1,
		AcquireTimeout: c.PostgresControlAcquireTimeout, StatementTimeout: c.PostgresControlStatementTimeout,
		LockTimeout: c.PostgresControlLockTimeout, IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
}

// ValidatePostgresUpgrade enforces the explicit two-credential operation
// contract. It is called by an upgrade command, never by serving startup, so
// an ordinary runtime process cannot accidentally open owner-capable pools.
func (c Config) ValidatePostgresUpgrade() error {
	coordinator, catalog := c.PostgresDuckLakeUpgradeConfig()
	if err := coordinator.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL upgrade coordinator configuration: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake migrator configuration: %w", err)
	}
	if coordinator.RuntimeRole == catalog.RuntimeRole {
		return errors.New("PostgreSQL upgrade coordinator and DuckLake migrator roles must be distinct")
	}
	if strings.TrimSpace(coordinator.URL) == strings.TrimSpace(c.PostgresControlURL) || strings.TrimSpace(coordinator.URL) == strings.TrimSpace(c.PostgresControlMigratorURL) || strings.TrimSpace(coordinator.URL) == strings.TrimSpace(c.PostgresDuckLakeURL) {
		return errors.New("PostgreSQL upgrade coordinator URL must use distinct credentials")
	}
	if strings.TrimSpace(catalog.URL) == strings.TrimSpace(c.PostgresDuckLakeURL) || strings.TrimSpace(catalog.URL) == strings.TrimSpace(c.PostgresControlURL) || strings.TrimSpace(catalog.URL) == strings.TrimSpace(c.PostgresControlMigratorURL) {
		return errors.New("PostgreSQL DuckLake migrator URL must use distinct credentials")
	}
	if c.Production && !c.PostgresRequireTLS {
		return errors.New("production PostgreSQL upgrade requires TLS")
	}
	return nil
}

// ValidatePostgresProduction enforces the clean-slate production startup
// contract.  Production must have separate migrator and runtime credentials;
// an optional readonly URL is validated when supplied.  This method is kept
// separate from Config.Validate so embedded development/test fixtures can
// continue to use their approved local SQLite cache without weakening the
// production serve command's fail-closed check.
func (c Config) ValidatePostgresProduction() error {
	if !c.Production {
		return nil
	}
	if strings.TrimSpace(c.DuckLakeCatalog) != "" {
		return errors.New("production serve rejects LEAPVIEW_DUCKLAKE_CATALOG_PATH; DuckLake catalog authority is PostgreSQL")
	}
	if strings.TrimSpace(c.PostgresControlURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_CONTROL_URL")
	}
	if strings.TrimSpace(c.PostgresControlMigratorURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	}
	if strings.TrimSpace(c.PostgresDuckLakeURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_DUCKLAKE_URL")
	}
	if strings.TrimSpace(c.PostgresControlMaintenanceURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL")
	}
	if strings.TrimSpace(c.PostgresDuckLakeMaintenanceURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL")
	}
	if !c.PostgresRequireTLS {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_REQUIRE_TLS=true")
	}
	if c.PostgresControlIntent != "" && c.PostgresControlIntent != string(platformpostgres.IntentReadWrite) {
		return fmt.Errorf("production serve requires LEAPVIEW_POSTGRES_CONTROL_INTENT=%q", platformpostgres.IntentReadWrite)
	}
	if c.PostgresDuckLakeIntent != "" && c.PostgresDuckLakeIntent != string(platformpostgres.IntentReadWrite) {
		return fmt.Errorf("production serve requires LEAPVIEW_POSTGRES_DUCKLAKE_INTENT=%q", platformpostgres.IntentReadWrite)
	}
	control := c.PostgresControlPlaneConfig()
	ducklake := c.PostgresDuckLakeRuntimeConfig()
	ducklakeMaintenance := c.PostgresDuckLakeMaintenanceConfig()
	if control.Migrator.RuntimeRole == control.Runtime.RuntimeRole {
		return errors.New("production control migrator and runtime roles must be distinct")
	}
	if postgresCredentialAlias(control.Migrator.URL, control.Runtime.URL) {
		return errors.New("production control migrator and runtime URLs must use distinct credentials")
	}
	if ducklake.RuntimeRole == control.Runtime.RuntimeRole || ducklake.RuntimeRole == control.Migrator.RuntimeRole || ducklake.RuntimeRole == control.Maintenance.RuntimeRole || ducklakeMaintenance.RuntimeRole == control.Runtime.RuntimeRole || ducklakeMaintenance.RuntimeRole == control.Migrator.RuntimeRole || ducklakeMaintenance.RuntimeRole == control.Maintenance.RuntimeRole {
		return errors.New("production DuckLake and control PostgreSQL roles must be distinct")
	}
	if postgresCredentialAlias(ducklake.URL, control.Runtime.URL) || postgresCredentialAlias(ducklake.URL, control.Migrator.URL) || postgresCredentialAlias(ducklake.URL, control.Maintenance.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, control.Runtime.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, control.Migrator.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, control.Maintenance.URL) {
		return errors.New("production DuckLake and control PostgreSQL URLs must use distinct database credentials")
	}
	if err := control.Migrator.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control migrator configuration: %w", err)
	}
	if err := control.Runtime.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control runtime configuration: %w", err)
	}
	if control.Maintenance.RuntimeRole == control.Runtime.RuntimeRole || control.Maintenance.RuntimeRole == control.Migrator.RuntimeRole {
		return errors.New("production control maintenance role must be distinct from runtime and migrator roles")
	}
	if control.Maintenance.RuntimeRole != postgresControlMaintenanceRole {
		return fmt.Errorf("production control maintenance role must be %q because the baseline grants only that fixed least-privilege role", postgresControlMaintenanceRole)
	}
	if postgresCredentialAlias(control.Maintenance.URL, control.Runtime.URL) || postgresCredentialAlias(control.Maintenance.URL, control.Migrator.URL) {
		return errors.New("production control maintenance URL must use distinct credentials")
	}
	if err := control.Maintenance.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control maintenance configuration: %w", err)
	}
	if control.Readonly != nil {
		if control.Readonly.RuntimeRole == control.Runtime.RuntimeRole || control.Readonly.RuntimeRole == control.Migrator.RuntimeRole || control.Readonly.RuntimeRole == control.Maintenance.RuntimeRole || control.Readonly.RuntimeRole == ducklake.RuntimeRole || control.Readonly.RuntimeRole == ducklakeMaintenance.RuntimeRole {
			return errors.New("production control readonly role must be distinct from runtime, migrator, maintenance, and DuckLake roles")
		}
		if postgresCredentialAlias(control.Readonly.URL, control.Runtime.URL) || postgresCredentialAlias(control.Readonly.URL, control.Migrator.URL) || postgresCredentialAlias(control.Readonly.URL, control.Maintenance.URL) || postgresCredentialAlias(control.Readonly.URL, ducklake.URL) || postgresCredentialAlias(control.Readonly.URL, ducklakeMaintenance.URL) {
			return errors.New("production control readonly URL must use distinct credentials")
		}
		if err := control.Readonly.Validate(); err != nil {
			return fmt.Errorf("invalid PostgreSQL control readonly configuration: %w", err)
		}
	}
	if err := ducklake.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake runtime configuration: %w", err)
	}
	if ducklakeMaintenance.RuntimeRole == ducklake.RuntimeRole || ducklakeMaintenance.RuntimeRole == strings.TrimSpace(c.PostgresDuckLakeMigratorRole) || ducklakeMaintenance.RuntimeRole == "leapview_ducklake_migrator" {
		return errors.New("production DuckLake maintenance role must be distinct from runtime and migrator roles")
	}
	if postgresCredentialAlias(ducklakeMaintenance.URL, ducklake.URL) || postgresCredentialAlias(ducklakeMaintenance.URL, c.PostgresDuckLakeMigratorURL) {
		return errors.New("production DuckLake maintenance URL must use distinct credentials")
	}
	if err := ducklakeMaintenance.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake maintenance configuration: %w", err)
	}
	physicalPoolID := strings.TrimSpace(c.DeliveryPhysicalPoolID)
	if physicalPoolID == "" || physicalPoolID != c.DeliveryPhysicalPoolID || len(physicalPoolID) > 255 {
		return errors.New("production serve requires a canonical LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID")
	}
	if err := platformdigest.ValidateSHA256Identity(c.DeliveryPhysicalPoolCompatibilityDigest); err != nil {
		return fmt.Errorf("production serve requires LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST: %w", err)
	}
	return nil
}
