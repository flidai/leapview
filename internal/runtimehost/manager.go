package runtimehost

// This file owns the process-local lifecycle for exactly one project and
// environment.  A serving generation is prepared privately, optionally
// installs its authorization snapshot, and is then published under one
// cutover lock.  Retired generations remain leaseable until their readers
// drain; cleanup is deliberately detached from publication.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ErrPreparedStale indicates that another cutover won while this prepared
// runtime was waiting for the publication fence. Reload treats this as a
// retryable race rather than surfacing a spurious failure to callers.
var ErrPreparedStale = errors.New("prepared runtime is stale")

var ErrProjectBindConflict = errors.New("runtime host project binding conflict")

var errReloadReadRace = errors.New("runtime host active generation changed during reload read")

type ServingStateRepository interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
}

type activeScopeRepository interface {
	ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error)
}

type SnapshotLeaseRepository interface {
	CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error)
	ReleaseQuerySnapshotLease(context.Context, string) error
	ExtendQuerySnapshotLease(context.Context, string, time.Time) error
}

// AuthorizationSnapshotInstaller is intentionally tiny. Its implementation
// belongs to access persistence; runtimehost only binds the graph-validated
// immutable snapshot to the exact generation that is about to become visible.
type AuthorizationSnapshotInstaller interface {
	InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error
}

type Runtime = projectruntime.Runtime
type RuntimeSnapshot interface{ DuckLakeSnapshotID() int64 }
type RuntimeLifetime interface{ Close() error }
type RuntimeLeaseHealth interface{ LeaseRenewalError() error }

// PreparedRuntime is the mandatory factory result. The authorization
// snapshot must have been built from the validated graph and exact serving
// identity before the runtime can be activated.
type PreparedRuntime interface {
	Runtime
	AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
}

type Lease = projectruntime.Lease
type Provider = projectruntime.Provider

type CleanupResource string

const (
	CleanupResourceRuntime       CleanupResource = "runtime"
	CleanupResourceManagedData   CleanupResource = "managed_data"
	CleanupResourceSnapshotLease CleanupResource = "snapshot_lease"
	CleanupResourceDependency    CleanupResource = "runtime_dependency"
)

type CleanupFailure struct {
	ProjectID          projectgraph.ResourceID
	ServingStateID     servingstate.ID
	DuckLakeSnapshotID int64
	Resource           CleanupResource
	Err                error
}

type RuntimeFactory interface {
	Prepare(context.Context, RuntimeInput) (PreparedRuntime, error)
}

// SealedRuntimeFactory is the production serving seam. Implementations must
// resolve the exact active candidate/generation root (or the explicit
// SealedActivationCandidate supplied while a target is still on its base)
// from durable delivery state, acquire its fenced query lease, and attach a
// read-only immutable catalog before returning. A manager configured with
// RequireSealedCatalog refuses to call the legacy Prepare method when this
// capability is absent.
type SealedRuntimeFactory interface {
	PrepareSealed(context.Context, RuntimeInput) (PreparedRuntime, error)
}

// PinnedSnapshotSealedFactory explicitly opts into a target whose sealed root
// is qualified by an exact DuckLake SNAPSHOT_VERSION. Legacy object/file
// factories intentionally do not implement this capability and continue to
// reject state rows that still carry a mutable snapshot pointer.
type PinnedSnapshotSealedFactory interface {
	SealedRuntimeFactory
	// PinnedSnapshotSealed is a marker method: a target either implements the
	// exact SNAPSHOT_VERSION serving contract or it does not. There is no
	// runtime boolean that callers could accidentally leave false.
	PinnedSnapshotSealed()
}

type ManagedDataResolution struct {
	RevisionID string
	Roots      map[string]string
	Revisions  map[string]string
	Lifetime   ManagedDataLifetime
}
type ManagedDataLifetime interface{ Release() error }
type ManagedDataResolver interface {
	ResolveManagedDataForIdentity(context.Context, projectgraph.ServingIdentity) (ManagedDataResolution, error)
}

func (m *Manager) resolveManagedData(ctx context.Context, state servingstate.State) (ManagedDataResolution, error) {
	if m == nil || m.managedData == nil {
		return ManagedDataResolution{}, nil
	}
	identity, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
	if err != nil {
		return ManagedDataResolution{}, err
	}
	return m.managedData.ResolveManagedDataForIdentity(ctx, identity)
}

