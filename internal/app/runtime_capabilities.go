package app

// This file contains the capability-specific construction seams used by the
// process composition root. Each builder validates its required inputs before
// invoking a capability package, keeping nil/partial bundles from becoming a
// partially governed runtime.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/brand"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	"github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

type analyticsCapabilityBundle struct {
	Module *analyticsmodule.Module
}

type accessCapabilityBundle struct {
	Module                 *accessmodule.Module
	Repository             access.Repository
	AuthorizationInstaller runtimehostmodule.AuthorizationSnapshotInstaller
}

type workloadCapabilityBundle struct {
	Controller workloadControl
	Jobs       *jobsmodule.Module
}

type analyticsCapabilityConfig struct {
	ConnectionBindings connectionbinding.BindingCatalog
	QueryAuditStore    analyticsmodule.QueryAuditStore
	Production         bool
	CredentialMode     analyticsmodule.CredentialMode
	CredentialTarget   string
	CredentialProject  projectgraph.ResourceID
	Environment        string
	TargetCredentials  analyticsmodule.TargetCredentialConfig
	RootDir            string
	ExtensionSupply    *extensionsupply.Supply
	CatalogPath        string
	DataPath           string
	MaxConnections     int
	MemoryMaxBytes     int64
	TempMaxBytes       int64
	MaxThreads         int
	TempDir            string
	DisableProcessEnv  bool
	RuntimeCacheItems  int
	RuntimeCacheBytes  int64
	NodeCacheItems     int
	NodeCacheBytes     int64
}

func buildAnalyticsCapability(ctx context.Context, cfg analyticsCapabilityConfig) (analyticsCapabilityBundle, error) {
	if cfg.ConnectionBindings == nil {
		return analyticsCapabilityBundle{}, errors.New("analytics persistence is required")
	}
	if cfg.ExtensionSupply == nil {
		return analyticsCapabilityBundle{}, errors.New("analytics extension supply is required")
	}
	module, err := analyticsmodule.Build(ctx, analyticsmodule.Config{
		ConnectionBindings: cfg.ConnectionBindings, QueryAuditStore: cfg.QueryAuditStore,
		CredentialMode: cfg.CredentialMode, Production: cfg.Production,
		CredentialTargetID: cfg.CredentialTarget, CredentialProjectID: cfg.CredentialProject, CredentialEnvironment: cfg.Environment,
		TargetCredentials: cfg.TargetCredentials,
		RootDir:           cfg.RootDir, ExtensionAdmission: cfg.ExtensionSupply,
		CatalogPath: cfg.CatalogPath, DataPath: cfg.DataPath,
		MaxConnections: cfg.MaxConnections, MemoryMaxBytes: cfg.MemoryMaxBytes,
		TempMaxBytes: cfg.TempMaxBytes, MaxThreads: cfg.MaxThreads, TempDir: cfg.TempDir,
		DisableProcessEnvironment: cfg.DisableProcessEnv,
		RuntimeCacheEntries:       cfg.RuntimeCacheItems, RuntimeCacheBytes: cfg.RuntimeCacheBytes,
		NodeCacheEntries: cfg.NodeCacheItems, NodeCacheBytes: cfg.NodeCacheBytes,
	})
	if err != nil {
		return analyticsCapabilityBundle{}, fmt.Errorf("build analytics capability: %w", err)
	}
	return analyticsCapabilityBundle{Module: module}, nil
}

type accessCapabilityConfig struct {
	Persistence      *accessmodule.Persistence
	Production       bool
	Auth             accessmodule.AuthConfig
	Assets           staticasset.Resolver
	AvatarBlobs      accessmodule.AvatarBlobStore
	PublicURL        string
	InstanceID       string
	MCPIssuerURL     string
	CurrentProject   func(context.Context) (projectgraph.ResourceID, error)
	AuthoringProject func(context.Context) (projectgraph.ResourceID, error)
}

func buildAccessCapability(ctx context.Context, cfg accessCapabilityConfig) (accessCapabilityBundle, error) {
	if cfg.Persistence == nil {
		return accessCapabilityBundle{}, errors.New("access persistence is required")
	}
	if cfg.CurrentProject == nil {
		return accessCapabilityBundle{}, errors.New("access current-project resolver is required")
	}
	module, err := accessmodule.Build(ctx, accessmodule.Config{
		Persistence: cfg.Persistence,
		Production:  cfg.Production,
		Auth:        cfg.Auth, Assets: cfg.Assets, AvatarBlobs: cfg.AvatarBlobs,
		PublicURL: cfg.PublicURL, InstanceID: cfg.InstanceID, MCPIssuerURL: cfg.MCPIssuerURL,
		CurrentProjectID:   cfg.CurrentProject,
		AuthoringProjectID: cfg.AuthoringProject,
		Presentation:       page.Presentation{ProductName: brand.Name, FaviconPath: brand.FaviconPath},
	})
	if err != nil {
		return accessCapabilityBundle{}, fmt.Errorf("build access capability: %w", err)
	}
	repository, err := accessRepository(module)
	if err != nil {
		return accessCapabilityBundle{}, err
	}
	installer, err := authorizationSnapshotInstaller(repository)
	if err != nil {
		return accessCapabilityBundle{}, err
	}
	return accessCapabilityBundle{Module: module, Repository: repository, AuthorizationInstaller: installer}, nil
}

type workloadCapabilityConfig struct {
	Persistence  *jobsmodule.Persistence
	Workload     workloadmodule.Config
	Production   bool
	LeaseTimeout time.Duration
	Logger       *slog.Logger
}

func buildWorkloadCapability(ctx context.Context, cfg workloadCapabilityConfig) (workloadCapabilityBundle, error) {
	if cfg.Persistence == nil {
		return workloadCapabilityBundle{}, errors.New("jobs persistence is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	controller, err := workloadmodule.Build(ctx, cfg.Workload)
	if err != nil {
		return workloadCapabilityBundle{}, fmt.Errorf("build workload capability: %w", err)
	}
	jobs, err := jobsmodule.Build(ctx, jobsmodule.Config{
		Persistence: cfg.Persistence,
		Production:  cfg.Production,
		Admission:   workloadmodule.JobAdmitter(controller), LeaseTimeout: cfg.LeaseTimeout, Logger: cfg.Logger,
	})
	if err != nil {
		controller.Close()
		return workloadCapabilityBundle{}, fmt.Errorf("build jobs capability: %w", err)
	}
	return workloadCapabilityBundle{Controller: controller, Jobs: jobs}, nil
}
