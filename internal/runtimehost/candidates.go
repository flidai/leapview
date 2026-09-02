package runtimehost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

type CandidateBindingVersion struct {
	BindingID, LogicalConnection, ConnectorKind string
	Revision                                    int64
	ProviderVersion, EndpointConfigHash         string
	Access                                      semanticmodel.ConnectionAccess
}
type CandidateRestriction struct {
	ID             string
	ObjectID       projectgraph.ResourceID
	ObjectKind     projectgraph.Kind
	Subject        *accesssnapshot.SubjectRef
	PolicyType     string
	ExpressionJSON string
}
type CandidateDataMode string

const (
	CandidateDataReuseBase      CandidateDataMode = "reuse_base"
	CandidateDataRefreshSources CandidateDataMode = "refresh_sources"
)

type CandidateAuthoredConnection struct {
	LogicalConnection string
	ConnectorKind     string
	Access            semanticmodel.ConnectionAccess
}
type CandidateCompatibility struct {
	ArtifactDigest, DataRevision             string
	DataMode                                 CandidateDataMode
	RuntimeVersion, AuthorizationFingerprint string
	GateEvidenceDigest                       string
	BindingFingerprint                       string
	Bindings                                 []CandidateBindingVersion
	AuthoredConnections                      []CandidateAuthoredConnection
	ManagedDataConnections                   []string
	Capabilities                             []RuntimeCapabilityEvidence
	Restrictions                             []CandidateRestriction
}
type CandidateRegistration struct {
	CandidateID, OwnerID string
	ProjectID            projectgraph.ResourceID
	ExpiresAt            time.Time
	Compatibility        CandidateCompatibility
}
type CandidateLeaseRequest struct {
	CandidateID, OwnerID string
	ProjectID            projectgraph.ResourceID
	Compatibility        CandidateCompatibility
}
type CandidatePreparation struct {
	Registration CandidateRegistration
	Identity     projectgraph.ServingIdentity
	Lifetime     RuntimeLifetime
}

type candidateRuntimeKey struct{ candidateID string }
type candidateGeneration struct {
	key           candidateRuntimeKey
	projectID     projectgraph.ResourceID
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
	cleanupErr    error
}

type OwnedCandidateView struct {
	CandidateID              string
	ProjectID                projectgraph.ResourceID
	Provider                 Provider
	Restrictions             []CandidateRestriction
	AuthorizationFingerprint string
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
	return &candidateRuntimeRegistry{now: now, current: map[candidateRuntimeKey]*candidateGeneration{}, retired: map[*candidateGeneration]struct{}{}}
}

