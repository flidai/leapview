package config

import (
	"strings"
	"testing"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/stretchr/testify/require"
)

func TestPostgresControlPlaneConfigUsesIndependentLeastPrivilegeRoles(t *testing.T) {
	cfg := Config{
		PostgresControlURL:                   "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresControlMigratorURL:           "postgres://migrator:secret@db/control?sslmode=verify-full",
		PostgresControlMaintenanceURL:        "postgres://maintenance:secret@db/control?sslmode=verify-full",
		PostgresControlUpgradeCoordinatorURL: "postgres://coordinator:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeMigratorURL:          "postgres://catalog-migrator:secret@db/ducklake?sslmode=verify-full",
		PostgresDuckLakeMaintenanceURL:       "postgres://catalog-maintenance:secret@db/ducklake?sslmode=verify-full",
		PostgresControlReadonlyURL:           "postgres://readonly:secret@db/control?sslmode=verify-full",
		PostgresControlRuntimeRole:           "runtime_role",
		PostgresControlMigratorRole:          "migrator_role",
		PostgresControlReadonlyRole:          "readonly_role",
		PostgresControlMaintenanceRole:       "maintenance_role",
		PostgresDuckLakeMaintenanceRole:      "catalog_maintenance_role",
		PostgresExpectedMajor:                18,
		PostgresRequireTLS:                   true,
		PostgresControlPoolMinConns:          2,
		PostgresControlPoolMaxConns:          8,
	}
	got := cfg.PostgresControlPlaneConfig()
	require.Equal(t, "migrator_role", got.Migrator.RuntimeRole)
	require.Equal(t, platformpostgres.IntentReadWrite, got.Migrator.Intent)
	require.Equal(t, "runtime_role", got.Runtime.RuntimeRole)
	require.Equal(t, "maintenance_role", got.Maintenance.RuntimeRole)
	require.Equal(t, platformpostgres.IntentReadWrite, got.Maintenance.Intent)
	require.EqualValues(t, 1, got.Maintenance.MinConns)
	require.EqualValues(t, 1, got.Maintenance.MaxConns)
	require.Equal(t, platformpostgres.IntentReadWrite, got.Runtime.Intent)
	if got.Readonly == nil {
		t.Fatal("readonly pool was omitted despite explicit URL")
	}
	require.Equal(t, "readonly_role", got.Readonly.RuntimeRole)
	require.Equal(t, platformpostgres.IntentReadOnly, got.Readonly.Intent)
	if got.Migrator.URL == cfg.PostgresControlUpgradeCoordinatorURL || got.Runtime.URL == cfg.PostgresDuckLakeMigratorURL {
		t.Fatal("serving control-plane config opened upgrade credentials")
	}
}

