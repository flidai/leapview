package config

import (
	"testing"
	"time"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/stretchr/testify/require"
)

func TestPostgresRuntimeConfigMapsIndependentDatabasePolicies(t *testing.T) {
	cfg := Config{
		PostgresControlURL:                     "postgres://control-runtime:control-secret@localhost/leapview_control?sslmode=require",
		PostgresDuckLakeURL:                    "postgres://ducklake-runtime:ducklake-secret@localhost/leapview_ducklake?sslmode=require",
		PostgresExpectedMajor:                  18,
		PostgresControlRuntimeRole:             "leapview_control_runtime",
		PostgresDuckLakeRuntimeRole:            "leapview_ducklake_runtime",
		PostgresControlIntent:                  "read-write",
		PostgresDuckLakeIntent:                 "read-only",
		PostgresRequireTLS:                     true,
		PostgresControlPoolMinConns:            2,
		PostgresControlPoolMaxConns:            8,
		PostgresDuckLakePoolMinConns:           1,
		PostgresDuckLakePoolMaxConns:           4,
		PostgresControlAcquireTimeout:          11 * time.Second,
		PostgresControlStatementTimeout:        12 * time.Second,
		PostgresControlLockTimeout:             13 * time.Second,
		PostgresControlIdleTransactionTimeout:  14 * time.Second,
		PostgresDuckLakeAcquireTimeout:         21 * time.Second,
		PostgresDuckLakeStatementTimeout:       22 * time.Second,
		PostgresDuckLakeLockTimeout:            23 * time.Second,
		PostgresDuckLakeIdleTransactionTimeout: 24 * time.Second,
	}

	got := cfg.PostgresRuntimeConfig()
	require.Equal(t, platformpostgres.RuntimeConfig{
		Control: platformpostgres.Config{
			URL:                    cfg.PostgresControlURL,
			ExpectedMajor:          18,
			RuntimeRole:            "leapview_control_runtime",
			Intent:                 platformpostgres.IntentReadWrite,
			RequireTLS:             true,
			MinConns:               2,
			MaxConns:               8,
			AcquireTimeout:         11 * time.Second,
			StatementTimeout:       12 * time.Second,
			LockTimeout:            13 * time.Second,
			IdleTransactionTimeout: 14 * time.Second,
		},
		DuckLake: platformpostgres.Config{
			URL:                    cfg.PostgresDuckLakeURL,
			ExpectedMajor:          18,
			RuntimeRole:            "leapview_ducklake_runtime",
			Intent:                 platformpostgres.IntentReadOnly,
			RequireTLS:             true,
			MinConns:               1,
			MaxConns:               4,
			AcquireTimeout:         21 * time.Second,
			StatementTimeout:       22 * time.Second,
			LockTimeout:            23 * time.Second,
			IdleTransactionTimeout: 24 * time.Second,
		},
	}, got)
}
