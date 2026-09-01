package runtimefactory

// Local SQLite sealed-catalog serving adapter. This file deliberately keeps
// delivery persistence behind a callback: development/evaluation composition
// owns the SQLite repository and object-store credentials, while this package
// only receives one exact immutable root and opens it read-only. Production
// serving uses the PostgreSQL adapter in postgres.go.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sealedcatalog"
	"github.com/flidai/leapview/internal/app/gcadapter"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	"github.com/flidai/leapview/internal/deployment/gc"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/runtimehost"
)

var (
	ErrSealedRootUnavailable = errors.New("sealed serving root is unavailable")
	ErrSealedRootMismatch    = errors.New("sealed serving root is not bound to the serving artifact")
)

// SealedServingRoot is the exact durable root selected for one serving
// generation. ServingStateID and ServingArtifactDigest bind the new delivery
// pointer to the compiled graph artifact; a resolver must reject mismatches.
type SealedServingRoot struct {
	GenerationID          string
	CandidateID           string
	AttemptID             string
	SealID                string
	CatalogDigest         string
	CatalogObjectKey      string
	CatalogObjectSize     int64
	ClosureDigest         string
	QualificationDigest   string
	PhysicalPoolID        string
	Compatibility         ducklake.CompatibilityTuple
	PoolContract          *ducklake.PoolContract
	ServingStateID        string
	ServingArtifactID     string
	ServingArtifactDigest string
	// PostgreSQL-backed roots carry relational catalog identity instead of a
	// serialized catalog object. These fields are optional for the legacy file
	// root and are consumed by NewPostgresSealedFactory.
	CatalogDatabase           string
	CatalogID                 string
	CatalogUUID               string
	CatalogMetadataSchema     string
	CatalogSnapshotID         int64
	DataPath                  string
	CatalogVersion            string
	CatalogVersionNumber      int64
	DuckDBVersion             string
	DuckLakeExtensionVersion  string
	DuckLakeSpecVersion       string
	CatalogSchemaVersion      string
	RelationNamespace         string
	RelationManifestDigest    string
	ObjectRoot                string
	ObjectRootDigest          string
	ArtifactRoot              string
	ArtifactRootDigest        string
	CompiledGraphDigest       string
	CompiledConfigDigest      string
	RequestDigest             string
	PlanDigest                string
	TenantDomain              string
	Region                    string
	EncryptionDomain          string
	ObjectNamespace           string
	DeliveryID                string
	FencingEpoch              int64
	CompatibilityDigest       string
	RuntimeVersion            string
	SecurityDomainFingerprint string
}

// SealedRootResolver resolves the active delivery generation (or a candidate
// preview) and its exact graph-artifact binding from durable control state.
// Returning a root for an unrelated serving state is a hard error.
type SealedRootResolver func(context.Context, runtimehost.RuntimeInput) (SealedServingRoot, error)

// SealedDashboardRuntimeBuilder opens dashboard data runtimes against the
// supplied immutable read-only environment. It must not retain the
// environment after the returned dashboard service is closed.
type SealedDashboardRuntimeBuilder func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment) (*dashboardruntime.Service, error)

// SealedAuthorizationInput is the small live authorization surface needed
// before a catalog query lease is acquired. It intentionally contains only
// durable serving identity, never object-store credentials or paths.
type SealedAuthorizationInput struct {
	ProjectID    string
	Environment  string
	TargetID     string
	GenerationID string
	SealID       string
	CandidateID  string
	OwnerID      string
}

// SQLiteSealedFactoryConfig contains composition-owned capabilities for the
// local SQLite adapter. Keeping this constructor here avoids making
// development/evaluation composition depend directly on deployment and
// physical-pool storage packages; production uses PostgresSealedFactoryConfig.
type SQLiteSealedFactoryConfig struct {
	Database              *sql.DB
	TargetID              string
	CatalogObjectRoot     string
	DuckDBDir             string
	RuntimeDir            string
	LeaseHolder           string
	ProjectRuntimeFactory func(*ducklake.Environment) analyticsruntime.ProjectFactory
	Authorize             func(context.Context, SealedAuthorizationInput) error
	DashboardMaxRows      int
	DashboardMaxBytes     int64
	PoolS3                gcadapter.S3Config
	ActivationEvidence    ActivationEvidenceSource
}

