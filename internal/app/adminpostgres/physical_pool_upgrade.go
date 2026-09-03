package adminpostgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/extensionsupplyloader"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/app/postgresducklake"
	"github.com/flidai/leapview/internal/extension"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/google/uuid"
)

const catalogUpgradeLease = time.Hour

var ErrNativeCatalogUpgradeUnavailable = errors.New("native PostgreSQL DuckLake catalog upgrade is unavailable outside production")

// UpgradePhysicalPoolCatalog is the explicit operator boundary for an
// existing PostgreSQL-backed DuckLake catalog. Serving startup never calls
// this path and therefore never opens either upgrade credential.
func (o Operations) UpgradePhysicalPoolCatalog(ctx context.Context, request admincli.CatalogUpgradeRequest, out io.Writer) error {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	pool, _, err := validateCatalogUpgradeRequest(request)
	if err != nil {
		return err
	}
	expected := admincli.CatalogUpgradeResult{
		MigrationID: request.MigrationID, PhysicalPoolID: pool.ID.String(), CatalogID: "ducklake:" + pool.ID.String(),
		CatalogSchemaVersion: request.CatalogSchemaVersion, RecoveryDecision: request.RecoveryDecision,
	}
	if !cfg.Production {
		return ErrNativeCatalogUpgradeUnavailable
	}
	if !request.Apply {
		return writeCatalogUpgradeResult(out, expected)
	}
	if out == nil {
		return errors.New("catalog upgrade output is required")
	}
	if err := cfg.ValidatePostgresUpgrade(); err != nil {
		return fmt.Errorf("validate production PostgreSQL catalog upgrade configuration: %w", err)
	}
	lock, err := deps.AcquireLock(cfg.HomeDir)
	if err != nil {
		return err
	}
	if lock == nil {
		return errors.New("catalog upgrade lock is unavailable")
	}
	defer lock.Release()
	result, err := deps.UpgradePool(ctx, cfg, request)
	if err != nil {
		return fmt.Errorf("PostgreSQL DuckLake catalog upgrade: %w", err)
	}
	if !result.Applied || result.MigrationID != expected.MigrationID || result.PhysicalPoolID != expected.PhysicalPoolID || result.CatalogID != expected.CatalogID || result.CatalogSchemaVersion != expected.CatalogSchemaVersion || result.RecoveryDecision != expected.RecoveryDecision {
		return errors.New("PostgreSQL DuckLake catalog upgrade returned different identity evidence")
	}
	return writeCatalogUpgradeResult(out, result)
}

func validateCatalogUpgradeRequest(request admincli.CatalogUpgradeRequest) (pool physicalpool.PhysicalPool, compatibilityDigest string, err error) {
	validated, compatibilityDigest, err := validatePhysicalPoolBootstrap(adminoffline.PhysicalPoolBootstrapRequest{Pool: request.Pool, Evidence: request.Evidence, Apply: request.Apply})
	if err != nil {
		return physicalpool.PhysicalPool{}, "", err
	}
	migrationID, err := uuid.Parse(request.MigrationID)
	if err != nil || migrationID == uuid.Nil || migrationID.String() != request.MigrationID {
		return physicalpool.PhysicalPool{}, "", errors.New("catalog migration id must be a canonical UUID")
	}
	if !canonicalCatalogUpgradeText(request.CatalogSchemaVersion, 128) {
		return physicalpool.PhysicalPool{}, "", errors.New("target catalog schema version is invalid")
	}
	if request.RecoveryDecision != admincli.CatalogUpgradeRecoveryRollback && request.RecoveryDecision != admincli.CatalogUpgradeRecoveryForwardRecovery {
		return physicalpool.PhysicalPool{}, "", errors.New("catalog recovery decision is invalid")
	}
	if !request.DrainVerified || !request.BackupVerified {
		return physicalpool.PhysicalPool{}, "", errors.New("catalog upgrade requires verified drain and backup evidence")
	}
	return validated, compatibilityDigest, nil
}

func canonicalCatalogUpgradeText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum && strings.IndexFunc(value, unicode.IsControl) < 0
}

