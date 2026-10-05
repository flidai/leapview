package composectl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

// FirstInstallPostgres carries the provider-created PostgreSQL URLs required
// to initialize a production Compose instance. Migrator URLs are operation
// inputs and are never written to leapview.env.
type FirstInstallPostgres struct {
	ControlURL             string `json:"controlUrl"`
	ControlMigratorURL     string `json:"controlMigratorUrl"`
	ControlMaintenanceURL  string `json:"controlMaintenanceUrl"`
	DuckLakeURL            string `json:"duckLakeUrl"`
	DuckLakeMaintenanceURL string `json:"duckLakeMaintenanceUrl"`
	DuckLakeMigratorURL    string `json:"duckLakeMigratorUrl"`
}

type FirstInstallPhysicalPool struct {
	Pool     physicalpool.PoolIdentity     `json:"pool"`
	Evidence physicalpool.EvidenceArtifact `json:"evidence"`
}

type FirstInstallOptions struct {
	Postgres     FirstInstallPostgres
	PhysicalPool FirstInstallPhysicalPool
}

func (options FirstInstallOptions) Validate() error {
	connections := []postgresConnection{
		{"control runtime", options.Postgres.ControlURL, postgresControlRuntimeRole, postgresControlDatabase},
		{"control migrator", options.Postgres.ControlMigratorURL, postgresControlMigratorRole, postgresControlDatabase},
		{"control maintenance", options.Postgres.ControlMaintenanceURL, postgresControlMaintenanceRole, postgresControlDatabase},
		{"DuckLake runtime", options.Postgres.DuckLakeURL, postgresDuckLakeRuntimeRole, postgresDuckLakeDatabase},
		{"DuckLake maintenance", options.Postgres.DuckLakeMaintenanceURL, postgresDuckLakeMaintenanceRole, postgresDuckLakeDatabase},
		{"DuckLake migrator", options.Postgres.DuckLakeMigratorURL, postgresDuckLakeMigratorRole, postgresDuckLakeDatabase},
	}
	if err := validateDistinctPostgresConnections(connections); err != nil {
		return fmt.Errorf("validate first-install PostgreSQL connections: %w", err)
	}
	if err := options.PhysicalPool.Pool.Validate(); err != nil {
		return fmt.Errorf("validate first-install physical-pool identity: %w", err)
	}
	if options.PhysicalPool.Evidence.SchemaVersion != physicalpool.EvidenceArtifactSchemaVersion {
		return fmt.Errorf("unsupported first-install physical-pool evidence schema version %d", options.PhysicalPool.Evidence.SchemaVersion)
	}
	if err := options.PhysicalPool.Evidence.Evidence.Verify(); err != nil {
		return fmt.Errorf("validate first-install physical-pool evidence: %w", err)
	}
	if !options.PhysicalPool.Pool.Compatibility.StableEqual(options.PhysicalPool.Evidence.Evidence.Compatibility) {
		return errors.New("first-install physical-pool identity and evidence compatibility differ")
	}
	return nil
}

func (postgres FirstInstallPostgres) operationEnvironment() (map[string]string, error) {
	return postgresPoolBootstrapEnvironment(postgres.ControlMigratorURL, postgres.DuckLakeMigratorURL)
}

// PrepareFirstInstall writes only serving credentials, then repeats the
// canonical delivery-pool bootstrap in dry-run mode before initialization.
func (c *Controller) PrepareFirstInstall(ctx context.Context, options FirstInstallOptions) error {
	if c == nil {
		return errors.New("controller is required")
	}
	if err := options.Validate(); err != nil {
		return err
	}
	if err := c.ensureDeploymentEnvironment(); err != nil {
		return err
	}
	appExists, err := nonEmptyRegularFile(c.path(appEnvName))
	if err != nil {
		return err
	}
	if !appExists {
		if err := seedApplicationEnvironment(c.root); err != nil {
			return err
		}
	}
	servingEnvironment, err := postgresServingEnvironment(
		options.Postgres.ControlURL, options.Postgres.ControlMaintenanceURL,
		options.Postgres.DuckLakeURL, options.Postgres.DuckLakeMaintenanceURL,
	)
	if err != nil {
		return err
	}
	if err := writePostgresServingEnvironment(
		c.path(appEnvName), servingEnvironment,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
	); err != nil {
		return err
	}
	artifacts, err := physicalPoolBootstrapArtifactsFromInput(
		filepath.Join(c.root, ".host-install-physical-pool"),
		options.PhysicalPool.Pool,
		options.PhysicalPool.Evidence,
	)
	if err != nil {
		return fmt.Errorf("prepare first-install physical-pool artifacts: %w", err)
	}
	if err := writePhysicalPoolBootstrapArtifacts(artifacts); err != nil {
		return err
	}
	result, err := c.runPhysicalPoolBootstrap(ctx, artifacts, false, nil)
	if err != nil {
		return fmt.Errorf("dry-run first-install physical-pool bootstrap: %w", err)
	}
	if err := verifyPhysicalPoolBootstrapResult(result, artifacts, false); err != nil {
		return err
	}
	for _, entry := range []struct{ key, value string }{
		{"LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID", artifacts.PoolID},
		{"LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST", artifacts.CompatibilityDigest},
	} {
		if err := appendOrReplaceEnvFile(c.path(appEnvName), entry.key, entry.value); err != nil {
			return fmt.Errorf("persist first-install physical-pool identity: %w", err)
		}
	}
	return nil
}

// InitializeFirstInstall supplies the control migrator only to the one-shot
// initializer process. The URL is absent from the serving environment file.
func (c *Controller) InitializeFirstInstall(ctx context.Context, options InitOptions, controlMigratorURL string) error {
	if c == nil {
		return errors.New("controller is required")
	}
	controlMigratorURL = strings.TrimSpace(controlMigratorURL)
	controlMigratorURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "first-install control migrator", value: controlMigratorURL,
		role: postgresControlMigratorRole, database: postgresControlDatabase,
	})
	if err != nil {
		return err
	}
	return c.initialize(ctx, options, map[string]string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL":  controlMigratorURL,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE": postgresControlMigratorRole,
	})
}

// ApplyFirstInstall repeats the dry-run with --apply after the control
// baseline exists. Both migrators cross this one subprocess boundary only.
func (c *Controller) ApplyFirstInstall(ctx context.Context, options FirstInstallOptions) error {
	if c == nil {
		return errors.New("controller is required")
	}
	if err := options.Validate(); err != nil {
		return err
	}
	artifacts, err := physicalPoolBootstrapArtifactsFromInput(
		filepath.Join(c.root, ".host-install-physical-pool"),
		options.PhysicalPool.Pool,
		options.PhysicalPool.Evidence,
	)
	if err != nil {
		return fmt.Errorf("prepare first-install physical-pool artifacts: %w", err)
	}
	if err := writePhysicalPoolBootstrapArtifacts(artifacts); err != nil {
		return err
	}
	operationEnvironment, err := options.Postgres.operationEnvironment()
	if err != nil {
		return err
	}
	result, err := c.runPhysicalPoolBootstrap(ctx, artifacts, true, operationEnvironment)
	if err != nil {
		return fmt.Errorf("apply first-install physical-pool bootstrap: %w", err)
	}
	if err := verifyPhysicalPoolBootstrapResult(result, artifacts, true); err != nil {
		return err
	}
	return removePostgresEnvironmentURLs(
		c.path(appEnvName),
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
	)
}