// NewSQLiteSealedFactory builds the fail-closed local serving factory from
// durable delivery and physical-pool repositories. It is available only to
// development/evaluation composition; production uses NewPostgresSealedFactory.
// No process-wide mutable DuckLake environment is opened by this adapter.
func NewSQLiteSealedFactory(config SQLiteSealedFactoryConfig) (runtimehost.RuntimeFactory, error) {
	if config.Database == nil || config.TargetID == "" || config.ProjectRuntimeFactory == nil || config.Authorize == nil {
		return nil, fmt.Errorf("SQLite sealed serving database, target, project runtime factory, and authorization are required")
	}
	delivery := deploymentsqlite.NewRepositoryWithHooks(config.Database, deploymentsqlite.ActivationHooks{})
	pools := physicalpoolsqlite.NewRepository(config.Database)
	resolve := NewSQLiteSealedRootResolver(config.Database, config.TargetID, delivery, pools)
	leases := deploymentsqlite.SealedCatalogLeaseAdapter{Repository: delivery}
	base := FactoryConfig{DuckDBDir: config.DuckDBDir, RuntimeDir: config.RuntimeDir, SealedLeaseHolder: config.LeaseHolder, ActivationEvidence: config.ActivationEvidence}
	authorize := func(ctx context.Context, artifact sealedcatalog.Artifact, lease catalogartifact.LeaseInput) error {
		return validateSealedAuthorizationEvidence(artifact, lease)
	}
	authorizeServing := func(ctx context.Context, input runtimehost.RuntimeInput, artifact sealedcatalog.Artifact, lease catalogartifact.LeaseInput) error {
		if err := authorize(ctx, artifact, lease); err != nil {
			return err
		}
		ownerID := ""
		if input.Candidate != nil {
			ownerID = input.Candidate.OwnerID
		}
		return config.Authorize(ctx, SealedAuthorizationInput{ProjectID: input.State.ProjectID.String(), Environment: string(input.State.Environment), TargetID: config.TargetID, GenerationID: lease.GenerationID, SealID: artifact.SealID, CandidateID: lease.CandidateID, OwnerID: ownerID})
	}
	buildRuntime := func(ctx context.Context, input dashboardruntimefactory.Input, environment *ducklake.Environment) (*dashboardruntime.Service, error) {
		return dashboardmodule.NewRuntimeFactory(dashboardmodule.RuntimeFactoryConfig{
			Projects: config.ProjectRuntimeFactory(environment), MaxRows: config.DashboardMaxRows, MaxBytes: config.DashboardMaxBytes,
		})(ctx, input)
	}
	credentialBootstrap := func(_ context.Context, contract *ducklake.PoolContract) (ducklake.CredentialBootstrap, error) {
		return gcadapter.NewPoolCredentialBootstrap(contract, config.PoolS3)
	}
	objectsForRoot := func(ctx context.Context, root SealedServingRoot) (sealedcatalog.ObjectStore, error) {
		admission, err := pools.LoadAdmissionContract(ctx, root.PhysicalPoolID, root.Compatibility)
		if err != nil {
			return nil, fmt.Errorf("load physical-pool object-store admission: %w", err)
		}
		pool := &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
		store, err := gcadapter.NewPoolStore(ctx, pool, config.PoolS3)
		if err != nil {
			return nil, err
		}
		return gcCatalogObjectStore{store: store}, nil
	}
	return newSealedFactory(base, resolve, LocalCatalogObjectStore{Root: config.CatalogObjectRoot}, objectsForRoot, credentialBootstrap, config.PoolS3.ExtensionAdmission, leases, authorize, authorizeServing, buildRuntime), nil
}

