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
