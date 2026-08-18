// Package adminoffline composes Admin-owned offline use cases from concrete
// capability adapters, platform mechanisms, and process configuration.
package adminoffline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
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

// BootstrapQualificationLocalPhysicalPool is the installed-candidate seam for
// the isolated evaluation target. It runs the substantive local conformance
// probe inside the exact candidate image and persists the resulting admission
// through the same lock-protected offline service used by operators. General
// production targets must continue to provide reviewed external evidence.
func (Operations) BootstrapQualificationLocalPhysicalPool(ctx context.Context, out io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.Production || strings.TrimSpace(cfg.Environment) != "evaluation" {
		return fmt.Errorf("qualification local physical-pool bootstrap requires the production evaluation environment")
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
	evidence, err := analyticsducklake.RunLocalPoolConformance(ctx, probeRoot, tuple)
	if err != nil {
		return fmt.Errorf("run qualification local physical-pool conformance: %w", err)
	}
	service, err := newService()
	if err != nil {
		return err
	}
	return service.BootstrapPhysicalPool(ctx, adminoffline.PhysicalPoolBootstrapRequest{
		Pool: physicalpool.PoolIdentity{
			StorageLocation: storageLocation, StorageNamespace: "delivery",
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
