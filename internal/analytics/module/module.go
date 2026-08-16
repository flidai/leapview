package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	queryauditsqlite "github.com/flidai/leapview/internal/analytics/queryaudit/sqlite"
	"github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	analyticssqlite "github.com/flidai/leapview/internal/analytics/sqlite"
	storagemaintenance "github.com/flidai/leapview/internal/servingstate/retention"
	"github.com/prometheus/client_golang/prometheus"
)

type CredentialMode string

const (
	CredentialModeNonSecret              CredentialMode = "non_secret"
	CredentialModeDevelopmentEnvironment CredentialMode = "development_environment"
)

type Config struct {
	Database              *sql.DB
	TargetCredentials     TargetCredentialConfig
	CredentialMode        CredentialMode
	CredentialTargetID    string
	CredentialEnvironment string
	RootDir               string
	CatalogPath           string
	DataPath              string
	MaxConnections        int
	MemoryMaxBytes        int64
	TempMaxBytes          int64
	MaxThreads            int
	TempDir               string
	RuntimeCacheEntries   int
	RuntimeCacheBytes     int64
	WorkspaceCacheEntries int
	WorkspaceCacheBytes   int64
	NodeCacheEntries      int
	NodeCacheBytes        int64
}

type Resources interface {
	resource.Provider
	resource.SessionProvider
}

func NewSurface(environment *analyticsducklake.Environment, cache *resultcache.Pool) *Module {
	return &Module{environment: environment, cache: cache}
}

// NewQueryAuditSurface constructs the analytics-owned control-plane adapter
// without opening the analytical runtime. It is useful to compose API-only
// surfaces and focused tests.
func NewQueryAuditSurface(database *sql.DB) *Module {
	if database == nil {
		return &Module{}
	}
	return &Module{queryAudit: queryauditsqlite.NewRepository(database)}
}

type QueryAuditSurface struct {
	repository queryaudit.Repository
}

func BuildQueryAuditSurface(database *sql.DB) *QueryAuditSurface {
	if database == nil {
		return &QueryAuditSurface{}
	}
	return &QueryAuditSurface{repository: queryauditsqlite.NewRepository(database)}
}

func (s *QueryAuditSurface) Provider() func() (queryaudit.Reader, error) {
	return func() (queryaudit.Reader, error) {
		if s == nil {
			return nil, nil
		}
		return s.repository, nil
	}
}

func (s *QueryAuditSurface) Recorder() queryaudit.Recorder {
	if s == nil {
		return nil
	}
	return s.repository
}

type Module struct {
	environment                  *analyticsducklake.Environment
	cache                        *resultcache.Pool
	queryAudit                   queryaudit.Repository
	connectionBindings           connectionbinding.BindingCatalog
	credentials                  analyticsduckdb.CredentialResolver
	targetResolvers              connectionbinding.ResolverSet
	targetID                     string
	targetEnvironment            string
	targetClass                  connectionbinding.TargetClass
	connectionFactory            connectionbinding.RuntimePoolFactory
	connectionPoolsMu            sync.Mutex
	connectionPools              *connectionbinding.PoolDirectory
	candidateRuntimeBindings     candidateRuntimeBindingRegistry
	activeRuntimeBindingEvidence ActiveRuntimeBindingEvidenceSource
}

