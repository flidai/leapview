package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/apiadapter"
	"github.com/flidai/leapview/internal/manageddata/binding"
	"github.com/flidai/leapview/internal/manageddata/control"
	manageddatahttp "github.com/flidai/leapview/internal/manageddata/http"
	"github.com/flidai/leapview/internal/manageddata/maintenance"
	maintenancesqlite "github.com/flidai/leapview/internal/manageddata/maintenance/sqlite"
	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	"github.com/flidai/leapview/internal/manageddata/runtimeview"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
	manageddatasqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/manageddata/storage"
	managedfilesystem "github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	managedtus "github.com/flidai/leapview/internal/manageddata/storage/tus"
	"github.com/flidai/leapview/internal/platform/filesystem"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

const (
	managedDataTusPath             = "/upload-protocols/tus"
	managedDataS3MultipartTemplate = "/api/v1/projects/{project}/connections/{connection}/upload-sessions/{uploadSession}/s3-multipart-uploads"
)

type managedDataStorage struct {
	blobs        storage.BlobStore
	inventory    storage.BlobInventory
	transport    control.Transport
	tusEngine    storage.ResumableUploadEngine
	materializer manageddata.RevisionMaterializer
	runtimeCache *runtimeview.Cache
	tus          http.Handler
	s3           *manageds3.Store
}

// Module owns managed-data adapter construction for one application process.
// Its exported methods are deliberately named ports instead of exposing the
// internal storage bundle.
type Module struct {
	handler             *manageddatahttp.Handler
	uploads             *control.Service
	finalizer           manageddatahttp.UploadCoordinator
	multipart           s3multipart.Coordinator
	multipartService    *s3multipart.Service
	materializer        manageddata.RevisionMaterializer
	tus                 http.Handler
	maintenance         Maintenance
	maintenanceWorker   *maintenanceWorker
	jobs                JobStore
	workflow            jobplatform.WorkflowRecorder
	eventMu             sync.Mutex
	bindings            *binding.Binder
	runtimeResolver     *manageddataresolver.Resolver
	metadata            DeploymentMetadata
	finalizeExecution   apigencommand.AsyncExecutionContract
	currentPrincipal    func(*http.Request) (Principal, bool)
	authorizeConnection manageddatahttp.ConnectionAuthorizer
	resolveTusTarget    func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error)
}

type repository interface {
	control.Repository
	s3multipart.Repository
	apiadapter.Repository
	binding.Repository
	manageddataresolver.Repository
	DeploymentMetadata
}

type JobStore interface {
	Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error)
	AppendEvent(context.Context, string, string, string, []byte) (jobs.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error)
}

type Principal struct {
	ID        string
	DevBypass bool
}

// ConnectionAuthorizer is the module-owned authorization port. The HTTP
// adapter receives a converted copy at construction time, keeping transport
// types out of the module configuration contract.
type ConnectionAuthorizer func(context.Context, string, string, string, access.Capability) (bool, error)

type Config struct {
	Database            *sql.DB
	Disabled            bool
	Product             ProductConfig
	Worker              MaintenanceWorkerConfig
	MaxJSONBodyBytes    int64
	Environment         string
	CurrentPrincipal    func(*http.Request) (Principal, bool)
	AuthorizeConnection ConnectionAuthorizer
	Jobs                JobStore
	Workflow            jobplatform.WorkflowRecorder
	ServingStates       ServingStateReader
	RecordAudit         func(context.Context, CommandAuditEvent) error
}

type ProductConfig struct {
	Backend           string
	Dir               string
	MaxFiles          int
	MaxFileBytes      int64
	MaxRevisionBytes  int64
	MinFreeBytes      int64
	UploadSessionTTL  time.Duration
	GCGracePeriod     time.Duration
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3SessionToken    string
	S3PathStyle       bool
	S3Endpoint        string
	S3Bucket          string
	S3Prefix          string
}

type ServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
}

