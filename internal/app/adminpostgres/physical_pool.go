package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

const assumeControlOwnerSQL = `SET LOCAL ROLE leapview_control_owner`

// BootstrapPhysicalPool admits one physical pool through native PostgreSQL
// and PostgreSQL-backed DuckLake authorities. Non-production targets do not
// expose this operation.
func (o Operations) BootstrapPhysicalPool(ctx context.Context, request adminoffline.PhysicalPoolBootstrapRequest, out io.Writer) error {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production {
		// Local development may bootstrap its loopback-only physical pool so
		// `task dev` can exercise the complete candidate/MCP journey. The same
		// PostgreSQL role and database boundaries remain mandatory; only TLS and
		// production admission gates differ.
		if err := cfg.ValidatePostgresDevelopment(); err != nil {
			return fmt.Errorf("validate development PostgreSQL physical-pool configuration: %w", err)
		}
	}
	pool, compatibilityDigest, err := validatePhysicalPoolBootstrap(request)
	if err != nil {
		return err
	}
	result := adminoffline.PhysicalPoolBootstrapResult{
		PoolID: pool.ID.String(), CompatibilityDigest: compatibilityDigest,
		EvidenceDigest: request.Evidence.Digest, ConformanceVersion: request.Evidence.ConformanceVersion,
	}
	if !request.Apply {
		return writePhysicalPoolBootstrapResult(out, result)
	}
	if out == nil {
		return errors.New("physical-pool bootstrap output is required")
	}
	lock, err := deps.AcquireLock(cfg.HomeDir)
	if err != nil {
		return err
	}
	if lock == nil {
		return errors.New("physical-pool bootstrap lock is unavailable")
	}
	defer lock.Release()
	result, err = deps.BootstrapPool(ctx, cfg, request)
	if err != nil {
		return fmt.Errorf("PostgreSQL physical-pool bootstrap: %w", err)
	}
	if !result.Applied || result.PoolID != pool.ID.String() || result.CompatibilityDigest != compatibilityDigest || result.EvidenceDigest != request.Evidence.Digest || result.ConformanceVersion != request.Evidence.ConformanceVersion {
		return errors.New("PostgreSQL physical-pool bootstrap returned different identity evidence")
	}
	return writePhysicalPoolBootstrapResult(out, result)
}