func TestPostgresControlPlaneConfigAppliesReviewedRoleDefaults(t *testing.T) {
	cfg := Config{
		PostgresControlURL:             "postgres://runtime:secret@db/control?sslmode=require",
		PostgresControlMigratorURL:     "postgres://migrator:secret@db/control?sslmode=require",
		PostgresControlMaintenanceURL:  "postgres://maintenance:secret@db/control?sslmode=require",
		PostgresDuckLakeMaintenanceURL: "postgres://catalog-maintenance:secret@db/ducklake?sslmode=require",
		PostgresRequireTLS:             true,
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

func TestValidatePostgresProductionFailsClosedWithoutMaintenance(t *testing.T) {
	cfg := Config{Production: true, PostgresRequireTLS: true,
		PostgresControlURL:         "postgres://runtime:secret@db/control?sslmode=require",
		PostgresControlMigratorURL: "postgres://migrator:secret@db/control?sslmode=require",
		PostgresDuckLakeURL:        "postgres://ducklake:secret@db/ducklake?sslmode=require"}
	if err := cfg.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL") {
		t.Fatalf("missing maintenance URL error = %v", err)
	}
}

func validDevelopmentPostgresConfig() Config {
	return Config{
		PostgresExpectedMajor:                   18,
		PostgresRequireTLS:                      false,
		PostgresControlURL:                      "postgres://runtime:secret@127.0.0.1:55432/control?sslmode=disable",
		PostgresControlMigratorURL:              "postgres://migrator:secret@127.0.0.1:55432/control?sslmode=disable",
		PostgresControlMaintenanceURL:           "postgres://maintenance:secret@127.0.0.1:55432/control?sslmode=disable",
		PostgresControlReadonlyURL:              "postgres://readonly:secret@127.0.0.1:55432/control?sslmode=disable",
		PostgresDuckLakeURL:                     "postgres://ducklake:secret@127.0.0.1:55432/ducklake?sslmode=disable",
		PostgresDuckLakeMaintenanceURL:          "postgres://catalog-maintenance:secret@127.0.0.1:55432/ducklake?sslmode=disable",
		PostgresControlRuntimeRole:              postgresControlRuntimeRole,
		PostgresControlMigratorRole:             postgresControlMigratorRole,
		PostgresControlReadonlyRole:             postgresControlReadonlyRole,
		PostgresControlMaintenanceRole:          postgresControlMaintenanceRole,
		PostgresDuckLakeRuntimeRole:             postgresDuckLakeRuntimeRole,
		PostgresDuckLakeMaintenanceRole:         postgresDuckLakeMaintenanceRole,
		DeliveryPhysicalPoolID:                  "",
		DeliveryPhysicalPoolCompatibilityDigest: "",
	}
}

func TestValidatePostgresDevelopmentAllowsLoopbackPlaintextWithoutPoolAdmission(t *testing.T) {
	cfg := validDevelopmentPostgresConfig()
	if err := cfg.ValidatePostgresDevelopment(); err != nil {
		t.Fatalf("valid loopback development PostgreSQL config rejected: %v", err)
	}
}

func TestValidatePostgresDevelopmentRejectsRemotePlaintext(t *testing.T) {
	cfg := validDevelopmentPostgresConfig()
	cfg.PostgresDuckLakeURL = "postgres://ducklake:secret@db.internal:5432/ducklake?sslmode=disable"
	if err := cfg.ValidatePostgresDevelopment(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("remote plaintext development URL accepted: %v", err)
	}
}

func TestValidatePostgresProductionRejectsPlaintextAndRequiresDelivery(t *testing.T) {
	base := Config{
		Production:                     true,
		PostgresControlURL:             "postgres://runtime:secret@db/control?sslmode=disable",
		PostgresControlMigratorURL:     "postgres://migrator:secret@db/control?sslmode=disable",
		PostgresControlMaintenanceURL:  "postgres://maintenance:secret@db/control?sslmode=disable",
		PostgresDuckLakeURL:            "postgres://ducklake:secret@db/ducklake?sslmode=disable",
		PostgresDuckLakeMaintenanceURL: "postgres://catalog-maintenance:secret@db/ducklake?sslmode=disable",
		PostgresControlRuntimeRole:     postgresControlRuntimeRole,
		PostgresControlMigratorRole:    postgresControlMigratorRole,
		PostgresDuckLakeRuntimeRole:    postgresDuckLakeRuntimeRole,
	}
	if err := base.ValidatePostgresProduction(); err == nil {
		t.Fatal("production PostgreSQL validation accepted plaintext URLs")
	}
	base.PostgresRequireTLS = true
	base.PostgresControlURL = "postgres://runtime:secret@db/control?sslmode=verify-full"
	base.PostgresControlMigratorURL = "postgres://migrator:secret@db/control?sslmode=verify-full"
	base.PostgresControlMaintenanceURL = "postgres://maintenance:secret@db/control?sslmode=verify-full"
	base.PostgresDuckLakeURL = "postgres://ducklake:secret@db/ducklake?sslmode=verify-full"
	base.PostgresDuckLakeMaintenanceURL = "postgres://catalog-maintenance:secret@db/ducklake?sslmode=verify-full"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID") {
		t.Fatalf("production PostgreSQL validation accepted incomplete deployment: %v", err)
	}
}

