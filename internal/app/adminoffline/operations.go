// Package adminoffline composes Admin-owned offline use cases from concrete
// capability adapters, platform mechanisms, and process configuration.
package adminoffline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/extensionsupplyloader"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

type Operations struct{}

// ErrProductionUnavailable identifies the legacy administrative surface that
// must not be used by a production instance. Production Admin commands must
// be implemented by native PostgreSQL owners (for example
// adminpostgres.Maintenance) and fail before any SQLite or local DuckLake
// dependency is opened when no native owner exists.
var ErrProductionUnavailable = errors.New("legacy offline admin operations are unavailable in production")

// loadNonProductionConfig is the single admission gate for this composition
// layer. Evaluation is the one explicit exception: it deliberately enables
// production runtime checks while retaining an isolated, loopback-only local
// authority. Keep the gate immediately after environment decoding so ordinary
// production callers cannot construct an extension loader, SQLite store, or
// filesystem-backed adapter before being rejected.
func loadNonProductionConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, err
	}
	if cfg.Production && !cfg.EvaluationMode {
		return config.Config{}, ErrProductionUnavailable
	}
	return cfg, nil
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

func (Operations) BootstrapPhysicalPool(ctx context.Context, request adminoffline.PhysicalPoolBootstrapRequest, out io.Writer) error {
	service, err := newService()
	if err != nil {
		return err
	}
	return service.BootstrapPhysicalPool(ctx, request, out)
}

const (
	qualificationIsolationBoundary  = "qualification"
	qualificationRetentionAuthority = "qualification"
	qualificationTenant             = "qualification"
	qualificationRegion             = "local"
)

// QualificationPoolArtifacts runs the complete local shared-pool conformance
// probe and returns only non-secret, content-addressed artifacts. It is safe
// to invoke against a production-shaped/native configuration: this export
// deliberately loads configuration and extension supply only, never opens the
// legacy SQLite platform store, reads an instance ID, or applies admission.
func (Operations) QualificationPoolArtifacts(ctx context.Context) (adminoffline.QualificationPoolArtifacts, error) {
	cfg, err := config.Load()
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Qualification must execute with the target-reviewed extension supply. The
	// loader selects the immutable packaged supply in production images and
	// validates any explicit local supply before DuckDB is opened.
	supply, err := extensionsupplyloader.Load(ctx, cfg)
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("load qualification extension supply: %w", err)
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
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("resolve qualification pool storage: %w", err)
	}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: storageLocation, StorageNamespace: "delivery", Region: qualificationRegion,
		Tenant: qualificationTenant, EncryptionDomain: "local",
		IsolationBoundary: qualificationIsolationBoundary, RetentionAuthority: qualificationRetentionAuthority,
		RetentionPolicy: physicalpool.RetentionPolicy{
			ReaderGracePeriodSeconds: 1800,
			OrphanGracePeriodSeconds: 3600,
			BuildGracePeriodSeconds:  3600,
		},
		Compatibility: tuple,
	})
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("build qualification pool identity: %w", err)
	}
	// Keep probe state private and disposable. The configured runtime directory
	// is target-local (/var/lib/leapview/... in the production image), while the
	// generated identity points at the configured DuckLake data location.
	if err := securefs.EnsurePrivateDir(cfg.RuntimeDir()); err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("prepare qualification runtime directory: %w", err)
	}
	probeRoot, err := os.MkdirTemp(cfg.RuntimeDir(), "qualification-delivery-conformance-")
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("create qualification conformance directory: %w", err)
	}
	defer os.RemoveAll(probeRoot)
	evidence, err := analyticsducklake.RunLocalPoolConformance(ctx, probeRoot, tuple, supply)
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("run qualification local physical-pool conformance: %w", err)
	}
	if err := (analyticsducklake.SharedPoolConformance{Compatibility: tuple}).ValidateEvidence(evidence); err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("validate qualification conformance evidence: %w", err)
	}
	artifacts := adminoffline.QualificationPoolArtifacts{
		SchemaVersion: adminoffline.QualificationPoolArtifactsSchemaVersion,
		Pool:          pool.Identity,
		Evidence: physicalpool.EvidenceArtifact{
			SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion,
			Evidence:      evidence,
		},
	}
	if _, err := adminoffline.MarshalQualificationPoolArtifacts(artifacts); err != nil {
		return adminoffline.QualificationPoolArtifacts{}, err
	}
	return artifacts, nil
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
		HomeDir:        cfg.HomeDir,
		DBPath:         cfg.DBPath(),
		Environment:    cfg.Environment,
		Production:     cfg.Production,
		BootstrapEmail: cfg.BootstrapEmail,
	}
	return adminoffline.New(normalized, adminoffline.Dependencies{
		Locker:      instanceLocker{home: cfg.HomeDir},
		State:       instanceState{dbPath: cfg.DBPath()},
		Initializer: instanceInitializer{dbPath: cfg.DBPath()},
		Recovery: credentialRecovery{
			path: filepath.Join(cfg.HomeDir, adminoffline.CredentialRecoveryFileName),
		},
		PhysicalPool: physicalPoolBootstrap{dbPath: cfg.DBPath(), s3: gcadapter.S3Config{
			Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID,
			SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken,
			Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionAdmission,
		}},
	}), nil
}
