package runtimehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type candidateManagedData struct {
	mu     sync.Mutex
	closes int
	err    error
}

func (l *candidateManagedData) Release() error {
	l.mu.Lock()
	l.closes++
	l.mu.Unlock()
	return l.err
}

type candidateResolver struct {
	lifetime *candidateManagedData
	identity projectgraph.ServingIdentity
}

func (r *candidateResolver) ResolveManagedDataForIdentity(_ context.Context, identity projectgraph.ServingIdentity) (ManagedDataResolution, error) {
	r.identity = identity
	return ManagedDataResolution{RevisionID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Roots: map[string]string{"warehouse": "/tmp/warehouse"}, Lifetime: r.lifetime}, nil
}

func candidateRegistration(expires time.Time) CandidateRegistration {
	return CandidateRegistration{
		CandidateID: "candidate_1", OwnerID: "owner_1", ProjectID: "project_demo", ExpiresAt: expires,
		Compatibility: CandidateCompatibility{
			ArtifactDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DataRevision:   "snapshot:42", DataMode: CandidateDataReuseBase, RuntimeVersion: "runtime-v1",
			AuthorizationFingerprint: "auth-v1", ManagedDataConnections: []string{"warehouse"},
		},
	}
}

func candidateRegistry(t *testing.T, now *time.Time, factory *lifecycleFactory, resolver *candidateResolver, cleanup func(CleanupFailure)) *Registry {
	t.Helper()
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DuckLakeSnapshotID: 42}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	return NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, ManagedData: resolver, Authorization: &lifecycleAuth{}, OnCleanupFailure: cleanup, Now: func() time.Time { return *now }, CleanupDrainTimeout: time.Second})
}

func TestCandidateAcquisitionRequiresBoundProject(t *testing.T) {
	now := time.Now().UTC()
	repo := &unboundLifecycleRepo{lifecycleRepo: &lifecycleRepo{noActive: true}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}, Now: func() time.Time { return now }})
	defer registry.Close()
	registration := candidateRegistration(now.Add(time.Hour))
	_, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: registration.Compatibility})
	if !errors.Is(err, ErrCandidateRuntimeIncompatible) {
		t.Fatalf("unbound candidate acquisition error = %v, want incompatible", err)
	}
}

func TestCandidateCompatibilityBindsCanonicalLocalCredentialVersion(t *testing.T) {
	const versionID = "0198f2c0-7c7a-7f00-8a11-000000000301"
	base := CandidateCompatibility{
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), DataRevision: "sources:1",
		DataMode: CandidateDataRefreshSources, RuntimeVersion: "runtime:v1", AuthorizationFingerprint: "auth:v1",
		Bindings: []CandidateBindingVersion{{
			BindingID: "binding_warehouse", LogicalConnection: "warehouse", ConnectorKind: "postgres", Revision: 7,
			CredentialVersionID: versionID, EndpointConfigHash: "sha256:" + strings.Repeat("b", 64),
		}},
	}
	normalized, err := normalizeCompatibility(base)
	if err != nil {
		t.Fatalf("normalize local credential version binding: %v", err)
	}
	if normalized.Bindings[0].CredentialVersionID != versionID {
		t.Fatalf("local credential version=%q, want %q", normalized.Bindings[0].CredentialVersionID, versionID)
	}

	other := base
	other.Bindings = append([]CandidateBindingVersion(nil), base.Bindings...)
	other.Bindings[0].CredentialVersionID = "0198f2c0-7c7a-7f00-8a11-000000000302"
	normalizedOther, err := normalizeCompatibility(other)
	if err != nil {
		t.Fatalf("normalize other local credential version binding: %v", err)
	}
	if normalized.BindingFingerprint == normalizedOther.BindingFingerprint {
		t.Fatal("candidate binding fingerprint did not change with local credential version")
	}
}

func TestCandidateCompatibilityRejectsInvalidLocalCredentialVersion(t *testing.T) {
	const versionID = "0198f2c0-7c7a-7f00-8a11-000000000301"
	base := CandidateCompatibility{
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), DataRevision: "sources:1",
		DataMode: CandidateDataRefreshSources, RuntimeVersion: "runtime:v1", AuthorizationFingerprint: "auth:v1",
		Bindings: []CandidateBindingVersion{{
			BindingID: "binding_warehouse", LogicalConnection: "warehouse", ConnectorKind: "postgres", Revision: 7,
			CredentialVersionID: versionID, EndpointConfigHash: "sha256:" + strings.Repeat("b", 64),
		}},
	}
	tests := map[string]func(*CandidateBindingVersion){
		"malformed UUID": func(value *CandidateBindingVersion) { value.CredentialVersionID = "not-a-uuid" },
		"nil UUID": func(value *CandidateBindingVersion) {
			value.CredentialVersionID = "00000000-0000-0000-0000-000000000000"
		},
		"both version sources": func(value *CandidateBindingVersion) { value.ProviderVersion = "provider:v1" },
		"non-postgres":         func(value *CandidateBindingVersion) { value.ConnectorKind = "s3" },
		"public binding":       func(value *CandidateBindingVersion) { value.Access = semanticmodel.ConnectionAccessPublic },
		"unknown access":       func(value *CandidateBindingVersion) { value.Access = semanticmodel.ConnectionAccess("unknown") },
		"missing both":         func(value *CandidateBindingVersion) { value.CredentialVersionID = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := base
			input.Bindings = append([]CandidateBindingVersion(nil), base.Bindings...)
			mutate(&input.Bindings[0])
			if _, err := normalizeCompatibility(input); !errors.Is(err, ErrCandidateRuntimeInvalid) {
				t.Fatalf("normalize invalid local credential version error=%v, want %v", err, ErrCandidateRuntimeInvalid)
			}
		})
	}
}