func (r *Registry) prepareCandidate(ctx context.Context, input CandidatePreparation) (result servingstate.PreparedRuntime, resultErr error) {
	ownedLifetime := input.Lifetime
	defer func() {
		if ownedLifetime != nil {
			resultErr = errors.Join(resultErr, closeRuntimeLifetime(ownedLifetime))
		}
	}()
	if r == nil || r.manager == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	normalized, fingerprint, err := normalizeRegistration(input.Registration, r.now())
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
	if err := input.Identity.Validate(); err != nil {
		return nil, fmt.Errorf("%w: candidate serving identity is invalid: %v", ErrCandidateRuntimeInvalid, err)
	}
	if input.Identity.ProjectID != normalized.ProjectID || input.Identity.Environment != string(r.Environment()) {
		return nil, fmt.Errorf("%w: candidate serving identity is outside project environment", ErrCandidateRuntimeIncompatible)
	}
	state, err := r.manager.repo.ByID(ctx, servingstate.ID(input.Identity.GenerationID))
	if err != nil {
		return nil, err
	}
	if state.ProjectID != input.Identity.ProjectID || servingstate.Environment(state.Environment) != servingstate.Environment(input.Identity.Environment) {
		return nil, fmt.Errorf("%w: candidate serving state is outside project environment", ErrCandidateRuntimeIncompatible)
	}
	artifact, err := r.manager.repo.ArtifactByServingState(ctx, state.ID)
	if err != nil {
		return nil, err
	}
	if err := r.manager.validateGeneration(state, artifact); err != nil {
		return nil, err
	}
	managedData, err := r.manager.resolveManagedData(ctx, state)
	if err != nil {
		return nil, err
	}
	// Candidate source refreshes in the local/evaluation file-catalog path are
	// sealed only after the physical build has produced its catalog seal. Their
	// serving-state row therefore intentionally has no legacy snapshot pointer
	// at this preparation boundary. PostgreSQL's sealed runtime, by contrast,
	// opts into the exact SNAPSHOT_VERSION contract through
	// PinnedSnapshotSealedFactory and must retain the positive snapshot check.
	requirePinnedSnapshot := candidateRequiresPinnedSnapshot(r.manager.factory, r.manager.requireSealedCatalog)
	if err := validateCandidateDataMode(state, normalized.Compatibility, managedData, requirePinnedSnapshot); err != nil {
		return nil, errors.Join(err, releaseManaged(managedData.Lifetime))
	}
	candidate := &candidatePreparationContext{runtime: CandidateRuntimeContext{
		CandidateID: normalized.CandidateID, OwnerID: normalized.OwnerID,
		AuthorizationFingerprint: normalized.Compatibility.AuthorizationFingerprint,
		BindingFingerprint:       fingerprintCandidateBindings(normalized.Compatibility.Bindings),
		RuntimeVersion:           normalized.Compatibility.RuntimeVersion,
		BindingKinds:             candidateBindingKinds(normalized.Compatibility),
		Capabilities:             cloneRuntimeCapabilities(normalized.Compatibility.Capabilities),
		CompatibilityFingerprint: "sha256:" + fmt.Sprintf("%x", fingerprint),
		GateEvidenceDigest:       normalized.Compatibility.GateEvidenceDigest,
	}, expiresAt: normalized.ExpiresAt, fingerprint: fingerprint, lifetime: input.Lifetime}
	// Ownership transfers to the prepared candidate; manager preparation closes
	// it on every subsequent failure path.
	ownedLifetime = nil
	prepared, err := r.manager.prepareResolvedWithCandidate(ctx, state, artifact, managedData, candidate)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

// candidateRequiresPinnedSnapshot scopes the positive snapshot requirement to
// targets whose sealed runtime explicitly implements the PostgreSQL-style
// SNAPSHOT_VERSION contract. Local sealed file catalogs remain immutable, but
// their candidate serving-state rows intentionally carry no legacy snapshot
// pointer until physical materialization has completed.
func candidateRequiresPinnedSnapshot(factory RuntimeFactory, requireSealedCatalog bool) bool {
	if !requireSealedCatalog {
		return false
	}
	_, ok := factory.(PinnedSnapshotSealedFactory)
	return ok
}

func candidateBindingKinds(value CandidateCompatibility) map[string]string {
	kinds := make(map[string]string, len(value.Bindings)+len(value.AuthoredConnections)+len(value.ManagedDataConnections))
	for _, binding := range value.Bindings {
		kinds[binding.LogicalConnection] = binding.ConnectorKind
	}
	for _, authored := range value.AuthoredConnections {
		kinds[authored.LogicalConnection] = authored.ConnectorKind
	}
	for _, connection := range value.ManagedDataConnections {
		kinds[connection] = "managed"
	}
	return kinds
}

func cloneRuntimeCapabilities(values []RuntimeCapabilityEvidence) []RuntimeCapabilityEvidence {
	return append([]RuntimeCapabilityEvidence(nil), values...)
}
func (r *Registry) registerPreparedCandidate(reg CandidateRegistration, candidate servingstate.PreparedRuntime) error {
	if r == nil || r.candidates == nil {
		return ErrCandidateRuntimeClosed
	}
	normalized, fingerprint, err := normalizeRegistration(reg, r.now())
	if err != nil {
		return err
	}
	prepared, ok := candidate.(*Prepared)
	if !ok || prepared == nil || prepared.owner != r.manager {
		return fmt.Errorf("%w: prepared runtime belongs to a different host", ErrCandidateRuntimeInvalid)
	}
	prepared.mu.Lock()
	if prepared.candidateID != normalized.CandidateID || prepared.candidateOwner != normalized.OwnerID || prepared.candidateHash != fingerprint {
		prepared.mu.Unlock()
		return errors.Join(ErrCandidateRuntimeIncompatible, prepared.Close())
	}
	prepared.mu.Unlock()
	sealed, err := r.manager.sealPrepared(prepared)
	if err != nil {
		return err
	}
	managed, err := sealed.consumeCandidate()
	if err != nil {
		return err
	}
	generation := &candidateGeneration{key: candidateRuntimeKey{candidateID: normalized.CandidateID}, projectID: normalized.ProjectID, ownerID: normalized.OwnerID, expiresAt: normalized.ExpiresAt, compatibility: normalized.Compatibility, fingerprint: fingerprint, manager: r.manager, managed: managed, cleanupDone: make(chan struct{})}
	r.candidates.mu.Lock()
	if r.candidates.closed {
		r.candidates.mu.Unlock()
		return errors.Join(ErrCandidateRuntimeClosed, r.manager.closeManaged(managed))
	}
	key := generation.key
	if existing := r.candidates.current[key]; existing != nil && !existing.closing && existing.ownerID != normalized.OwnerID {
		r.candidates.mu.Unlock()
		return errors.Join(ErrCandidateRuntimeConflict, r.manager.closeManaged(managed))
	}
	old := r.candidates.current[key]
	var drained *candidateGeneration
	if old != nil {
		drained = r.candidates.retireLocked(old)
	}
	r.candidates.current[key] = generation
	r.candidates.mu.Unlock()
	if drained != nil {
		r.cleanupCandidateGeneration(drained)
	}
	return nil
}

func normalizeRegistration(input CandidateRegistration, now time.Time) (CandidateRegistration, [sha256.Size]byte, error) {
	if input.CandidateID != strings.TrimSpace(input.CandidateID) || input.OwnerID != strings.TrimSpace(input.OwnerID) {
		return CandidateRegistration{}, [sha256.Size]byte{}, fmt.Errorf("%w: candidate and owner IDs must be canonical", ErrCandidateRuntimeInvalid)
	}
	if !input.ProjectID.Valid() || input.CandidateID == "" || input.OwnerID == "" {
		return CandidateRegistration{}, [sha256.Size]byte{}, fmt.Errorf("%w: candidate identity is incomplete", ErrCandidateRuntimeInvalid)
	}
	if input.ExpiresAt.IsZero() {
		return CandidateRegistration{}, [sha256.Size]byte{}, fmt.Errorf("%w: candidate expiry is required", ErrCandidateRuntimeInvalid)
	}
	if !input.ExpiresAt.After(now) {
		return CandidateRegistration{}, [sha256.Size]byte{}, ErrCandidateRuntimeExpired
	}
	compatibility, err := normalizeCompatibility(input.Compatibility)
	if err != nil {
		return CandidateRegistration{}, [sha256.Size]byte{}, err
	}
	input.Compatibility = compatibility
	data, err := json.Marshal(input.Compatibility)
	if err != nil {
		return CandidateRegistration{}, [sha256.Size]byte{}, err
	}
	return input, sha256.Sum256(data), nil
}

func normalizeLeaseRequest(input CandidateLeaseRequest, now time.Time) (CandidateLeaseRequest, [sha256.Size]byte, error) {
	if input.CandidateID != strings.TrimSpace(input.CandidateID) || input.OwnerID != strings.TrimSpace(input.OwnerID) {
		return CandidateLeaseRequest{}, [sha256.Size]byte{}, fmt.Errorf("%w: candidate and owner IDs must be canonical", ErrCandidateRuntimeInvalid)
	}
	if !input.ProjectID.Valid() || input.CandidateID == "" || input.OwnerID == "" {
		return CandidateLeaseRequest{}, [sha256.Size]byte{}, fmt.Errorf("%w: candidate identity is incomplete", ErrCandidateRuntimeInvalid)
	}
	if now.IsZero() {
		now = time.Now()
	}
	compatibility, err := normalizeCompatibility(input.Compatibility)
	if err != nil {
		return CandidateLeaseRequest{}, [sha256.Size]byte{}, err
	}
	input.Compatibility = compatibility
	data, err := json.Marshal(input.Compatibility)
	if err != nil {
		return CandidateLeaseRequest{}, [sha256.Size]byte{}, err
	}
	return input, sha256.Sum256(data), nil
}

func normalizeCompatibility(value CandidateCompatibility) (CandidateCompatibility, error) {
	if value.ArtifactDigest != strings.TrimSpace(value.ArtifactDigest) || value.DataRevision != strings.TrimSpace(value.DataRevision) || value.RuntimeVersion != strings.TrimSpace(value.RuntimeVersion) || value.AuthorizationFingerprint != strings.TrimSpace(value.AuthorizationFingerprint) || value.GateEvidenceDigest != strings.TrimSpace(value.GateEvidenceDigest) || value.BindingFingerprint != strings.TrimSpace(value.BindingFingerprint) {
		return CandidateCompatibility{}, fmt.Errorf("%w: compatibility fingerprints must be canonical", ErrCandidateRuntimeInvalid)
	}
	value.ArtifactDigest = strings.TrimSpace(value.ArtifactDigest)
	value.DataRevision = strings.TrimSpace(value.DataRevision)
	value.RuntimeVersion = strings.TrimSpace(value.RuntimeVersion)
	value.AuthorizationFingerprint = strings.TrimSpace(value.AuthorizationFingerprint)
	value.GateEvidenceDigest = strings.TrimSpace(value.GateEvidenceDigest)
	value.BindingFingerprint = strings.TrimSpace(value.BindingFingerprint)
	if value.GateEvidenceDigest != "" {
		if err := platformdigest.ValidateSHA256Identity(value.GateEvidenceDigest); err != nil {
			return CandidateCompatibility{}, fmt.Errorf("%w: gate evidence digest: %v", ErrCandidateRuntimeInvalid, err)
		}
	}
	if value.ArtifactDigest == "" || value.DataRevision == "" || value.RuntimeVersion == "" || value.AuthorizationFingerprint == "" {
		return CandidateCompatibility{}, fmt.Errorf("%w: artifact, data, runtime, and authorization fingerprints are required", ErrCandidateRuntimeInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(value.ArtifactDigest); err != nil {
		return CandidateCompatibility{}, fmt.Errorf("%w: artifact digest: %v", ErrCandidateRuntimeInvalid, err)
	}
	if value.DataMode != CandidateDataReuseBase && value.DataMode != CandidateDataRefreshSources {
		return CandidateCompatibility{}, fmt.Errorf("%w: candidate data mode is required", ErrCandidateRuntimeInvalid)
	}
	bindings := append([]CandidateBindingVersion(nil), value.Bindings...)
	for i := range bindings {
		b := &bindings[i]
		if b.BindingID != strings.TrimSpace(b.BindingID) || b.LogicalConnection != strings.TrimSpace(b.LogicalConnection) || b.ConnectorKind != strings.TrimSpace(b.ConnectorKind) || b.ProviderVersion != strings.TrimSpace(b.ProviderVersion) || b.EndpointConfigHash != strings.TrimSpace(b.EndpointConfigHash) {
			return CandidateCompatibility{}, fmt.Errorf("%w: binding identity must be canonical", ErrCandidateRuntimeInvalid)
		}
		if b.Access != "" && b.Access != semanticmodel.ConnectionAccessPublic {
			return CandidateCompatibility{}, fmt.Errorf("%w: unsupported binding access policy", ErrCandidateRuntimeInvalid)
		}
		if b.BindingID == "" || b.LogicalConnection == "" || b.ConnectorKind == "" || b.Revision < 1 || b.ProviderVersion == "" {
			return CandidateCompatibility{}, fmt.Errorf("%w: binding identity, positive revision, and provider version are required", ErrCandidateRuntimeInvalid)
		}
		if err := platformdigest.ValidateSHA256Identity(b.EndpointConfigHash); err != nil {
			return CandidateCompatibility{}, fmt.Errorf("%w: endpoint config hash: %v", ErrCandidateRuntimeInvalid, err)
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].BindingID < bindings[j].BindingID })
	for i := 1; i < len(bindings); i++ {
		if bindings[i-1].BindingID == bindings[i].BindingID {
			return CandidateCompatibility{}, fmt.Errorf("%w: duplicate binding %q", ErrCandidateRuntimeInvalid, bindings[i].BindingID)
		}
	}
	value.Bindings = bindings
	computedBindingFingerprint := fingerprintCandidateBindings(bindings)
	if value.BindingFingerprint != "" && value.BindingFingerprint != computedBindingFingerprint {
		return CandidateCompatibility{}, fmt.Errorf("%w: binding fingerprint mismatch", ErrCandidateRuntimeInvalid)
	}
	value.BindingFingerprint = computedBindingFingerprint
	connections := append([]CandidateAuthoredConnection(nil), value.AuthoredConnections...)
	for i := range connections {
		if connections[i].LogicalConnection != strings.TrimSpace(connections[i].LogicalConnection) || connections[i].ConnectorKind != strings.TrimSpace(connections[i].ConnectorKind) || connections[i].LogicalConnection == "" || connections[i].ConnectorKind == "" {
			return CandidateCompatibility{}, fmt.Errorf("%w: authored connection identity and connector kind are required and canonical", ErrCandidateRuntimeInvalid)
		}
		if connections[i].Access != "" && connections[i].Access != semanticmodel.ConnectionAccessPublic {
			return CandidateCompatibility{}, fmt.Errorf("%w: unsupported authored connection access policy", ErrCandidateRuntimeInvalid)
		}
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].LogicalConnection < connections[j].LogicalConnection })
	for i := 1; i < len(connections); i++ {
		if connections[i-1].LogicalConnection == connections[i].LogicalConnection {
			return CandidateCompatibility{}, fmt.Errorf("%w: duplicate authored connection %q", ErrCandidateRuntimeInvalid, connections[i].LogicalConnection)
		}
	}
	value.AuthoredConnections = connections
	managed := append([]string(nil), value.ManagedDataConnections...)
	for i := range managed {
		if managed[i] != strings.TrimSpace(managed[i]) || managed[i] == "" {
			return CandidateCompatibility{}, fmt.Errorf("%w: managed-data connection identity is required and canonical", ErrCandidateRuntimeInvalid)
		}
	}
	sort.Strings(managed)
	for i := 1; i < len(managed); i++ {
		if managed[i-1] == managed[i] {
			return CandidateCompatibility{}, fmt.Errorf("%w: duplicate managed-data connection %q", ErrCandidateRuntimeInvalid, managed[i])
		}
	}
	value.ManagedDataConnections = managed
	capabilities := cloneRuntimeCapabilities(value.Capabilities)
	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].Name != capabilities[j].Name {
			return capabilities[i].Name < capabilities[j].Name
		}
		if capabilities[i].Identity != capabilities[j].Identity {
			return capabilities[i].Identity < capabilities[j].Identity
		}
		return capabilities[i].Digest < capabilities[j].Digest
	})
	value.Capabilities = capabilities
	restrictions := append([]CandidateRestriction(nil), value.Restrictions...)
	for i := range restrictions {
		p := &restrictions[i]
		if p.ID != strings.TrimSpace(p.ID) || p.ObjectID.String() != strings.TrimSpace(p.ObjectID.String()) || p.PolicyType != strings.TrimSpace(p.PolicyType) || p.ExpressionJSON != strings.TrimSpace(p.ExpressionJSON) || p.ID == "" || p.ObjectID == "" || p.ExpressionJSON == "" {
			return CandidateCompatibility{}, fmt.Errorf("%w: candidate restriction identity and expression are required and canonical", ErrCandidateRuntimeInvalid)
		}
		if p.ObjectID.Validate() != nil || !p.ObjectKind.Valid() || p.Subject != nil && p.Subject.Validate() != nil {
			return CandidateCompatibility{}, fmt.Errorf("%w: candidate restriction object or subject is invalid", ErrCandidateRuntimeInvalid)
		}
		if p.PolicyType != "row_filter" && p.PolicyType != "column_mask" {
			return CandidateCompatibility{}, fmt.Errorf("%w: unsupported candidate restriction type %q", ErrCandidateRuntimeInvalid, p.PolicyType)
		}
	}
	sort.Slice(restrictions, func(i, j int) bool { return restrictions[i].ID < restrictions[j].ID })
	for i := 1; i < len(restrictions); i++ {
		if restrictions[i-1].ID == restrictions[i].ID {
			return CandidateCompatibility{}, fmt.Errorf("%w: duplicate candidate restriction %q", ErrCandidateRuntimeInvalid, restrictions[i].ID)
		}
	}
	value.Restrictions = restrictions
	return value, nil
}