func Build(ctx context.Context, cfg Config) (*Module, error) {
	finalizeExecution, err := loadFinalizeUploadExecutionContract()
	if err != nil {
		return nil, err
	}
	var commandAudit func(context.Context, manageddatahttp.CommandAuditInput) error
	if !cfg.Disabled {
		commandAudit, err = buildManagedDataCommandAuditRecorder(cfg.RecordAudit)
		if err != nil {
			return nil, err
		}
	}
	currentPrincipal := func(r *http.Request) (manageddatahttp.Principal, bool) {
		if cfg.CurrentPrincipal == nil {
			return manageddatahttp.Principal{}, false
		}
		principal, ok := cfg.CurrentPrincipal(r)
		return manageddatahttp.Principal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
	}
	if cfg.Disabled {
		module := &Module{jobs: cfg.Jobs, currentPrincipal: cfg.CurrentPrincipal, authorizeConnection: manageddatahttp.ConnectionAuthorizer(cfg.AuthorizeConnection), maintenanceWorker: newMaintenanceWorker(nil, cfg.Worker), finalizeExecution: finalizeExecution}
		module.handler = manageddatahttp.NewHandler(manageddatahttp.Options{
			CurrentPrincipal: currentPrincipal, MaxJSONBodyBytes: cfg.MaxJSONBodyBytes,
			Environment: cfg.Environment, AuthorizeConnection: manageddatahttp.ConnectionAuthorizer(cfg.AuthorizeConnection),
			RecordCommandAudit: commandAudit, Logger: cfg.Worker.Logger,
		})
		return module, nil
	}
	if cfg.Database == nil {
		return nil, errors.New("managed-data database is required")
	}
	repository := manageddatasqlite.NewRepositoryWithWorkflow(cfg.Database, cfg.Workflow)
	services, err := newManagedDataStorage(ctx, cfg.Product)
	if err != nil {
		return nil, err
	}
	uploads, err := newManagedDataControl(repository, services, cfg.Product)
	if err != nil {
		return nil, err
	}
	collector, err := newManagedDataCollector(cfg.Database, services, cfg.Product)
	if err != nil {
		return nil, err
	}
	runtimeCollector, err := newManagedDataRuntimeCollector(services, cfg.Product)
	if err != nil {
		return nil, err
	}
	var multipart s3multipart.Coordinator
	var multipartService *s3multipart.Service
	if services.s3 != nil {
		multipartService, err = s3multipart.New(repository, services.s3, s3multipart.Config{Backend: "s3"})
		if err != nil {
			return nil, err
		}
		multipart = multipartService
	}
	apiRepository, err := apiadapter.New(repository)
	if err != nil {
		return nil, err
	}
	bindings, err := binding.New(repository)
	if err != nil {
		return nil, err
	}
	var runtimeResolver *manageddataresolver.Resolver
	if cfg.ServingStates != nil {
		runtimeResolver, err = manageddataresolver.New(repository, cfg.ServingStates, services.materializer)
		if err != nil {
			return nil, err
		}
	}
	module := &Module{
		uploads:          uploads,
		finalizer:        uploads,
		multipart:        multipart,
		multipartService: multipartService,
		materializer:     services.materializer,
		tus:              services.tus,
		maintenance: Maintenance{
			uploads: uploads, multipart: multipartService, uploadTTL: cfg.Product.UploadSessionTTL,
			collector: collector, runtime: runtimeCollector,
		},
		jobs: cfg.Jobs, workflow: cfg.Workflow, bindings: bindings, runtimeResolver: runtimeResolver,
		currentPrincipal: cfg.CurrentPrincipal, authorizeConnection: manageddatahttp.ConnectionAuthorizer(cfg.AuthorizeConnection),
		metadata: metadataReader{repository: repository}, finalizeExecution: finalizeExecution,
	}
	module.resolveTusTarget = newTusTargetResolver(services.tusEngine, repository)
	module.handler = manageddatahttp.NewHandler(manageddatahttp.Options{
		Repository: apiRepository, Uploads: uploads, Multipart: multipart,
		CurrentPrincipal: currentPrincipal, AuthorizeConnection: manageddatahttp.ConnectionAuthorizer(cfg.AuthorizeConnection), Environment: cfg.Environment,
		BeginFinalize: module.beginFinalize, RecordUploadCreated: module.recordUploadCreated,
		AbortUpload: module.abortUpload, RecordCommandAudit: commandAudit, Logger: cfg.Worker.Logger,
	})
	module.maintenanceWorker = newMaintenanceWorker(module.maintenance, cfg.Worker)
	if err := validateFinalizeUploadJobHandlers(finalizeExecution, module.JobHandlers(cfg.Jobs)); err != nil {
		return nil, err
	}
	return module, nil
}

func (m *Module) HasFinalizeJobs() bool { return m.finalizer != nil }

func (m *Module) SupportsS3Multipart() bool { return m != nil && m.multipart != nil }

func (m *Module) Materializer() manageddata.RevisionMaterializer { return m.materializer }