func validateSealedAuthorizationEvidence(artifact sealedcatalog.Artifact, lease catalogartifact.LeaseInput) error {
	if artifact.SealID == "" || lease.SealID == "" || artifact.SealID != lease.SealID ||
		(lease.GenerationID == "") == (lease.CandidateID == "") {
		return fmt.Errorf("sealed serving authorization evidence is incomplete")
	}
	return nil
}

type sealedServingFactory struct {
	base                servingStateRuntimeFactory
	resolve             SealedRootResolver
	objects             sealedcatalog.ObjectStore
	objectsForRoot      func(context.Context, SealedServingRoot) (sealedcatalog.ObjectStore, error)
	credentialBootstrap func(context.Context, *ducklake.PoolContract) (ducklake.CredentialBootstrap, error)
	extensionAdmission  extension.Admission
	leases              catalogartifact.LeaseRepository
	authorize           sealedcatalog.Authorization
	authorizeServing    func(context.Context, runtimehost.RuntimeInput, sealedcatalog.Artifact, catalogartifact.LeaseInput) error
	buildRuntime        SealedDashboardRuntimeBuilder
	holder              string
	now                 func() time.Time
}

// NewSealedFactory wraps the normal project-artifact loader with the sealed
// catalog attach path. It is safe for tests to use an in-memory ObjectStore;
// production composition supplies a target-owned read-only object adapter.
func NewSealedFactory(base FactoryConfig, resolve SealedRootResolver, objects sealedcatalog.ObjectStore, leases catalogartifact.LeaseRepository, authorize sealedcatalog.Authorization, buildRuntime SealedDashboardRuntimeBuilder) runtimehost.RuntimeFactory {
	return newSealedFactory(base, resolve, objects, nil, nil, nil, leases, authorize, nil, buildRuntime)
}

func newSealedFactory(base FactoryConfig, resolve SealedRootResolver, objects sealedcatalog.ObjectStore, objectsForRoot func(context.Context, SealedServingRoot) (sealedcatalog.ObjectStore, error), credentialBootstrap func(context.Context, *ducklake.PoolContract) (ducklake.CredentialBootstrap, error), extensionAdmission extension.Admission, leases catalogartifact.LeaseRepository, authorize sealedcatalog.Authorization, authorizeServing func(context.Context, runtimehost.RuntimeInput, sealedcatalog.Artifact, catalogartifact.LeaseInput) error, buildRuntime SealedDashboardRuntimeBuilder) runtimehost.RuntimeFactory {
	return sealedServingFactory{base: servingStateRuntimeFactory{duckDBDir: base.DuckDBDir, runtimeDir: base.RuntimeDir, activationEvidence: base.ActivationEvidence}, resolve: resolve, objects: objects, objectsForRoot: objectsForRoot, credentialBootstrap: credentialBootstrap, extensionAdmission: extensionAdmission, leases: leases, authorize: authorize, authorizeServing: authorizeServing, buildRuntime: buildRuntime, holder: firstNonEmpty(base.SealedLeaseHolder, "runtimehost"), now: time.Now}
}

func (f sealedServingFactory) Prepare(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	return nil, fmt.Errorf("sealed serving factory cannot use legacy Prepare")
}