type RuntimeInput struct {
	State       servingstate.State
	Artifact    servingstate.Artifact
	ManagedData ManagedDataResolution
	DuckDBDir   string
	RuntimeDir  string
	Candidate   *CandidateRuntimeContext
	// Candidate carries candidate identity/evidence to runtime factories. It is
	// normally supplied for private candidate preparation; when
	// SealedActivationCandidate is also set, the same candidate is an explicit
	// durable-generation preparation for native refresh completion and the
	// resulting Prepared remains publishable by ActivatePrepared.
	SealedActivationCandidate *CandidateRuntimeContext
	// OnLeaseRenewalFailure is a runtime-owned health signal. Factories that
	// hold durable query roots invoke it promptly when their heartbeat fails;
	// nil clears the generation's transient renewal error.
	OnLeaseRenewalFailure func(error)
}

type CandidateRuntimeContext struct {
	CandidateID              string
	OwnerID                  string
	AuthorizationFingerprint string
	BindingFingerprint       string
	RuntimeVersion           string
	BindingKinds             map[string]string
	Capabilities             []RuntimeCapabilityEvidence
	CompatibilityFingerprint string
	GateEvidenceDigest       string
}

// RuntimeCapabilityEvidence is the non-secret, target-verified identity of
// one admitted analytical runtime capability. It deliberately carries no
// artifact path, origin, signature, or request-scoped state.
type RuntimeCapabilityEvidence struct {
	Name             string
	Identity         string
	Digest           string
	DuckDBVersion    string
	ExtensionVersion string
	GOOS             string
	GOARCH           string
	Platform         string
	SupportProfile   string
}

type ManagerOptions struct {
	Repo                        ServingStateRepository
	ProjectID                   projectgraph.ResourceID
	Environment                 servingstate.Environment
	Factory                     RuntimeFactory
	ManagedData                 ManagedDataResolver
	Authorization               AuthorizationSnapshotInstaller
	OnDrained                   func(servingstate.ID, int64)
	LeaseTTL                    time.Duration
	LeaseOwner                  string
	Logger                      *slog.Logger
	OnLeaseRenewalFailure       func(error)
	OnCleanupFailure            func(CleanupFailure)
	LeaseReleaseQueueCapacity   int
	LeaseReleaseShutdownTimeout time.Duration
	CleanupDrainTimeout         time.Duration
	RequireSealedCatalog        bool
}

type Manager struct {
	mu        sync.RWMutex
	cutoverMu sync.RWMutex
	// closed is guarded by cutoverMu (and read under mu while the cutover
	// lock is held).  Close takes the same fence as activation so an
	// in-flight prepared runtime cannot publish after resources have been
	// drained.
	closed                 bool
	repo                   ServingStateRepository
	projectID              projectgraph.ResourceID
	environment            servingstate.Environment
	factory                RuntimeFactory
	managedData            ManagedDataResolver
	authorization          AuthorizationSnapshotInstaller
	onDrained              func(servingstate.ID, int64)
	leaseTTL               time.Duration
	leaseOwner             string
	logger                 *slog.Logger
	onLeaseRenewalFailure  func(error)
	onCleanupFailure       func(CleanupFailure)
	leaseRenewalErrors     map[string]error
	current                *managedRuntime
	retired                []*managedRuntime
	cleanupWorkerRunning   bool
	cleanupDrainTimeout    time.Duration
	releaseQueue           *snapshotLeaseReleaseQueue
	releaseShutdownTimeout time.Duration
	requireSealedCatalog   bool
}

type Prepared struct {
	mu              sync.Mutex
	owner           *Manager
	state           preparedState
	servingStateID  servingstate.ID
	digest          string
	managedRevision string
	runtime         Runtime
	managedData     ManagedDataLifetime
	snapshotLease   *persistentSnapshotLease
	runtimeLifetime RuntimeLifetime
	snapshotID      int64
	sealed          bool
	authorization   accesssnapshot.AuthorizationSnapshot
	noChange        bool
	baseActiveID    servingstate.ID
	candidateID     string
	candidateOwner  string
	candidateExpiry time.Time
	candidateHash   [32]byte
}

type candidatePreparationContext struct {
	runtime     CandidateRuntimeContext
	expiresAt   time.Time
	fingerprint [32]byte
	lifetime    RuntimeLifetime
}

type preparedState uint8

const (
	preparedStateOpen preparedState = iota
	preparedStateSealed
	preparedStatePublished
	preparedStateRegistered
	preparedStateClosed
)

func (p *Prepared) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != preparedStateOpen {
		return nil
	}
	p.state = preparedStateClosed
	return errors.Join(closeRuntime(p.runtime), releaseManaged(p.managedData), closeSnapshotLease(p.snapshotLease), closeRuntimeLifetime(p.runtimeLifetime))
}
func (p *Prepared) DuckLakeSnapshotID() int64 {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotID
}