func writeCatalogUpgradeResult(out io.Writer, result admincli.CatalogUpgradeResult) error {
	if out == nil {
		return nil
	}
	_, err := fmt.Fprintf(out, "migration_id: %s\nphysical_pool_id: %s\ncatalog_id: %s\ncatalog_schema_version: %s\nrecovery_decision: %s\napplied: %t\n", result.MigrationID, result.PhysicalPoolID, result.CatalogID, result.CatalogSchemaVersion, result.RecoveryDecision, result.Applied)
	return err
}

func upgradeNativePhysicalPoolCatalog(ctx context.Context, cfg config.Config, request admincli.CatalogUpgradeRequest) (admincli.CatalogUpgradeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	targetPool, targetDigest, err := validateCatalogUpgradeRequest(request)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, err
	}
	if err := cfg.ValidatePostgresUpgrade(); err != nil {
		return admincli.CatalogUpgradeResult{}, err
	}
	controlMigratorConfig := cfg.PostgresControlPlaneConfig().Migrator
	coordinatorConfig, catalogConfig := cfg.PostgresDuckLakeUpgradeConfig()
	if err := controlMigratorConfig.Validate(); err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("invalid PostgreSQL control migrator configuration: %w", err)
	}
	if controlMigratorConfig.RuntimeRole == coordinatorConfig.RuntimeRole || strings.TrimSpace(controlMigratorConfig.URL) == strings.TrimSpace(coordinatorConfig.URL) {
		return admincli.CatalogUpgradeResult{}, errors.New("control migrator and upgrade coordinator credentials must be distinct")
	}
	extensionSupply, err := extensionsupplyloader.Load(ctx, cfg)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("load reviewed DuckDB extension supply: %w", err)
	}

	controlMigrator, err := platformpostgres.OpenControl(ctx, controlMigratorConfig)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("open PostgreSQL control migrator: %w", err)
	}
	defer controlMigrator.Close()
	if err := postgresbaseline.Verify(ctx, controlMigrator); err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("verify PostgreSQL control baseline: %w", err)
	}
	coordinatorDB, err := platformpostgres.OpenControl(ctx, coordinatorConfig)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("open PostgreSQL upgrade coordinator: %w", err)
	}
	defer coordinatorDB.Close()
	catalogAdmin, err := platformpostgres.OpenDuckLake(ctx, catalogConfig)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("open PostgreSQL DuckLake migrator: %w", err)
	}
	defer catalogAdmin.Close()
	for _, check := range []struct {
		label, database, role string
		db                    ducklakepostgres.DBTX
	}{
		{label: "control migrator", database: ducklakepostgres.DefaultControlDatabase, role: controlMigratorConfig.RuntimeRole, db: controlMigrator},
		{label: "upgrade coordinator", database: ducklakepostgres.DefaultControlDatabase, role: coordinatorConfig.RuntimeRole, db: coordinatorDB},
		{label: "DuckLake migrator", database: ducklakepostgres.DefaultDuckLakeDatabase, role: catalogConfig.RuntimeRole, db: catalogAdmin},
	} {
		identity, readErr := ducklakepostgres.ReadDatabaseIdentity(ctx, check.db)
		if readErr != nil {
			return admincli.CatalogUpgradeResult{}, fmt.Errorf("read %s identity: %w", check.label, readErr)
		}
		if readErr := ducklakepostgres.ValidateDatabaseIdentity(identity, check.database, check.role); readErr != nil {
			return admincli.CatalogUpgradeResult{}, fmt.Errorf("validate %s identity: %w", check.label, readErr)
		}
	}

	control := ducklakepostgres.New(coordinatorDB)
	catalogIdentity, err := control.LoadCatalog(ctx, targetPool.ID.String())
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("load registered DuckLake catalog: %w", err)
	}
	current, err := control.LoadCatalogRuntimeCompatibility(ctx, targetPool.ID.String())
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("load current DuckLake compatibility: %w", err)
	}
	expectedCatalogID := "ducklake:" + targetPool.ID.String()
	expectedSchema := ducklake.MetadataSchemaForPool(targetPool.ID.String())
	expectedCatalogIdentity, err := ducklakepostgres.DeriveCatalogIdentity(targetPool.ID.String(), ducklakepostgres.DefaultDuckLakeDatabase)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("derive expected DuckLake catalog identity: %w", err)
	}
	if catalogIdentity.PhysicalPoolID != targetPool.ID.String() || catalogIdentity.CatalogID != expectedCatalogID || catalogIdentity.CatalogUUID != expectedCatalogIdentity.CatalogUUID || catalogIdentity.CatalogDatabase != ducklakepostgres.DefaultDuckLakeDatabase || catalogIdentity.MetadataSchema != expectedSchema || current.CatalogID != expectedCatalogID {
		return admincli.CatalogUpgradeResult{}, errors.New("registered DuckLake catalog identity differs from target pool")
	}
	target := ducklakepostgres.RuntimeCompatibility{
		RuntimeTuple: ducklakepostgres.RuntimeTuple{
			DuckDBRuntime: request.Evidence.Compatibility.DuckDBRuntime, DuckLakeExtension: request.Evidence.Compatibility.DuckLakeExtension,
			CatalogFormat: request.Evidence.Compatibility.CatalogFormat,
		},
		CompatibilityDigest: targetDigest, CatalogSchemaVersion: request.CatalogSchemaVersion,
	}
	if current.RuntimeCompatibility == target {
		return admincli.CatalogUpgradeResult{}, errors.New("target DuckLake compatibility is already active")
	}

	// Qualification admission is append-only evidence, not the active catalog
	// tuple. Persist it with the control migrator before the coordinator fence;
	// a failed migration may safely reuse the admission, while only successful
	// fenced completion can advance catalog_runtime_compatibility to target.
	admittedPool, admission, ownerID, contract, dataPath, s3Config, err := admitCatalogUpgradeTarget(ctx, cfg, controlMigrator, targetPool, request, extensionSupply)
	if err != nil {
		return admincli.CatalogUpgradeResult{}, err
	}
	if admittedPool.ID != targetPool.ID || admission.CompatibilityDigest != targetDigest {
		return admincli.CatalogUpgradeResult{}, errors.New("target physical-pool admission returned different compatibility evidence")
	}
	credentialBootstrap, err := postgresducklake.NewCredentialBootstrap(postgresducklake.CredentialConfig{
		PostgresURL: catalogConfig.URL, Contract: contract, ExtensionAdmission: extensionSupply, S3: s3Config,
	})
	if err != nil {
		return admincli.CatalogUpgradeResult{}, err
	}
	session, err := ducklake.OpenPostgresCatalogUpgradeSession(ctx, ducklake.PostgresCatalogUpgradeSessionConfig{
		DataPath: dataPath, TempDir: cfg.DuckDBTempDirPath(), MemoryMaxBytes: cfg.DuckDBNodeMemoryMaxBytes,
		TempMaxBytes: cfg.DuckDBNodeTempMaxBytes, MaxThreads: cfg.DuckDBNodeMaxThreads,
		ExtensionAdmission: extensionSupply, CredentialBootstrap: credentialBootstrap,
	})
	if err != nil {
		return admincli.CatalogUpgradeResult{}, fmt.Errorf("open DuckLake catalog upgrade session: %w", err)
	}
	defer session.Close()
	executor := &ducklakepostgres.SQLCatalogExecutor{
		IdentityDB: catalogAdmin, Exec: session.Conn(), Query: session.Conn(), CatalogAdmin: catalogAdmin,
		DuckLakeSecret: postgresducklake.DuckLakeSecret, PostgresSecret: postgresducklake.PostgresSecret, DataPath: dataPath,
		RuntimeRole: cfg.PostgresDuckLakeRuntimeConfig().RuntimeRole, CatalogDatabase: ducklakepostgres.DefaultDuckLakeDatabase, CatalogRole: catalogConfig.RuntimeRole,
	}
	migration, err := (&ducklakepostgres.UpgradeCoordinator{
		Control: control, ControlDB: coordinatorDB, Catalog: executor,
		ControlDatabase: ducklakepostgres.DefaultControlDatabase, ControlRole: coordinatorConfig.RuntimeRole,
		CatalogDatabase: ducklakepostgres.DefaultDuckLakeDatabase, CatalogRole: catalogConfig.RuntimeRole,
	}).Run(ctx, ducklakepostgres.UpgradeRequest{
		MigrationID: request.MigrationID, PhysicalPoolID: targetPool.ID.String(), CatalogID: expectedCatalogID,
		MetadataSchema: expectedSchema, DataPath: dataPath, OwnerID: ownerID, Current: current.RuntimeCompatibility, Target: target,
		LeaseExpiresAt: time.Now().UTC().Add(catalogUpgradeLease), DrainVerified: request.DrainVerified, BackupVerified: request.BackupVerified,
		RecoveryDecision: request.RecoveryDecision,
	})
	if err != nil {
		return admincli.CatalogUpgradeResult{}, err
	}
	if migration.State != "completed" || migration.MigrationID != request.MigrationID || migration.PhysicalPoolID != targetPool.ID.String() || migration.CatalogID != expectedCatalogID || migration.Target != target {
		return admincli.CatalogUpgradeResult{}, errors.New("completed DuckLake catalog migration returned different evidence")
	}
	return admincli.CatalogUpgradeResult{
		MigrationID: migration.MigrationID, PhysicalPoolID: migration.PhysicalPoolID, CatalogID: migration.CatalogID,
		CatalogSchemaVersion: migration.Target.CatalogSchemaVersion, RecoveryDecision: request.RecoveryDecision, Applied: true,
	}, nil
}

