package runtimehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var (
	ErrCandidateRuntimeInvalid      = errors.New("candidate runtime invalid")
	ErrCandidateRuntimeNotFound     = errors.New("candidate runtime not found")
	ErrCandidateRuntimeIncompatible = errors.New("candidate runtime incompatible")
	ErrCandidateRuntimeConflict     = errors.New("candidate runtime conflict")
	ErrCandidateRuntimeExpired      = errors.New("candidate runtime expired")
	ErrCandidateRuntimeClosed       = errors.New("candidate runtime registry closed")
)

// CandidateBindingVersion is the non-secret identity of one validated target
// connection generation used to prepare a candidate runtime.
type CandidateBindingVersion struct {
	BindingID          string
	LogicalConnection  string
	ConnectorKind      string
	Revision           int64
	ProviderVersion    string
	EndpointConfigHash string
}

type CandidateRestriction struct {
	ID             string
	WorkspaceID    string
	ObjectID       string
	PolicyType     string
	ExpressionJSON string
}

type CandidateDataMode string

const (
	CandidateDataReuseSnapshot  CandidateDataMode = "reuse_snapshot"
	CandidateDataRefreshSources CandidateDataMode = "refresh_sources"
)

type CandidateAuthoredConnection struct {
	LogicalConnection string
	ConnectorKind     string
}

// CandidateCompatibility describes every runtime-wide boundary that must
// remain equal before a private candidate generation can be leased.
//
// Query-specific semantic and effective-policy fingerprints remain part of
// query/result cache keys. AuthorizationFingerprint is the effective
// principal/policy boundary for acquiring this runtime generation.
type CandidateCompatibility struct {
	ArtifactDigest           string
	DataRevision             string
	DataMode                 CandidateDataMode
	RuntimeVersion           string
	AuthorizationFingerprint string
	Bindings                 []CandidateBindingVersion
	AuthoredConnections      []CandidateAuthoredConnection
	ManagedDataConnections   []string
	Restrictions             []CandidateRestriction
}

type CandidateRegistration struct {
	CandidateID   string
	OwnerID       string
	WorkspaceID   servingstate.WorkspaceID
	ExpiresAt     time.Time
	Compatibility CandidateCompatibility
}

type CandidateLeaseRequest struct {
	CandidateID   string
	OwnerID       string
	WorkspaceID   servingstate.WorkspaceID
	Compatibility CandidateCompatibility
}

type CandidatePreparation struct {
	Registration   CandidateRegistration
	ServingStateID string
	Lifetime       RuntimeLifetime
}

type candidateRuntimeKey struct {
	candidateID string
	workspaceID servingstate.WorkspaceID
}

type candidateGeneration struct {
	key           candidateRuntimeKey
	ownerID       string
	expiresAt     time.Time
	compatibility CandidateCompatibility
	fingerprint   [sha256.Size]byte
	manager       *Manager
	managed       *managedRuntime
	refs          int
	closing       bool
	cleanupDone   chan struct{}
	cleanupOnce   sync.Once
}

// OwnedCandidateView is server-resolved metadata for an authenticated
// candidate owner. Compatibility details never need to round-trip through the
// browser before the private runtime can be acquired.
type OwnedCandidateView struct {
	CandidateID string
	Workspaces  []OwnedCandidateWorkspace
}

type OwnedCandidateWorkspace struct {
	WorkspaceID              servingstate.WorkspaceID
	AuthorizationFingerprint string
	Provider                 Provider
	Restrictions             []CandidateRestriction
}

type ownedCandidateProvider struct {
	registry    *Registry
	candidateID string
	ownerID     string
	workspaceID servingstate.WorkspaceID
}

func (p ownedCandidateProvider) Acquire(ctx context.Context) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.registry == nil || p.registry.candidates == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	generation, retired, err := p.registry.candidates.acquireOwned(
		p.candidateID,
		p.ownerID,
		p.workspaceID,
	)
	p.registry.cleanupCandidateGeneration(retired)
	if err != nil {
		return nil, err
	}
	return &candidateRuntimeLease{registry: p.registry, generation: generation}, nil
}

type candidateRuntimeRegistry struct {
	mu      sync.Mutex
	now     func() time.Time
	current map[candidateRuntimeKey]*candidateGeneration
	retired map[*candidateGeneration]struct{}
	closed  bool
}

