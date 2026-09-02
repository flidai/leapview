package config

import (
	"strings"
	"testing"
	"time"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

func TestPostgresControlAuthoritiesRemainSeparated(t *testing.T) {
	cfg := Config{
		PostgresControlURL:                    "postgres://runtime/control?sslmode=require",
		PostgresControlMigratorURL:            "postgres://migrator/control?sslmode=require",
		PostgresDuckLakeMigratorURL:           "postgres://catalog-migrator/ducklake?sslmode=require",
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
	catalogMigrator := cfg.PostgresDuckLakeMigratorConfig()
	if runtime.URL == migrator.URL || runtime.RuntimeRole == migrator.RuntimeRole {
		t.Fatalf("PostgreSQL authorities aliased: runtime=%#v migrator=%#v", runtime, migrator)
	}
	if runtime.Intent != platformpostgres.IntentReadWrite || migrator.MinConns != 1 || migrator.MaxConns != 1 {
		t.Fatalf("unexpected authority policy: runtime=%#v migrator=%#v", runtime, migrator)
	}
	if catalogMigrator.URL == migrator.URL || catalogMigrator.RuntimeRole == migrator.RuntimeRole || catalogMigrator.MinConns != 1 || catalogMigrator.MaxConns != 1 {
		t.Fatalf("DuckLake migrator authority aliased: control=%#v catalog=%#v", migrator, catalogMigrator)
	}
}

func TestPostgresControlRuntimeConfigMapsIntentAndDefaults(t *testing.T) {
	cfg := Config{PostgresControlIntent: "read-write"}
	if got := cfg.PostgresControlRuntimeConfig().Intent; got != platformpostgres.IntentReadWrite {
		t.Fatalf("read-write control intent = %q, want %q", got, platformpostgres.IntentReadWrite)
	}
	if got := (Config{}).PostgresControlRuntimeConfig().Intent; got != platformpostgres.IntentReadWrite {
		t.Fatalf("unset control intent = %q, want %q", got, platformpostgres.IntentReadWrite)
	}
	if got := (Config{PostgresControlIntent: " read-write "}).PostgresControlRuntimeConfig().Intent; got != platformpostgres.IntentReadWrite {
		t.Fatalf("trimmed control intent = %q, want %q", got, platformpostgres.IntentReadWrite)
	}
}

func TestValidatePostgresRejectsUnsupportedIntentAndRoleOverrides(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "read-only control intent", mutate: func(cfg *Config) { cfg.PostgresControlIntent = "read-only" }, want: "LEAPVIEW_POSTGRES_CONTROL_INTENT"},
		{name: "control runtime role", mutate: func(cfg *Config) { cfg.PostgresControlRuntimeRole = "custom_runtime" }, want: "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE"},
		{name: "control migrator role", mutate: func(cfg *Config) { cfg.PostgresControlMigratorRole = "custom_migrator" }, want: "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE"},
		{name: "DuckLake migrator role", mutate: func(cfg *Config) { cfg.PostgresDuckLakeMigratorRole = "custom_migrator" }, want: "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE"},
		{name: "DuckLake runtime role", mutate: func(cfg *Config) { cfg.PostgresDuckLakeRuntimeRole = "custom_runtime" }, want: "LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE"},
		{name: "DuckLake maintenance role", mutate: func(cfg *Config) { cfg.PostgresDuckLakeMaintenanceRole = "custom_maintenance" }, want: "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{}
			test.mutate(&cfg)
			if err := cfg.ValidatePostgres(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidatePostgres() error = %v, want %s rejection", err, test.want)
			}
		})
	}
}