func TestValidatePostgresProductionRequiresCertificateAndHostnameVerification(t *testing.T) {
	base := Config{
		Production:                              true,
		PostgresRequireTLS:                      true,
		PostgresControlURL:                      "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresControlMigratorURL:              "postgres://migrator:secret@db/control?sslmode=verify-full",
		PostgresControlMaintenanceURL:           "postgres://maintenance:secret@db/control?sslmode=verify-full",
		PostgresControlReadonlyURL:              "postgres://readonly:secret@db/control?sslmode=verify-full",
		PostgresControlUpgradeCoordinatorURL:    "postgres://coordinator:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeURL:                     "postgres://ducklake:secret@db/ducklake?sslmode=verify-full",
		PostgresDuckLakeMigratorURL:             "postgres://catalog-migrator:secret@db/ducklake?sslmode=verify-full",
		PostgresDuckLakeMaintenanceURL:          "postgres://catalog-maintenance:secret@db/ducklake?sslmode=verify-full",
		PostgresControlRuntimeRole:              postgresControlRuntimeRole,
		PostgresControlMigratorRole:             postgresControlMigratorRole,
		PostgresControlMaintenanceRole:          postgresControlMaintenanceRole,
		PostgresDuckLakeRuntimeRole:             postgresDuckLakeRuntimeRole,
		DeliveryPhysicalPoolID:                  "pool-prod",
		DeliveryPhysicalPoolCompatibilityDigest: "sha256:" + strings.Repeat("a", 64),
	}
	for _, sslmode := range []string{"require", "verify-ca"} {
		for _, test := range []struct {
			name string
			set  func(*Config, string)
		}{
			{name: "control runtime", set: func(cfg *Config, mode string) {
				cfg.PostgresControlURL = "postgres://runtime:secret@db/control?sslmode=" + mode
			}},
			{name: "control migrator", set: func(cfg *Config, mode string) {
				cfg.PostgresControlMigratorURL = "postgres://migrator:secret@db/control?sslmode=" + mode
			}},
			{name: "control upgrade coordinator", set: func(cfg *Config, mode string) {
				cfg.PostgresControlUpgradeCoordinatorURL = "postgres://coordinator:secret@db/control?sslmode=" + mode
			}},
			{name: "control maintenance", set: func(cfg *Config, mode string) {
				cfg.PostgresControlMaintenanceURL = "postgres://maintenance:secret@db/control?sslmode=" + mode
			}},
			{name: "control readonly", set: func(cfg *Config, mode string) {
				cfg.PostgresControlReadonlyURL = "postgres://readonly:secret@db/control?sslmode=" + mode
			}},
			{name: "DuckLake runtime", set: func(cfg *Config, mode string) {
				cfg.PostgresDuckLakeURL = "postgres://ducklake:secret@db/ducklake?sslmode=" + mode
			}},
			{name: "DuckLake migrator", set: func(cfg *Config, mode string) {
				cfg.PostgresDuckLakeMigratorURL = "postgres://catalog-migrator:secret@db/ducklake?sslmode=" + mode
			}},
			{name: "DuckLake maintenance", set: func(cfg *Config, mode string) {
				cfg.PostgresDuckLakeMaintenanceURL = "postgres://catalog-maintenance:secret@db/ducklake?sslmode=" + mode
			}},
		} {
			t.Run(sslmode+"/"+test.name, func(t *testing.T) {
				cfg := base
				test.set(&cfg, sslmode)
				if err := cfg.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "sslmode=verify-full") {
					t.Fatalf("production sslmode=%s accepted for %s: %v", sslmode, test.name, err)
				}
			})
		}
	}
}

func TestValidatePostgresProductionRequiresTwoDatabasesAndDistinctRoles(t *testing.T) {
	base := Config{
		Production: true, PostgresRequireTLS: true,
		PostgresControlURL:            "postgres://runtime:secret@db/control?sslmode=require",
		PostgresControlMigratorURL:    "postgres://migrator:secret@db/control?sslmode=require",
		PostgresControlMaintenanceURL: "postgres://maintenance:secret@db/control?sslmode=require",
	}
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_DUCKLAKE_URL") {
		t.Fatalf("missing DuckLake database error = %v", err)
	}
	base.PostgresDuckLakeURL = "postgres://ducklake:secret@db/ducklake?sslmode=require"
	base.PostgresDuckLakeMaintenanceURL = "postgres://catalog-maintenance:secret@db/ducklake?sslmode=require"
	base.PostgresDuckLakeRuntimeRole = "runtime_role"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "only provisioned PostgreSQL roles") {
		t.Fatalf("unprovisioned DuckLake role accepted: %v", err)
	}
	base.PostgresDuckLakeRuntimeRole = postgresDuckLakeRuntimeRole
	base.PostgresControlMigratorRole = "runtime_role"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "only provisioned PostgreSQL roles") {
		t.Fatalf("unprovisioned control role accepted: %v", err)
	}
}

func TestValidatePostgresProductionRequiresDistinctMaintenanceCredentials(t *testing.T) {
	base := Config{
		Production:                              true,
		PostgresRequireTLS:                      true,
		PostgresControlURL:                      "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresControlMigratorURL:              "postgres://migrator:secret@db/control?sslmode=verify-full",
		PostgresControlMaintenanceURL:           "postgres://maintenance:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeURL:                     "postgres://ducklake:secret@db/ducklake?sslmode=verify-full",
		PostgresDuckLakeMaintenanceURL:          "postgres://catalog-maintenance:secret@db/ducklake?sslmode=verify-full",
		PostgresControlRuntimeRole:              postgresControlRuntimeRole,
		PostgresControlMigratorRole:             postgresControlMigratorRole,
		PostgresControlMaintenanceRole:          postgresControlMaintenanceRole,
		PostgresDuckLakeRuntimeRole:             postgresDuckLakeRuntimeRole,
		DeliveryPhysicalPoolID:                  "pool-prod",
		DeliveryPhysicalPoolCompatibilityDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := base.ValidatePostgresProduction(); err != nil {
		t.Fatalf("valid maintenance credentials rejected: %v", err)
	}
	base.PostgresControlMaintenanceRole = "provider_maintenance"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "only provisioned PostgreSQL roles") {
		t.Fatalf("unsupported custom maintenance role accepted: %v", err)
	}
	base.PostgresControlMaintenanceRole = base.PostgresControlRuntimeRole
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "only provisioned PostgreSQL roles") {
		t.Fatalf("maintenance/runtime role reuse accepted: %v", err)
	}
	base.PostgresControlMaintenanceRole = postgresControlMaintenanceRole
	base.PostgresControlMaintenanceURL = base.PostgresControlURL
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "maintenance URL") {
		t.Fatalf("maintenance/runtime URL reuse accepted: %v", err)
	}
}