func fingerprintCandidateBindings(bindings []CandidateBindingVersion) string {
	type bindingFingerprintInput struct {
		BindingID          string                         `json:"bindingId"`
		ConnectionID       string                         `json:"connectionId"`
		ConnectorKind      string                         `json:"connectorKind"`
		Revision           int64                          `json:"revision"`
		ProviderVersion    string                         `json:"providerVersion"`
		EndpointConfigHash string                         `json:"endpointConfigHash"`
		Access             semanticmodel.ConnectionAccess `json:"access,omitempty"`
	}
	preimage := make([]bindingFingerprintInput, len(bindings))
	for i, binding := range bindings {
		preimage[i] = bindingFingerprintInput{BindingID: binding.BindingID, ConnectionID: binding.LogicalConnection, ConnectorKind: binding.ConnectorKind, Revision: binding.Revision, ProviderVersion: binding.ProviderVersion, EndpointConfigHash: binding.EndpointConfigHash, Access: binding.Access}
	}
	data, _ := json.Marshal(preimage)
	sum := sha256.Sum256(data)
	return "sha256:" + fmt.Sprintf("%x", sum)
}

func validateCandidateDataMode(state servingstate.State, compatibility CandidateCompatibility, data ManagedDataResolution, requireSealedCatalog bool) error {
	// ManagedDataResolution.RevisionID is an aggregate content binding digest
	// owned by the managed-data resolver. CandidateCompatibility.DataRevision
	// is release provenance (for example, sources:<digest> or snapshot:<id>).
	// These identifiers intentionally live in different namespaces; immutable
	// serving-state bindings and the exact managed connection set below are the
	// runtime guarantees, so the two revisions must not be compared.
	switch compatibility.DataMode {
	case CandidateDataReuseBase:
		if state.Digest == "" || len(compatibility.AuthoredConnections) != 0 {
			return fmt.Errorf("%w: sealed-base reuse requires an exact serving-state identity and no authored refresh connections", ErrCandidateRuntimeIncompatible)
		}
	case CandidateDataRefreshSources:
		if len(compatibility.Bindings) == 0 && len(compatibility.ManagedDataConnections) == 0 && len(compatibility.AuthoredConnections) == 0 {
			return fmt.Errorf("%w: source refresh requires declared connections", ErrCandidateRuntimeIncompatible)
		}
		// In a sealed target, refresh_sources records how the candidate was
		// built; serving attaches the exact materialized snapshot admitted by
		// delivery. Unsealed targets still perform that preparation locally and
		// therefore require an unmaterialized state at this boundary.
		if requireSealedCatalog {
			if state.DuckLakeSnapshotID <= 0 {
				return fmt.Errorf("%w: sealed source refresh requires a positive serving-state snapshot", ErrCandidateRuntimeIncompatible)
			}
		} else if state.DuckLakeSnapshotID != 0 {
			return fmt.Errorf("%w: source refresh requires an unmaterialized state", ErrCandidateRuntimeIncompatible)
		}
	default:
		return ErrCandidateRuntimeInvalid
	}
	resolved := make([]string, 0, len(data.Roots))
	for connection, root := range data.Roots {
		if connection == "" || connection != strings.TrimSpace(connection) || strings.TrimSpace(root) == "" {
			return fmt.Errorf("%w: resolved managed-data roots are invalid", ErrCandidateRuntimeIncompatible)
		}
		resolved = append(resolved, connection)
	}
	sort.Strings(resolved)
	if len(resolved) != len(compatibility.ManagedDataConnections) {
		return fmt.Errorf("%w: managed-data connection set changed during runtime preparation", ErrCandidateRuntimeIncompatible)
	}
	for i := range resolved {
		if resolved[i] != compatibility.ManagedDataConnections[i] {
			return fmt.Errorf("%w: managed-data connection set changed during runtime preparation", ErrCandidateRuntimeIncompatible)
		}
	}
	return nil
}

