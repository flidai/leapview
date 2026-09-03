// Local SQLite sealed-catalog serving adapter. This package owns the local
// persistence and object-store composition; the generic runtimefactory package
// receives only capability-oriented adapters and remains free of SQLite
// dependencies.

package localruntimefactory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sealedcatalog"
	"github.com/flidai/leapview/internal/app/gcadapter"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	"github.com/flidai/leapview/internal/deployment/gc"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/runtimehost"
)

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
// local SQLite adapter. Production composition uses the PostgreSQL adapter in
// app/runtimefactory instead.
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
	ActivationEvidence    appruntimefactory.ActivationEvidenceSource
}

// NewSQLiteSealedFactory builds the fail-closed local serving factory from
// durable delivery and physical-pool repositories. No process-wide mutable
// DuckLake environment is opened by this adapter.
func NewSQLiteSealedFactory(config SQLiteSealedFactoryConfig) (runtimehost.RuntimeFactory, error) {
	if config.Database == nil || strings.TrimSpace(config.TargetID) == "" || config.TargetID != strings.TrimSpace(config.TargetID) || config.ProjectRuntimeFactory == nil || config.Authorize == nil {
		return nil, fmt.Errorf("SQLite sealed serving database, target, project runtime factory, and authorization are required")
	}
	delivery := deploymentsqlite.NewRepositoryWithHooks(config.Database, deploymentsqlite.ActivationHooks{})
	pools := physicalpoolsqlite.NewRepository(config.Database)
	resolve := NewSQLiteSealedRootResolver(config.Database, config.TargetID, delivery, pools)
	leases := deploymentsqlite.SealedCatalogLeaseAdapter{Repository: delivery}
	base := appruntimefactory.FactoryConfig{DuckDBDir: config.DuckDBDir, RuntimeDir: config.RuntimeDir, SealedLeaseHolder: config.LeaseHolder, ActivationEvidence: config.ActivationEvidence}
	authorize := func(_ context.Context, artifact sealedcatalog.Artifact, lease catalogartifact.LeaseInput) error {
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
	buildRuntime := func(ctx context.Context, input dashboardruntimefactory.Input, environment *ducklake.Environment, _ resulttier.Tier) (*dashboardruntime.Service, error) {
		return dashboardmodule.NewRuntimeFactory(dashboardmodule.RuntimeFactoryConfig{
			Projects: config.ProjectRuntimeFactory(environment), MaxRows: config.DashboardMaxRows, MaxBytes: config.DashboardMaxBytes,
		})(ctx, input)
	}
	credentialBootstrap := func(_ context.Context, contract *ducklake.PoolContract) (ducklake.CredentialBootstrap, error) {
		return gcadapter.NewPoolCredentialBootstrap(contract, config.PoolS3)
	}
	objectsForRoot := func(ctx context.Context, root appruntimefactory.SealedServingRoot) (sealedcatalog.ObjectStore, error) {
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
	return appruntimefactory.NewSealedFactoryWithAdapters(base, appruntimefactory.SealedFactoryAdapters{
		Resolve: resolve, Objects: LocalCatalogObjectStore{Root: config.CatalogObjectRoot}, ObjectsForRoot: objectsForRoot,
		CredentialBootstrap: credentialBootstrap, ExtensionAdmission: config.PoolS3.ExtensionAdmission,
		Leases: leases, Authorize: authorize, AuthorizeServing: authorizeServing, BuildRuntime: buildRuntime,
	}), nil
}

func validateSealedAuthorizationEvidence(artifact sealedcatalog.Artifact, lease catalogartifact.LeaseInput) error {
	if artifact.SealID == "" || lease.SealID == "" || artifact.SealID != lease.SealID ||
		(lease.GenerationID == "") == (lease.CandidateID == "") {
		return fmt.Errorf("sealed serving authorization evidence is incomplete")
	}
	return nil
}

// LocalCatalogObjectStore is a read-only adapter for the local target. It
// maps canonical relative object keys beneath Root and derives provider
// metadata from immutable file bytes; it has no create/write capability.
type LocalCatalogObjectStore struct{ Root string }

// gcCatalogObjectStore adapts the target-owned, pool-scoped GC object store
// to the sealed reader's read-only object contract. It does not expose list or
// delete capabilities to serving.
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
		return sealedcatalog.Object{}, fmt.Errorf("%w: invalid local catalog object key", appruntimefactory.ErrSealedRootUnavailable)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return sealedcatalog.Object{}, fmt.Errorf("%w: unsafe local catalog object key", appruntimefactory.ErrSealedRootUnavailable)
		}
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return sealedcatalog.Object{}, err
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(key)))
	if err != nil || (path != root && !strings.HasPrefix(path, root+string(os.PathSeparator))) {
		return sealedcatalog.Object{}, fmt.Errorf("%w: local catalog object escaped root", appruntimefactory.ErrSealedRootUnavailable)
	}
	file, err := os.Open(path)
	if err != nil {
		return sealedcatalog.Object{}, fmt.Errorf("%w: %v", appruntimefactory.ErrSealedRootUnavailable, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return sealedcatalog.Object{}, fmt.Errorf("%w: catalog object is not a regular file", appruntimefactory.ErrSealedRootUnavailable)
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