type BindingValidation interface {
	AfterArtifactValidation(context.Context, servingstate.State, servingstate.Validation) error
	ValidateServingStatePins(context.Context, projectgraph.ServingIdentity, map[projectgraph.ResourceID]string) error
	ResolveCandidatePins(context.Context, projectgraph.ResourceID, []projectgraph.ResourceID, string) (map[projectgraph.ResourceID]string, error)
}

func (m *Module) BindingValidation() BindingValidation {
	if m == nil {
		return nil
	}
	return m.bindings
}

type RuntimeResolver interface {
	ResolveManagedData(context.Context, projectgraph.ServingIdentity) (manageddataresolver.Resolution, error)
}

func (m *Module) RuntimeResolution() RuntimeResolver {
	if m == nil {
		return nil
	}
	if m.runtimeResolver == nil {
		return disabledRuntimeResolver{}
	}
	return m.runtimeResolver
}

// disabledRuntimeResolver is the explicit no-op capability used when the
// managed-data feature is disabled. Runtime-host treats an empty resolution as
// having no managed-data roots, so callers never need nil checks.
type disabledRuntimeResolver struct{}

func (disabledRuntimeResolver) ResolveManagedData(context.Context, projectgraph.ServingIdentity) (manageddataresolver.Resolution, error) {
	return manageddataresolver.Resolution{Roots: map[projectgraph.ResourceID]string{}}, nil
}

type DeploymentMetadata interface {
	CollectionByID(context.Context, projectgraph.ResourceID) (manageddata.Collection, error)
	RevisionByID(context.Context, manageddata.RevisionID) (manageddata.Revision, error)
}

type metadataReader struct {
	repository repository
}

func (r metadataReader) CollectionByID(ctx context.Context, id projectgraph.ResourceID) (manageddata.Collection, error) {
	return r.repository.CollectionByID(ctx, id)
}

func (r metadataReader) RevisionByID(ctx context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	return r.repository.RevisionByID(ctx, id)
}

func (m *Module) DeploymentMetadata() DeploymentMetadata {
	if m == nil {
		return nil
	}
	return m.metadata
}

func (m *Module) TusHandler() http.Handler {
	if m == nil || m.tus == nil {
		return nil
	}
	return TusProtocolHandler(m.tus)
}

func TusProtocolHandler(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			// Upload chunks are capacity protected and session expiry bounds
			// abandoned bodies, so they must not inherit the general API read
			// deadline.
			_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
			next.ServeHTTP(w, r)
		case http.MethodOptions, http.MethodHead, http.MethodDelete:
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "OPTIONS, HEAD, PATCH, DELETE")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		}
	})
}

func (m *Module) Start(ctx context.Context) {
	if m != nil {
		m.maintenanceWorker.Start(ctx)
	}
}

func (m *Module) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return m.maintenanceWorker.Stop(ctx)
}

func (m *Module) HTTP() *manageddatahttp.Handler {
	if m == nil {
		return nil
	}
	return m.handler
}

func (m *Module) SetAuthorizeConnection(authorizer ConnectionAuthorizer) {
	if m != nil && m.handler != nil {
		m.handler.SetAuthorizeConnection(manageddatahttp.ConnectionAuthorizer(authorizer))
	}
}

