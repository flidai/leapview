// Package adminoffline composes Admin-owned offline use cases from concrete
// capability adapters, platform mechanisms, and process configuration.
package adminoffline

import (
	"context"
	"io"
	"path/filepath"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/gcadapter"
)

type Operations struct{}

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

func (Operations) BootstrapPhysicalPool(ctx context.Context, request adminoffline.PhysicalPoolBootstrapRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.BootstrapPhysicalPool(ctx, request, out)
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
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	normalized := adminoffline.Config{
		HomeDir:            cfg.HomeDir,
		DBPath:             cfg.DBPath(),
		Environment:        cfg.Environment,
		Production:         cfg.Production,
		BootstrapEmail:     cfg.BootstrapEmail,
		DuckLakeCatalog:    cfg.DuckLakeCatalogPath(),
		DuckLakeData:       cfg.DuckLakeDataDir(),
		ArtifactDir:        cfg.ArtifactDir(),
		RuntimeDir:         cfg.RuntimeDir(),
		ManagedDataDir:     cfg.ManagedDataDir,
		ManagedDataBackend: cfg.ManagedDataBackend,
	}
	return adminoffline.New(normalized, adminoffline.Dependencies{
		Locker:      instanceLocker{home: cfg.HomeDir},
		State:       instanceState{dbPath: cfg.DBPath()},
		Initializer: instanceInitializer{dbPath: cfg.DBPath()},
		Recovery: credentialRecovery{
			path: filepath.Join(cfg.HomeDir, adminoffline.CredentialRecoveryFileName),
		},
		Retention: operationalRetention{dbPath: cfg.DBPath()},
		Storage: storageCleaner{
			dbPath: cfg.DBPath(), home: cfg.HomeDir,
			catalogPath: cfg.DuckLakeCatalogPath(), dataPath: cfg.DuckLakeDataDir(),
		},
		PhysicalPool: physicalPoolBootstrap{dbPath: cfg.DBPath(), s3: gcadapter.S3Config{
			Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
			SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
			Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle,
		}},
		DeliveryRepair: deliveryRepair{dbPath: cfg.DBPath(), home: cfg.HomeDir, stagingRoot: cfg.RuntimeDir(), s3: gcadapter.S3Config{
			Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
			SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
			Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle,
		}},
		Archive: instanceArchive{home: cfg.HomeDir, dbPath: cfg.DBPath()},
	}), nil
}
