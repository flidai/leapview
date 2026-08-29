package config

import (
	"strings"
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

func TestPostgresControlPlaneConfigUsesIndependentLeastPrivilegeRoles(t *testing.T) {
	cfg := Config{
		PostgresControlURL:          "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresControlMigratorURL:  "postgres://migrator:secret@db/control?sslmode=verify-full",
		PostgresControlReadonlyURL:  "postgres://readonly:secret@db/control?sslmode=verify-full",
		PostgresControlRuntimeRole:  "runtime_role",
		PostgresControlMigratorRole: "migrator_role",
		PostgresControlReadonlyRole: "readonly_role",
		PostgresExpectedMajor:       18,
		PostgresRequireTLS:          true,
		PostgresControlPoolMinConns: 2,
		PostgresControlPoolMaxConns: 8,
	}
	got := cfg.PostgresControlPlaneConfig()
	require.Equal(t, "migrator_role", got.Migrator.RuntimeRole)
	require.Equal(t, platformpostgres.IntentReadWrite, got.Migrator.Intent)
	require.Equal(t, "runtime_role", got.Runtime.RuntimeRole)
	require.Equal(t, platformpostgres.IntentReadWrite, got.Runtime.Intent)
	if got.Readonly == nil {
		t.Fatal("readonly pool was omitted despite explicit URL")
	}
	require.Equal(t, "readonly_role", got.Readonly.RuntimeRole)
	require.Equal(t, platformpostgres.IntentReadOnly, got.Readonly.Intent)
}

func TestPostgresControlPlaneConfigAppliesReviewedRoleDefaults(t *testing.T) {
	cfg := Config{
		PostgresControlURL:         "postgres://runtime:secret@db/control?sslmode=require",
		PostgresControlMigratorURL: "postgres://migrator:secret@db/control?sslmode=require",
		PostgresRequireTLS:         true,
	}
	got := cfg.PostgresControlPlaneConfig()
	require.Equal(t, "leapview_control_migrator", got.Migrator.RuntimeRole)
	require.Equal(t, "leapview_control_runtime", got.Runtime.RuntimeRole)
}

func TestValidatePostgresProductionFailsClosedWithoutSeparateMigrator(t *testing.T) {
	cfg := Config{Production: true, PostgresRequireTLS: true, PostgresControlURL: "postgres://runtime:secret@db/control?sslmode=require"}
	if err := cfg.ValidatePostgresProduction(); err == nil {
		t.Fatal("production PostgreSQL validation accepted a missing migrator URL")
	}
}

func TestValidatePostgresProductionRejectsPlaintextAndReadOnlyRuntime(t *testing.T) {
	base := Config{
		Production:                  true,
		PostgresControlURL:          "postgres://runtime:secret@db/control?sslmode=disable",
		PostgresControlMigratorURL:  "postgres://migrator:secret@db/control?sslmode=disable",
		PostgresDuckLakeURL:         "postgres://ducklake:secret@db/ducklake?sslmode=disable",
		PostgresControlRuntimeRole:  "runtime_role",
		PostgresControlMigratorRole: "migrator_role",
		PostgresDuckLakeRuntimeRole: "ducklake_role",
		PostgresControlIntent:       "read-write",
		PostgresDuckLakeIntent:      "read-write",
	}
	if err := base.ValidatePostgresProduction(); err == nil {
		t.Fatal("production PostgreSQL validation accepted plaintext URLs")
	}
	base.PostgresRequireTLS = true
	base.PostgresControlURL = "postgres://runtime:secret@db/control?sslmode=require"
	base.PostgresControlMigratorURL = "postgres://migrator:secret@db/control?sslmode=require"
	base.PostgresDuckLakeURL = "postgres://ducklake:secret@db/ducklake?sslmode=require"
	base.PostgresControlIntent = "read-only"
	if err := base.ValidatePostgresProduction(); err == nil {
		t.Fatal("production PostgreSQL validation accepted read-only runtime intent")
	}
}

func TestValidatePostgresProductionRequiresTwoDatabasesAndDistinctRoles(t *testing.T) {
	base := Config{
		Production: true, PostgresRequireTLS: true,
		PostgresControlURL:         "postgres://runtime:secret@db/control?sslmode=require",
		PostgresControlMigratorURL: "postgres://migrator:secret@db/control?sslmode=require",
		PostgresControlRuntimeRole: "runtime_role", PostgresControlMigratorRole: "migrator_role",
	}
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_DUCKLAKE_URL") {
		t.Fatalf("missing DuckLake database error = %v", err)
	}
	base.PostgresDuckLakeURL = "postgres://ducklake:secret@db/ducklake?sslmode=require"
	base.PostgresDuckLakeRuntimeRole = "runtime_role"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "roles must be distinct") {
		t.Fatalf("shared control/DuckLake role error = %v", err)
	}
	base.PostgresDuckLakeRuntimeRole = "ducklake_role"
	base.PostgresControlMigratorRole = "runtime_role"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "migrator and runtime roles must be distinct") {
		t.Fatalf("shared control role error = %v", err)
	}
}