func newManagedDataStorage(ctx context.Context, cfg ProductConfig) (managedDataStorage, error) {
	root, err := filepath.Abs(strings.TrimSpace(cfg.Dir))
	if err != nil || strings.TrimSpace(cfg.Dir) == "" {
		return managedDataStorage{}, fmt.Errorf("%w: managed-data directory is required", storage.ErrInvalid)
	}
	if err := securefs.EnsurePrivateDir(root); err != nil {
		return managedDataStorage{}, err
	}

	var result managedDataStorage
	switch strings.TrimSpace(cfg.Backend) {
	case "local":
		blobs, err := managedfilesystem.New(filepath.Join(root, "objects"))
		if err != nil {
			return managedDataStorage{}, err
		}
		engine, err := managedtus.New(filepath.Join(root, "uploads"), blobs)
		if err != nil {
			return managedDataStorage{}, err
		}
		transport, err := control.NewTusTransport("local", managedDataTusPath, engine)
		if err != nil {
			return managedDataStorage{}, err
		}
		handler, err := engine.HTTPHandler(managedtus.HTTPConfig{BasePath: managedDataTusPath, MaxSize: cfg.MaxFileBytes})
		if err != nil {
			return managedDataStorage{}, err
		}
		capacity, err := maintenance.NewCapacityChecker(root, cfg.MinFreeBytes)
		if err != nil {
			return managedDataStorage{}, err
		}
		result.blobs, result.transport, result.tusEngine, result.materializer, result.tus = blobs, transport, engine, blobs, capacityProtectedTus(handler, capacity)
	case "s3":
		store, err := newManagedDataS3Store(ctx, cfg)
		if err != nil {
			return managedDataStorage{}, err
		}
		transport, err := control.NewS3MultipartTransport("s3", control.S3MultipartDescription{
			CreateEndpoint:  managedDataS3MultipartTemplate,
			MinimumPartSize: s3multipart.MinimumPartSize,
			MaximumPartSize: s3multipart.MaximumPartSize,
			MaximumParts:    s3multipart.MaximumParts,
		})
		if err != nil {
			return managedDataStorage{}, err
		}
		cache, err := runtimeview.New(filepath.Join(root, "runtime"), store)
		if err != nil {
			return managedDataStorage{}, err
		}
		result.blobs, result.transport, result.materializer, result.runtimeCache, result.s3 = store, transport, cache, cache, store
	default:
		return managedDataStorage{}, fmt.Errorf("%w: managed-data backend must be local or s3", storage.ErrInvalid)
	}
	inventory, ok := result.blobs.(storage.BlobInventory)
	if !ok {
		return managedDataStorage{}, fmt.Errorf("%w: managed-data backend has no blob inventory", storage.ErrInvalid)
	}
	result.inventory = inventory
	return result, nil
}

func newManagedDataCollector(db *sql.DB, services managedDataStorage, cfg ProductConfig) (*maintenance.BlobCollector, error) {
	reachability, err := maintenancesqlite.New(db)
	if err != nil {
		return nil, err
	}
	return maintenance.NewBlobCollector(services.inventory, reachability, maintenance.BlobGCConfig{
		GraceAge: cfg.GCGracePeriod,
	})
}

func newManagedDataRuntimeCollector(services managedDataStorage, cfg ProductConfig) (*maintenance.RuntimeViewCollector, error) {
	if services.runtimeCache == nil {
		return nil, nil
	}
	return maintenance.NewRuntimeViewCollector(services.runtimeCache, maintenance.RuntimeViewGCConfig{
		GraceAge: cfg.GCGracePeriod,
		Limit:    100,
	})
}

func capacityProtectedTus(next http.Handler, capacity *maintenance.CapacityChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength < 0 {
			http.Error(w, "Content-Length is required", http.StatusLengthRequired)
			return
		}
		reservation, err := capacity.Reserve(r.Context(), r.ContentLength)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, maintenance.ErrInsufficientCapacity) {
				status = http.StatusInsufficientStorage
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		defer reservation.Release()
		next.ServeHTTP(w, r)
	})
}

func newManagedDataS3Store(ctx context.Context, cfg ProductConfig) (*manageds3.Store, error) {
	return newS3BlobStore(ctx, cfg, cfg.S3Prefix)
}

// NewS3BlobStore constructs a content-addressed store that shares the managed
// data S3 connection settings but uses an independent key prefix. Independent
// prefixes keep each capability's reachability and garbage collection isolated.
func NewS3BlobStore(ctx context.Context, cfg ProductConfig, prefix string) (storage.BlobStore, error) {
	return newS3BlobStore(ctx, cfg, prefix)
}

func newS3BlobStore(ctx context.Context, cfg ProductConfig, prefix string) (*manageds3.Store, error) {
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(strings.TrimSpace(cfg.S3Region))}
	if cfg.S3AccessKeyID != "" {
		provider := credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			cfg.S3SessionToken,
		)
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(provider))
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("initialize managed-data S3 client: %w", err)
	}
	client := awss3.NewFromConfig(awsConfig, func(options *awss3.Options) {
		options.UsePathStyle = cfg.S3PathStyle
		if endpoint := strings.TrimSpace(cfg.S3Endpoint); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})
	return manageds3.New(client, awss3.NewPresignClient(client), manageds3.Config{
		Bucket: cfg.S3Bucket,
		Prefix: prefix,
	})
}

func newManagedDataControl(repo control.Repository, services managedDataStorage, cfg ProductConfig) (*control.Service, error) {
	return control.New(repo, services.blobs, control.Config{
		Limits: manageddata.Limits{
			MaxFiles:         cfg.MaxFiles,
			MaxFileBytes:     cfg.MaxFileBytes,
			MaxRevisionBytes: cfg.MaxRevisionBytes,
		},
		UploadTTL: cfg.UploadSessionTTL,
		Transport: services.transport,
	})
}