func TestCandidateOwnershipCompatibilityAndRetireDrain(t *testing.T) {
	now := time.Now().UTC()
	managed := &candidateManagedData{}
	registry := candidateRegistry(t, &now, &lifecycleFactory{}, &candidateResolver{lifetime: managed}, nil)
	defer registry.Close()
	registration := candidateRegistration(now.Add(time.Hour))
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1"}}); err != nil {
		t.Fatal(err)
	}
	view, err := registry.ResolveOwnedCandidate(registration.CandidateID, registration.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.CandidateID != registration.CandidateID || view.ProjectID != registration.ProjectID || view.AuthorizationFingerprint != registration.Compatibility.AuthorizationFingerprint {
		t.Fatalf("owned candidate view = %+v", view)
	}
	providerLease, err := view.Provider.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if providerLease.Runtime() == nil {
		t.Fatal("owned candidate provider returned a lease without a runtime")
	}
	if got := providerLease.Identity(); got.ProjectID != registration.ProjectID || got.GenerationID != "generation_1" {
		t.Fatalf("owned candidate lease identity = %+v", got)
	}
	ownedLease, ok := providerLease.(*candidateRuntimeLease)
	if !ok {
		t.Fatalf("owned candidate lease type = %T", providerLease)
	}
	if got := ownedLease.DuckLakeSnapshotID(); got != 42 {
		t.Fatalf("owned candidate lease snapshot = %d, want 42", got)
	}
	if got := ownedLease.AuthorizationSnapshot().Identity(); got.GenerationID != "generation_1" {
		t.Fatalf("owned candidate authorization identity = %+v", got)
	}
	providerLease.Release()
	wrong := registration
	wrong.Compatibility.AuthorizationFingerprint = "other"
	if _, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: wrong.Compatibility}); !errors.Is(err, ErrCandidateRuntimeIncompatible) {
		t.Fatalf("compatibility mismatch error = %v", err)
	}
	if _, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: "owner_2", ProjectID: registration.ProjectID, Compatibility: registration.Compatibility}); !errors.Is(err, ErrCandidateRuntimeNotFound) {
		t.Fatalf("foreign owner error = %v", err)
	}
	lease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: registration.Compatibility})
	if err != nil {
		t.Fatal(err)
	}
	runtime := lease.Runtime().(*lifecycleRuntime)
	if _, err := registry.RetireCandidate(CandidateRetirementRequest{
		CandidateID: registration.CandidateID, OwnerID: registration.OwnerID,
		Identity: lease.Identity(), Compatibility: registration.Compatibility,
	}); err != nil {
		t.Fatalf("retire candidate: %v", err)
	}
	select {
	case <-runtime.closed:
		t.Fatal("candidate runtime closed while reader lease was active")
	default:
	}
	lease.Release()
	select {
	case <-runtime.closed:
	case <-time.After(time.Second):
		t.Fatal("candidate runtime did not drain")
	}
}

func TestCandidateExpiryAndCanonicalIdentity(t *testing.T) {
	now := time.Now().UTC()
	registry := candidateRegistry(t, &now, &lifecycleFactory{}, &candidateResolver{lifetime: &candidateManagedData{}}, nil)
	defer registry.Close()
	registration := candidateRegistration(now.Add(time.Second))
	registration.CandidateID = " candidate_1"
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1"}}); !errors.Is(err, ErrCandidateRuntimeInvalid) {
		t.Fatalf("non-canonical candidate ID error = %v", err)
	}
	registration = candidateRegistration(now.Add(time.Second))
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1"}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if got := registry.ReapExpiredCandidates(now); got != 1 {
		t.Fatalf("reaped candidates = %d", got)
	}
	if _, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: registration.Compatibility}); !errors.Is(err, ErrCandidateRuntimeNotFound) {
		t.Fatalf("expired acquire error = %v", err)
	}
}

func TestCandidateCloseWaitsForLeasesAndReportsCleanupFailure(t *testing.T) {
	now := time.Now().UTC()
	fail := errors.New("managed data cleanup failed")
	managed := &candidateManagedData{err: fail}
	var reported chan CleanupFailure
	registry := candidateRegistry(t, &now, &lifecycleFactory{}, &candidateResolver{lifetime: managed}, func(f CleanupFailure) {
		select {
		case reported <- f:
		default:
		}
	})
	reported = make(chan CleanupFailure, 1)
	registration := candidateRegistration(now.Add(time.Hour))
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1"}}); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: registration.Compatibility})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- registry.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("registry closed while candidate lease was active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	lease.Release()
	if err := <-closed; !errors.Is(err, fail) {
		t.Fatalf("close error = %v, want cleanup failure", err)
	}
	select {
	case failure := <-reported:
		if failure.Resource != CleanupResourceManagedData {
			t.Fatalf("cleanup resource = %s", failure.Resource)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup failure was not reported")
	}
}
