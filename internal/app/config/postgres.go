package config

import (
	"strings"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// PostgresControlRuntimeConfig maps the ordinary access/control authority.
// It never contains the owner-capable migration credential.
func (c Config) PostgresControlRuntimeConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlRuntimeRole)
	if role == "" {
		role = "leapview_control_runtime"
	}
	return platformpostgres.Config{
		URL:                    c.PostgresControlURL,
		ExpectedMajor:          c.PostgresExpectedMajor,
		RuntimeRole:            role,
		Intent:                 platformpostgres.IntentReadWrite,
		RequireTLS:             c.PostgresRequireTLS,
		MinConns:               int32(c.PostgresControlPoolMinConns),
		MaxConns:               int32(c.PostgresControlPoolMaxConns),
		AcquireTimeout:         c.PostgresControlAcquireTimeout,
		StatementTimeout:       c.PostgresControlStatementTimeout,
		LockTimeout:            c.PostgresControlLockTimeout,
		IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
}

// PostgresControlMigratorConfig is a bounded one-connection authority used
// only to apply and verify capability-owned schema before runtime opens.
func (c Config) PostgresControlMigratorConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlMigratorRole)
	if role == "" {
		role = "leapview_control_migrator"
	}
	return platformpostgres.Config{
		URL:                    c.PostgresControlMigratorURL,
		ExpectedMajor:          c.PostgresExpectedMajor,
		RuntimeRole:            role,
		Intent:                 platformpostgres.IntentReadWrite,
		RequireTLS:             c.PostgresRequireTLS,
		MinConns:               1,
		MaxConns:               1,
		AcquireTimeout:         c.PostgresControlAcquireTimeout,
		StatementTimeout:       c.PostgresControlStatementTimeout,
		LockTimeout:            c.PostgresControlLockTimeout,
		IdleTransactionTimeout: c.PostgresControlIdleTransactionTimeout,
	}
}

// PostgresDuckLakeMigratorConfig is the one-connection, owner-capable catalog
// initializer. It is intentionally absent from ordinary runtime config.
func (c Config) PostgresDuckLakeMigratorConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresDuckLakeMigratorRole)
	if role == "" {
		role = "leapview_ducklake_migrator"
	}
	return platformpostgres.Config{
		URL:                    c.PostgresDuckLakeMigratorURL,
		ExpectedMajor:          c.PostgresExpectedMajor,
		RuntimeRole:            role,
		Intent:                 platformpostgres.IntentReadWrite,
		RequireTLS:             c.PostgresRequireTLS,
		MinConns:               1,
		MaxConns:               1,
		AcquireTimeout:         c.PostgresDuckLakeAcquireTimeout,
		StatementTimeout:       c.PostgresDuckLakeStatementTimeout,
		LockTimeout:            c.PostgresDuckLakeLockTimeout,
		IdleTransactionTimeout: c.PostgresDuckLakeIdleTransactionTimeout,
	}
}

func (c Config) PostgresDuckLakeRuntimeRoleName() string {
	if role := strings.TrimSpace(c.PostgresDuckLakeRuntimeRole); role != "" {
		return role
	}
	return "leapview_ducklake_runtime"
}

func (c Config) PostgresDuckLakeMaintenanceRoleName() string {
	if role := strings.TrimSpace(c.PostgresDuckLakeMaintenanceRole); role != "" {
		return role
	}
	return "leapview_ducklake_maintenance"
}
