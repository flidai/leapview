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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

type ManagedDataResolution struct {
	RevisionID string
	Roots      map[string]string
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
}

type CandidateRuntimeContext struct {
	CandidateID              string
	OwnerID                  string
	AuthorizationFingerprint string
	BindingFingerprint       string
	CompatibilityFingerprint string
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
}

type Manager struct {
	mu                     sync.RWMutex
	cutoverMu              sync.RWMutex
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
	m := &Manager{repo: options.Repo, projectID: options.ProjectID, environment: environment, factory: options.Factory, managedData: options.ManagedData, authorization: options.Authorization, onDrained: options.OnDrained, leaseTTL: normalizedLeaseTTL(options.LeaseTTL), leaseOwner: firstNonEmpty(options.LeaseOwner, "runtimehost"), logger: logger, onLeaseRenewalFailure: options.OnLeaseRenewalFailure, onCleanupFailure: options.OnCleanupFailure, leaseRenewalErrors: map[string]error{}, cleanupDrainTimeout: normalizedCleanupDrainTimeout(options.CleanupDrainTimeout), releaseShutdownTimeout: shutdown}
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

func (m *Manager) validateGeneration(state servingstate.State, artifact servingstate.Artifact) error {
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
	var data ManagedDataResolution
	var err error
	if m.managedData != nil {
		data, err = m.resolveManagedData(ctx, state)
		if err != nil {
			return nil, err
		}
	}
	return m.prepareResolvedWithCandidate(ctx, state, artifact, data, candidate)
}

func (m *Manager) prepareResolvedWithCandidate(ctx context.Context, state servingstate.State, artifact servingstate.Artifact, data ManagedDataResolution, candidate *candidatePreparationContext) (*Prepared, error) {
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
	}
	runtime, err := m.factory.Prepare(ctx, RuntimeInput{State: state, Artifact: artifact, ManagedData: factoryData, Candidate: candidateInput})
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
	snapshotID := state.DuckLakeSnapshotID
	if snap, ok := runtime.(RuntimeSnapshot); ok && snap.DuckLakeSnapshotID() > 0 {
		snapshotID = snap.DuckLakeSnapshotID()
	}
	lease, err := m.createPersistentLease(ctx, state.ID, snapshotID)
	if err != nil {
		return nil, errors.Join(err, closeRuntime(runtime), releaseManaged(data.Lifetime), closeCandidatePreparationLifetime(candidate))
	}
	m.mu.RLock()
	baseActiveID := servingstate.ID("")
	if m.current != nil {
		baseActiveID = m.current.servingStateID
	}
	m.mu.RUnlock()
	p := &Prepared{owner: m, servingStateID: state.ID, digest: artifact.Digest, managedRevision: data.RevisionID, runtime: runtime, managedData: data.Lifetime, snapshotLease: lease, snapshotID: snapshotID, authorization: authorization, baseActiveID: baseActiveID}
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
	currentID := servingstate.ID("")
	boundProjectID := m.projectID
	if m.current != nil {
		currentID = m.current.servingStateID
	}
	m.mu.RUnlock()
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
	s := &sealedPrepared{manager: m, source: candidate, servingStateID: candidate.servingStateID, digest: candidate.digest, managedRevision: candidate.managedRevision, runtime: candidate.runtime, managedData: candidate.managedData, snapshotLease: candidate.snapshotLease, runtimeLifetime: candidate.runtimeLifetime, snapshotID: candidate.snapshotID, authorization: candidate.authorization, noChange: candidate.noChange, candidateID: candidate.candidateID, candidateOwner: candidate.candidateOwner, candidateExpiry: candidate.candidateExpiry, candidateHash: candidate.candidateHash, baseActiveID: candidate.baseActiveID}
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
	next := &managedRuntime{identity: projectgraph.ServingIdentity{ProjectID: s.manager.projectID, Environment: string(s.manager.environment), GenerationID: string(s.servingStateID)}, authorization: s.authorization, servingStateID: s.servingStateID, digest: s.digest, managedRevision: s.managedRevision, runtime: s.runtime, managedData: s.managedData, snapshotLease: s.snapshotLease, runtimeLifetime: s.runtimeLifetime, snapshotID: s.snapshotID}
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
	next := &managedRuntime{identity: projectgraph.ServingIdentity{ProjectID: s.manager.projectID, Environment: string(s.manager.environment), GenerationID: string(s.servingStateID)}, authorization: s.authorization, servingStateID: s.servingStateID, digest: s.digest, managedRevision: s.managedRevision, runtime: s.runtime, managedData: s.managedData, snapshotLease: s.snapshotLease, runtimeLifetime: s.runtimeLifetime, snapshotID: s.snapshotID}
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

func (m *Manager) Acquire(context.Context) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.closing {
		return nil, errors.New("no active LeapView serving state")
	}
	m.current.refs++
	return &runtimeLease{manager: m, managed: m.current}, nil
}
func (m *Manager) LeasedSnapshots() []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := map[int64]struct{}{}
	if m.current != nil && m.current.snapshotID > 0 {
		set[m.current.snapshotID] = struct{}{}
	}
	for _, r := range m.retired {
		if r.snapshotID > 0 {
			set[r.snapshotID] = struct{}{}
		}
	}
	return snapshotKeys(set)
}
func (m *Manager) Close() error {
	return m.close(false)
}