func Build(ctx context.Context, config Config) (*Module, error) {
	credentials, err := buildCredentialResolver(config)
	if err != nil {
		return nil, err
	}
	targetResolvers, err := buildTargetResolvers(config.TargetCredentials)
	if err != nil {
		return nil, err
	}
	if config.CredentialMode == CredentialModeDevelopmentEnvironment {
		development, err := buildProcessDevelopmentTargetResolver(
			config.CredentialTargetID,
			config.CredentialEnvironment,
		)
		if err != nil {
			return nil, err
		}
		targetResolvers.Environment = development
	}
	environment, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir: config.RootDir, CatalogPath: config.CatalogPath, DataPath: config.DataPath,
		MaxConnections: config.MaxConnections, MemoryMaxBytes: config.MemoryMaxBytes,
		TempMaxBytes: config.TempMaxBytes, MaxThreads: config.MaxThreads, TempDir: config.TempDir,
	})
	if err != nil {
		return nil, err
	}
	cache, err := resultcache.New(resultcache.Limits{
		RuntimeEntries: config.RuntimeCacheEntries, RuntimeBytes: config.RuntimeCacheBytes,
		WorkspaceEntries: config.WorkspaceCacheEntries, WorkspaceBytes: config.WorkspaceCacheBytes,
		NodeEntries: config.NodeCacheEntries, NodeBytes: config.NodeCacheBytes,
	})
	if err != nil {
		_ = environment.Close()
		return nil, err
	}
	var queryAudit queryaudit.Repository
	var connectionBindings connectionbinding.BindingCatalog
	if config.Database != nil {
		queryAudit = queryauditsqlite.NewRepository(config.Database)
		connectionBindings = analyticssqlite.NewConnectionBindingRepository(config.Database)
	}
	targetClass := connectionbinding.TargetProduction
	if config.CredentialMode == CredentialModeDevelopmentEnvironment {
		targetClass = connectionbinding.TargetDevelopment
	}
	memoryMax := config.MemoryMaxBytes
	if memoryMax <= 0 {
		memoryMax = 128 << 20
	}
	tempMax := config.TempMaxBytes
	if tempMax <= 0 {
		tempMax = 64 << 20
	}
	maxThreads := config.MaxThreads
	if maxThreads <= 0 {
		maxThreads = 1
	}
	connectionFactory, err := analyticsduckdb.NewTargetRuntimePoolFactory(
		analyticsduckdb.TargetRuntimePoolFactoryConfig{
			Open: analyticsduckdb.NewIsolatedTargetRuntimeOpener(),
			Limits: analyticsduckdb.TargetRuntimeLimits{
				MemoryMaxBytes: memoryMax, TempMaxBytes: tempMax, MaxThreads: maxThreads,
			},
			RequireTLS: targetClass == connectionbinding.TargetProduction,
		},
	)
	if err != nil {
		_ = cache.Close()
		_ = environment.Close()
		return nil, err
	}
	return &Module{
		environment: environment, cache: cache, queryAudit: queryAudit,
		connectionBindings: connectionBindings,
		credentials:        credentials, targetResolvers: targetResolvers,
		targetID: config.CredentialTargetID, targetEnvironment: config.CredentialEnvironment,
		targetClass: targetClass, connectionFactory: connectionFactory,
	}, nil
}

func (m *Module) NewConnectionAdministration(
	config ConnectionAdministrationConfig,
) (*connectionbinding.Administration, error) {
	if m == nil || m.connectionBindings == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	if err := requireConnectionBindingAuditSinks(
		config.Audit,
		config.AdministrationAudit,
	); err != nil {
		return nil, err
	}
	if config.Pools == nil {
		pools, err := m.ensureConnectionPools(
			config.Now,
			config.Audit,
			config.RefreshTimeout,
			config.MaxConcurrent,
		)
		if err != nil {
			return nil, err
		}
		config.Pools = pools
	}
	authorize := config.Authorize
	return connectionbinding.NewAdministration(connectionbinding.AdministrationConfig{
		Repository:  m.connectionBindings,
		EnsureScope: config.EnsureScope,
		Authorize: func(
			ctx context.Context,
			actor string,
			permission connectionbinding.AdministrationPermission,
			binding connectionbinding.TargetBinding,
		) error {
			if binding.TargetID != m.targetID || binding.Scope.Environment != m.targetEnvironment {
				return connectionbinding.ErrUnauthorizedBinding
			}
			return authorize(ctx, actor, permission, binding)
		},
		Dependencies: config.Dependencies, Pools: config.Pools,
		Audit: config.AdministrationAudit, Now: config.Now,
	})
}

func (m *Module) NewRuntimeBindingLeaser(
	config RuntimeBindingLeaserConfig,
) (*connectionbinding.RuntimeBindingLeaser, error) {
	if m == nil || m.connectionBindings == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	if err := requireConnectionRotationAuditSink(config.Audit); err != nil {
		return nil, err
	}
	pools, err := m.ensureConnectionPools(
		config.Now,
		config.Audit,
		config.RefreshTimeout,
		config.MaxConcurrent,
	)
	if err != nil {
		return nil, err
	}
	return connectionbinding.NewRuntimeBindingLeaser(
		connectionbinding.RuntimeBindingLeaserConfig{
			Bindings: m.connectionBindings, Pools: pools, Authorize: config.Authorize,
		},
	)
}

