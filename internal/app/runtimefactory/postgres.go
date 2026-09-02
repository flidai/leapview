package runtimefactory

// PostgreSQL-backed serving runtime. This seam is intentionally parallel to
// the legacy sealed object-catalog adapter: it opens a target-owned DuckDB
// session attached directly to DuckLake PostgreSQL metadata and never stages,
// hashes, uploads, or downloads a catalog file.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var (
	ErrPostgresLeaseRenewal                  = errors.New("PostgreSQL DuckLake snapshot lease renewal failed")
	ErrPostgresRuntimeAttachProbeUnavailable = errors.New("PostgreSQL DuckLake runtime attach eligibility probe is unavailable")
	postgresLeaseReleaseTimeout              = 5 * time.Second
)

// DuckLakeRuntimeAttachChecker is the narrow, read-only authority required
// immediately before a PostgreSQL-backed DuckLake serving attachment. The
// checker implementation owns the DuckLake runtime pool; serving composition
// never receives the broader migration/owner repository.
type DuckLakeRuntimeAttachChecker interface {
	CheckRuntimeAttachEligibility(context.Context, ducklakepostgres.RuntimeAttachInput) (ducklakepostgres.RuntimeAttachEligibility, error)
}

// NewPostgresRuntimeAttachChecker adapts the target-owned DuckLake PostgreSQL
// runtime pool to the narrow checker consumed by the sealed factory. Keeping
// construction here prevents process composition from depending on the
// adapter package directly.
func NewPostgresRuntimeAttachChecker(pool ducklakepostgres.DBTX) DuckLakeRuntimeAttachChecker {
	if pool == nil {
		return nil
	}
	return ducklakepostgres.New(pool)
}

// PostgresDashboardRuntimeConfig contains the module-owned inputs needed to
// build a dashboard runtime over an immutable DuckLake environment.
type PostgresDashboardRuntimeConfig struct {
	Projects func(*ducklake.Environment) analyticsruntime.ProjectFactory
	MaxRows  int
	MaxBytes int64
}

// NewPostgresDashboardRuntimeBuilder projects the dashboard module builder
// behind this composition seam. Callers do not need to import dashboard
// runtime implementation or factory packages.
func NewPostgresDashboardRuntimeBuilder(config PostgresDashboardRuntimeConfig) SealedDashboardRuntimeBuilder {
	if config.Projects == nil {
		return nil
	}
	return func(ctx context.Context, input dashboardruntimefactory.Input, environment *ducklake.Environment) (*dashboardruntime.Service, error) {
		return dashboardmodule.NewRuntimeFactory(dashboardmodule.RuntimeFactoryConfig{
			Projects: config.Projects(environment), MaxRows: config.MaxRows, MaxBytes: config.MaxBytes,
		})(ctx, input)
	}
}

// PostgresSealedFactoryConfig supplies only target-owned capabilities. The
// resolver must return the exact catalog identity and snapshot selected by
// durable delivery state; it is not allowed to infer identity from process
// configuration.
type PostgresSealedFactoryConfig struct {
	Base                       FactoryConfig
	Resolve                    SealedRootResolver
	BuildRuntime               SealedDashboardRuntimeBuilder
	SnapshotLeases             runtimehost.SnapshotLeaseRepository
	Authorize                  func(context.Context, PostgresServingAuthorizationInput) error
	RuntimeAttachChecker       DuckLakeRuntimeAttachChecker
	LeaseHolder                string
	CredentialBootstrapFactory func(context.Context, *ducklake.PoolContract) (ducklake.CredentialBootstrap, error)
	ExtensionAdmission         extension.Admission
	DuckLakeSecret             string
	PostgresSecret             string
}

type postgresSealedFactory struct {
	base                       servingStateRuntimeFactory
	resolve                    SealedRootResolver
	buildRuntime               SealedDashboardRuntimeBuilder
	snapshotLeases             runtimehost.SnapshotLeaseRepository
	authorize                  func(context.Context, PostgresServingAuthorizationInput) error
	runtimeAttachChecker       DuckLakeRuntimeAttachChecker
	leaseHolder                string
	credentialBootstrapFactory func(context.Context, *ducklake.PoolContract) (ducklake.CredentialBootstrap, error)
	extensionAdmission         extension.Admission
	duckLakeSecret             string
	postgresSecret             string
}