func (m *Manager) closeWithoutReleaseQueue() error {
	return m.close(true)
}

func (m *Manager) close(skipReleaseQueue bool) error {
	m.mu.Lock()
	current := m.current
	m.current = nil
	targets := m.retireLocked(current)
	waiting := m.scheduledCleanupLocked()
	m.mu.Unlock()
	m.cleanupRetired(targets)
	cleanupErr := m.waitForCleanup(waiting)
	if cleanupErr != nil {
		// Keep the release queue alive while reader-draining generations still
		// own persistent snapshot leases. A later Release can then enqueue its
		// cleanup after the caller resolves the shutdown timeout.
		go m.closeReleaseQueueAfterCleanup(waiting)
		return cleanupErr
	}
	var queueErr error
	if !skipReleaseQueue && m.releaseQueue != nil {
		queueErr = m.releaseQueue.close(m.releaseShutdownTimeout)
	}
	return errors.Join(cleanupErr, queueErr)
}

func (m *Manager) closeReleaseQueueAfterCleanup(targets []*managedRuntime) {
	for _, runtime := range targets {
		if runtime != nil && runtime.cleanupDone != nil {
			<-runtime.cleanupDone
		}
	}
	if m.releaseQueue != nil {
		_ = m.releaseQueue.close(m.releaseShutdownTimeout)
	}
}

func (m *Manager) retireLocked(runtime *managedRuntime) *managedRuntime {
	if runtime == nil {
		return nil
	}
	if runtime.closing {
		return nil
	}
	runtime.closing = true
	runtime.cleanupState = generationCleanupDraining
	runtime.cleanupDone = make(chan struct{})
	m.retired = append(m.retired, runtime)
	if runtime.refs == 0 {
		runtime.cleanupState = generationCleanupPending
		return runtime
	}
	return nil
}
func (m *Manager) release(runtime *managedRuntime) {
	var drained *managedRuntime
	m.mu.Lock()
	if runtime != nil && runtime.refs > 0 {
		runtime.refs--
		if runtime.refs == 0 && runtime.closing {
			drained = runtime
			runtime.cleanupState = generationCleanupPending
		}
	}
	m.mu.Unlock()
	m.cleanupRetired(drained)
}

type managedRuntime struct {
	identity                projectgraph.ServingIdentity
	authorization           accesssnapshot.AuthorizationSnapshot
	servingStateID          servingstate.ID
	digest, managedRevision string
	runtime                 Runtime
	managedData             ManagedDataLifetime
	snapshotLease           *persistentSnapshotLease
	runtimeLifetime         RuntimeLifetime
	snapshotID              int64
	refs                    int
	closing                 bool
	cleanupState            generationCleanupState
	cleanupDone             chan struct{}
	cleanupErr              error
	cleanupOnce             sync.Once
	cleanupResults          []cleanupResult
}
type generationCleanupState uint8

