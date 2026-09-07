package adminpostgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/extensionsupplyloader"
	"github.com/flidai/leapview/internal/app/poolcompatibility"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const (
	qualificationIsolationBoundary  = "qualification"
	qualificationRetentionAuthority = "qualification"
	qualificationTenant             = "qualification"
	qualificationRegion             = "local"
)

// QualificationPoolArtifacts runs the reviewed local shared-pool conformance
// probe used by the compose qualification controller. It returns only
// non-secret, content-addressed artifacts; no control-plane state is opened
// or mutated by this export.
func (o Operations) QualificationPoolArtifacts(ctx context.Context) (adminoffline.QualificationPoolArtifacts, error) {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, err
	}
	// Qualification is an offline lifecycle probe and may run without a
	// request-scoped context before it opens local resources.
	if ctx == nil {
		ctx = context.Background()
	}
	supply, err := extensionsupplyloader.Load(ctx, cfg)
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("load qualification extension supply: %w", err)
	}
	tuple, err := poolcompatibility.LocalPool(ctx, supply)
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, fmt.Errorf("resolve qualification pool compatibility: %w", err)
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