func (r *candidateRuntimeRegistry) acquire(candidateID, ownerID string, projectID projectgraph.ResourceID, fingerprint [sha256.Size]byte) (*candidateGeneration, []*candidateGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCandidateRuntimeClosed
	}
	g := r.current[candidateRuntimeKey{candidateID: candidateID}]
	if g == nil || g.projectID != projectID || g.ownerID != ownerID {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	if !g.expiresAt.After(r.now()) {
		return nil, r.retireLockedList(g), ErrCandidateRuntimeExpired
	}
	if g.closing {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	if g.fingerprint != fingerprint {
		return nil, nil, ErrCandidateRuntimeIncompatible
	}
	g.refs++
	return g, nil, nil
}
func (r *candidateRuntimeRegistry) acquireOwned(candidateID, ownerID string, projectID projectgraph.ResourceID) (*candidateGeneration, []*candidateGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCandidateRuntimeClosed
	}
	g := r.current[candidateRuntimeKey{candidateID: candidateID}]
	if g == nil || g.projectID != projectID || g.ownerID != ownerID {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	if !g.expiresAt.After(r.now()) {
		return nil, r.retireLockedList(g), ErrCandidateRuntimeExpired
	}
	if g.closing {
		return nil, nil, ErrCandidateRuntimeNotFound
	}
	g.refs++
	return g, nil, nil
}
func (r *candidateRuntimeRegistry) resolveOwned(candidateID, ownerID string, projectID projectgraph.ResourceID, registry *Registry) (OwnedCandidateView, []*candidateGeneration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.current[candidateRuntimeKey{candidateID: candidateID}]
	if g == nil || g.ownerID != ownerID || g.projectID != projectID || g.closing {
		return OwnedCandidateView{}, nil, ErrCandidateRuntimeNotFound
	}
	if !g.expiresAt.After(r.now()) {
		return OwnedCandidateView{}, r.retireLockedList(g), ErrCandidateRuntimeExpired
	}
	return OwnedCandidateView{CandidateID: g.key.candidateID, ProjectID: g.projectID, Provider: &candidateRuntimeProvider{registry: registry, candidateID: g.key.candidateID, ownerID: g.ownerID}, Restrictions: append([]CandidateRestriction(nil), g.compatibility.Restrictions...), AuthorizationFingerprint: g.compatibility.AuthorizationFingerprint}, nil, nil
}
func (r *candidateRuntimeRegistry) retire(id string) ([]*candidateGeneration, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g := r.current[candidateRuntimeKey{candidateID: id}]; g != nil {
		return r.retireLockedList(g), 1
	}
	return nil, 0
}
func (r *candidateRuntimeRegistry) reap(now time.Time) ([]*candidateGeneration, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now = now.UTC()
	var drained []*candidateGeneration
	count := 0
	for _, g := range r.current {
		if !g.expiresAt.After(now) && !g.closing {
			if d := r.retireLocked(g); d != nil {
				drained = append(drained, d)
			}
			count++
		}
	}
	return drained, count
}
func (r *candidateRuntimeRegistry) retireLockedList(g *candidateGeneration) []*candidateGeneration {
	if d := r.retireLocked(g); d != nil {
		return []*candidateGeneration{d}
	}
	return nil
}
func (r *candidateRuntimeRegistry) retireLocked(g *candidateGeneration) *candidateGeneration {
	if g == nil || g.closing {
		return g
	}
	g.closing = true
	delete(r.current, g.key)
	r.retired[g] = struct{}{}
	if g.refs == 0 {
		return g
	}
	return nil
}
func (r *candidateRuntimeRegistry) release(g *candidateGeneration) *candidateGeneration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g == nil || g.refs == 0 {
		return nil
	}
	g.refs--
	if g.refs == 0 && g.closing {
		delete(r.retired, g)
		return g
	}
	return nil
}
func (r *candidateRuntimeRegistry) close() ([]*candidateGeneration, []*candidateGeneration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil
	}
	r.closed = true
	var drained, targets []*candidateGeneration
	seen := map[*candidateGeneration]struct{}{}
	for _, g := range r.current {
		if d := r.retireLocked(g); d != nil {
			drained = append(drained, d)
		}
		if _, ok := seen[g]; !ok {
			seen[g] = struct{}{}
			targets = append(targets, g)
		}
	}
	for g := range r.retired {
		if _, ok := seen[g]; !ok {
			seen[g] = struct{}{}
			targets = append(targets, g)
		}
	}
	return drained, targets
}