const (
	generationCleanupNone generationCleanupState = iota
	generationCleanupDraining
	generationCleanupPending
	generationCleanupRunning
	generationCleanupFinished
)

type GenerationCleanupState string

const (
	GenerationCleanupDraining GenerationCleanupState = "draining_readers"
	GenerationCleanupPending  GenerationCleanupState = "cleanup_pending"
	GenerationCleanupRunning  GenerationCleanupState = "cleanup_running"
)

type RetiredGeneration struct {
	ServingStateID     servingstate.ID
	DuckLakeSnapshotID int64
	Readers            int
	CleanupState       GenerationCleanupState
}

func (m *Manager) RetiredGenerations() []RetiredGeneration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RetiredGeneration, 0, len(m.retired))
	for _, r := range m.retired {
		state := GenerationCleanupDraining
		if r.cleanupState == generationCleanupPending {
			state = GenerationCleanupPending
		} else if r.cleanupState == generationCleanupRunning {
			state = GenerationCleanupRunning
		}
		out = append(out, RetiredGeneration{r.servingStateID, r.snapshotID, r.refs, state})
	}
	return out
}
func (m *Manager) cleanupRetired(runtime *managedRuntime) {
	if runtime == nil {
		return
	}
	m.mu.Lock()
	if runtime.cleanupState == generationCleanupPending && !m.cleanupWorkerRunning {
		m.cleanupWorkerRunning = true
		go m.runCleanupWorker()
	}
	m.mu.Unlock()
}
func (m *Manager) runCleanupWorker() {
	for {
		m.mu.Lock()
		var runtime *managedRuntime
		for _, r := range m.retired {
			if r.cleanupState == generationCleanupPending {
				runtime = r
				break
			}
		}
		if runtime == nil {
			m.cleanupWorkerRunning = false
			m.mu.Unlock()
			return
		}
		runtime.cleanupState = generationCleanupRunning
		m.mu.Unlock()
		results := m.closeManagedResources(runtime)
		for _, result := range results {
			if result.err != nil && m.onCleanupFailure != nil {
				m.onCleanupFailure(CleanupFailure{ProjectID: m.ProjectID(), ServingStateID: runtime.servingStateID, DuckLakeSnapshotID: runtime.snapshotID, Resource: result.resource, Err: result.err})
			}
		}
		m.mu.Lock()
		var errs []error
		for _, result := range results {
			errs = append(errs, result.err)
		}
		runtime.cleanupErr = errors.Join(errs...)
		runtime.cleanupState = generationCleanupFinished
		m.removeRetiredLocked(runtime)
		close(runtime.cleanupDone)
		m.mu.Unlock()
		if m.onDrained != nil {
			m.onDrained(runtime.servingStateID, runtime.snapshotID)
		}
	}
}
func (m *Manager) removeRetiredLocked(runtime *managedRuntime) {
	for i, r := range m.retired {
		if r == runtime {
			m.retired = append(m.retired[:i], m.retired[i+1:]...)
			return
		}
	}
}
func (m *Manager) scheduledCleanupLocked() []*managedRuntime {
	out := make([]*managedRuntime, 0, len(m.retired))
	for _, r := range m.retired {
		if r.cleanupState != generationCleanupFinished {
			out = append(out, r)
		}
	}
	return out
}
func (m *Manager) waitForCleanup(targets []*managedRuntime) error {
	if len(targets) == 0 {
		return nil
	}
	timer := time.NewTimer(m.cleanupDrainTimeout)
	defer timer.Stop()
	var errs []error
	for _, r := range targets {
		select {
		case <-r.cleanupDone:
			errs = append(errs, r.cleanupErr)
		case <-timer.C:
			return errors.Join(errors.Join(errs...), fmt.Errorf("runtime cleanup did not drain within %s", m.cleanupDrainTimeout))
		}
	}
	return errors.Join(errs...)
}

type cleanupResult struct {
	resource CleanupResource
	err      error
}

