// Package adminoffline composes Admin-owned offline use cases from concrete
// capability adapters, platform mechanisms, and process configuration.
package adminoffline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/extensionsupplyloader"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	recovery "github.com/flidai/leapview/internal/refresh/module"
)

type Operations struct{}

// ErrProductionUnavailable identifies the legacy administrative surface that
// must not be used by a production instance. Production Admin commands must
// be implemented by native PostgreSQL owners (for example
// adminpostgres.Maintenance) and fail before any SQLite or local DuckLake
// dependency is opened when no native owner exists.
var ErrProductionUnavailable = errors.New("legacy offline admin operations are unavailable in production")

// loadNonProductionConfig is the single admission gate for this composition
// layer. Keep the production check immediately after environment decoding so
// callers cannot accidentally construct an extension loader, SQLite store, or
// filesystem-backed adapter before being rejected.
func loadNonProductionConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, err
	}
	if cfg.Production {
		return config.Config{}, ErrProductionUnavailable
	}
	return cfg, nil
}

func (Operations) RecoveryLedgerStatus(ctx context.Context, out io.Writer) error {
	cfg, err := loadNonProductionConfig()
	if err != nil {
		return err
	}
	if out == nil {
		return fmt.Errorf("recovery ledger status output is required")
	}
	store, err := platform.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	repository := recovery.NewRecoveryRepository(store.SQLDB())
	snapshot, err := repository.Status(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	response := struct {
		SchemaVersion int                     `json:"schemaVersion"`
		Status        recovery.StatusSnapshot `json:"status"`
		Metrics       []recovery.Metric       `json:"metrics"`
	}{SchemaVersion: 1, Status: snapshot, Metrics: snapshot.Metrics()}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

func (Operations) Initialize(ctx context.Context, request adminoffline.InitializeRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.Initialize(ctx, request, out)
}

func (Operations) AcknowledgeInitialCredentials(ctx context.Context) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.AcknowledgeInitialCredentials(ctx)
}

func (Operations) StorageCleanup(ctx context.Context, request adminoffline.StorageCleanupRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.StorageCleanup(ctx, request, out)
}

func (Operations) Maintenance(ctx context.Context, request adminoffline.MaintenanceRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.Maintenance(ctx, request, out)
}

func (Operations) AuditOutbox(ctx context.Context, request adminoffline.AuditOutboxRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.AuditOutbox(ctx, request, out)
}

func (Operations) BootstrapPhysicalPool(ctx context.Context, request adminoffline.PhysicalPoolBootstrapRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.BootstrapPhysicalPool(ctx, request, out)
}

// BootstrapQualificationLocalPhysicalPool is the legacy installed-candidate
// seam for the isolated evaluation target. The shared production admission
// guard runs before its local conformance probe; production callers must use
// reviewed native evidence instead of this SQLite-backed path.
func (Operations) BootstrapQualificationLocalPhysicalPool(ctx context.Context, out io.Writer) error {
	cfg, err := loadNonProductionConfig()
	if err != nil {
		return err
	}
	if !cfg.Production || strings.TrimSpace(cfg.Environment) != "evaluation" {
		return fmt.Errorf("qualification local physical-pool bootstrap requires the production evaluation environment")
	}
	var extensionAdmission extension.Admission
	if strings.TrimSpace(cfg.DuckDBExtensionSupplyPath) != "" {
		supply, supplyErr := extensionsupplyloader.Load(ctx, cfg)
		if supplyErr != nil {
			return fmt.Errorf("load qualification extension supply: %w", supplyErr)
		}
		extensionAdmission = supply
	}
	store, err := platform.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	instanceID, err := store.InstanceID(ctx)
	closeErr := store.Close()
	if err != nil {
		return fmt.Errorf("read durable instance identity: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	identity := buildinfo.Current()
	tuple := physicalpool.Compatibility{
		DuckDBRuntime:         "duckdb:" + identity.Version + ":" + identity.Revision,
		DuckLakeExtension:     "ducklake:managed",
		CatalogFormat:         "ducklake-catalog:v1",
		StorageImplementation: "local",
		ObjectNamingContract:  "uuidv7:v1",
	}
	storageLocation, err := filepath.Abs(cfg.DuckLakeDataDir())
	if err != nil {
		return fmt.Errorf("resolve qualification pool storage: %w", err)
	}
	probeRoot := filepath.Join(cfg.RuntimeDir(), "qualification-delivery-conformance")
	defer os.RemoveAll(probeRoot)
	evidence, err := analyticsducklake.RunLocalPoolConformance(ctx, probeRoot, tuple, extensionAdmission)
	if err != nil {
		return fmt.Errorf("run qualification local physical-pool conformance: %w", err)
	}
	service, err := newService()
	if err != nil {
		return err
	}
	return service.BootstrapPhysicalPool(ctx, adminoffline.PhysicalPoolBootstrapRequest{
		Pool: physicalpool.PoolIdentity{
			StorageLocation: storageLocation, StorageNamespace: "delivery", EncryptionDomain: "local",
			IsolationBoundary: instanceID, RetentionAuthority: instanceID,
			RetentionPolicy: physicalpool.RetentionPolicy{
				ReaderGracePeriodSeconds: 1800,
				OrphanGracePeriodSeconds: 3600,
				BuildGracePeriodSeconds:  3600,
			},
			Compatibility: tuple,
		},
		Evidence: evidence,
		Apply:    true,
	}, out)
}

func (Operations) RepairDeliveryRoot(ctx context.Context, request adminoffline.DeliveryRepairRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.RepairDeliveryRoot(ctx, request, out)
}

func (Operations) AuditDeliveryRoots(ctx context.Context, request adminoffline.DeliveryAuditRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.AuditDeliveryRoots(ctx, request, out)
}

func (Operations) Backup(ctx context.Context, request adminoffline.BackupRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.Backup(ctx, request, out)
}

func (Operations) Restore(ctx context.Context, request adminoffline.RestoreRequest, in io.Reader, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.Restore(ctx, request, in, out)
}

func newService() (*adminoffline.Service, error) {
	cfg, err := loadNonProductionConfig()
	if err != nil {
		return nil, err
	}
	var extensionAdmission extension.Admission
	if strings.TrimSpace(cfg.DuckDBExtensionSupplyPath) != "" {
		supply, supplyErr := extensionsupplyloader.Load(context.Background(), cfg)
		if supplyErr != nil {
			return nil, supplyErr
		}
		extensionAdmission = supply
	}
	normalized := adminoffline.Config{
		HomeDir:               cfg.HomeDir,
		DBPath:                cfg.DBPath(),
		Environment:           cfg.Environment,
		Production:            cfg.Production,
		BootstrapEmail:        cfg.BootstrapEmail,
		DuckLakeCatalog:       cfg.DuckLakeCatalogPath(),
		DuckLakeData:          cfg.DuckLakeDataDir(),
		ArtifactDir:           cfg.ArtifactDir(),
		RuntimeDir:            cfg.RuntimeDir(),
		ManagedDataDir:        cfg.ManagedDataDir,
		ManagedDataBackend:    cfg.ManagedDataBackend,
		ManagedDataS3Endpoint: cfg.ManagedDataS3Endpoint,
		ManagedDataS3Region:   cfg.ManagedDataS3Region,
		ManagedDataS3Bucket:   cfg.ManagedDataS3Bucket,
		ManagedDataS3Prefix:   cfg.ManagedDataS3Prefix,
	}
	return adminoffline.New(normalized, adminoffline.Dependencies{
		Locker:      instanceLocker{home: cfg.HomeDir},
		State:       instanceState{dbPath: cfg.DBPath()},
		Initializer: instanceInitializer{dbPath: cfg.DBPath()},
		Recovery: credentialRecovery{
			path: filepath.Join(cfg.HomeDir, adminoffline.CredentialRecoveryFileName),
		},
		Retention:   operationalRetention{dbPath: cfg.DBPath()},
		AuditOutbox: auditOutboxControl{dbPath: cfg.DBPath()},
		Storage: storageCleaner{
			dbPath: cfg.DBPath(), home: cfg.HomeDir,
			catalogPath: cfg.DuckLakeCatalogPath(), dataPath: cfg.DuckLakeDataDir(),
			extensionAdmission: extensionAdmission,
		},
		PhysicalPool: physicalPoolBootstrap{dbPath: cfg.DBPath(), s3: gcadapter.S3Config{
			Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
			SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
			Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionAdmission,
		}},
		DeliveryRepair: deliveryRepair{dbPath: cfg.DBPath(), home: cfg.HomeDir, stagingRoot: cfg.RuntimeDir(), s3: gcadapter.S3Config{
			Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
			SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
			Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionAdmission,
		}},
		Archive: instanceArchive{home: cfg.HomeDir, dbPath: cfg.DBPath()},
	}), nil
}