func newCandidateRuntimeRegistry(now func() time.Time) *candidateRuntimeRegistry {
	if now == nil {
		now = time.Now
	}
	return &candidateRuntimeRegistry{
		now: now, current: map[candidateRuntimeKey]*candidateGeneration{},
		retired: map[*candidateGeneration]struct{}{},
	}
}

// RegisterPreparedCandidate transfers an isolated prepared runtime into the
// private candidate registry without publishing it as an active generation.
func (r *Registry) RegisterPreparedCandidate(
	registration CandidateRegistration,
	candidate servingstate.PreparedRuntime,
) error {
	generation, err := r.consumePreparedCandidate(registration, candidate)
	if err != nil {
		return err
	}
	retired, err := r.candidates.register(generation)
	if err != nil {
		r.cleanupUnregisteredCandidate(generation)
		return err
	}
	r.cleanupCandidateGeneration(retired)
	return nil
}

func (r *Registry) consumePreparedCandidate(
	registration CandidateRegistration,
	candidate servingstate.PreparedRuntime,
) (*candidateGeneration, error) {
	if r == nil || r.candidates == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	normalized, fingerprint, err := normalizeCandidateRegistration(registration, r.candidates.now())
	if err != nil {
		return nil, err
	}
	prepared, ok := candidate.(*RegistryPrepared)
	if !ok || prepared == nil || prepared.registry != r {
		return nil, fmt.Errorf("%w: prepared runtime belongs to a different host", ErrCandidateRuntimeInvalid)
	}
	if prepared.workspaceID != normalized.WorkspaceID {
		return nil, fmt.Errorf(
			"%w: prepared workspace %q does not match registration workspace %q",
			ErrCandidateRuntimeInvalid, prepared.workspaceID, normalized.WorkspaceID,
		)
	}
	sealed, err := r.sealRegistryPrepared(prepared)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCandidateRuntimeInvalid, err)
	}
	if sealed.candidateID != normalized.CandidateID ||
		sealed.candidateOwner != normalized.OwnerID ||
		!sealed.candidateExpiry.Equal(normalized.ExpiresAt) ||
		sealed.candidateHash != fingerprint {
		return nil, errors.Join(ErrCandidateRuntimeIncompatible, sealed.abort())
	}
	managed, err := sealed.consumeCandidate()
	if err != nil {
		return nil, err
	}
	return &candidateGeneration{
		key: candidateRuntimeKey{
			candidateID: normalized.CandidateID, workspaceID: normalized.WorkspaceID,
		},
		ownerID: normalized.OwnerID, expiresAt: normalized.ExpiresAt,
		compatibility: normalized.Compatibility, fingerprint: fingerprint,
		manager: sealed.manager, managed: managed, cleanupDone: make(chan struct{}),
	}, nil
}

func (r *Registry) PrepareCandidate(
	ctx context.Context,
	input CandidatePreparation,
) (_ servingstate.PreparedRuntime, resultErr error) {
	if r == nil || r.candidates == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	current, err := r.repo.ByID(ctx, servingstate.ID(strings.TrimSpace(input.ServingStateID)))
	if err != nil {
		return nil, errors.Join(err, closeRuntimeLifetime(input.Lifetime))
	}
	if servingstate.NormalizeEnvironment(current.Environment) != r.environment {
		return nil, errors.Join(
			fmt.Errorf(
				"serving state %s environment = %q, want %q",
				input.ServingStateID,
				current.Environment,
				r.environment,
			),
			closeRuntimeLifetime(input.Lifetime),
		)
	}
	if input.Registration.WorkspaceID != current.WorkspaceID {
		return nil, errors.Join(
			fmt.Errorf(
				"%w: serving state workspace %q does not match registration workspace %q",
				ErrCandidateRuntimeInvalid,
				current.WorkspaceID,
				input.Registration.WorkspaceID,
			),
			closeRuntimeLifetime(input.Lifetime),
		)
	}
	normalized, fingerprint, err := normalizeCandidateRegistration(
		input.Registration,
		r.candidates.now(),
	)
	if err != nil {
		return nil, errors.Join(err, closeRuntimeLifetime(input.Lifetime))
	}
	artifact, err := r.repo.ArtifactByServingState(ctx, current.ID)
	if err != nil {
		return nil, errors.Join(err, closeRuntimeLifetime(input.Lifetime))
	}
	managedData, err := r.managerForWorkspace(current.WorkspaceID).resolveManagedData(ctx, current.ID)
	if err != nil {
		return nil, errors.Join(err, closeRuntimeLifetime(input.Lifetime))
	}
	if err := validateCandidateDataMode(
		current,
		normalized.Compatibility,
		managedData,
	); err != nil {
		return nil, errors.Join(
			err,
			releaseManagedDataLifetime(managedData.Lifetime),
			closeRuntimeLifetime(input.Lifetime),
		)
	}
	candidate := &candidatePreparationContext{
		runtime: CandidateRuntimeContext{
			CandidateID:              normalized.CandidateID,
			OwnerID:                  normalized.OwnerID,
			AuthorizationFingerprint: normalized.Compatibility.AuthorizationFingerprint,
			BindingFingerprint:       fingerprintCandidateBindings(normalized.Compatibility.Bindings),
			CompatibilityFingerprint: "sha256:" + hex.EncodeToString(fingerprint[:]),
		},
		expiresAt: normalized.ExpiresAt, fingerprint: fingerprint, lifetime: input.Lifetime,
	}
	manager := r.managerForWorkspace(current.WorkspaceID)
	r.prepareMu.Lock()
	prepared, err := manager.prepareResolvedWithCandidate(
		ctx,
		current,
		artifact,
		managedData,
		candidate,
	)
	r.prepareMu.Unlock()
	if err != nil {
		return nil, err
	}
	return &RegistryPrepared{
		registry: r, workspaceID: current.WorkspaceID, manager: manager, prepared: prepared,
	}, nil
}