func (m *Manager) closeManaged(runtime *managedRuntime) error {
	var errs []error
	for _, result := range m.closeManagedResources(runtime) {
		errs = append(errs, result.err)
	}
	return errors.Join(errs...)
}
func (m *Manager) closeManagedResources(runtime *managedRuntime) []cleanupResult {
	if runtime == nil {
		return nil
	}
	runtime.cleanupOnce.Do(func() {
		var out []cleanupResult
		if err := closeRuntime(runtime.runtime); err != nil {
			out = append(out, cleanupResult{CleanupResourceRuntime, err})
		}
		if err := releaseManaged(runtime.managedData); err != nil {
			out = append(out, cleanupResult{CleanupResourceManagedData, err})
		}
		if err := closeSnapshotLease(runtime.snapshotLease); err != nil {
			out = append(out, cleanupResult{CleanupResourceSnapshotLease, err})
		}
		if err := closeRuntimeLifetime(runtime.runtimeLifetime); err != nil {
			out = append(out, cleanupResult{CleanupResourceDependency, err})
		}
		runtime.cleanupResults = out
	})
	return append([]cleanupResult(nil), runtime.cleanupResults...)
}

type runtimeLease struct {
	manager *Manager
	managed *managedRuntime
	once    sync.Once
}

func (l *runtimeLease) Runtime() Runtime {
	if l == nil || l.managed == nil {
		return nil
	}
	return l.managed.runtime
}
func (l *runtimeLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	if l == nil || l.managed == nil {
		return accesssnapshot.AuthorizationSnapshot{}
	}
	return l.managed.authorization
}
func (l *runtimeLease) Identity() projectgraph.ServingIdentity {
	if l == nil || l.managed == nil {
		return projectgraph.ServingIdentity{}
	}
	return l.managed.identity
}
func (l *runtimeLease) DuckLakeSnapshotID() int64 {
	if l == nil || l.managed == nil {
		return 0
	}
	return l.managed.snapshotID
}
func (l *runtimeLease) Release() {
	if l == nil || l.manager == nil {
		return
	}
	l.once.Do(func() { l.manager.release(l.managed) })
}

type persistentSnapshotLease struct {
	repo           SnapshotLeaseRepository
	id             string
	servingStateID servingstate.ID
	snapshotID     int64
	cancel         context.CancelFunc
	enqueue        func(snapshotLeaseReleaseTask) error
	once           sync.Once
	err            error
}

func (l *persistentSnapshotLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		if l.enqueue != nil {
			l.err = l.enqueue(snapshotLeaseReleaseTask{repo: l.repo, leaseID: l.id, servingStateID: l.servingStateID, snapshotID: l.snapshotID})
		} else {
			l.err = releaseSnapshotLease(l.repo, l.id)
		}
	})
	return l.err
}
func (m *Manager) createPersistentLease(ctx context.Context, id servingstate.ID, snapshotID int64) (*persistentSnapshotLease, error) {
	repo, ok := m.repo.(SnapshotLeaseRepository)
	if !ok || snapshotID <= 0 {
		return nil, nil
	}
	leaseID, err := repo.CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{ServingStateID: id, DuckLakeSnapshotID: snapshotID, OwnerID: m.leaseOwner, ExpiresAt: time.Now().Add(m.leaseTTL)})
	if err != nil {
		return nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(context.Background())
	go m.heartbeatLease(heartbeatCtx, repo, leaseID)
	return &persistentSnapshotLease{repo: repo, id: leaseID, servingStateID: id, snapshotID: snapshotID, cancel: cancel, enqueue: m.releaseQueue.enqueue}, nil
}
func (m *Manager) heartbeatLease(ctx context.Context, repo SnapshotLeaseRepository, id string) {
	defer m.setLeaseRenewalError(id, nil)
	interval := m.leaseTTL / 2
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := renewSnapshotLease(ctx, repo, id, time.Now().Add(m.leaseTTL), 3, 100*time.Millisecond)
			m.setLeaseRenewalError(id, err)
			if err != nil && m.onLeaseRenewalFailure != nil {
				m.onLeaseRenewalFailure(err)
			}
		}
	}
}
func renewSnapshotLease(ctx context.Context, repo SnapshotLeaseRepository, id string, expires time.Time, attempts int, backoff time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		request, cancel := context.WithTimeout(ctx, 5*time.Second)
		last = repo.ExtendQuerySnapshotLease(request, id, expires)
		cancel()
		if last == nil {
			return nil
		}
		if i+1 < attempts {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
		}
	}
	return fmt.Errorf("extend snapshot lease %q after %d attempts: %w", id, attempts, last)
}
func releaseSnapshotLease(repo SnapshotLeaseRepository, id string) error {
	if repo == nil || id == "" {
		return nil
	}
	delay := 25 * time.Millisecond
	var last error
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		last = repo.ReleaseQuerySnapshotLease(ctx, id)
		cancel()
		if last == nil {
			return nil
		}
		time.Sleep(delay)
		delay *= 2
	}
	return last
}
func (m *Manager) releaseSnapshotLease(task snapshotLeaseReleaseTask) error {
	err := releaseSnapshotLease(task.repo, task.leaseID)
	if err != nil && m.onCleanupFailure != nil {
		m.onCleanupFailure(CleanupFailure{ProjectID: m.ProjectID(), ServingStateID: task.servingStateID, DuckLakeSnapshotID: task.snapshotID, Resource: CleanupResourceSnapshotLease, Err: err})
	}
	return err
}