func TestValidatePostgresProductionRequiresDeliveryPoolContract(t *testing.T) {
	base := Config{
		Production:                              true,
		PostgresRequireTLS:                      true,
		PostgresControlURL:                      "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresControlMigratorURL:              "postgres://migrator:secret@db/control?sslmode=verify-full",
		PostgresControlMaintenanceURL:           "postgres://maintenance:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeURL:                     "postgres://ducklake:secret@db/ducklake?sslmode=verify-full",
		PostgresDuckLakeMaintenanceURL:          "postgres://catalog-maintenance:secret@db/ducklake?sslmode=verify-full",
		PostgresControlRuntimeRole:              postgresControlRuntimeRole,
		PostgresControlMigratorRole:             postgresControlMigratorRole,
		PostgresControlMaintenanceRole:          postgresControlMaintenanceRole,
		PostgresDuckLakeRuntimeRole:             postgresDuckLakeRuntimeRole,
		DeliveryPhysicalPoolCompatibilityDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID") {
		t.Fatalf("missing delivery physical pool accepted: %v", err)
	}
	base.DeliveryPhysicalPoolID = "pool-prod"
	base.DeliveryPhysicalPoolCompatibilityDigest = "not-a-digest"
	if err := base.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST") {
		t.Fatalf("invalid delivery pool compatibility digest accepted: %v", err)
	}
	base.DeliveryPhysicalPoolCompatibilityDigest = "sha256:" + strings.Repeat("b", 64)
	if err := base.ValidatePostgresProduction(); err != nil {
		t.Fatalf("valid delivery pool contract rejected: %v", err)
	}
}

func TestPostgresCredentialAliasNormalizesDefaultPortAndQueryOrdering(t *testing.T) {
	left := "postgres://runtime:secret@DB/control?sslmode=require&sslrootcert=%2Fetc%2Fca.pem"
	right := "postgresql://runtime:secret@db:5432/control?sslrootcert=%2Fetc%2Fca.pem&sslmode=require"
	if !postgresCredentialAlias(left, right) {
		t.Fatalf("semantically identical PostgreSQL credentials were not recognized as aliases")
	}
}

func TestValidatePostgresProductionRejectsSemanticMaintenanceCredentialAlias(t *testing.T) {
	cfg := Config{
		Production:                     true,
		PostgresRequireTLS:             true,
		PostgresControlURL:             "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresControlMigratorURL:     "postgres://migrator:secret@db/control?sslmode=verify-full",
		PostgresControlMaintenanceURL:  "postgres://maintenance:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeURL:            "postgres://ducklake:secret@db/ducklake?sslmode=verify-full",
		PostgresDuckLakeMaintenanceURL: "postgresql://ducklake:secret@db:5432/ducklake?sslrootcert=%2Fetc%2Fca.pem&sslmode=verify-full",
		PostgresControlRuntimeRole:     postgresControlRuntimeRole,
		PostgresControlMigratorRole:    postgresControlMigratorRole,
		PostgresControlMaintenanceRole: postgresControlMaintenanceRole,
		PostgresDuckLakeRuntimeRole:    postgresDuckLakeRuntimeRole,
	}
	if err := cfg.ValidatePostgresProduction(); err == nil || !strings.Contains(err.Error(), "DuckLake maintenance URL") {
		t.Fatalf("semantic maintenance credential alias accepted: %v", err)
	}
}

func TestValidatePostgresUpgradeRequiresIndependentOwnerCredentials(t *testing.T) {
	base := Config{PostgresExpectedMajor: 18, PostgresRequireTLS: true,
		PostgresControlURL:                   "postgres://runtime:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeURL:                  "postgres://duck:secret@db/ducklake?sslmode=verify-full",
		PostgresControlUpgradeCoordinatorURL: "postgres://coordinator:secret@db/control?sslmode=verify-full",
		PostgresDuckLakeMigratorURL:          "postgres://catalog-migrator:secret@db/ducklake?sslmode=verify-full"}
	if err := base.ValidatePostgresUpgrade(); err != nil {
		t.Fatalf("valid independent upgrade credentials rejected: %v", err)
	}
	base.PostgresDuckLakeMigratorURL = base.PostgresDuckLakeURL
	if err := base.ValidatePostgresUpgrade(); err == nil || !strings.Contains(err.Error(), "DuckLake migrator URL") {
		t.Fatalf("shared catalog credentials accepted: %v", err)
	}
}