func fingerprintCandidateBindings(bindings []CandidateBindingVersion) string {
	encoded, _ := json.Marshal(bindings)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *Registry) PrepareAndRegisterCandidate(
	ctx context.Context,
	input CandidatePreparation,
) error {
	prepared, err := r.PrepareCandidate(ctx, input)
	if err != nil {
		return err
	}
	defer prepared.Close()
	return r.RegisterPreparedCandidate(input.Registration, prepared)
}

func (r *Registry) PrepareAndRegisterCandidateSet(
	ctx context.Context,
	inputs []CandidatePreparation,
) (resultErr error) {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: candidate preparation set is empty", ErrCandidateRuntimeInvalid)
	}
	ownedLifetimes := make([]bool, len(inputs))
	for index := range ownedLifetimes {
		ownedLifetimes[index] = true
	}
	defer func() {
		for index, owned := range ownedLifetimes {
			if owned {
				resultErr = errors.Join(resultErr, closeRuntimeLifetime(inputs[index].Lifetime))
			}
		}
	}()
	candidateID := strings.TrimSpace(inputs[0].Registration.CandidateID)
	ownerID := strings.TrimSpace(inputs[0].Registration.OwnerID)
	expiresAt := inputs[0].Registration.ExpiresAt.UTC()
	workspaces := map[servingstate.WorkspaceID]struct{}{}
	for _, input := range inputs {
		if strings.TrimSpace(input.Registration.CandidateID) != candidateID ||
			strings.TrimSpace(input.Registration.OwnerID) != ownerID ||
			!input.Registration.ExpiresAt.UTC().Equal(expiresAt) {
			return fmt.Errorf(
				"%w: candidate set identity, owner, and expiry must match",
				ErrCandidateRuntimeInvalid,
			)
		}
		workspaceID := servingstate.WorkspaceID(
			strings.TrimSpace(string(input.Registration.WorkspaceID)),
		)
		if _, duplicate := workspaces[workspaceID]; duplicate {
			return fmt.Errorf(
				"%w: duplicate candidate workspace %q",
				ErrCandidateRuntimeInvalid,
				workspaceID,
			)
		}
		workspaces[workspaceID] = struct{}{}
	}
	prepared := make([]servingstate.PreparedRuntime, 0, len(inputs))
	defer func() {
		for _, item := range prepared {
			_ = item.Close()
		}
	}()
	for index, input := range inputs {
		// PrepareCandidate accepts ownership on both success and failure.
		ownedLifetimes[index] = false
		item, err := r.PrepareCandidate(ctx, input)
		if err != nil {
			return err
		}
		prepared = append(prepared, item)
	}
	generations := make([]*candidateGeneration, 0, len(inputs))
	for index, item := range prepared {
		generation, err := r.consumePreparedCandidate(inputs[index].Registration, item)
		if err != nil {
			for _, generation := range generations {
				r.cleanupUnregisteredCandidate(generation)
			}
			return err
		}
		generations = append(generations, generation)
	}
	retired, err := r.candidates.registerSet(generations)
	if err != nil {
		for _, generation := range generations {
			r.cleanupUnregisteredCandidate(generation)
		}
		return err
	}
	for _, generation := range retired {
		r.cleanupCandidateGeneration(generation)
	}
	return nil
}