func (m *Module) ensureConnectionPools(
	now func() time.Time,
	audit connectionbinding.RotationAuditRecorder,
	refreshTimeout time.Duration,
	maxConcurrent int,
) (*connectionbinding.PoolDirectory, error) {
	if m == nil || m.connectionBindings == nil || m.connectionFactory == nil || now == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	if refreshTimeout <= 0 {
		refreshTimeout = 30 * time.Second
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	m.connectionPoolsMu.Lock()
	defer m.connectionPoolsMu.Unlock()
	if m.connectionPools != nil {
		return m.connectionPools, nil
	}
	pools, err := connectionbinding.NewPoolDirectory(connectionbinding.PoolDirectoryConfig{
		Build: func(binding connectionbinding.TargetBinding) (*connectionbinding.PoolManager, error) {
			if binding.TargetID != m.targetID ||
				binding.Scope.Environment != m.targetEnvironment ||
				binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
				return nil, connectionbinding.ErrUnauthorizedBinding
			}
			resolver, err := connectionbinding.SelectResolver(
				connectionbinding.ResolverSelection{
					TargetID: binding.TargetID, Environment: binding.Scope.Environment,
					TargetClass: m.targetClass, Kind: m.connectionResolverKind(),
				},
				m.targetResolvers,
			)
			if err != nil {
				return nil, err
			}
			return connectionbinding.NewPoolManager(connectionbinding.PoolManagerConfig{
				Binding: binding, Resolver: resolver, Factory: m.connectionFactory,
				Store: m.connectionBindings, Audit: audit,
				Now: now, StaleAfter: 15 * time.Minute,
			})
		},
		RefreshTimeout: refreshTimeout, MaxConcurrent: maxConcurrent,
	})
	if err != nil {
		return nil, err
	}
	m.connectionPools = pools
	return pools, nil
}

func (m *Module) connectionResolverKind() connectionbinding.ResolverKind {
	if m != nil && m.targetResolvers.Infisical != nil {
		return connectionbinding.ResolverInfisical
	}
	if m != nil && m.targetClass == connectionbinding.TargetDevelopment &&
		m.targetResolvers.Environment != nil {
		return connectionbinding.ResolverEnvironment
	}
	return connectionbinding.ResolverInfisical
}

func buildCredentialResolver(config Config) (analyticsduckdb.CredentialResolver, error) {
	switch config.CredentialMode {
	case "", CredentialModeNonSecret:
		return analyticsduckdb.NonSecretCredentialResolver{}, nil
	case CredentialModeDevelopmentEnvironment:
		selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
			TargetID: config.CredentialTargetID, Environment: config.CredentialEnvironment,
			TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
		})
		if err != nil {
			return nil, err
		}
		return analyticsduckdb.NewDevelopmentEnvironmentCredentialResolver(selection)
	default:
		return nil, fmt.Errorf("%w: unsupported analytics credential mode", connectionbinding.ErrInvalidBinding)
	}
}

func (m *Module) WorkspaceMaterializer() analyticsmaterialization.Executor {
	if m == nil || m.environment == nil {
		return nil
	}
	return duckDBWorkspaceMaterializer{environment: m.environment, credentials: m.credentials, module: m}
}

func (m *Module) RetentionSnapshots() storagemaintenance.SnapshotMaintenance {
	if m == nil {
		return nil
	}
	return m.environment
}

func (m *Module) AdminResources() Resources {
	if m == nil || m.environment == nil {
		return nil
	}
	return m.environment
}

func (m *Module) Collector() prometheus.Collector {
	if m == nil {
		return NewCollector(nil, nil)
	}
	return NewCollector(m.environment, m.cache)
}

func NewWorkspaceMaterializer(environment *analyticsducklake.Environment) analyticsmaterialization.Executor {
	return NewWorkspaceMaterializerWithCredentials(environment, analyticsduckdb.NonSecretCredentialResolver{})
}

func NewWorkspaceMaterializerWithCredentials(
	environment *analyticsducklake.Environment,
	credentials analyticsduckdb.CredentialResolver,
) analyticsmaterialization.Executor {
	if environment == nil {
		return nil
	}
	if credentials == nil {
		credentials = analyticsduckdb.NonSecretCredentialResolver{}
	}
	return duckDBWorkspaceMaterializer{environment: environment, credentials: credentials}
}

func (m *Module) QueryAuditReader() queryaudit.Reader {
	if m == nil {
		return nil
	}
	return m.queryAudit
}

func (m *Module) QueryAuditProvider() func() (queryaudit.Reader, error) {
	return func() (queryaudit.Reader, error) {
		return m.QueryAuditReader(), nil
	}
}

func (m *Module) QueryAuditRecorder() queryaudit.Recorder {
	if m == nil {
		return nil
	}
	return m.queryAudit
}

func (m *Module) Healthy() error {
	if m == nil || m.environment == nil {
		return nil
	}
	return m.environment.Healthy()
}

func (m *Module) Fatal() <-chan struct{} {
	if m == nil || m.environment == nil {
		return nil
	}
	return m.environment.Fatal()
}

func (m *Module) Close() error {
	if m == nil {
		return nil
	}
	var errs []error
	m.connectionPoolsMu.Lock()
	connectionPools := m.connectionPools
	m.connectionPools = nil
	m.connectionPoolsMu.Unlock()
	if connectionPools != nil {
		errs = append(errs, connectionPools.Close())
	}
	if m.cache != nil {
		errs = append(errs, m.cache.Close())
	}
	if m.environment != nil {
		errs = append(errs, m.environment.Close())
	}
	return errors.Join(errs...)
}