// PostgresServingAuthorizationInput carries the exact immutable root and
// lease identity to the target authorization boundary. It contains no secret
// names or credentials.
type PostgresServingAuthorizationInput struct {
	Root    SealedServingRoot
	LeaseID string
	OwnerID string
	Fence   int64
}

// NewPostgresSealedFactory builds the production serving runtime whose DuckLake
// catalog is PostgreSQL-backed. NewSQLiteSealedFactory is reserved
// for development/evaluation composition and is never a production fallback.
func NewPostgresSealedFactory(config PostgresSealedFactoryConfig) runtimehost.RuntimeFactory {
	return postgresSealedFactory{
		base:    servingStateRuntimeFactory{duckDBDir: config.Base.DuckDBDir, runtimeDir: config.Base.RuntimeDir, activationEvidence: config.Base.ActivationEvidence},
		resolve: config.Resolve, buildRuntime: config.BuildRuntime,
		credentialBootstrapFactory: config.CredentialBootstrapFactory, extensionAdmission: config.ExtensionAdmission,
		duckLakeSecret: config.DuckLakeSecret, postgresSecret: config.PostgresSecret,
		snapshotLeases: config.SnapshotLeases, authorize: config.Authorize,
		runtimeAttachChecker: config.RuntimeAttachChecker,
		leaseHolder:          config.LeaseHolder,
	}
}

// postgresRuntimeAttachInputFromRoot projects only the exact identity and
// version evidence required by the DuckLake PostgreSQL attach gate. The
// projection is deliberately sourced from the sealed serving root; it never
// reads a current catalog row and therefore cannot self-assert an upgrade.
func postgresRuntimeAttachInputFromRoot(root SealedServingRoot) (ducklakepostgres.RuntimeAttachInput, error) {
	values := map[string]string{
		"physical pool ID":                 root.PhysicalPoolID,
		"catalog ID":                       root.CatalogID,
		"DuckDB version":                   root.DuckDBVersion,
		"DuckLake extension version":       root.DuckLakeExtensionVersion,
		"DuckLake specification version":   root.DuckLakeSpecVersion,
		"compatibility digest":             root.CompatibilityDigest,
		"catalog schema version":           root.CatalogSchemaVersion,
		"admitted DuckDB runtime":          root.Compatibility.DuckDBRuntime,
		"admitted DuckLake extension":      root.Compatibility.DuckLakeExtension,
		"admitted DuckLake catalog format": root.Compatibility.CatalogFormat,
	}
	for name, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return ducklakepostgres.RuntimeAttachInput{}, fmt.Errorf("%w: sealed serving root %s is unavailable", ErrPostgresRuntimeAttachProbeUnavailable, name)
		}
	}
	return ducklakepostgres.RuntimeAttachInput{
		PhysicalPoolID: root.PhysicalPoolID,
		CatalogID:      root.CatalogID,
		Compatibility: ducklakepostgres.RuntimeCompatibility{
			RuntimeTuple: ducklakepostgres.RuntimeTuple{
				DuckDBRuntime:     root.Compatibility.DuckDBRuntime,
				DuckLakeExtension: root.Compatibility.DuckLakeExtension,
				CatalogFormat:     root.Compatibility.CatalogFormat,
			},
			CompatibilityDigest:  root.CompatibilityDigest,
			CatalogSchemaVersion: root.CatalogSchemaVersion,
		},
	}, nil
}

// checkPostgresRuntimeAttachEligibility runs the existing PostgreSQL DuckLake
// gate against the exact root selected for serving. It also verifies the
// evidence returned by the checker at this boundary so an adapter cannot
// report an unrelated catalog as eligible.
func checkPostgresRuntimeAttachEligibility(ctx context.Context, checker DuckLakeRuntimeAttachChecker, root SealedServingRoot) error {
	if checker == nil {
		return ErrPostgresRuntimeAttachProbeUnavailable
	}
	input, err := postgresRuntimeAttachInputFromRoot(root)
	if err != nil {
		return err
	}
	eligibility, err := checker.CheckRuntimeAttachEligibility(ctx, input)
	if err != nil {
		return fmt.Errorf("%w: %w", ducklakepostgres.ErrRuntimeAttachIneligible, err)
	}
	if !eligibility.Eligible {
		reason := strings.TrimSpace(eligibility.Reason)
		if reason == "" {
			reason = "checker reported ineligible"
		}
		return fmt.Errorf("%w: %s", ducklakepostgres.ErrRuntimeAttachIneligible, reason)
	}
	current := eligibility.Current
	if current.PhysicalPoolID != input.PhysicalPoolID || current.CatalogID != input.CatalogID || current.RuntimeCompatibility != input.Compatibility || current.CurrentMigrationID == "" {
		return fmt.Errorf("%w: identity/version compatibility mismatch", ducklakepostgres.ErrRuntimeAttachIneligible)
	}
	if _, err := uuid.Parse(current.CurrentMigrationID); err != nil {
		return fmt.Errorf("%w: identity/version compatibility mismatch", ducklakepostgres.ErrRuntimeAttachIneligible)
	}
	return nil
}