func (r *Registry) cleanupUnregisteredCandidate(generation *candidateGeneration) {
	if generation == nil || generation.managed == nil {
		return
	}
	generation.closing = true
	generation.managed.closing = true
	r.cleanupCandidateGeneration(generation)
}

func (r *Registry) AcquireCandidate(ctx context.Context, request CandidateLeaseRequest) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.candidates == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	normalized, fingerprint, err := normalizeCandidateLeaseRequest(request)
	if err != nil {
		return nil, err
	}
	generation, retired, err := r.candidates.acquire(
		normalized.CandidateID,
		normalized.OwnerID,
		normalized.WorkspaceID,
		fingerprint,
	)
	r.cleanupCandidateGeneration(retired)
	if err != nil {
		return nil, err
	}
	return &candidateRuntimeLease{registry: r, generation: generation}, nil
}

// ResolveOwnedCandidate returns deterministic workspace providers for the
// current private generation. Foreign candidate identities are concealed.
func (r *Registry) ResolveOwnedCandidate(candidateID, ownerID string) (OwnedCandidateView, error) {
	candidateID = strings.TrimSpace(candidateID)
	ownerID = strings.TrimSpace(ownerID)
	if candidateID == "" || ownerID == "" {
		return OwnedCandidateView{}, ErrCandidateRuntimeNotFound
	}
	if r == nil || r.candidates == nil {
		return OwnedCandidateView{}, ErrCandidateRuntimeClosed
	}
	generations, retired, err := r.candidates.resolveOwned(candidateID, ownerID)
	for _, generation := range retired {
		r.cleanupCandidateGeneration(generation)
	}
	if err != nil {
		return OwnedCandidateView{}, err
	}
	view := OwnedCandidateView{
		CandidateID: candidateID,
		Workspaces:  make([]OwnedCandidateWorkspace, 0, len(generations)),
	}
	for _, generation := range generations {
		view.Workspaces = append(view.Workspaces, OwnedCandidateWorkspace{
			WorkspaceID:              generation.key.workspaceID,
			AuthorizationFingerprint: generation.compatibility.AuthorizationFingerprint,
			Restrictions: append(
				[]CandidateRestriction(nil),
				generation.compatibility.Restrictions...,
			),
			Provider: ownedCandidateProvider{
				registry: r, candidateID: candidateID, ownerID: ownerID,
				workspaceID: generation.key.workspaceID,
			},
		})
	}
	return view, nil
}

// RetireCandidate stops all new acquisitions for a candidate while allowing
// existing query leases to drain safely.
func (r *Registry) RetireCandidate(candidateID string) int {
	if r == nil || r.candidates == nil {
		return 0
	}
	retired, count := r.candidates.retireCandidate(strings.TrimSpace(candidateID))
	for _, generation := range retired {
		r.cleanupCandidateGeneration(generation)
	}
	return count
}

func (r *Registry) ReapExpiredCandidates(now time.Time) int {
	if r == nil || r.candidates == nil {
		return 0
	}
	retired, count := r.candidates.reapExpired(now.UTC())
	for _, generation := range retired {
		r.cleanupCandidateGeneration(generation)
	}
	return count
}

func (r *Registry) cleanupCandidateGeneration(generation *candidateGeneration) {
	if generation == nil || generation.manager == nil || generation.managed == nil {
		return
	}
	generation.cleanupOnce.Do(func() {
		generation.manager.cleanupRetired(generation.managed)
		go func() {
			generation.manager.mu.RLock()
			done := generation.managed.cleanupDone
			generation.manager.mu.RUnlock()
			if done != nil {
				<-done
			}
			close(generation.cleanupDone)
		}()
	})
}

func (r *candidateRuntimeRegistry) register(
	generation *candidateGeneration,
) (*candidateGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrCandidateRuntimeClosed
	}
	current := r.current[generation.key]
	if current != nil && current.ownerID != generation.ownerID {
		return nil, ErrCandidateRuntimeConflict
	}
	r.current[generation.key] = generation
	return r.retireLocked(current), nil
}