func NewManagerWithFactory(options ManagerOptions) *Manager {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	capacity := options.LeaseReleaseQueueCapacity
	if capacity <= 0 {
		capacity = 64
	}
	shutdown := options.LeaseReleaseShutdownTimeout
	if shutdown <= 0 {
		shutdown = 5 * time.Second
	}
	environment := options.Environment
	m := &Manager{repo: options.Repo, projectID: options.ProjectID, environment: environment, factory: options.Factory, managedData: options.ManagedData, authorization: options.Authorization, onDrained: options.OnDrained, leaseTTL: normalizedLeaseTTL(options.LeaseTTL), leaseOwner: firstNonEmpty(options.LeaseOwner, "runtimehost"), logger: logger, onLeaseRenewalFailure: options.OnLeaseRenewalFailure, onCleanupFailure: options.OnCleanupFailure, leaseRenewalErrors: map[string]error{}, cleanupDrainTimeout: normalizedCleanupDrainTimeout(options.CleanupDrainTimeout), releaseShutdownTimeout: shutdown, requireSealedCatalog: options.RequireSealedCatalog}
	m.releaseQueue = newSnapshotLeaseReleaseQueue(capacity, m.releaseSnapshotLease)
	return m
}

func (m *Manager) ProjectID() projectgraph.ResourceID {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.projectID
}

// ActiveArtifact reports the exact active serving generation for this
// manager's immutable project/environment scope. It deliberately consults
// the serving-state repository rather than inferring activity from the
// process-local runtime pointer, which may be empty during initial boot or
// between reloads.
func (m *Manager) ActiveArtifact(ctx context.Context) (servingstate.State, servingstate.Artifact, error) {
	if m == nil || m.repo == nil {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	projectID := m.ProjectID()
	if err := projectID.Validate(); err != nil {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return m.repo.ActiveArtifact(ctx, projectID, m.environment)
}

func (m *Manager) Environment() servingstate.Environment {
	if m == nil {
		return ""
	}
	return m.environment
}

// BindClaimedProject installs the durable instance claim into this process
// before any active generation is prepared or published. It is serialized
// with cutover and is permanently immutable for the lifetime of the manager.
func (m *Manager) BindClaimedProject(projectID projectgraph.ResourceID, environment servingstate.Environment) error {
	if m == nil {
		return errors.New("runtime host is nil")
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("invalid claimed project: %w", err)
	}
	if projectID.String() != strings.TrimSpace(projectID.String()) {
		return errors.New("invalid claimed project: project id must be canonical")
	}
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return fmt.Errorf("invalid claimed environment: %w", err)
	}
	if string(environment) != strings.TrimSpace(string(environment)) {
		return errors.New("invalid claimed environment: environment must be canonical")
	}
	m.cutoverMu.Lock()
	defer m.cutoverMu.Unlock()
	if environment != m.environment {
		return fmt.Errorf("%w: runtime host environment is %q, claimed environment is %q", ErrProjectBindConflict, m.environment, environment)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.projectID == "" {
		m.projectID = projectID
		return nil
	}
	if m.projectID != projectID {
		return fmt.Errorf("%w: runtime host project changed from %q to %q", ErrProjectBindConflict, m.projectID, projectID)
	}
	return nil
}
func (m *Manager) LeaseRenewalError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var errs []error
	for _, e := range m.leaseRenewalErrors {
		errs = append(errs, e)
	}
	if m.current != nil {
		if health, ok := m.current.runtime.(RuntimeLeaseHealth); ok {
			errs = append(errs, health.LeaseRenewalError())
		}
	}
	for _, retired := range m.retired {
		if retired == nil {
			continue
		}
		if health, ok := retired.runtime.(RuntimeLeaseHealth); ok {
			errs = append(errs, health.LeaseRenewalError())
		}
	}
	return errors.Join(errs...)
}
func (m *Manager) SnapshotLeaseReleaseBacklog() int {
	if m == nil || m.releaseQueue == nil {
		return 0
	}
	return m.releaseQueue.len()
}
func (m *Manager) setLeaseRenewalError(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.leaseRenewalErrors, id)
	} else {
		m.leaseRenewalErrors[id] = err
	}
}

// Reload retries a bounded number of times when another cutover wins the
// publication fence. This keeps concurrent activation/reload races benign
// without unbounded recursive retries.
func (m *Manager) Reload(ctx context.Context) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = m.reloadOnce(ctx)
		if !errors.Is(err, ErrPreparedStale) && !errors.Is(err, errReloadReadRace) {
			return err
		}
	}
	return err
}