func (f postgresSealedFactory) Prepare(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	return nil, fmt.Errorf("PostgreSQL sealed serving factory cannot use legacy Prepare")
}

// PinnedSnapshotSealed marks this target as implementing exact
// SNAPSHOT_VERSION-qualified serving. It is intentionally a zero-value
// capability marker, so callers cannot accidentally opt out through a bool.
func (f postgresSealedFactory) PinnedSnapshotSealed() {}

func (f postgresSealedFactory) PrepareSealed(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	if f.resolve == nil || f.buildRuntime == nil || f.snapshotLeases == nil || f.authorize == nil || f.credentialBootstrapFactory == nil || f.extensionAdmission == nil {
		return nil, fmt.Errorf("PostgreSQL sealed serving resolver, pool admission, snapshot leases, authorization, credentials, extension admission, and dashboard builder are required")
	}
	if f.runtimeAttachChecker == nil {
		return nil, ErrPostgresRuntimeAttachProbeUnavailable
	}
	root, err := f.resolve(ctx, input)
	if err != nil {
		return nil, err
	}
	if root.ServingStateID != string(input.State.ID) || root.ServingArtifactID != input.Artifact.ID || root.ServingArtifactDigest != input.Artifact.Digest {
		return nil, fmt.Errorf("%w: persisted root is not bound to requested serving artifact", ErrSealedRootMismatch)
	}
	poolContract := root.PoolContract
	if poolContract == nil {
		return nil, fmt.Errorf("%w: PostgreSQL root physical-pool admission is unavailable", ErrSealedRootUnavailable)
	}
	if err := poolContract.Validate(); err != nil {
		return nil, fmt.Errorf("physical-pool admission: %w", err)
	}
	if root.PhysicalPoolID == "" || root.PhysicalPoolID != poolContract.Pool.ID.String() {
		return nil, fmt.Errorf("%w: PostgreSQL root physical-pool identity is incomplete", ErrSealedRootUnavailable)
	}
	if root.Compatibility != poolContract.Tuple {
		return nil, fmt.Errorf("%w: PostgreSQL root compatibility admission is not canonical", ErrSealedRootMismatch)
	}
	if root.CatalogDatabase == "" || root.CatalogID == "" || root.CatalogUUID == "" || root.DeliveryID == "" || root.GenerationID == "" || root.CandidateID == "" || root.AttemptID == "" || root.FencingEpoch <= 0 || root.DataPath == "" {
		return nil, fmt.Errorf("%w: PostgreSQL catalog and generation identity is incomplete", ErrSealedRootUnavailable)
	}
	if _, err := uuid.Parse(root.CatalogUUID); err != nil {
		return nil, fmt.Errorf("%w: PostgreSQL catalog UUID is invalid", ErrSealedRootMismatch)
	}
	catalogVersion, err := strconv.ParseInt(root.CatalogVersion, 10, 64)
	if err != nil || catalogVersion != root.CatalogVersionNumber {
		return nil, fmt.Errorf("%w: PostgreSQL catalog version evidence is inconsistent", ErrSealedRootMismatch)
	}
	if err := poolContract.ValidateDataPathBinding(root.DataPath); err != nil {
		return nil, fmt.Errorf("%w: PostgreSQL DATA_PATH evidence differs: %v", ErrSealedRootMismatch, err)
	}
	if root.SealID == "" || root.QualificationDigest == "" || root.ClosureDigest == "" || root.CompatibilityDigest == "" || root.RuntimeVersion == "" || root.SecurityDomainFingerprint == "" || root.CatalogVersion == "" || root.CatalogVersionNumber <= 0 || root.DuckDBVersion == "" || root.DuckLakeExtensionVersion == "" || root.DuckLakeSpecVersion == "" || root.CatalogSchemaVersion == "" || root.RelationNamespace == "" || root.RelationManifestDigest == "" || root.ObjectRoot == "" || root.ObjectRootDigest == "" || root.ArtifactRoot == "" || root.ArtifactRootDigest == "" || root.CompiledGraphDigest == "" || root.CompiledConfigDigest == "" || root.RequestDigest == "" || root.PlanDigest == "" || root.TenantDomain == "" || root.Region == "" || root.EncryptionDomain == "" || root.ObjectNamespace == "" {
		return nil, fmt.Errorf("%w: PostgreSQL qualification evidence is incomplete", ErrSealedRootUnavailable)
	}
	for name, value := range map[string]string{"qualification digest": root.QualificationDigest, "closure digest": root.ClosureDigest, "compatibility digest": root.CompatibilityDigest, "security fingerprint": root.SecurityDomainFingerprint, "serving artifact digest": root.ServingArtifactDigest, "relation manifest digest": root.RelationManifestDigest, "object root digest": root.ObjectRootDigest, "artifact root digest": root.ArtifactRootDigest, "compiled graph digest": root.CompiledGraphDigest, "compiled config digest": root.CompiledConfigDigest, "request digest": root.RequestDigest, "plan digest": root.PlanDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return nil, fmt.Errorf("%w: %s is invalid: %v", ErrSealedRootUnavailable, name, err)
		}
	}
	for name, value := range map[string]string{"delivery ID": root.DeliveryID, "generation ID": root.GenerationID, "candidate ID": root.CandidateID, "attempt ID": root.AttemptID, "seal ID": root.SealID, "relation namespace": root.RelationNamespace, "object root": root.ObjectRoot, "artifact root": root.ArtifactRoot, "tenant domain": root.TenantDomain, "region": root.Region, "encryption domain": root.EncryptionDomain, "object namespace": root.ObjectNamespace, "catalog database": root.CatalogDatabase, "catalog ID": root.CatalogID, "catalog UUID": root.CatalogUUID, "catalog version": root.CatalogVersion, "DuckDB version": root.DuckDBVersion, "DuckLake extension version": root.DuckLakeExtensionVersion, "DuckLake spec version": root.DuckLakeSpecVersion, "catalog schema version": root.CatalogSchemaVersion, "runtime version": root.RuntimeVersion} {
		if strings.TrimSpace(value) != value || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") || len(value) > 512 {
			return nil, fmt.Errorf("%w: PostgreSQL %s identity is not normalized", ErrSealedRootMismatch, name)
		}
	}
	if err := ducklake.ValidateRelationNamespace(root.RelationNamespace); err != nil {
		return nil, fmt.Errorf("%w: PostgreSQL relation namespace is invalid: %v", ErrSealedRootMismatch, err)
	}
	expectedNamespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{
		CandidateID: root.CandidateID, AttemptID: root.AttemptID, FencingEpoch: root.FencingEpoch,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: derive PostgreSQL relation namespace: %v", ErrSealedRootMismatch, err)
	}
	if root.RelationNamespace != expectedNamespace {
		return nil, fmt.Errorf("%w: PostgreSQL relation namespace differs from candidate attempt fence", ErrSealedRootMismatch)
	}
	compatibilityDigest, err := poolContract.Tuple.Digest()
	if err != nil {
		return nil, err
	}
	if root.CompatibilityDigest != compatibilityDigest {
		return nil, fmt.Errorf("%w: PostgreSQL runtime compatibility evidence differs", ErrSealedRootMismatch)
	}
	sealedCatalogVersion, sealedCatalogErr := ducklake.CatalogVersionNumber(root.DuckLakeSpecVersion)
	admittedCatalogVersion, admittedCatalogErr := ducklake.CatalogVersionNumber(root.Compatibility.CatalogFormat)
	if root.DuckDBVersion != root.Compatibility.DuckDBRuntime || root.DuckLakeExtensionVersion != root.Compatibility.DuckLakeExtension || sealedCatalogErr != nil || admittedCatalogErr != nil || sealedCatalogVersion != admittedCatalogVersion || sealedCatalogVersion != root.CatalogVersionNumber {
		return nil, fmt.Errorf("%w: PostgreSQL sealed runtime versions differ from admitted compatibility", ErrSealedRootMismatch)
	}
	metadataSchema := strings.TrimSpace(root.CatalogMetadataSchema)
	if metadataSchema == "" || metadataSchema != ducklake.MetadataSchemaForPool(root.PhysicalPoolID) {
		return nil, fmt.Errorf("%w: PostgreSQL metadata schema is not bound to physical pool", ErrSealedRootMismatch)
	}
	// Secret names are capability-owned deployment configuration. Durable
	// roots carry no secret references; only this process-owned factory config
	// may select the temporary names used during connector bootstrap.
	duckLakeSecret := strings.TrimSpace(f.duckLakeSecret)
	postgresSecret := strings.TrimSpace(f.postgresSecret)
	if duckLakeSecret == "" || postgresSecret == "" {
		return nil, fmt.Errorf("%w: PostgreSQL DuckLake secret identities are required", ErrSealedRootUnavailable)
	}
	snapshotID := root.CatalogSnapshotID
	if snapshotID <= 0 || input.State.DuckLakeSnapshotID <= 0 {
		return nil, fmt.Errorf("%w: exact PostgreSQL DuckLake snapshot is required", ErrSealedRootUnavailable)
	}
	if snapshotID != input.State.DuckLakeSnapshotID {
		return nil, fmt.Errorf("%w: PostgreSQL snapshot does not match serving state", ErrSealedRootMismatch)
	}
	leaseOwner := firstNonEmpty(f.leaseHolder, "runtimehost")
	now := time.Now().UTC()
	expiresAt := now.Add(30 * time.Minute)
	leaseID, err := f.snapshotLeases.CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{
		ServingStateID: input.State.ID, DuckLakeSnapshotID: snapshotID,
		OwnerID: leaseOwner, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL DuckLake snapshot lease: %w", err)
	}
	if strings.TrimSpace(leaseID) == "" || strings.TrimSpace(leaseID) != leaseID {
		return nil, fmt.Errorf("acquire PostgreSQL DuckLake snapshot lease: empty or unnormalized lease ID")
	}
	leaseHandle := newPostgresLeaseHandle(f.snapshotLeases, leaseID, expiresAt, input.OnLeaseRenewalFailure)
	if err := f.authorize(ctx, PostgresServingAuthorizationInput{Root: root, LeaseID: leaseID, OwnerID: leaseOwner, Fence: root.FencingEpoch}); err != nil {
		_ = leaseHandle.Close()
		return nil, err
	}
	if err := checkPostgresRuntimeAttachEligibility(ctx, f.runtimeAttachChecker, root); err != nil {
		_ = leaseHandle.Close()
		return nil, fmt.Errorf("verify PostgreSQL DuckLake runtime attach eligibility: %w", err)
	}
	credentialBootstrap, err := f.credentialBootstrapFactory(ctx, poolContract)
	if err != nil {
		_ = leaseHandle.Close()
		return nil, fmt.Errorf("build PostgreSQL DuckLake credential bootstrap: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(poolContract.Tuple.StorageImplementation), "s3") && credentialBootstrap == nil {
		_ = leaseHandle.Close()
		return nil, fmt.Errorf("%w: PostgreSQL S3 physical-pool credentials are unavailable", ErrSealedRootUnavailable)
	}
	catalog := ducklake.PostgresCatalogConfig{
		PhysicalPoolID: root.PhysicalPoolID, DuckLakeSecret: duckLakeSecret, PostgresSecret: postgresSecret,
		MetadataSchema: metadataSchema, Mode: ducklake.PostgresCatalogServing, SnapshotVersion: snapshotID,
	}
	env, err := ducklake.Open(ctx, ducklake.Config{
		RootDir: input.RuntimeDir, PhysicalPoolID: root.PhysicalPoolID, PoolContract: poolContract,
		Compatibility: root.Compatibility, PostgresCatalog: &catalog,
		CredentialBootstrap: credentialBootstrap, ExtensionAdmission: f.extensionAdmission,
	})
	if err != nil {
		_ = leaseHandle.Close()
		return nil, err
	}
	runtime, err := f.base.prepareDashboard(ctx, input, f.buildRuntime, env, root.RelationNamespace)
	if err != nil {
		_ = env.Close()
		_ = leaseHandle.Close()
		return nil, err
	}
	return &postgresPreparedRuntime{dashboardRuntimeWithGraph: runtime, environment: env, lease: leaseHandle}, nil
}

