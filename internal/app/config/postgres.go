package config

import (
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