func (r *candidateRuntimeRegistry) registerSet(
	generations []*candidateGeneration,
) ([]*candidateGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrCandidateRuntimeClosed
	}
	keys := make(map[candidateRuntimeKey]struct{}, len(generations))
	for _, generation := range generations {
		if generation == nil || generation.managed == nil {
			return nil, ErrCandidateRuntimeInvalid
		}
		if _, duplicate := keys[generation.key]; duplicate {
			return nil, ErrCandidateRuntimeConflict
		}
		keys[generation.key] = struct{}{}
		if current := r.current[generation.key]; current != nil &&
			current.ownerID != generation.ownerID {
			return nil, ErrCandidateRuntimeConflict
		}
	}
	var drained []*candidateGeneration
	for _, generation := range generations {
		current := r.current[generation.key]
		r.current[generation.key] = generation
		if retired := r.retireLocked(current); retired != nil {
			drained = append(drained, retired)
		}
	}
	return drained, nil
}

func (r *candidateRuntimeRegistry) acquire(
	candidateID string,
	ownerID string,
	workspaceID servingstate.WorkspaceID,
	compatibility [sha256.Size]byte,
) (generation *candidateGeneration, retired *candidateGeneration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCandidateRuntimeClosed
	}
	key := candidateRuntimeKey{candidateID: candidateID, workspaceID: workspaceID}
	current := r.current[key]
	if current == nil || current.ownerID != ownerID {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	if !r.now().UTC().Before(current.expiresAt) {
		delete(r.current, key)
		return nil, r.retireLocked(current), ErrCandidateRuntimeExpired
	}
	if current.fingerprint != compatibility {
		return nil, nil, ErrCandidateRuntimeIncompatible
	}
	current.refs++
	return current, nil, nil
}

func (r *candidateRuntimeRegistry) acquireOwned(
	candidateID string,
	ownerID string,
	workspaceID servingstate.WorkspaceID,
) (generation *candidateGeneration, retired *candidateGeneration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCandidateRuntimeClosed
	}
	key := candidateRuntimeKey{candidateID: candidateID, workspaceID: workspaceID}
	current := r.current[key]
	if current == nil || current.ownerID != ownerID {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	if !r.now().UTC().Before(current.expiresAt) {
		delete(r.current, key)
		return nil, r.retireLocked(current), ErrCandidateRuntimeExpired
	}
	current.refs++
	return current, nil, nil
}

func (r *candidateRuntimeRegistry) resolveOwned(
	candidateID string,
	ownerID string,
) (generations []*candidateGeneration, drained []*candidateGeneration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCandidateRuntimeClosed
	}
	now := r.now().UTC()
	for key, generation := range r.current {
		if key.candidateID != candidateID || generation.ownerID != ownerID {
			continue
		}
		if !now.Before(generation.expiresAt) {
			delete(r.current, key)
			if retired := r.retireLocked(generation); retired != nil {
				drained = append(drained, retired)
			}
			continue
		}
		generations = append(generations, generation)
	}
	if len(generations) == 0 {
		return nil, drained, ErrCandidateRuntimeNotFound
	}
	sort.Slice(generations, func(i, j int) bool {
		return generations[i].key.workspaceID < generations[j].key.workspaceID
	})
	return generations, drained, nil
}

func (r *candidateRuntimeRegistry) retireCandidate(
	candidateID string,
) (drained []*candidateGeneration, count int) {
	if candidateID == "" {
		return nil, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, generation := range r.current {
		if key.candidateID != candidateID {
			continue
		}
		delete(r.current, key)
		count++
		if generation := r.retireLocked(generation); generation != nil {
			drained = append(drained, generation)
		}
	}
	return drained, count
}

func (r *candidateRuntimeRegistry) reapExpired(
	now time.Time,
) (drained []*candidateGeneration, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, generation := range r.current {
		if now.Before(generation.expiresAt) {
			continue
		}
		delete(r.current, key)
		count++
		if generation := r.retireLocked(generation); generation != nil {
			drained = append(drained, generation)
		}
	}
	return drained, count
}

func (r *candidateRuntimeRegistry) retireLocked(
	generation *candidateGeneration,
) *candidateGeneration {
	if generation == nil || generation.closing {
		return nil
	}
	generation.closing = true
	generation.managed.closing = true
	if generation.refs > 0 {
		r.retired[generation] = struct{}{}
		return nil
	}
	return generation
}

func (r *candidateRuntimeRegistry) release(generation *candidateGeneration) *candidateGeneration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation == nil || generation.refs == 0 {
		return nil
	}
	generation.refs--
	if generation.refs != 0 || !generation.closing {
		return nil
	}
	delete(r.retired, generation)
	return generation
}