// ReconcileSealed activates the exact durable serving-state generation named
// by the sealed delivery commit. It performs only read-only catalog attach and
// runtime cutover; delivery metadata has already been committed by the
// sealed-control coordinator, so no legacy activation callback or snapshot
// pinning is reachable here.
func (m *Manager) ReconcileSealed(ctx context.Context, id servingstate.ID) error {
	if m == nil || id == "" {
		return errors.New("sealed serving generation is required")
	}
	state, err := m.repo.ByID(ctx, id)
	if err != nil {
		return err
	}
	artifact, err := m.repo.ArtifactByServingState(ctx, id)
	if err != nil {
		return err
	}
	if err := m.validateGeneration(state, artifact); err != nil {
		return err
	}
	// Reconciliation is intentionally active-only.  Do not use prepare's
	// no-change fast path here: that path is appropriate for Reload and can
	// otherwise bypass the sealed resolver's durable active-pointer check when
	// a replay names the process-local generation after the target has moved.
	prepared, err := m.prepareActiveSealed(ctx, state, artifact)
	if err != nil {
		return err
	}
	return m.activatePreparedContext(ctx, prepared, func() error { return nil })
}

func (m *Manager) prepareActiveSealed(ctx context.Context, state servingstate.State, artifact servingstate.Artifact) (*Prepared, error) {
	var data ManagedDataResolution
	var err error
	if m.managedData != nil {
		data, err = m.resolveManagedData(ctx, state)
		if err != nil {
			return nil, errors.Join(err, releaseManaged(data.Lifetime))
		}
	}
	return m.prepareResolvedWithCandidate(ctx, state, artifact, data, nil, nil)
}

func (m *Manager) reloadOnce(ctx context.Context) error {
	if m.ProjectID() == "" {
		return m.reloadUnbound(ctx)
	}
	projectID := m.ProjectID()
	current, artifact, err := m.repo.ActiveArtifact(ctx, projectID, m.environment)
	if errors.Is(err, servingstate.ErrNotFound) {
		// A no-active read is only authoritative while serialized with
		// activation. Re-check under the cutover lock before retiring anything;
		// if another actor published meanwhile, let the normal active path load
		// that exact generation.
		m.cutoverMu.Lock()
		_, _, confirmErr := m.repo.ActiveArtifact(ctx, projectID, m.environment)
		if confirmErr == nil {
			m.cutoverMu.Unlock()
			return errReloadReadRace
		}
		if !errors.Is(confirmErr, servingstate.ErrNotFound) {
			m.cutoverMu.Unlock()
			return confirmErr
		}
		m.mu.Lock()
		current := m.current
		m.current = nil
		retired := m.retireLocked(current)
		m.mu.Unlock()
		m.cutoverMu.Unlock()
		m.cleanupRetired(retired)
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.validateGeneration(current, artifact); err != nil {
		return err
	}
	m.mu.RLock()
	unchanged := m.current != nil && m.current.servingStateID == current.ID && m.current.digest == artifact.Digest && m.current.snapshotID == current.DuckLakeSnapshotID
	m.mu.RUnlock()
	if unchanged {
		return nil
	}
	prepared, err := m.prepare(ctx, current, artifact, nil)
	if err != nil {
		return err
	}
	if current.DuckLakeSnapshotID == 0 && prepared.DuckLakeSnapshotID() > 0 {
		if err := m.repo.RecordDuckLakeSnapshot(ctx, current.ID, prepared.DuckLakeSnapshotID()); err != nil {
			_ = prepared.Close()
			return err
		}
	}
	return m.activatePreparedContext(ctx, prepared, func() error { return nil })
}

// reloadUnbound discovers the first active project for this process's fixed
// environment. A fresh installation legitimately has no active scope yet;
// in that case the host remains unbound until the first exact generation is
// activated. Multiple active projects are rejected instead of selecting one.
func (m *Manager) reloadUnbound(ctx context.Context) error {
	repo, ok := m.repo.(activeScopeRepository)
	if !ok {
		return errors.New("unbound runtime host requires active-scope discovery")
	}
	m.cutoverMu.Lock()
	scopes, err := repo.ListActiveScopes(ctx)
	if err != nil {
		m.cutoverMu.Unlock()
		return err
	}
	var projectID projectgraph.ResourceID
	for _, scope := range scopes {
		if scope.Environment != m.environment {
			continue
		}
		if err := scope.ProjectID.Validate(); err != nil {
			m.cutoverMu.Unlock()
			return fmt.Errorf("active serving scope project is invalid: %w", err)
		}
		if projectID == "" {
			projectID = scope.ProjectID
			continue
		}
		if scope.ProjectID != projectID {
			m.cutoverMu.Unlock()
			return fmt.Errorf("active serving scopes span multiple projects: %q and %q", projectID, scope.ProjectID)
		}
	}
	if projectID != "" {
		m.mu.Lock()
		if m.projectID == "" {
			m.projectID = projectID
		} else if m.projectID != projectID {
			existingProjectID := m.projectID
			m.mu.Unlock()
			m.cutoverMu.Unlock()
			return fmt.Errorf("runtime host project changed from %q to %q", existingProjectID, projectID)
		}
		m.mu.Unlock()
	}
	if projectID == "" {
		m.mu.Lock()
		current := m.current
		m.current = nil
		retired := m.retireLocked(current)
		m.mu.Unlock()
		m.cutoverMu.Unlock()
		m.cleanupRetired(retired)
		return nil
	}
	m.cutoverMu.Unlock()
	return m.Reload(ctx)
}

func (m *Manager) PrepareServingState(ctx context.Context, id string) (*Prepared, error) {
	state, err := m.repo.ByID(ctx, servingstate.ID(id))
	if err != nil {
		return nil, err
	}
	artifact, err := m.repo.ArtifactByServingState(ctx, state.ID)
	if err != nil {
		return nil, err
	}
	if err := m.validateGeneration(state, artifact); err != nil {
		return nil, err
	}
	return m.prepare(ctx, state, artifact, nil)
}

// PrepareSealedActivation prepares one exact, qualified delivery generation
// before its target pointer is advanced. Unlike PrepareServingState, which
// resolves the active delivery pointer, this explicit seam supplies the
// candidate identity to a sealed resolver. The resulting Prepared is still
// an ordinary activatable runtime (it is not an owned private candidate).
func (m *Manager) PrepareSealedActivation(ctx context.Context, id, candidateID string) (*Prepared, error) {
	if m == nil || !m.requireSealedCatalog {
		return nil, errors.New("sealed activation preparation requires a sealed runtime host")
	}
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
		return nil, errors.New("sealed activation serving state id must be canonical")
	}
	if strings.TrimSpace(candidateID) == "" || candidateID != strings.TrimSpace(candidateID) {
		return nil, errors.New("sealed activation candidate id must be canonical")
	}
	state, err := m.repo.ByID(ctx, servingstate.ID(id))
	if err != nil {
		return nil, err
	}
	artifact, err := m.repo.ArtifactByServingState(ctx, state.ID)
	if err != nil {
		return nil, err
	}
	if err := m.validateGeneration(state, artifact); err != nil {
		return nil, err
	}
	var data ManagedDataResolution
	if m.managedData != nil {
		data, err = m.resolveManagedData(ctx, state)
		if err != nil {
			return nil, err
		}
	}
	return m.prepareResolvedWithCandidate(ctx, state, artifact, data, nil, &CandidateRuntimeContext{CandidateID: candidateID})
}

