package config

import (
	"strings"
	"testing"
	"time"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

func TestPostgresAuthorityMappingsRemainSeparated(t *testing.T) {
	cfg := Config{
		PostgresControlURL:                    "postgres://runtime/control?sslmode=require",
		PostgresControlMigratorURL:            "postgres://migrator/control?sslmode=require",
		PostgresControlRuntimeRole:            "leapview_control_runtime",
		PostgresControlMigratorRole:           "leapview_control_migrator",
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

func TestValidatePostgresRejectsFoundationOverrides(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"read-only control intent", func(cfg *Config) { cfg.PostgresControlIntent = "read-only" }, "LEAPVIEW_POSTGRES_CONTROL_INTENT"},
		{"control runtime role", func(cfg *Config) { cfg.PostgresControlRuntimeRole = "custom_runtime" }, "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE"},
		{"control migrator role", func(cfg *Config) { cfg.PostgresControlMigratorRole = "custom_migrator" }, "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE"},
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

func TestValidatePostgresControlInitializationFailsClosed(t *testing.T) {
	valid := Config{
		PostgresControlURL:         "postgres://runtime:runtime-secret@db.example/control?sslmode=require",
		PostgresControlMigratorURL: "postgres://migrator:migrator-secret@db.example/control?sslmode=require",
		PostgresControlRuntimeRole: "leapview_control_runtime", PostgresControlMigratorRole: "leapview_control_migrator",
		PostgresControlIntent: "read-write", PostgresRequireTLS: true, PostgresExpectedMajor: 18,
		PostgresControlPoolMinConns: 1, PostgresControlPoolMaxConns: 8,
	}
	if err := valid.ValidatePostgresControlInitialization(); err != nil {
		t.Fatalf("valid control initialization config: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing runtime", func(cfg *Config) { cfg.PostgresControlURL = "" }},
		{"missing migrator", func(cfg *Config) { cfg.PostgresControlMigratorURL = "" }},
		{"shared credential", func(cfg *Config) {
			cfg.PostgresControlMigratorURL = "postgres://runtime:runtime-secret@db.example:5432/control?application_name=migrator&sslmode=require"
		}},
		{"TLS disabled", func(cfg *Config) { cfg.PostgresRequireTLS = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			if err := cfg.ValidatePostgresControlInitialization(); err == nil {
				t.Fatal("invalid control initialization config was accepted")
			}
		})
	}
}
