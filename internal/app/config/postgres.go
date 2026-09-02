package config

import (
	"fmt"
	"strings"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

const (
	postgresControlMigratorRole     = "leapview_control_migrator"
	postgresControlRuntimeRole      = "leapview_control_runtime"
	postgresDuckLakeMigratorRole    = "leapview_ducklake_migrator"
	postgresDuckLakeRuntimeRole     = "leapview_ducklake_runtime"
	postgresDuckLakeMaintenanceRole = "leapview_ducklake_maintenance"
)

// ValidatePostgres checks the application-owned PostgreSQL configuration
// against the canonical foundation role policy. The migrations and baseline
// ACLs name these roles directly, so accepting aliases would only defer a
// configuration error until pool startup or schema preparation.
func (c Config) ValidatePostgres() error {
	roles := []struct {
		name, configured, canonical string
	}{
		{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE", c.PostgresControlMigratorRole, postgresControlMigratorRole},
		{"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE", c.PostgresControlRuntimeRole, postgresControlRuntimeRole},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE", c.PostgresDuckLakeMigratorRole, postgresDuckLakeMigratorRole},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE", c.PostgresDuckLakeRuntimeRole, postgresDuckLakeRuntimeRole},
		{"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE", c.PostgresDuckLakeMaintenanceRole, postgresDuckLakeMaintenanceRole},
	}
	for _, role := range roles {
		if configured := strings.TrimSpace(role.configured); configured != "" && configured != role.canonical {
			return fmt.Errorf("%s must be %q; custom PostgreSQL role names are not supported by the foundation", role.name, role.canonical)
		}
	}
	if intent := strings.TrimSpace(c.PostgresControlIntent); intent != "" && intent != string(platformpostgres.IntentReadWrite) {
		return fmt.Errorf("LEAPVIEW_POSTGRES_CONTROL_INTENT must be %q; the foundation does not support read-only control access", platformpostgres.IntentReadWrite)
	}
	return nil
}

// PostgresControlRuntimeConfig maps the ordinary access/control authority.
// It never contains the owner-capable migration credential.
func (c Config) PostgresControlRuntimeConfig() platformpostgres.Config {
	role := strings.TrimSpace(c.PostgresControlRuntimeRole)
	if role == "" {
		role = postgresControlRuntimeRole
	}
	return platformpostgres.Config{
		URL:                    c.PostgresControlURL,
		ExpectedMajor:          c.PostgresExpectedMajor,
		RuntimeRole:            role,
		Intent:                 configuredPostgresIntent(c.PostgresControlIntent),
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
		role = postgresControlMigratorRole
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
		role = postgresDuckLakeMigratorRole
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
	return postgresDuckLakeRuntimeRole
}

func (c Config) PostgresDuckLakeMaintenanceRoleName() string {
	if role := strings.TrimSpace(c.PostgresDuckLakeMaintenanceRole); role != "" {
		return role
	}
	return postgresDuckLakeMaintenanceRole
}

func configuredPostgresIntent(raw string) platformpostgres.Intent {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return platformpostgres.IntentReadWrite
	}
	return platformpostgres.Intent(raw)
}