func (m *Manager) validateGeneration(state servingstate.State, artifact servingstate.Artifact) error {
	if m.requireSealedCatalog && state.DuckLakeSnapshotID > 0 {
		if _, ok := m.factory.(PinnedSnapshotSealedFactory); ok {
			// PostgreSQL-backed sealed roots pin SNAPSHOT_VERSION in the
			// attachment and verify the same value against durable qualification.
		} else {
			return fmt.Errorf("sealed serving migration is incomplete: generation %s still pins DuckLake snapshot %d; rebuild and publish a verified catalog seal before startup", state.ID, state.DuckLakeSnapshotID)
		}
	}
	boundProjectID := m.ProjectID()
	if boundProjectID != "" && state.ProjectID != boundProjectID {
		return fmt.Errorf("serving state %s project = %q, want %q", state.ID, state.ProjectID, boundProjectID)
	}
	if servingstate.Environment(state.Environment) != m.environment {
		return fmt.Errorf("serving state %s environment = %q, want %q", state.ID, state.Environment, m.environment)
	}
	if !state.CanActivate() {
		return fmt.Errorf("serving state %s has status %q and cannot be prepared", state.ID, state.Status)
	}
	if err := platformdigest.ValidateSHA256Identity(state.Digest); err != nil {
		return fmt.Errorf("serving state digest is invalid: %w", err)
	}
	if state.Digest != artifact.Digest {
		return fmt.Errorf("serving state digest = %q, artifact digest = %q", state.Digest, artifact.Digest)
	}
	if _, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID)); err != nil {
		return err
	}
	if artifact.ServingStateID != state.ID {
		return fmt.Errorf("artifact serving state = %q, want %q", artifact.ServingStateID, state.ID)
	}
	if err := platformdigest.ValidateSHA256Identity(artifact.Digest); err != nil {
		return fmt.Errorf("artifact digest is invalid: %w", err)
	}
	return nil
}