type candidateRuntimeProvider struct {
	registry             *Registry
	candidateID, ownerID string
}

func (p *candidateRuntimeProvider) Acquire(ctx context.Context) (Lease, error) {
	if p == nil || p.registry == nil {
		return nil, ErrCandidateRuntimeClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g, retired, err := p.registry.candidates.acquireOwned(p.candidateID, p.ownerID, p.registry.ProjectID())
	for _, x := range retired {
		p.registry.cleanupCandidateGeneration(x)
	}
	if err != nil {
		return nil, err
	}
	return &candidateRuntimeLease{registry: p.registry, generation: g}, nil
}

type candidateRuntimeLease struct {
	registry   *Registry
	generation *candidateGeneration
	once       sync.Once
}

func (l *candidateRuntimeLease) Runtime() Runtime {
	if l == nil || l.generation == nil {
		return nil
	}
	return l.generation.managed.runtime
}
func (l *candidateRuntimeLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	if l == nil || l.generation == nil || l.generation.managed == nil {
		return accesssnapshot.AuthorizationSnapshot{}
	}
	return l.generation.managed.authorization
}
func (l *candidateRuntimeLease) Identity() projectgraph.ServingIdentity {
	if l == nil || l.generation == nil {
		return projectgraph.ServingIdentity{}
	}
	return l.generation.managed.identity
}
func (l *candidateRuntimeLease) DuckLakeSnapshotID() int64 {
	if l == nil || l.generation == nil {
		return 0
	}
	return l.generation.managed.snapshotID
}
func (l *candidateRuntimeLease) Release() {
	if l == nil || l.registry == nil {
		return
	}
	l.once.Do(func() {
		if g := l.registry.candidates.release(l.generation); g != nil {
			l.registry.cleanupCandidateGeneration(g)
		}
	})
}
