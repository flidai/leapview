package config

import (
	"testing"
	"time"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

func TestPostgresControlAuthoritiesRemainSeparated(t *testing.T) {
	cfg := Config{
		PostgresControlURL:                    "postgres://runtime/control?sslmode=require",
		PostgresControlMigratorURL:            "postgres://migrator/control?sslmode=require",
		PostgresExpectedMajor:                 18,
		PostgresRequireTLS:                    true,
		PostgresControlPoolMinConns:           1,
		PostgresControlPoolMaxConns:           8,
		PostgresControlAcquireTimeout:         time.Second,
		PostgresControlStatementTimeout:       time.Second,
		PostgresControlLockTimeout:            time.Second,
		PostgresControlIdleTransactionTimeout: time.Second,
	}
	runtime := cfg.PostgresControlRuntimeConfig()
	migrator := cfg.PostgresControlMigratorConfig()
	if runtime.URL == migrator.URL || runtime.RuntimeRole == migrator.RuntimeRole {
		t.Fatalf("PostgreSQL authorities aliased: runtime=%#v migrator=%#v", runtime, migrator)
	}
	if runtime.Intent != platformpostgres.IntentReadWrite || migrator.MinConns != 1 || migrator.MaxConns != 1 {
		t.Fatalf("unexpected authority policy: runtime=%#v migrator=%#v", runtime, migrator)
	}
}