func admitCatalogUpgradeTarget(
	ctx context.Context,
	cfg config.Config,
	controlMigrator *platformpostgres.Pool,
	targetPool physicalpool.PhysicalPool,
	request admincli.CatalogUpgradeRequest,
	extensionSupply extension.Admission,
) (physicalpool.PhysicalPool, physicalpool.PoolAdmission, string, *ducklake.PoolContract, string, gcadapter.S3Config, error) {
	admission, err := targetPool.Admit(request.Evidence)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	admittedPool, err := targetPool.ApplyAdmission(admission)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	contract := &ducklake.PoolContract{Pool: admittedPool, Tuple: admission.Compatibility, Admission: admission, Evidence: request.Evidence}
	dataPath, err := admittedPool.DataPath()
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	if implementation := strings.ToLower(admission.Compatibility.StorageImplementation); implementation == "local" || implementation == "filesystem" {
		if err := os.MkdirAll(dataPath, 0o700); err != nil {
			return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, fmt.Errorf("create local physical-pool namespace: %w", err)
		}
	}
	s3Config := gcadapter.S3Config{
		Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey,
		SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply,
	}
	store, err := gcadapter.NewPoolStore(ctx, contract, s3Config)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	marker, ok := store.(physicalpool.NamespaceOwnership)
	if !ok {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, errors.New("physical-pool store does not support ownership markers")
	}
	tx, err := controlMigrator.Begin(ctx)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if _, err := tx.Exec(ctx, assumeControlOwnerSQL); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, fmt.Errorf("assume PostgreSQL control owner role: %w", err)
	}
	ownerID, err := platformbootstrap.New(tx).InstanceID(ctx)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	createdPool, createdAdmission, err := physicalpoolpostgres.New(tx).CreateAndAdmitWithOwnership(ctx, targetPool, request.Evidence, ownerID, marker)
	if err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return physicalpool.PhysicalPool{}, physicalpool.PoolAdmission{}, "", nil, "", gcadapter.S3Config{}, err
	}
	committed = true
	createdContract := &ducklake.PoolContract{Pool: createdPool, Tuple: createdAdmission.Compatibility, Admission: createdAdmission, Evidence: request.Evidence}
	return createdPool, createdAdmission, ownerID, createdContract, dataPath, s3Config, nil
}
