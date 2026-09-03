// Generic sealed-catalog serving core. This file deliberately keeps delivery
// persistence behind capability callbacks: local and PostgreSQL composition
// own their repositories and object-store credentials, while this package only
// receives one exact immutable root and opens it read-only.

package runtimefactory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	"github.com/flidai/leapview/internal/analytics/sealedcatalog"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
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
	// TargetID is the canonical configured delivery target that owns this
	// serving root. DeliveryID remains build provenance and must never be used
	// as a serving-scope selector.
	TargetID              string
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
type SealedDashboardRuntimeBuilder func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment, resulttier.Tier) (*dashboardruntime.Service, error)

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

// SealedFactoryAdapters supplies the generic capabilities needed by a sealed
// serving factory. Local and PostgreSQL composition may provide different
// object-store, credential, and authorization adapters without exposing their
// persistence implementations through this package.
type SealedFactoryAdapters struct {
	Resolve             SealedRootResolver
	Objects             sealedcatalog.ObjectStore
	ObjectsForRoot      func(context.Context, SealedServingRoot) (sealedcatalog.ObjectStore, error)
	CredentialBootstrap func(context.Context, *ducklake.PoolContract) (ducklake.CredentialBootstrap, error)
	ExtensionAdmission  extension.Admission
	Leases              catalogartifact.LeaseRepository
	Authorize           sealedcatalog.Authorization
	AuthorizeServing    func(context.Context, runtimehost.RuntimeInput, sealedcatalog.Artifact, catalogartifact.LeaseInput) error
	BuildRuntime        SealedDashboardRuntimeBuilder
}

// NewSealedFactoryWithAdapters builds a sealed serving factory from generic
// adapters. It is the narrow bridge used by local composition for its
// target-owned storage capabilities.
func NewSealedFactoryWithAdapters(base FactoryConfig, adapters SealedFactoryAdapters) runtimehost.RuntimeFactory {
	return newSealedFactory(base, adapters.Resolve, adapters.Objects, adapters.ObjectsForRoot, adapters.CredentialBootstrap, adapters.ExtensionAdmission, adapters.Leases, adapters.Authorize, adapters.AuthorizeServing, adapters.BuildRuntime)
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
	if root.TargetID == "" || root.TargetID != strings.TrimSpace(root.TargetID) || root.GenerationID == "" && root.CandidateID == "" || root.SealID == "" || root.CatalogDigest == "" || root.CatalogObjectKey == "" || root.CatalogObjectSize <= 0 || root.ClosureDigest == "" || root.QualificationDigest == "" || root.ServingArtifactID == "" || root.ServingArtifactDigest == "" {
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
	runtime, err := f.base.prepareDashboard(ctx, input, f.buildRuntime, reader.Environment(), "", root.TargetID, root.SealID, nil)
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