func (f sealedServingFactory) PrepareSealed(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	if f.resolve == nil || (f.objects == nil && f.objectsForRoot == nil) || f.leases == nil || f.authorize == nil || f.buildRuntime == nil {
		return nil, fmt.Errorf("%w: resolver, object store, leases, authorization, and dashboard builder are required", ErrSealedRootUnavailable)
	}
	root, err := f.resolve(ctx, input)
	if err != nil {
		return nil, err
	}
	if root.ServingStateID != string(input.State.ID) || root.ServingArtifactID != input.Artifact.ID || root.ServingArtifactDigest != input.Artifact.Digest {
		return nil, fmt.Errorf("%w: persisted root state=%q artifact=%q digest=%q, input state=%q artifact=%q digest=%q", ErrSealedRootMismatch, root.ServingStateID, root.ServingArtifactID, root.ServingArtifactDigest, input.State.ID, input.Artifact.ID, input.Artifact.Digest)
	}
	if root.PoolContract == nil || root.PhysicalPoolID != root.PoolContract.Pool.ID.String() || root.Compatibility != root.PoolContract.Tuple {
		return nil, fmt.Errorf("%w: physical-pool admission evidence is incomplete", ErrSealedRootUnavailable)
	}
	if root.GenerationID == "" && root.CandidateID == "" || root.SealID == "" || root.CatalogDigest == "" || root.CatalogObjectKey == "" || root.CatalogObjectSize <= 0 || root.ClosureDigest == "" || root.QualificationDigest == "" || root.ServingArtifactID == "" || root.ServingArtifactDigest == "" {
		return nil, fmt.Errorf("%w: complete catalog identity is required", ErrSealedRootUnavailable)
	}
	now := time.Now().UTC()
	if f.now != nil {
		now = f.now().UTC()
	}
	leaseID := "query-" + randomID()
	leaseInput := catalogartifact.LeaseInput{
		ID: leaseID, HolderID: firstNonEmpty(f.holder, "runtimehost"), GenerationID: root.GenerationID,
		SealID: root.SealID, CatalogDigest: root.CatalogDigest, ObjectKey: root.CatalogObjectKey,
		ObjectSize: root.CatalogObjectSize, ClosureDigest: root.ClosureDigest, QualificationDigest: root.QualificationDigest,
		PhysicalPoolID: root.PhysicalPoolID, CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if root.GenerationID == "" {
		leaseInput.CandidateID = root.CandidateID
	}
	authorize := f.authorize
	if f.authorizeServing != nil {
		authorize = func(authCtx context.Context, artifact sealedcatalog.Artifact, lease catalogartifact.LeaseInput) error {
			return f.authorizeServing(authCtx, input, artifact, lease)
		}
	}
	objects := f.objects
	if f.objectsForRoot != nil {
		objects, err = f.objectsForRoot(ctx, root)
		if err != nil {
			return nil, err
		}
	}
	var credentialBootstrap ducklake.CredentialBootstrap
	if f.credentialBootstrap != nil {
		credentialBootstrap, err = f.credentialBootstrap(ctx, root.PoolContract)
		if err != nil {
			return nil, err
		}
	}
	reader, err := sealedcatalog.Open(ctx, sealedcatalog.Request{
		Artifact: sealedcatalog.Artifact{ObjectKey: root.CatalogObjectKey, SealID: root.SealID, CatalogDigest: root.CatalogDigest, SizeBytes: root.CatalogObjectSize, ClosureDigest: root.ClosureDigest, QualificationDigest: root.QualificationDigest, PhysicalPoolID: root.PhysicalPoolID, Compatibility: root.Compatibility, PoolContract: root.PoolContract},
		Store:    objects, Leases: f.leases, Lease: leaseInput, Authorize: authorize, CredentialBootstrap: credentialBootstrap, ExtensionAdmission: f.extensionAdmission,
		OnLeaseRenewalFailure: input.OnLeaseRenewalFailure,
		StagingRoot:           filepath.Join(f.base.runtimeDir, "sealed-catalogs"),
	})
	if err != nil {
		return nil, err
	}
	closeReader := func() error { return reader.Close() }
	// Reuse the normal project artifact extraction and authorization compiler,
	// but direct dashboard query runtimes at this reader's immutable catalog.
	runtime, err := f.base.prepareDashboard(ctx, input, f.buildRuntime, reader.Environment(), "")
	if err != nil {
		_ = closeReader()
		return nil, err
	}
	// Embed the concrete graph-bearing runtime rather than the narrow
	// PreparedRuntime interface.  The runtimehost lifecycle stores runtimes
	// behind a small interface, but request handlers discover optional
	// resolver/query capabilities from the concrete value.  Embedding the
	// interface here would erase those promoted methods and make sealed
	// semantic-model queries look like missing resources after cutover.
	return &sealedPreparedRuntime{dashboardRuntimeWithGraph: runtime, reader: reader, closeReader: closeReader}, nil
}

type sealedPreparedRuntime struct {
	*dashboardRuntimeWithGraph
	reader      *sealedcatalog.Reader
	closeReader func() error
}

func (r *sealedPreparedRuntime) LeaseRenewalError() error {
	if r == nil || r.reader == nil {
		return nil
	}
	return r.reader.LeaseRenewalError()
}

func (r *sealedPreparedRuntime) Close() error {
	if r == nil {
		return nil
	}
	var runtimeErr error
	if r.dashboardRuntimeWithGraph != nil && r.dashboardRuntimeWithGraph.Service != nil {
		runtimeErr = r.dashboardRuntimeWithGraph.Close()
	}
	var readerErr error
	if r.closeReader != nil {
		readerErr = r.closeReader()
	}
	return errors.Join(runtimeErr, readerErr)
}

// LocalCatalogObjectStore is a read-only adapter for the local target. It
// maps canonical relative object keys beneath Root and derives provider
// metadata from the immutable file bytes; it has no create/write capability.
type LocalCatalogObjectStore struct{ Root string }

// gcCatalogObjectStore adapts the target-owned, pool-scoped GC object store
// to the sealed reader's read-only object contract. It does not expose list
// or delete capabilities to serving.
type gcCatalogObjectStore struct{ store gc.PoolStore }

func (s gcCatalogObjectStore) Open(ctx context.Context, key string) (sealedcatalog.Object, error) {
	if s.store == nil {
		return sealedcatalog.Object{}, fmt.Errorf("pool catalog object store is unavailable")
	}
	object, err := s.store.Open(ctx, key)
	if err != nil {
		return sealedcatalog.Object{}, err
	}
	metadata := map[string]string{}
	for k, v := range object.Metadata {
		metadata[k] = v
	}
	if digest := metadata["sha256"]; digest != "" {
		metadata[catalogseal.MetadataDigest] = digest
	}
	if _, ok := metadata[catalogseal.MetadataSize]; !ok {
		metadata[catalogseal.MetadataSize] = fmt.Sprint(object.Size)
	}
	return sealedcatalog.Object{Body: object.Body, Size: object.Size, Metadata: metadata}, nil
}

func (s LocalCatalogObjectStore) Open(ctx context.Context, key string) (sealedcatalog.Object, error) {
	if err := ctx.Err(); err != nil {
		return sealedcatalog.Object{}, err
	}
	if strings.TrimSpace(s.Root) == "" || key == "" || key != strings.TrimSpace(key) || filepath.IsAbs(key) || strings.Contains(key, "\\") {
		return sealedcatalog.Object{}, fmt.Errorf("%w: invalid local catalog object key", ErrSealedRootUnavailable)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return sealedcatalog.Object{}, fmt.Errorf("%w: unsafe local catalog object key", ErrSealedRootUnavailable)
		}
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return sealedcatalog.Object{}, err
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(key)))
	if err != nil || (path != root && !strings.HasPrefix(path, root+string(os.PathSeparator))) {
		return sealedcatalog.Object{}, fmt.Errorf("%w: local catalog object escaped root", ErrSealedRootUnavailable)
	}
	file, err := os.Open(path)
	if err != nil {
		return sealedcatalog.Object{}, fmt.Errorf("%w: %v", ErrSealedRootUnavailable, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return sealedcatalog.Object{}, fmt.Errorf("%w: catalog object is not a regular file", ErrSealedRootUnavailable)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return sealedcatalog.Object{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return sealedcatalog.Object{}, err
	}
	return sealedcatalog.Object{Body: file, Size: info.Size(), Metadata: map[string]string{catalogseal.MetadataDigest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), catalogseal.MetadataSize: fmt.Sprint(info.Size())}}, nil
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))[:16]
	}
	return hex.EncodeToString(b[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