func (m *Manager) prepare(ctx context.Context, state servingstate.State, artifact servingstate.Artifact, candidate *candidatePreparationContext) (*Prepared, error) {
	if err := m.validateGeneration(state, artifact); err != nil {
		return nil, errors.Join(err, closeCandidatePreparationLifetime(candidate))
	}
	// A content-addressed serving state can be published again by a distinct
	// reviewed candidate. Keep the active runtime and its cache scope, but
	// return a consumable preparation so the caller can still commit the
	// publication metadata under the normal activation fence.
	if candidate == nil {
		m.mu.RLock()
		current := m.current
		if current != nil && current.servingStateID == state.ID {
			if current.digest != artifact.Digest {
				m.mu.RUnlock()
				return nil, fmt.Errorf("active serving state %s digest = %q, requested artifact digest = %q", state.ID, current.digest, artifact.Digest)
			}
			prepared := &Prepared{
				owner:           m,
				servingStateID:  state.ID,
				digest:          current.digest,
				managedRevision: current.managedRevision,
				snapshotID:      current.snapshotID,
				sealed:          current.sealed,
				authorization:   current.authorization,
				noChange:        true,
				baseActiveID:    current.servingStateID,
			}
			m.mu.RUnlock()
			return prepared, nil
		}
		m.mu.RUnlock()
	}
	var data ManagedDataResolution
	var err error
	if m.managedData != nil {
		data, err = m.resolveManagedData(ctx, state)
		if err != nil {
			return nil, err
		}
	}
	return m.prepareResolvedWithCandidate(ctx, state, artifact, data, candidate, nil)
}