func validatePhysicalPoolBootstrap(request adminoffline.PhysicalPoolBootstrapRequest) (physicalpool.PhysicalPool, string, error) {
	if err := request.Pool.Validate(); err != nil {
		return physicalpool.PhysicalPool{}, "", fmt.Errorf("physical-pool identity: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "tenant", value: request.Pool.Tenant},
		{name: "region", value: request.Pool.Region},
	} {
		if field.value == "" || field.value != strings.TrimSpace(field.value) {
			return physicalpool.PhysicalPool{}, "", fmt.Errorf("physical-pool identity: %s is required and must be canonical", field.name)
		}
	}
	if err := request.Evidence.Verify(); err != nil {
		return physicalpool.PhysicalPool{}, "", fmt.Errorf("physical-pool conformance evidence: %w", err)
	}
	if err := (ducklake.SharedPoolConformance{Compatibility: request.Evidence.Compatibility}).ValidateEvidence(request.Evidence); err != nil {
		return physicalpool.PhysicalPool{}, "", fmt.Errorf("physical-pool conformance checklist: %w", err)
	}
	if !request.Pool.Compatibility.StableEqual(request.Evidence.Compatibility) {
		return physicalpool.PhysicalPool{}, "", errors.New("physical-pool identity and evidence storage contract differ")
	}
	pool, err := physicalpool.NewPhysicalPool(request.Pool)
	if err != nil {
		return physicalpool.PhysicalPool{}, "", err
	}
	compatibilityDigest, err := request.Evidence.Compatibility.Digest()
	if err != nil {
		return physicalpool.PhysicalPool{}, "", err
	}
	return pool, compatibilityDigest, nil
}

func writePhysicalPoolBootstrapResult(out io.Writer, result adminoffline.PhysicalPoolBootstrapResult) error {
	if out == nil {
		return nil
	}
	_, err := fmt.Fprintf(out, "pool_id: %s\ncompatibility_digest: %s\nevidence_digest: %s\nconformance_version: %s\napplied: %t\n", result.PoolID, result.CompatibilityDigest, result.EvidenceDigest, result.ConformanceVersion, result.Applied)
	return err
}

func bootstrapNativePhysicalPool(ctx context.Context, cfg config.Config, request adminoffline.PhysicalPoolBootstrapRequest) (result adminoffline.PhysicalPoolBootstrapResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	pool, compatibilityDigest, err := validatePhysicalPoolBootstrap(request)
	if err != nil {
		return result, err
	}
	if cfg.Production && !cfg.PostgresRequireTLS {
		return result, errors.New("production physical-pool bootstrap requires LEAPVIEW_POSTGRES_REQUIRE_TLS=true")
	}
	controlConfig := cfg.PostgresControlPlaneConfig().Migrator
	_, catalogConfig := cfg.PostgresDuckLakeUpgradeConfig()
	if err := controlConfig.Validate(); err != nil {
		return result, fmt.Errorf("invalid PostgreSQL control migrator configuration: %w", err)
	}
	if err := catalogConfig.Validate(); err != nil {
		return result, fmt.Errorf("invalid PostgreSQL DuckLake migrator configuration: %w", err)
	}
	if controlConfig.RuntimeRole == catalogConfig.RuntimeRole || strings.TrimSpace(controlConfig.URL) == strings.TrimSpace(catalogConfig.URL) {
		return result, errors.New("control and DuckLake pool bootstrap credentials must be distinct")
	}

	extensionSupply, err := extensionsupplyloader.Load(ctx, cfg)
	if err != nil {
		return result, fmt.Errorf("load reviewed DuckDB extension supply: %w", err)
	}
	control, err := platformpostgres.OpenControl(ctx, controlConfig)
	if err != nil {
		return result, fmt.Errorf("open PostgreSQL control migrator: %w", err)
	}
	defer control.Close()
	if err := postgresbaseline.VerifyProvider(ctx, control); err != nil {
		return result, fmt.Errorf("verify PostgreSQL control baseline: %w", err)
	}
	catalogAdmin, err := platformpostgres.OpenDuckLake(ctx, catalogConfig)
	if err != nil {
		return result, fmt.Errorf("open PostgreSQL DuckLake migrator: %w", err)
	}
	defer catalogAdmin.Close()
	controlIdentity, err := ducklakepostgres.ReadDatabaseIdentity(ctx, control)
	if err != nil {
		return result, err
	}
	if err := ducklakepostgres.ValidateDatabaseIdentity(controlIdentity, ducklakepostgres.DefaultControlDatabase, controlConfig.RuntimeRole); err != nil {
		return result, err
	}
	catalogIdentity, err := ducklakepostgres.ReadDatabaseIdentity(ctx, catalogAdmin)
	if err != nil {
		return result, err
	}
	if err := ducklakepostgres.ValidateDatabaseIdentity(catalogIdentity, ducklakepostgres.DefaultDuckLakeDatabase, catalogConfig.RuntimeRole); err != nil {
		return result, err
	}

	tx, err := control.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	// The authenticated migrator is deliberately NOINHERIT. Assume its fixed
	// owner capability only inside this caller-owned transaction so pool and
	// catalog registration cannot leak owner authority to later operations.
	// sqlc-exception: analyzer-incompatible. PostgreSQL SET LOCAL ROLE cannot be prepared by sqlc vet.
	if _, err := tx.Exec(ctx, assumeControlOwnerSQL); err != nil {
		return result, fmt.Errorf("assume PostgreSQL control owner role: %w", err)
	}
	ownerID, err := platformbootstrap.New(tx).InstanceID(ctx)
	if err != nil {
		return result, fmt.Errorf("read PostgreSQL instance identity: %w", err)
	}
	admission, err := pool.Admit(request.Evidence)
	if err != nil {
		return result, err
	}
	admittedPool, err := pool.ApplyAdmission(admission)
	if err != nil {
		return result, err
	}
	contract := &ducklake.PoolContract{Pool: admittedPool, Tuple: admission.Compatibility, Admission: admission, Evidence: request.Evidence}
	dataPath, err := admittedPool.DataPath()
	if err != nil {
		return result, err
	}
	if implementation := strings.ToLower(admission.Compatibility.StorageImplementation); implementation == "local" || implementation == "filesystem" {
		if err := os.MkdirAll(dataPath, 0o700); err != nil {
			return result, fmt.Errorf("create local physical-pool namespace: %w", err)
		}
	}
	s3Config := gcadapter.S3Config{
		Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
		SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
		Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle,
		ExtensionAdmission: extensionSupply,
	}
	store, err := gcadapter.NewPoolStore(ctx, contract, s3Config)
	if err != nil {
		return result, fmt.Errorf("physical-pool ownership marker store: %w", err)
	}
	marker, ok := store.(physicalpool.NamespaceOwnership)
	if !ok {
		return result, errors.New("physical-pool store does not support ownership markers")
	}
	createdPool, createdAdmission, err := physicalpoolpostgres.New(tx).CreateAndAdmitWithOwnership(ctx, pool, request.Evidence, ownerID, marker)
	if err != nil {
		return result, err
	}

	metadataSchema := ducklake.MetadataSchemaForPool(createdPool.ID.String())
	if err := ducklakepostgres.EnsureCatalogMetadataSchema(ctx, catalogAdmin, metadataSchema); err != nil {
		return result, err
	}
	credentialBootstrap, err := postgresducklake.NewCredentialBootstrap(postgresducklake.CredentialConfig{
		PostgresURL: catalogConfig.URL, AllowPlaintextLoopback: !cfg.Production, Contract: contract, ExtensionAdmission: extensionSupply, S3: s3Config,
	})
	if err != nil {
		return result, err
	}
	environment, err := ducklake.Open(ctx, ducklake.Config{
		RootDir: cfg.RuntimeDir(), DataPath: dataPath,
		PhysicalPoolID: createdPool.ID.String(), SharedPool: true,
		Compatibility: createdAdmission.Compatibility, PoolContract: contract,
		CredentialBootstrap: credentialBootstrap, ExtensionAdmission: extensionSupply, MaxConnections: 1,
		MemoryMaxBytes: cfg.DuckDBNodeMemoryMaxBytes, TempMaxBytes: cfg.DuckDBNodeTempMaxBytes,
		MaxThreads: cfg.DuckDBNodeMaxThreads, TempDir: cfg.DuckDBTempDirPath(),
		PostgresCatalog: &ducklake.PostgresCatalogConfig{
			PhysicalPoolID: createdPool.ID.String(), DuckLakeSecret: postgresducklake.DuckLakeSecret,
			PostgresSecret: postgresducklake.PostgresSecret, MetadataSchema: metadataSchema,
			DataPath: dataPath, Mode: ducklake.PostgresCatalogInitialize,
		},
	})
	if err != nil {
		return result, fmt.Errorf("initialize PostgreSQL-backed DuckLake catalog: %w", err)
	}
	if err := environment.Close(); err != nil {
		return result, fmt.Errorf("close DuckLake bootstrap session: %w", err)
	}
	runtimeRole := cfg.PostgresDuckLakeRuntimeConfig().RuntimeRole
	if err := ducklakepostgres.ProvisionCatalogRuntimePrivileges(ctx, catalogAdmin, metadataSchema, runtimeRole); err != nil {
		return result, err
	}
	maintenanceRole := cfg.PostgresDuckLakeMaintenanceConfig().RuntimeRole
	if err := ducklakepostgres.ProvisionCatalogMaintenancePrivileges(ctx, catalogAdmin, metadataSchema, maintenanceRole); err != nil {
		return result, err
	}
	registrationEvidence, err := ducklakepostgres.ReadCatalogRegistrationEvidence(ctx, catalogAdmin, metadataSchema)
	if err != nil {
		return result, err
	}
	identity, err := ducklakepostgres.DeriveCatalogIdentity(createdPool.ID.String(), registrationEvidence.CatalogDatabase)
	if err != nil {
		return result, err
	}
	runtimeCompatibility := ducklakepostgres.RuntimeCompatibility{
		RuntimeTuple: ducklakepostgres.RuntimeTuple{
			DuckDBRuntime: createdAdmission.Compatibility.DuckDBRuntime, DuckLakeExtension: createdAdmission.Compatibility.DuckLakeExtension,
			CatalogFormat: createdAdmission.Compatibility.CatalogFormat,
		},
		CompatibilityDigest: compatibilityDigest, CatalogSchemaVersion: registrationEvidence.CatalogSchemaVersion,
	}
	if _, _, err := ducklakepostgres.BootstrapCatalog(ctx, tx, identity, runtimeCompatibility); err != nil {
		return result, err
	}
	beginEvidence, err := json.Marshal(map[string]any{
		"backup_verified": true, "bootstrap": true, "conformance_evidence_digest": request.Evidence.Digest, "drain_verified": true,
	})
	if err != nil {
		return result, err
	}
	completionEvidence, err := json.Marshal(map[string]any{
		"bootstrap": true, "catalog_registration_verified": true, "conformance_evidence_digest": request.Evidence.Digest,
	})
	if err != nil {
		return result, err
	}
	if _, err := ducklakepostgres.QualifyCatalogBootstrap(ctx, tx, ducklakepostgres.CatalogBootstrapQualificationInput{
		PhysicalPoolID: identity.PhysicalPoolID, CatalogID: identity.CatalogID, OwnerID: ownerID,
		Compatibility: runtimeCompatibility, BeginEvidence: beginEvidence, CompletionEvidence: completionEvidence,
	}); err != nil {
		return result, fmt.Errorf("qualify initial PostgreSQL-backed DuckLake catalog: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	return adminoffline.PhysicalPoolBootstrapResult{
		PoolID: createdPool.ID.String(), CompatibilityDigest: createdAdmission.CompatibilityDigest,
		EvidenceDigest: createdAdmission.EvidenceDigest, ConformanceVersion: createdAdmission.ConformanceVersion, Applied: true,
	}, nil
}