type snapshotLeaseReleaseTask struct {
	repo           SnapshotLeaseRepository
	leaseID        string
	servingStateID servingstate.ID
	snapshotID     int64
}
type snapshotLeaseReleaseQueue struct {
	mu         sync.Mutex
	queue      chan snapshotLeaseReleaseTask
	accepting  bool
	workerDone chan struct{}
	process    func(snapshotLeaseReleaseTask) error
	pending    atomic.Int64
}

func newSnapshotLeaseReleaseQueue(capacity int, process func(snapshotLeaseReleaseTask) error) *snapshotLeaseReleaseQueue {
	q := &snapshotLeaseReleaseQueue{queue: make(chan snapshotLeaseReleaseTask, capacity), accepting: true, workerDone: make(chan struct{}), process: process}
	go q.run()
	return q
}
func (q *snapshotLeaseReleaseQueue) enqueue(task snapshotLeaseReleaseTask) error {
	if q == nil || task.repo == nil || task.leaseID == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.accepting {
		return errors.New("snapshot lease release queue is closed")
	}
	select {
	case q.queue <- task:
		q.pending.Add(1)
		return nil
	default:
		return errors.New("snapshot lease release queue is full")
	}
}
func (q *snapshotLeaseReleaseQueue) run() {
	defer close(q.workerDone)
	for task := range q.queue {
		if q.process != nil {
			_ = q.process(task)
		}
		q.pending.Add(-1)
	}
}
func (q *snapshotLeaseReleaseQueue) close(timeout time.Duration) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if q.accepting {
		q.accepting = false
		close(q.queue)
	}
	q.mu.Unlock()
	if timeout <= 0 {
		<-q.workerDone
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-q.workerDone:
		return nil
	case <-timer.C:
		return fmt.Errorf("snapshot lease release queue did not drain before shutdown (remaining=%d)", q.len())
	}
}
func (q *snapshotLeaseReleaseQueue) len() int {
	if q == nil {
		return 0
	}
	return int(q.pending.Load())
}

func closeRuntime(runtime Runtime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Close()
}
func releaseManaged(value ManagedDataLifetime) error {
	if value == nil {
		return nil
	}
	return value.Release()
}
func closeRuntimeLifetime(value RuntimeLifetime) error {
	if value == nil {
		return nil
	}
	return value.Close()
}
func closeSnapshotLease(value *persistentSnapshotLease) error {
	if value == nil {
		return nil
	}
	return value.Close()
}
func closeCandidatePreparationLifetime(candidate *candidatePreparationContext) error {
	if candidate == nil {
		return nil
	}
	err := closeRuntimeLifetime(candidate.lifetime)
	candidate.lifetime = nil
	return err
}
func normalizedLeaseTTL(value time.Duration) time.Duration {
	if value <= 0 {
		return 5 * time.Minute
	}
	return value
}
func normalizedCleanupDrainTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return 15 * time.Second
	}
	return value
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func snapshotKeys(values map[int64]struct{}) []int64 {
	keys := make([]int64, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