func (m *Manager) prepareResolvedWithCandidate(ctx context.Context, state servingstate.State, artifact servingstate.Artifact, data ManagedDataResolution, candidate *candidatePreparationContext, sealedActivationCandidate *CandidateRuntimeContext) (*Prepared, error) {
	if err := m.validateGeneration(state, artifact); err != nil {
		return nil, errors.Join(err, releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	if m.factory == nil {
		return nil, errors.Join(errors.New("runtime factory is required"), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	factoryData := data
	factoryData.Lifetime = nil
	var candidateInput *CandidateRuntimeContext
	if candidate != nil {
		copy := candidate.runtime
		candidateInput = &copy
	} else if sealedActivationCandidate != nil {
		copy := *sealedActivationCandidate
		candidateInput = &copy
	}
	leaseHealthID := "sealed:" + string(state.ID)
	input := RuntimeInput{State: state, Artifact: artifact, ManagedData: factoryData, Candidate: candidateInput,
		OnLeaseRenewalFailure: func(renewalErr error) {
			m.setLeaseRenewalError(leaseHealthID, renewalErr)
			if renewalErr != nil && m.onLeaseRenewalFailure != nil {
				m.onLeaseRenewalFailure(renewalErr)
			}
		},
	}
	if sealedActivationCandidate != nil {
		// Keep activation candidate evidence in its dedicated field. The regular
		// Candidate field selects candidate-specific dependency evidence in
		// factories; native activation must instead use the persisted generation's
		// release provenance while the target pointer is still on its base.
		input.Candidate = nil
		input.SealedActivationCandidate = candidateInput
	}
	sealed := false
	var runtime PreparedRuntime
	var err error
	if m.requireSealedCatalog {
		factory, ok := m.factory.(SealedRuntimeFactory)
		if !ok {
			return nil, errors.Join(errors.New("production serving requires a sealed catalog runtime factory"), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
		}
		runtime, err = factory.PrepareSealed(ctx, input)
		sealed = true
	} else {
		runtime, err = m.factory.Prepare(ctx, input)
	}
	if err != nil {
		return nil, errors.Join(err, releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	if runtime == nil {
		return nil, errors.Join(errors.New("runtime factory returned nil"), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	expectedIdentity, err := projectgraph.NewServingIdentity(state.ProjectID, string(servingstate.Environment(state.Environment)), string(state.ID))
	if err != nil {
		return nil, errors.Join(err, closeRuntime(runtime), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	authorization := runtime.AuthorizationSnapshot()
	if authorization.Identity() != expectedIdentity {
		return nil, errors.Join(fmt.Errorf("authorization snapshot identity = %#v, want %#v", authorization.Identity(), expectedIdentity), closeRuntime(runtime), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	if err := authorization.ValidateBound(); err != nil {
		return nil, errors.Join(fmt.Errorf("authorization snapshot is invalid: %w", err), closeRuntime(runtime), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	snapshotID := int64(0)
	if !sealed {
		snapshotID = state.DuckLakeSnapshotID
		if snap, ok := runtime.(RuntimeSnapshot); ok && snap.DuckLakeSnapshotID() > 0 {
			snapshotID = snap.DuckLakeSnapshotID()
		}
	}
	var lease *persistentSnapshotLease
	if !sealed {
		lease, err = m.createPersistentLease(ctx, state.ID, snapshotID)
		if err != nil {
			return nil, errors.Join(err, closeRuntime(runtime), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
		}
	}
	m.mu.RLock()
	baseActiveID := servingstate.ID("")
	if m.current != nil {
		baseActiveID = m.current.servingStateID
	}
	m.mu.RUnlock()
	p := &Prepared{owner: m, servingStateID: state.ID, digest: artifact.Digest, managedRevision: data.RevisionID, runtime: runtime, managedData: data.Lifetime, snapshotLease: lease, snapshotID: snapshotID, sealed: sealed, authorization: authorization, baseActiveID: baseActiveID}
	if candidate != nil {
		p.runtimeLifetime = candidate.lifetime
		candidate.lifetime = nil
		p.candidateID = candidate.runtime.CandidateID
		p.candidateOwner = candidate.runtime.OwnerID
		p.candidateExpiry = candidate.expiresAt
		p.candidateHash = candidate.fingerprint
	}
	return p, nil
}

// ActivatePrepared publishes a durable generation only after authorization
// installation and the caller's durable activation callback both succeed.
func (m *Manager) ActivatePrepared(candidate *Prepared, activate func() error) error {
	return m.activatePreparedContext(context.Background(), candidate, activate)
}
func (m *Manager) activatePreparedContext(ctx context.Context, candidate *Prepared, activate func() error) error {
	sealed, err := m.sealPrepared(candidate)
	if err != nil {
		return err
	}
	if sealed.candidateID != "" {
		return errors.Join(errors.New("private candidate runtime cannot be activated"), sealed.abort())
	}
	m.cutoverMu.Lock()
	defer m.cutoverMu.Unlock()
	m.mu.RLock()
	closed := m.closed
	currentID := servingstate.ID("")
	boundProjectID := m.projectID
	if m.current != nil {
		currentID = m.current.servingStateID
	}
	m.mu.RUnlock()
	if closed {
		return errors.Join(errors.New("runtime host is closed"), sealed.abort())
	}
	if currentID != sealed.baseActiveID {
		return errors.Join(ErrPreparedStale, sealed.abort())
	}
	if boundProjectID != "" && sealed.authorization.Identity().ProjectID != boundProjectID {
		return errors.Join(fmt.Errorf("prepared runtime project = %q, want %q", sealed.authorization.Identity().ProjectID, boundProjectID), sealed.abort())
	}
	if boundProjectID == "" {
		boundProjectID = sealed.authorization.Identity().ProjectID
	}
	expectedIdentity, err := projectgraph.NewServingIdentity(boundProjectID, string(m.environment), string(sealed.servingStateID))
	if err != nil || sealed.authorization.Identity() != expectedIdentity {
		if err == nil {
			err = fmt.Errorf("authorization snapshot identity = %#v, want %#v", sealed.authorization.Identity(), expectedIdentity)
		}
		return errors.Join(err, sealed.abort())
	}
	if err := sealed.authorization.ValidateBound(); err != nil {
		return errors.Join(fmt.Errorf("authorization snapshot is invalid: %w", err), sealed.abort())
	}
	if m.authorization == nil {
		return errors.Join(errors.New("authorization snapshot installer is required"), sealed.abort())
	}
	if err := m.authorization.InstallAuthorizationSnapshot(ctx, sealed.authorization); err != nil {
		return errors.Join(err, sealed.abort())
	}
	if activate == nil {
		return errors.Join(errors.New("metadata activation is required"), sealed.abort())
	}
	if err := activate(); err != nil {
		return errors.Join(err, sealed.abort())
	}
	// Cutover serialization proves the project binding cannot change between
	// the validated read above and this commit. There is deliberately no
	// fallible path after the durable activation callback succeeds.
	m.mu.Lock()
	if m.projectID == "" {
		m.projectID = boundProjectID
	}
	m.mu.Unlock()
	retired := sealed.publish()
	m.cleanupRetired(retired)
	return nil
}

type sealedPrepared struct {
	manager                     *Manager
	source                      *Prepared
	servingStateID              servingstate.ID
	digest, managedRevision     string
	runtime                     Runtime
	managedData                 ManagedDataLifetime
	snapshotLease               *persistentSnapshotLease
	runtimeLifetime             RuntimeLifetime
	snapshotID                  int64
	sealed                      bool
	authorization               accesssnapshot.AuthorizationSnapshot
	noChange                    bool
	candidateID, candidateOwner string
	candidateExpiry             time.Time
	candidateHash               [32]byte
	baseActiveID                servingstate.ID
}

func (m *Manager) sealPrepared(candidate *Prepared) (*sealedPrepared, error) {
	if candidate == nil {
		return nil, errors.New("prepared runtime is nil")
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if candidate.owner != m {
		return nil, errors.New("prepared runtime belongs to a different host")
	}
	if candidate.state != preparedStateOpen {
		return nil, errors.New("prepared runtime is already consumed")
	}
	if candidate.runtime == nil && !candidate.noChange {
		return nil, errors.New("prepared runtime is incomplete")
	}
	s := &sealedPrepared{manager: m, source: candidate, servingStateID: candidate.servingStateID, digest: candidate.digest, managedRevision: candidate.managedRevision, runtime: candidate.runtime, managedData: candidate.managedData, snapshotLease: candidate.snapshotLease, runtimeLifetime: candidate.runtimeLifetime, snapshotID: candidate.snapshotID, sealed: candidate.sealed, authorization: candidate.authorization, noChange: candidate.noChange, candidateID: candidate.candidateID, candidateOwner: candidate.candidateOwner, candidateExpiry: candidate.candidateExpiry, candidateHash: candidate.candidateHash, baseActiveID: candidate.baseActiveID}
	candidate.runtime = nil
	candidate.managedData = nil
	candidate.snapshotLease = nil
	candidate.runtimeLifetime = nil
	candidate.state = preparedStateSealed
	return s, nil
}
func (s *sealedPrepared) publish() *managedRuntime {
	if s == nil {
		return nil
	}
	if s.noChange {
		s.finish(preparedStatePublished)
		return nil
	}
	next := &managedRuntime{identity: projectgraph.ServingIdentity{ProjectID: s.manager.projectID, Environment: string(s.manager.environment), GenerationID: string(s.servingStateID)}, authorization: s.authorization, servingStateID: s.servingStateID, digest: s.digest, managedRevision: s.managedRevision, runtime: s.runtime, managedData: s.managedData, snapshotLease: s.snapshotLease, runtimeLifetime: s.runtimeLifetime, snapshotID: s.snapshotID, sealed: s.sealed}
	s.manager.mu.Lock()
	old := s.manager.current
	s.manager.current = next
	retired := s.manager.retireLocked(old)
	s.manager.mu.Unlock()
	s.runtime = nil
	s.managedData = nil
	s.snapshotLease = nil
	s.runtimeLifetime = nil
	s.finish(preparedStatePublished)
	return retired
}
func (s *sealedPrepared) consumeCandidate() (*managedRuntime, error) {
	if s == nil || s.noChange || s.runtime == nil || s.candidateID == "" {
		if s != nil {
			s.finish(preparedStateClosed)
		}
		return nil, errors.New("candidate preparation must own an isolated runtime")
	}
	next := &managedRuntime{identity: projectgraph.ServingIdentity{ProjectID: s.manager.projectID, Environment: string(s.manager.environment), GenerationID: string(s.servingStateID)}, authorization: s.authorization, servingStateID: s.servingStateID, digest: s.digest, managedRevision: s.managedRevision, runtime: s.runtime, managedData: s.managedData, snapshotLease: s.snapshotLease, runtimeLifetime: s.runtimeLifetime, snapshotID: s.snapshotID, sealed: s.sealed}
	s.runtime = nil
	s.managedData = nil
	s.snapshotLease = nil
	s.runtimeLifetime = nil
	s.finish(preparedStateRegistered)
	return next, nil
}
func (s *sealedPrepared) abort() error {
	if s == nil {
		return nil
	}
	err := s.manager.closeManaged(&managedRuntime{servingStateID: s.servingStateID, runtime: s.runtime, managedData: s.managedData, snapshotLease: s.snapshotLease, runtimeLifetime: s.runtimeLifetime, snapshotID: s.snapshotID})
	s.runtime = nil
	s.managedData = nil
	s.snapshotLease = nil
	s.runtimeLifetime = nil
	s.finish(preparedStateClosed)
	return err
}
func (s *sealedPrepared) finish(state preparedState) {
	if s == nil || s.source == nil {
		return
	}
	s.source.mu.Lock()
	s.source.state = state
	s.source.mu.Unlock()
}
