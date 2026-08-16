package runtimehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
}

func (r *candidateResolver) ResolveManagedData(context.Context, servingstate.ID) (ManagedDataResolution, error) {
	return ManagedDataResolution{RevisionID: "rev1", Roots: map[string]string{"warehouse": "/tmp/warehouse"}, Lifetime: r.lifetime}, nil
}

func candidateRegistration(expires time.Time) CandidateRegistration {
	return CandidateRegistration{
		CandidateID: "candidate_1", OwnerID: "owner_1", ProjectID: "project_demo", ExpiresAt: expires,
		Compatibility: CandidateCompatibility{
			ArtifactDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DataRevision:   "rev1", DataMode: CandidateDataReuseSnapshot, RuntimeVersion: "runtime-v1",
			AuthorizationFingerprint: "auth-v1", ManagedDataConnections: []string{"warehouse"},
		},
	}
}

func candidateRegistry(t *testing.T, now *time.Time, factory *lifecycleFactory, resolver *candidateResolver, cleanup func(CleanupFailure)) *Registry {
	t.Helper()
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, DuckLakeSnapshotID: 42}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	return NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, ManagedData: resolver, Authorization: &lifecycleAuth{}, OnCleanupFailure: cleanup, Now: func() time.Time { return *now }, CleanupDrainTimeout: time.Second})
}

func TestCandidateOwnershipCompatibilityAndRetireDrain(t *testing.T) {
	now := time.Now().UTC()
	managed := &candidateManagedData{}
	registry := candidateRegistry(t, &now, &lifecycleFactory{}, &candidateResolver{lifetime: managed}, nil)
	defer registry.Close()
	registration := candidateRegistration(now.Add(time.Hour))
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, ServingStateID: "generation_1"}); err != nil {
		t.Fatal(err)
	}
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
	if registry.RetireCandidate(registration.CandidateID) != 1 {
		t.Fatal("candidate was not retired")
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
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, ServingStateID: "generation_1"}); !errors.Is(err, ErrCandidateRuntimeInvalid) {
		t.Fatalf("non-canonical candidate ID error = %v", err)
	}
	registration = candidateRegistration(now.Add(time.Second))
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, ServingStateID: "generation_1"}); err != nil {
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
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, ServingStateID: "generation_1"}); err != nil {
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