type postgresPreparedRuntime struct {
	*dashboardRuntimeWithGraph
	environment *ducklake.Environment
	lease       *postgresLeaseHandle
}

func (r *postgresPreparedRuntime) Close() error {
	if r == nil {
		return nil
	}
	var runtimeErr, environmentErr, leaseErr error
	if r.dashboardRuntimeWithGraph != nil {
		runtimeErr = r.dashboardRuntimeWithGraph.Close()
	}
	if r.environment != nil {
		environmentErr = r.environment.Close()
	}
	if r.lease != nil {
		leaseErr = r.lease.Close()
	}
	return errors.Join(runtimeErr, environmentErr, leaseErr)
}

func (r *postgresPreparedRuntime) LeaseRenewalError() error {
	if r == nil || r.lease == nil {
		return nil
	}
	return r.lease.Err()
}

type postgresLeaseHandle struct {
	repo        runtimehost.SnapshotLeaseRepository
	leaseID     string
	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.RWMutex
	err         error
	leaseExpiry time.Time
	onFail      func(error)
	close       sync.Once
	releaseOnce sync.Once
	releaseErr  error
}

func newPostgresLeaseHandle(repo runtimehost.SnapshotLeaseRepository, leaseID string, expiresAt time.Time, onFail func(error)) *postgresLeaseHandle {
	ctx, cancel := context.WithCancel(context.Background())
	h := &postgresLeaseHandle{repo: repo, leaseID: leaseID, cancel: cancel, done: make(chan struct{}), leaseExpiry: expiresAt, onFail: onFail}
	interval := time.Until(expiresAt) / 3
	if interval <= 0 || interval > 10*time.Minute {
		interval = 10 * time.Minute
	}
	go h.heartbeat(ctx, interval)
	return h
}

