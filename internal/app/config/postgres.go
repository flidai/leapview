package config

import (
	"errors"
	"fmt"
	"strings"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

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
// the capability-neutral pool policy.  The readonly pool is omitted when its
// URL is empty; it is never synthesized from the runtime credentials.
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

// PostgresControlMaintenanceConfig describes the separately authenticated
// bounded maintenance pool. It is intentionally not part of the serving
// runtime pool set; callers open it only around an explicit maintenance
// operation and close it immediately afterwards.
func (c Config) PostgresControlMaintenanceConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlMaintenanceRole)
	if role == "" {
		role = "leapview_control_maintenance"
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
	if strings.TrimSpace(c.PostgresControlURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_CONTROL_URL")
	}
	if strings.TrimSpace(c.PostgresControlMigratorURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	}
	if strings.TrimSpace(c.PostgresDuckLakeURL) == "" {
		return errors.New("production serve requires LEAPVIEW_POSTGRES_DUCKLAKE_URL")
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
	if control.Migrator.RuntimeRole == control.Runtime.RuntimeRole {
		return errors.New("production control migrator and runtime roles must be distinct")
	}
	if strings.TrimSpace(control.Migrator.URL) == strings.TrimSpace(control.Runtime.URL) {
		return errors.New("production control migrator and runtime URLs must use distinct credentials")
	}
	if ducklake.RuntimeRole == control.Runtime.RuntimeRole || ducklake.RuntimeRole == control.Migrator.RuntimeRole {
		return errors.New("production DuckLake and control PostgreSQL roles must be distinct")
	}
	if strings.TrimSpace(ducklake.URL) == strings.TrimSpace(control.Runtime.URL) || strings.TrimSpace(ducklake.URL) == strings.TrimSpace(control.Migrator.URL) {
		return errors.New("production DuckLake and control PostgreSQL URLs must use distinct database credentials")
	}
	if err := control.Migrator.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control migrator configuration: %w", err)
	}
	if err := control.Runtime.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL control runtime configuration: %w", err)
	}
	if control.Readonly != nil {
		if control.Readonly.RuntimeRole == control.Runtime.RuntimeRole || control.Readonly.RuntimeRole == control.Migrator.RuntimeRole || control.Readonly.RuntimeRole == ducklake.RuntimeRole {
			return errors.New("production control readonly role must be distinct from runtime, migrator, and DuckLake roles")
		}
		if strings.TrimSpace(control.Readonly.URL) == strings.TrimSpace(control.Runtime.URL) || strings.TrimSpace(control.Readonly.URL) == strings.TrimSpace(control.Migrator.URL) || strings.TrimSpace(control.Readonly.URL) == strings.TrimSpace(ducklake.URL) {
			return errors.New("production control readonly URL must use distinct credentials")
		}
		if err := control.Readonly.Validate(); err != nil {
			return fmt.Errorf("invalid PostgreSQL control readonly configuration: %w", err)
		}
	}
	if err := ducklake.Validate(); err != nil {
		return fmt.Errorf("invalid PostgreSQL DuckLake runtime configuration: %w", err)
	}
	return nil
}
