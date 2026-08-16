package runtimehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// Registry is retained as the process-local composition object, but owns one
// manager only. There is no container map and no partial graph swap.
type RegistryOptions struct {
	Repo                        ServingStateRepository
	ProjectID                   projectgraph.ResourceID
	Environment                 servingstate.Environment
	Factory                     RuntimeFactory
	ManagedData                 ManagedDataResolver
	Authorization               AuthorizationSnapshotInstaller
	Now                         func() time.Time
	OnDrained                   func(servingstate.ID, int64)
	Logger                      *slog.Logger
	OnCleanupFailure            func(CleanupFailure)
	OnLeaseRenewalFailure       func(error)
	LeaseTTL                    time.Duration
	LeaseOwner                  string
	LeaseReleaseQueueCapacity   int
	LeaseReleaseShutdownTimeout time.Duration
	CleanupDrainTimeout         time.Duration
}

type Registry struct {
	mu         sync.Mutex
	manager    *Manager
	candidates *candidateRuntimeRegistry
	now        func() time.Time
	closed     bool
	closeErr   error
	closeDone  chan struct{}
}

var ErrRegistryClosed = errors.New("runtime registry closed")

type PreparedVerification struct{ Digest string }
type RuntimeVerifier interface{ Verify(context.Context) error }

type ServingStateCandidate struct {
	Identity    projectgraph.ServingIdentity
	ManagedData ManagedDataResolution
}

func NewRegistryWithFactory(options RegistryOptions) *Registry {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	r := &Registry{now: now, closeDone: make(chan struct{})}
	r.manager = NewManagerWithFactory(ManagerOptions{Repo: options.Repo, ProjectID: options.ProjectID, Environment: options.Environment, Factory: options.Factory, ManagedData: options.ManagedData, Authorization: options.Authorization, OnDrained: options.OnDrained, Logger: options.Logger, OnCleanupFailure: options.OnCleanupFailure, OnLeaseRenewalFailure: options.OnLeaseRenewalFailure, LeaseTTL: options.LeaseTTL, LeaseOwner: options.LeaseOwner, LeaseReleaseQueueCapacity: options.LeaseReleaseQueueCapacity, LeaseReleaseShutdownTimeout: options.LeaseReleaseShutdownTimeout, CleanupDrainTimeout: options.CleanupDrainTimeout})
	r.candidates = newCandidateRuntimeRegistry(now)
	return r
}

func (r *Registry) ProjectID() projectgraph.ResourceID {
	if r == nil || r.manager == nil {
		return ""
	}
	return r.manager.ProjectID()
}