func (h *postgresLeaseHandle) heartbeat(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	defer close(h.done)
	deadline := h.leaseExpiry
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(interval * 3)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			renewCtx, cancel := context.WithDeadline(ctx, deadline)
			expires := time.Now().UTC().Add(30 * time.Minute)
			err := h.repo.ExtendQuerySnapshotLease(renewCtx, h.leaseID, expires)
			cancel()
			if err == nil && !time.Now().UTC().Before(deadline) {
				err = context.DeadlineExceeded
			}
			if err != nil {
				// Retry transient provider failures while the last confirmed
				// durable expiry remains in the future. The renewal call itself
				// is bounded by that expiry, so a hung PostgreSQL request cannot
				// outlive the lease or hide a lost root.
				if time.Now().UTC().Before(deadline) {
					retry := interval / 4
					if retry <= 0 {
						retry = time.Millisecond
					}
					if remaining := time.Until(deadline); remaining > 0 && retry > remaining {
						retry = remaining
					}
					timer.Reset(retry)
					continue
				}
				healthErr := fmt.Errorf("%w: %w", ErrPostgresLeaseRenewal, err)
				h.mu.Lock()
				h.err = healthErr
				h.mu.Unlock()
				if h.onFail != nil {
					h.onFail(healthErr)
				}
				return
			}
			// Only a confirmed renewal advances the deadline. A successful
			// callback is the sole evidence that the new durable expiry exists.
			deadline = expires
			h.mu.Lock()
			h.leaseExpiry = deadline
			h.err = nil
			h.mu.Unlock()
			if h.onFail != nil {
				h.onFail(nil)
			}
			timer.Reset(interval)
		}
	}
}

func (h *postgresLeaseHandle) Err() error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

func (h *postgresLeaseHandle) Close() error {
	if h == nil {
		return nil
	}
	h.close.Do(func() {
		h.cancel()
		<-h.done
		h.releaseOnce.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), postgresLeaseReleaseTimeout)
			defer cancel()
			h.releaseErr = h.repo.ReleaseQuerySnapshotLease(releaseCtx, h.leaseID)
		})
	})
	return h.releaseErr
}
