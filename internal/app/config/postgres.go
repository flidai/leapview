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