// ActiveArtifact resolves activity from the repository for this registry's
// fixed project/environment scope. Callers should use errors.Is with
// servingstate.ErrNotFound to distinguish an empty deployment from a store
// failure.
func (r *Registry) ActiveArtifact(ctx context.Context) (servingstate.State, servingstate.Artifact, error) {
	if r == nil || r.manager == nil {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return r.manager.ActiveArtifact(ctx)
}

func (r *Registry) Environment() servingstate.Environment {
	if r == nil || r.manager == nil {
		return ""
	}
	return r.manager.Environment()
}
func (r *Registry) Reload(ctx context.Context) error {
	if r == nil || r.manager == nil {
		return ErrRegistryClosed
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ErrRegistryClosed
	}
	return r.manager.Reload(ctx)
}
func (r *Registry) PrepareServingState(ctx context.Context, id string) (*Prepared, error) {
	if r == nil || r.manager == nil {
		return nil, ErrRegistryClosed
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrRegistryClosed
	}
	prepared, err := r.manager.PrepareServingState(ctx, id)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (r *Registry) PrepareServingStateCandidate(ctx context.Context, input ServingStateCandidate) (*Prepared, error) {
	if r == nil || r.manager == nil {
		return nil, ErrRegistryClosed
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrRegistryClosed
	}
	if err := input.Identity.Validate(); err != nil {
		return nil, fmt.Errorf("candidate serving identity is invalid: %w", err)
	}
	if (r.ProjectID() != "" && input.Identity.ProjectID != r.ProjectID()) || input.Identity.Environment != string(r.Environment()) {
		return nil, fmt.Errorf("candidate serving identity is outside project environment")
	}
	state, err := r.manager.repo.ByID(ctx, servingstate.ID(input.Identity.GenerationID))
	if err != nil {
		return nil, err
	}
	if state.ProjectID != input.Identity.ProjectID || servingstate.Environment(state.Environment) != servingstate.Environment(input.Identity.Environment) {
		return nil, fmt.Errorf("serving state %s is outside project environment", input.Identity.GenerationID)
	}
	artifact, err := r.manager.repo.ArtifactByServingState(ctx, state.ID)
	if err != nil {
		return nil, err
	}
	// Managed data was resolved by the caller and its lifetime is transferred
	// to the prepared generation exactly once.
	prepared, err := r.manager.prepareResolved(ctx, state, artifact, input.ManagedData)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (m *Manager) prepareResolved(ctx context.Context, state servingstate.State, artifact servingstate.Artifact, data ManagedDataResolution) (*Prepared, error) {
	return m.prepareResolvedWithCandidate(ctx, state, artifact, data, nil)
}

func (r *Registry) VerifyPrepared(ctx context.Context, prepared *Prepared) (PreparedVerification, error) {
	if prepared == nil || prepared.owner != r.manager {
		return PreparedVerification{}, errors.New("prepared runtime belongs to a different host")
	}
	prepared.mu.Lock()
	if prepared.state != preparedStateOpen {
		prepared.mu.Unlock()
		return PreparedVerification{}, errors.New("prepared runtime is no longer verifiable")
	}
	runtime := prepared.runtime
	id := prepared.servingStateID
	digest := prepared.digest
	snapshot := prepared.snapshotID
	prepared.mu.Unlock()
	if runtime == nil {
		return PreparedVerification{}, errors.New("prepared runtime is incomplete")
	}
	verifier, ok := runtime.(RuntimeVerifier)
	if !ok {
		return PreparedVerification{}, fmt.Errorf("prepared runtime %s does not support verification", id)
	}
	if err := verifier.Verify(ctx); err != nil {
		return PreparedVerification{}, fmt.Errorf("verify prepared runtime %s: %w", id, err)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", id, digest, snapshot)))
	return PreparedVerification{Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func (r *Registry) ActivatePrepared(candidate *Prepared, activate func() error) error {
	if candidate == nil || candidate.owner != r.manager {
		return errors.New("prepared runtime belongs to a different host")
	}
	if candidate.candidateID != "" {
		return errors.New("private candidate runtime cannot be activated")
	}
	return r.manager.ActivatePrepared(candidate, activate)
}
func (r *Registry) ActivatePreparedContext(ctx context.Context, prepared *Prepared, activate func() error) error {
	if prepared == nil || prepared.owner != r.manager {
		return errors.New("prepared runtime belongs to a different host")
	}
	return r.manager.activatePreparedContext(ctx, prepared, activate)
}

func (r *Registry) Acquire(ctx context.Context) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.manager == nil {
		return nil, ErrRegistryClosed
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrRegistryClosed
	}
	return r.manager.Acquire(ctx)
}
func (r *Registry) Provider() Provider { return r }
func (r *Registry) LeasedSnapshots() []int64 {
	if r == nil || r.manager == nil {
		return nil
	}
	return r.manager.LeasedSnapshots()
}
func (r *Registry) LeaseRenewalError() error {
	if r == nil || r.manager == nil {
		return nil
	}
	return r.manager.LeaseRenewalError()
}
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		return r.closeErr
	}
	r.closed = true
	r.mu.Unlock()
	var errs []error
	var candidateTargets []*candidateGeneration
	var candidateCleanupErr error
	if r.candidates != nil {
		drained, targets := r.candidates.close()
		candidateTargets = targets
		for _, g := range drained {
			r.cleanupCandidateGeneration(g)
		}
		if candidateCleanupErr = r.waitForCandidateCleanup(targets); candidateCleanupErr != nil {
			errs = append(errs, candidateCleanupErr)
		}
	}
	if r.manager != nil {
		if candidateCleanupErr != nil {
			errs = append(errs, r.manager.closeWithoutReleaseQueue())
			go func() {
				_ = r.waitForCandidateCleanup(candidateTargets)
				if r.manager.releaseQueue != nil {
					_ = r.manager.releaseQueue.close(r.manager.releaseShutdownTimeout)
				}
			}()
		} else {
			errs = append(errs, r.manager.Close())
		}
	}
	r.closeErr = errors.Join(errs...)
	close(r.closeDone)
	return r.closeErr
}

// Candidate runtime methods remain project-scoped.  They intentionally do
// not expose project or target selectors.
func (r *Registry) RegisterPreparedCandidate(reg CandidateRegistration, candidate servingstate.PreparedRuntime) error {
	return r.registerPreparedCandidate(reg, candidate)
}
func (r *Registry) PrepareCandidate(ctx context.Context, input CandidatePreparation) (servingstate.PreparedRuntime, error) {
	return r.prepareCandidate(ctx, input)
}
func (r *Registry) PrepareAndRegisterCandidate(ctx context.Context, input CandidatePreparation) error {
	candidate, err := r.PrepareCandidate(ctx, input)
	if err != nil {
		return err
	}
	return r.RegisterPreparedCandidate(input.Registration, candidate)
}
func (r *Registry) AcquireCandidate(ctx context.Context, request CandidateLeaseRequest) (Lease, error) {
	if r == nil || r.candidates == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, fingerprint, err := normalizeLeaseRequest(request, r.now())
	if err != nil {
		return nil, err
	}
	boundProjectID := r.ProjectID()
	if boundProjectID == "" {
		return nil, fmt.Errorf("%w: runtime host project is not bound", ErrCandidateRuntimeIncompatible)
	}
	if normalized.ProjectID != boundProjectID {
		return nil, fmt.Errorf("%w: candidate project does not match runtime host", ErrCandidateRuntimeIncompatible)
	}
	g, retired, err := r.candidates.acquire(normalized.CandidateID, normalized.OwnerID, normalized.ProjectID, fingerprint)
	for _, x := range retired {
		r.cleanupCandidateGeneration(x)
	}
	if err != nil {
		return nil, err
	}
	return &candidateRuntimeLease{registry: r, generation: g}, nil
}
func (r *Registry) ResolveOwnedCandidate(candidateID, ownerID string) (OwnedCandidateView, error) {
	if r == nil || r.candidates == nil {
		return OwnedCandidateView{}, ErrCandidateRuntimeClosed
	}
	if candidateID != strings.TrimSpace(candidateID) || ownerID != strings.TrimSpace(ownerID) || candidateID == "" || ownerID == "" {
		return OwnedCandidateView{}, ErrCandidateRuntimeNotFound
	}
	view, retired, err := r.candidates.resolveOwned(candidateID, ownerID, r.ProjectID(), r)
	for _, g := range retired {
		r.cleanupCandidateGeneration(g)
	}
	return view, err
}
func (r *Registry) RetireCandidate(id string) int {
	if r == nil || r.candidates == nil || id != strings.TrimSpace(id) {
		return 0
	}
	retired, count := r.candidates.retire(id)
	for _, g := range retired {
		r.cleanupCandidateGeneration(g)
	}
	return count
}
func (r *Registry) ReapExpiredCandidates(now time.Time) int {
	if r == nil || r.candidates == nil {
		return 0
	}
	retired, count := r.candidates.reap(now)
	for _, g := range retired {
		r.cleanupCandidateGeneration(g)
	}
	return count
}
func (r *Registry) cleanupCandidateGeneration(g *candidateGeneration) {
	if g == nil {
		return
	}
	g.cleanupOnce.Do(func() {
		results := r.manager.closeManagedResources(g.managed)
		var errs []error
		for _, result := range results {
			if result.err != nil {
				errs = append(errs, result.err)
				if r.manager.onCleanupFailure != nil {
					r.manager.onCleanupFailure(CleanupFailure{ProjectID: r.ProjectID(), ServingStateID: g.managed.servingStateID, DuckLakeSnapshotID: g.managed.snapshotID, Resource: result.resource, Err: result.err})
				}
			}
		}
		g.cleanupErr = errors.Join(errs...)
		close(g.cleanupDone)
	})
}
func (r *Registry) waitForCandidateCleanup(targets []*candidateGeneration) error {
	if len(targets) == 0 {
		return nil
	}
	timer := time.NewTimer(r.manager.cleanupDrainTimeout)
	defer timer.Stop()
	var errs []error
	for _, g := range targets {
		select {
		case <-g.cleanupDone:
			errs = append(errs, g.cleanupErr)
		case <-timer.C:
			return errors.Join(errors.Join(errs...), fmt.Errorf("candidate cleanup did not drain before shutdown"))
		}
	}
	return errors.Join(errs...)
}

var _ Provider = (*Registry)(nil)