func (r *candidateRuntimeRegistry) close() (drained, targets []*candidateGeneration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil
	}
	r.closed = true
	seen := map[*candidateGeneration]struct{}{}
	for generation := range r.retired {
		seen[generation] = struct{}{}
		targets = append(targets, generation)
	}
	for key, generation := range r.current {
		delete(r.current, key)
		if _, ok := seen[generation]; !ok {
			seen[generation] = struct{}{}
			targets = append(targets, generation)
		}
		if closed := r.retireLocked(generation); closed != nil {
			drained = append(drained, closed)
		}
	}
	return drained, targets
}

func (r *candidateRuntimeRegistry) leasedSnapshots() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshots := map[int64]struct{}{}
	for _, generation := range r.current {
		if generation.managed.snapshotLease != nil && generation.managed.snapshotID > 0 {
			snapshots[generation.managed.snapshotID] = struct{}{}
		}
	}
	for generation := range r.retired {
		if generation.managed.snapshotLease != nil && generation.managed.snapshotID > 0 {
			snapshots[generation.managed.snapshotID] = struct{}{}
		}
	}
	return snapshotKeys(snapshots)
}

type candidateRuntimeLease struct {
	registry   *Registry
	generation *candidateGeneration
	once       sync.Once
}

func (l *candidateRuntimeLease) Runtime() Runtime {
	if l == nil || l.generation == nil || l.generation.managed == nil {
		return nil
	}
	return l.generation.managed.runtime
}

func (l *candidateRuntimeLease) ServingStateID() servingstate.ID {
	if l == nil || l.generation == nil || l.generation.managed == nil {
		return ""
	}
	return l.generation.managed.servingStateID
}

func (l *candidateRuntimeLease) DuckLakeSnapshotID() int64 {
	if l == nil || l.generation == nil || l.generation.managed == nil {
		return 0
	}
	return l.generation.managed.snapshotID
}

func (l *candidateRuntimeLease) Release() {
	if l == nil || l.registry == nil || l.registry.candidates == nil || l.generation == nil {
		return
	}
	l.once.Do(func() {
		l.registry.cleanupCandidateGeneration(l.registry.candidates.release(l.generation))
	})
}

func normalizeCandidateRegistration(
	registration CandidateRegistration,
	now time.Time,
) (CandidateRegistration, [sha256.Size]byte, error) {
	registration.CandidateID = strings.TrimSpace(registration.CandidateID)
	registration.OwnerID = strings.TrimSpace(registration.OwnerID)
	registration.WorkspaceID = servingstate.WorkspaceID(strings.TrimSpace(string(registration.WorkspaceID)))
	registration.ExpiresAt = registration.ExpiresAt.UTC()
	if registration.CandidateID == "" || registration.OwnerID == "" || registration.WorkspaceID == "" {
		return CandidateRegistration{}, [sha256.Size]byte{}, fmt.Errorf(
			"%w: candidate, owner, and workspace are required",
			ErrCandidateRuntimeInvalid,
		)
	}
	if registration.ExpiresAt.IsZero() || !registration.ExpiresAt.After(now.UTC()) {
		return CandidateRegistration{}, [sha256.Size]byte{}, fmt.Errorf(
			"%w: expiry must be in the future",
			ErrCandidateRuntimeInvalid,
		)
	}
	compatibility, fingerprint, err := normalizeCandidateCompatibility(registration.Compatibility)
	if err != nil {
		return CandidateRegistration{}, [sha256.Size]byte{}, err
	}
	registration.Compatibility = compatibility
	return registration, fingerprint, nil
}

func normalizeCandidateLeaseRequest(
	request CandidateLeaseRequest,
) (CandidateLeaseRequest, [sha256.Size]byte, error) {
	request.CandidateID = strings.TrimSpace(request.CandidateID)
	request.OwnerID = strings.TrimSpace(request.OwnerID)
	request.WorkspaceID = servingstate.WorkspaceID(strings.TrimSpace(string(request.WorkspaceID)))
	if request.CandidateID == "" || request.OwnerID == "" || request.WorkspaceID == "" {
		return CandidateLeaseRequest{}, [sha256.Size]byte{}, fmt.Errorf(
			"%w: candidate, owner, and workspace are required",
			ErrCandidateRuntimeInvalid,
		)
	}
	compatibility, fingerprint, err := normalizeCandidateCompatibility(request.Compatibility)
	if err != nil {
		return CandidateLeaseRequest{}, [sha256.Size]byte{}, err
	}
	request.Compatibility = compatibility
	return request, fingerprint, nil
}

func normalizeCandidateCompatibility(
	compatibility CandidateCompatibility,
) (CandidateCompatibility, [sha256.Size]byte, error) {
	compatibility.ArtifactDigest = strings.TrimSpace(compatibility.ArtifactDigest)
	compatibility.DataRevision = strings.TrimSpace(compatibility.DataRevision)
	compatibility.RuntimeVersion = strings.TrimSpace(compatibility.RuntimeVersion)
	compatibility.AuthorizationFingerprint = strings.TrimSpace(compatibility.AuthorizationFingerprint)
	if compatibility.ArtifactDigest == "" || compatibility.DataRevision == "" ||
		compatibility.RuntimeVersion == "" || compatibility.AuthorizationFingerprint == "" {
		return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
			"%w: artifact, data, runtime, and authorization fingerprints are required",
			ErrCandidateRuntimeInvalid,
		)
	}
	if compatibility.DataMode != CandidateDataReuseSnapshot &&
		compatibility.DataMode != CandidateDataRefreshSources {
		return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
			"%w: candidate data mode is required",
			ErrCandidateRuntimeInvalid,
		)
	}
	normalizedBindings := append([]CandidateBindingVersion(nil), compatibility.Bindings...)
	for index := range normalizedBindings {
		normalizedBindings[index].BindingID = strings.TrimSpace(normalizedBindings[index].BindingID)
		normalizedBindings[index].LogicalConnection = strings.TrimSpace(normalizedBindings[index].LogicalConnection)
		normalizedBindings[index].ConnectorKind = strings.TrimSpace(normalizedBindings[index].ConnectorKind)
		normalizedBindings[index].ProviderVersion = strings.TrimSpace(normalizedBindings[index].ProviderVersion)
		normalizedBindings[index].EndpointConfigHash = strings.TrimSpace(normalizedBindings[index].EndpointConfigHash)
		if normalizedBindings[index].BindingID == "" || normalizedBindings[index].LogicalConnection == "" ||
			normalizedBindings[index].ConnectorKind == "" || normalizedBindings[index].Revision < 1 ||
			normalizedBindings[index].ProviderVersion == "" ||
			platformdigest.ValidateSHA256Identity(normalizedBindings[index].EndpointConfigHash) != nil {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: binding identity, positive revision, and provider version are required",
				ErrCandidateRuntimeInvalid,
			)
		}
	}
	sort.Slice(normalizedBindings, func(i, j int) bool {
		return normalizedBindings[i].BindingID < normalizedBindings[j].BindingID
	})
	for index := 1; index < len(normalizedBindings); index++ {
		if normalizedBindings[index-1].BindingID == normalizedBindings[index].BindingID {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: duplicate binding %q",
				ErrCandidateRuntimeInvalid,
				normalizedBindings[index].BindingID,
			)
		}
	}
	compatibility.Bindings = normalizedBindings
	normalizedAuthoredConnections := append(
		[]CandidateAuthoredConnection(nil),
		compatibility.AuthoredConnections...,
	)
	for index := range normalizedAuthoredConnections {
		normalizedAuthoredConnections[index].LogicalConnection = strings.TrimSpace(
			normalizedAuthoredConnections[index].LogicalConnection,
		)
		normalizedAuthoredConnections[index].ConnectorKind = strings.TrimSpace(
			normalizedAuthoredConnections[index].ConnectorKind,
		)
	}
	sort.Slice(normalizedAuthoredConnections, func(i, j int) bool {
		return normalizedAuthoredConnections[i].LogicalConnection <
			normalizedAuthoredConnections[j].LogicalConnection
	})
	for index, connection := range normalizedAuthoredConnections {
		if connection.LogicalConnection == "" || connection.ConnectorKind == "" ||
			index > 0 && normalizedAuthoredConnections[index-1].LogicalConnection ==
				connection.LogicalConnection {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: authored connection identity and connector kind are required and unique",
				ErrCandidateRuntimeInvalid,
			)
		}
	}
	compatibility.AuthoredConnections = normalizedAuthoredConnections
	normalizedManagedDataConnections := append(
		[]string(nil),
		compatibility.ManagedDataConnections...,
	)
	for index := range normalizedManagedDataConnections {
		normalizedManagedDataConnections[index] = strings.TrimSpace(
			normalizedManagedDataConnections[index],
		)
		if normalizedManagedDataConnections[index] == "" {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: managed-data connection identity is required",
				ErrCandidateRuntimeInvalid,
			)
		}
	}
	sort.Strings(normalizedManagedDataConnections)
	for index := 1; index < len(normalizedManagedDataConnections); index++ {
		if normalizedManagedDataConnections[index-1] ==
			normalizedManagedDataConnections[index] {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: duplicate managed-data connection %q",
				ErrCandidateRuntimeInvalid,
				normalizedManagedDataConnections[index],
			)
		}
	}
	compatibility.ManagedDataConnections = normalizedManagedDataConnections
	normalizedRestrictions := append([]CandidateRestriction(nil), compatibility.Restrictions...)
	for index := range normalizedRestrictions {
		restriction := &normalizedRestrictions[index]
		restriction.ID = strings.TrimSpace(restriction.ID)
		restriction.WorkspaceID = strings.TrimSpace(restriction.WorkspaceID)
		restriction.ObjectID = strings.TrimSpace(restriction.ObjectID)
		restriction.PolicyType = strings.TrimSpace(restriction.PolicyType)
		restriction.ExpressionJSON = strings.TrimSpace(restriction.ExpressionJSON)
		if restriction.ID == "" || restriction.WorkspaceID == "" ||
			restriction.ObjectID == "" || restriction.ExpressionJSON == "" {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: candidate restriction identity, workspace, object, and expression are required",
				ErrCandidateRuntimeInvalid,
			)
		}
		if restriction.PolicyType != "row_filter" && restriction.PolicyType != "column_mask" {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: unsupported candidate restriction type %q",
				ErrCandidateRuntimeInvalid,
				restriction.PolicyType,
			)
		}
	}
	sort.Slice(normalizedRestrictions, func(i, j int) bool {
		return normalizedRestrictions[i].ID < normalizedRestrictions[j].ID
	})
	for index := 1; index < len(normalizedRestrictions); index++ {
		if normalizedRestrictions[index-1].ID == normalizedRestrictions[index].ID {
			return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
				"%w: duplicate candidate restriction %q",
				ErrCandidateRuntimeInvalid,
				normalizedRestrictions[index].ID,
			)
		}
	}
	compatibility.Restrictions = normalizedRestrictions
	encoded, err := json.Marshal(compatibility)
	if err != nil {
		return CandidateCompatibility{}, [sha256.Size]byte{}, fmt.Errorf(
			"%w: encode compatibility: %v",
			ErrCandidateRuntimeInvalid,
			err,
		)
	}
	return compatibility, sha256.Sum256(encoded), nil
}

func validateCandidateDataMode(
	state servingstate.State,
	compatibility CandidateCompatibility,
	managedData ManagedDataResolution,
) error {
	switch compatibility.DataMode {
	case CandidateDataReuseSnapshot:
		if state.DuckLakeSnapshotID <= 0 || len(compatibility.AuthoredConnections) != 0 {
			return fmt.Errorf(
				"%w: immutable snapshot reuse requires an existing snapshot and no authored refresh connections",
				ErrCandidateRuntimeIncompatible,
			)
		}
	case CandidateDataRefreshSources:
		if state.DuckLakeSnapshotID != 0 ||
			len(compatibility.Bindings) == 0 &&
				len(compatibility.ManagedDataConnections) == 0 &&
				len(compatibility.AuthoredConnections) == 0 {
			return fmt.Errorf(
				"%w: source refresh requires an unmaterialized state and declared target, managed-data, or authored connections",
				ErrCandidateRuntimeIncompatible,
			)
		}
	default:
		return ErrCandidateRuntimeInvalid
	}
	resolvedManagedConnections := make([]string, 0, len(managedData.Roots))
	for connection, root := range managedData.Roots {
		if strings.TrimSpace(connection) == "" ||
			connection != strings.TrimSpace(connection) ||
			strings.TrimSpace(root) == "" {
			return fmt.Errorf(
				"%w: resolved managed-data roots are invalid",
				ErrCandidateRuntimeIncompatible,
			)
		}
		resolvedManagedConnections = append(resolvedManagedConnections, connection)
	}
	sort.Strings(resolvedManagedConnections)
	if len(resolvedManagedConnections) != len(compatibility.ManagedDataConnections) {
		return fmt.Errorf(
			"%w: managed-data connection set changed during runtime preparation",
			ErrCandidateRuntimeIncompatible,
		)
	}
	for index, connection := range compatibility.ManagedDataConnections {
		if resolvedManagedConnections[index] != connection {
			return fmt.Errorf(
				"%w: managed-data connection set changed during runtime preparation",
				ErrCandidateRuntimeIncompatible,
			)
		}
	}
	return nil
}
