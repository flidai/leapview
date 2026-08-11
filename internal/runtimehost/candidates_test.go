package runtimehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestCandidatePreparedLeaseRenewalFailureAggregatesAtRegistry(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeRepo{deployment: servingstate.State{ID: "candidate_sales_lease", WorkspaceID: "sales", Environment: "prod", Status: servingstate.StatusValidated, DuckLakeSnapshotID: 41}, artifact: servingstate.Artifact{ServingStateID: "candidate_sales_lease", WorkspaceID: "sales", Environment: "prod", Digest: "candidate-lease"}}
	repo.extendAlwaysFail = true
	repo.extendFailureErr = errors.New("candidate lease renewal unavailable")
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{}, Now: func() time.Time { return now },
		LeaseTTL: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = registry.Close() })
	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_lease", OwnerID: "owner", WorkspaceID: "sales", ExpiresAt: now.Add(time.Hour),
		Compatibility: candidateCompatibility("lease"),
	}, "candidate_sales_lease")
	deadline := time.Now().Add(2 * time.Second)
	for registry.LeaseRenewalError() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := registry.LeaseRenewalError(); err == nil || !strings.Contains(err.Error(), "candidate lease renewal unavailable") {
		t.Fatalf("candidate lease renewal error = %v", err)
	}
}

func TestCandidateRuntimeReplacementIsPrivateAndDrainsLeasedGeneration(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	repo.active["sales/prod"] = registryDeploymentArtifact{
		deployment: servingstate.State{
			ID: "active_sales", WorkspaceID: "sales", Environment: "prod",
			Status: servingstate.StatusActive, DuckLakeSnapshotID: 11,
		},
		artifact: servingstate.Artifact{
			ServingStateID: "active_sales", WorkspaceID: "sales", Environment: "prod", Digest: "active",
		},
	}
	addCandidateServingState(repo, "candidate_sales_1", "sales", "candidate-1", 21)
	addCandidateServingState(repo, "candidate_sales_2", "sales", "candidate-2", 22)
	factory := &recordingRegistryFactory{}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, WorkspaceIDs: []servingstate.WorkspaceID{"sales"}, Environment: "prod",
		Factory: factory, Now: func() time.Time { return now },
	})
	if err := registry.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
	}, "candidate_sales_1")
	oldLease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		Compatibility: candidateCompatibility("one"),
	})
	require.NoError(t, err)
	oldRuntime := oldLease.Runtime().(*recordingRuntime)

	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("two"),
	}, "candidate_sales_2")
	if oldRuntime.closed.Load() {
		t.Fatal("replaced candidate runtime closed while a lease was active")
	}
	newLease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		Compatibility: candidateCompatibility("two"),
	})
	require.NoError(t, err)
	if newLease.ServingStateID() != "candidate_sales_2" || newLease.DuckLakeSnapshotID() != 22 {
		t.Fatalf("new candidate lease = (%s, %d)", newLease.ServingStateID(), newLease.DuckLakeSnapshotID())
	}
	newLease.Release()

	active, err := registry.AcquireForWorkspace(t.Context(), "sales")
	require.NoError(t, err)
	if active.ServingStateID() != "active_sales" || active.DuckLakeSnapshotID() != 11 {
		t.Fatalf("candidate replacement changed active runtime = (%s, %d)", active.ServingStateID(), active.DuckLakeSnapshotID())
	}
	active.Release()

	oldLease.Release()
	waitForManagerCleanup(t, registry.managerForWorkspace("sales"))
	if !oldRuntime.closed.Load() {
		t.Fatal("replaced candidate runtime remained open after its final lease")
	}
}

func TestOwnedCandidateViewProvidesServerResolvedCompatibilityAndProvider(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	registration := CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
	}
	registerCandidateRuntime(t, registry, registration, "candidate_sales_1")

	view, err := registry.ResolveOwnedCandidate("cand_1", "author_1")
	if err != nil {
		t.Fatalf("ResolveOwnedCandidate() error = %v", err)
	}
	if len(view.Workspaces) != 1 ||
		view.Workspaces[0].WorkspaceID != "sales" ||
		view.Workspaces[0].AuthorizationFingerprint != registration.Compatibility.AuthorizationFingerprint {
		t.Fatalf("ResolveOwnedCandidate() = %#v", view)
	}

	lease, err := view.Workspaces[0].Provider.Acquire(t.Context())
	if err != nil {
		t.Fatalf("candidate provider Acquire() error = %v", err)
	}
	defer lease.Release()
	if lease.ServingStateID() != "candidate_sales_1" {
		t.Fatalf("candidate serving state = %q", lease.ServingStateID())
	}
}

func TestOwnedCandidateViewConcealsForeignAndMissingCandidates(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
	}, "candidate_sales_1")

	for _, request := range []struct {
		candidate string
		owner     string
	}{
		{candidate: "cand_1", owner: "author_2"},
		{candidate: "missing", owner: "author_1"},
	} {
		if _, err := registry.ResolveOwnedCandidate(request.candidate, request.owner); !errors.Is(err, ErrCandidateRuntimeNotFound) {
			t.Fatalf("ResolveOwnedCandidate(%q, %q) error = %v", request.candidate, request.owner, err)
		}
	}
}

func TestCandidateRuntimeLeaseFailsClosedAcrossOwnershipAndCompatibilityBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
	}, "candidate_sales_1")

	for name, request := range map[string]CandidateLeaseRequest{
		"owner": {
			CandidateID: "cand_1", OwnerID: "author_2", WorkspaceID: "sales",
			Compatibility: candidateCompatibility("one"),
		},
		"workspace": {
			CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "operations",
			Compatibility: candidateCompatibility("one"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.AcquireCandidate(t.Context(), request); !errors.Is(err, ErrCandidateRuntimeNotFound) {
				t.Fatalf("AcquireCandidate() error = %v, want concealed not found", err)
			}
		})
	}
	if _, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		Compatibility: candidateCompatibility("two"),
	}); !errors.Is(err, ErrCandidateRuntimeIncompatible) {
		t.Fatalf("AcquireCandidate() error = %v, want incompatible", err)
	}
}

func TestCandidateRuntimeExpiryRejectsNewLeasesAndDrainsExistingLease(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Minute), Compatibility: candidateCompatibility("one"),
	}, "candidate_sales_1")
	request := CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		Compatibility: candidateCompatibility("one"),
	}
	lease, err := registry.AcquireCandidate(t.Context(), request)
	require.NoError(t, err)
	runtime := lease.Runtime().(*recordingRuntime)

	now = now.Add(2 * time.Minute)
	if _, err := registry.AcquireCandidate(t.Context(), request); !errors.Is(err, ErrCandidateRuntimeExpired) {
		t.Fatalf("expired AcquireCandidate() error = %v", err)
	}
	if runtime.closed.Load() {
		t.Fatal("expired candidate runtime closed while an existing lease was active")
	}
	lease.Release()
	waitForManagerCleanup(t, registry.managerForWorkspace("sales"))
	if !runtime.closed.Load() {
		t.Fatal("expired candidate runtime remained open after its final lease")
	}
	if removed := registry.ReapExpiredCandidates(now); removed != 0 {
		t.Fatalf("reaped candidates = %d, want already retired generation", removed)
	}
}

func TestCandidateRuntimeRetirementIsSafeWithConcurrentLeaseRelease(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	registerCandidateRuntime(t, registry, CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
	}, "candidate_sales_1")
	request := CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		Compatibility: candidateCompatibility("one"),
	}
	leases := make([]Lease, 32)
	for index := range leases {
		lease, err := registry.AcquireCandidate(t.Context(), request)
		require.NoError(t, err)
		leases[index] = lease
	}
	runtime := leases[0].Runtime().(*recordingRuntime)

	var wait sync.WaitGroup
	wait.Add(len(leases) + 1)
	for _, lease := range leases {
		go func(lease Lease) {
			defer wait.Done()
			lease.Release()
		}(lease)
	}
	go func() {
		defer wait.Done()
		registry.RetireCandidate("cand_1")
	}()
	wait.Wait()
	waitForManagerCleanup(t, registry.managerForWorkspace("sales"))

	if !runtime.closed.Load() {
		t.Fatal("retired candidate runtime remained open after concurrent releases")
	}
	if _, err := registry.AcquireCandidate(context.Background(), request); !errors.Is(err, ErrCandidateRuntimeNotFound) {
		t.Fatalf("AcquireCandidate() after retirement error = %v", err)
	}
}

func TestCandidateRuntimeOwnsExternalDependenciesUntilGenerationDrains(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	lifetime := &candidateTestLifetime{}
	registration := CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
	}
	if err := registry.PrepareAndRegisterCandidate(
		t.Context(),
		CandidatePreparation{
			Registration: registration, ServingStateID: "candidate_sales_1",
			Lifetime: lifetime,
		},
	); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: registration.CandidateID,
		OwnerID:     registration.OwnerID, WorkspaceID: registration.WorkspaceID,
		Compatibility: registration.Compatibility,
	})
	require.NoError(t, err)
	registry.RetireCandidate(registration.CandidateID)
	if lifetime.closes.Load() != 0 {
		t.Fatal("candidate dependency closed while a runtime lease was active")
	}
	lease.Release()
	waitForManagerCleanup(t, registry.managerForWorkspace("sales"))
	if lifetime.closes.Load() != 1 {
		t.Fatalf("candidate dependency closes = %d, want 1 after drain", lifetime.closes.Load())
	}
}

func TestCandidateRuntimeRejectsRegistrationUnderDifferentCompatibility(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	registry := candidateTestRegistry(t, func() time.Time { return now })
	prepared, err := registry.PrepareCandidate(t.Context(), CandidatePreparation{
		Registration: CandidateRegistration{
			CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
			ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("one"),
		},
		ServingStateID: "candidate_sales_1",
	})
	require.NoError(t, err)
	defer prepared.Close()

	err = registry.RegisterPreparedCandidate(CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("two"),
	}, prepared)
	if !errors.Is(err, ErrCandidateRuntimeIncompatible) {
		t.Fatalf("RegisterPreparedCandidate() error = %v, want incompatible", err)
	}
}

func TestCandidateRuntimeSetReplacesEveryWorkspaceAsOneGeneration(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	for _, state := range []struct {
		id        servingstate.ID
		workspace servingstate.WorkspaceID
		snapshot  int64
	}{
		{id: "sales_1", workspace: "sales", snapshot: 11},
		{id: "ops_1", workspace: "operations", snapshot: 12},
		{id: "sales_2", workspace: "sales", snapshot: 21},
		{id: "ops_2", workspace: "operations", snapshot: 22},
	} {
		addCandidateServingState(repo, state.id, state.workspace, string(state.id), state.snapshot)
	}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = registry.Close() })
	prepareSet := func(suffix string) []CandidatePreparation {
		return []CandidatePreparation{
			{
				Registration: CandidateRegistration{
					CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
					ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("sales-" + suffix),
				},
				ServingStateID: "sales_" + suffix,
			},
			{
				Registration: CandidateRegistration{
					CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "operations",
					ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("ops-" + suffix),
				},
				ServingStateID: "ops_" + suffix,
			},
		}
	}
	if err := registry.PrepareAndRegisterCandidateSet(t.Context(), prepareSet("1")); err != nil {
		t.Fatal(err)
	}
	old, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
		Compatibility: candidateCompatibility("sales-1"),
	})
	require.NoError(t, err)
	oldRuntime := old.Runtime().(*recordingRuntime)

	if err := registry.PrepareAndRegisterCandidateSet(t.Context(), prepareSet("2")); err != nil {
		t.Fatal(err)
	}
	for workspace, compatibility := range map[servingstate.WorkspaceID]CandidateCompatibility{
		"sales":      candidateCompatibility("sales-2"),
		"operations": candidateCompatibility("ops-2"),
	} {
		lease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
			CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: workspace,
			Compatibility: compatibility,
		})
		if err != nil {
			t.Fatalf("acquire %s: %v", workspace, err)
		}
		if lease.DuckLakeSnapshotID() < 20 {
			t.Fatalf("%s candidate retained old snapshot %d", workspace, lease.DuckLakeSnapshotID())
		}
		lease.Release()
	}
	if oldRuntime.closed.Load() {
		t.Fatal("old workspace runtime closed before its outstanding lease drained")
	}
	old.Release()
	waitForManagerCleanup(t, registry.managerForWorkspace("sales"))
	if !oldRuntime.closed.Load() {
		t.Fatal("old workspace runtime remained open after set replacement drained")
	}
}

func TestCandidateRuntimeSetClosesEverySuppliedLifetimeWhenPreparationFails(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "sales_1", "sales", "sales", 11)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = registry.Close() })
	lifetimes := []*candidateTestLifetime{{}, {}, {}}
	inputs := []CandidatePreparation{
		{
			Registration: CandidateRegistration{
				CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
				ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("sales"),
			},
			ServingStateID: "sales_1", Lifetime: lifetimes[0],
		},
		{
			Registration: CandidateRegistration{
				CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "operations",
				ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("operations"),
			},
			ServingStateID: "missing", Lifetime: lifetimes[1],
		},
		{
			Registration: CandidateRegistration{
				CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "finance",
				ExpiresAt: now.Add(time.Hour), Compatibility: candidateCompatibility("finance"),
			},
			ServingStateID: "unreached", Lifetime: lifetimes[2],
		},
	}

	if err := registry.PrepareAndRegisterCandidateSet(t.Context(), inputs); err == nil {
		t.Fatal("PrepareAndRegisterCandidateSet() error = nil, want preparation failure")
	}
	for index, lifetime := range lifetimes {
		if lifetime.closes.Load() != 1 {
			t.Fatalf("lifetime %d closes = %d, want 1", index, lifetime.closes.Load())
		}
	}
}

func TestCandidateRuntimeDataModeFailsClosedAgainstServingSnapshotAndBindings(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "with_snapshot", "sales", "snapshot", 11)
	addCandidateServingState(repo, "without_snapshot", "sales", "refresh", 0)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = registry.Close() })
	for name, input := range map[string]CandidatePreparation{
		"reuse_without_snapshot": {
			Registration: CandidateRegistration{
				CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
				ExpiresAt: now.Add(time.Hour),
				Compatibility: CandidateCompatibility{
					ArtifactDigest: "artifact", DataRevision: "data",
					DataMode:       CandidateDataReuseSnapshot,
					RuntimeVersion: "runtime", AuthorizationFingerprint: "policy",
				},
			},
			ServingStateID: "without_snapshot",
		},
		"refresh_with_snapshot": {
			Registration: CandidateRegistration{
				CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
				ExpiresAt: now.Add(time.Hour),
				Compatibility: CandidateCompatibility{
					ArtifactDigest: "artifact", DataRevision: "data",
					DataMode:       CandidateDataRefreshSources,
					RuntimeVersion: "runtime", AuthorizationFingerprint: "policy",
					Bindings: []CandidateBindingVersion{{
						BindingID: "warehouse", LogicalConnection: "warehouse",
						ConnectorKind: "postgres", Revision: 1, ProviderVersion: "provider",
						EndpointConfigHash: "sha256:" + strings.Repeat("9", 64),
					}},
				},
			},
			ServingStateID: "with_snapshot",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.PrepareCandidate(t.Context(), input); !errors.Is(
				err,
				ErrCandidateRuntimeIncompatible,
			) {
				t.Fatalf("PrepareCandidate() error = %v, want incompatible", err)
			}
		})
	}
}

func TestCandidateRuntimeRefreshAcceptsExactValidatedManagedDataConnections(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "managed_refresh", "sales", "refresh", 0)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		ManagedData: fakeManagedDataResolver{resolution: ManagedDataResolution{
			RevisionID: "sha256:" + strings.Repeat("a", 64),
			Roots:      map[string]string{"olist": "/managed/olist"},
		}},
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = registry.Close() })

	compatibility := CandidateCompatibility{
		ArtifactDigest: "artifact", DataRevision: "data",
		DataMode:       CandidateDataRefreshSources,
		RuntimeVersion: "runtime", AuthorizationFingerprint: "policy",
		ManagedDataConnections: []string{"olist"},
	}
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: CandidateRegistration{
			CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
			ExpiresAt: now.Add(time.Hour), Compatibility: compatibility,
		},
		ServingStateID: "managed_refresh",
	}); err != nil {
		t.Fatalf("PrepareAndRegisterCandidate() error = %v", err)
	}
}

func TestCandidateRuntimeRefreshAcceptsDeclaredAuthoredConnections(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "authored_refresh", "public", "refresh", 0)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = registry.Close() })

	compatibility := CandidateCompatibility{
		ArtifactDigest: "artifact", DataRevision: "data",
		DataMode:       CandidateDataRefreshSources,
		RuntimeVersion: "runtime", AuthorizationFingerprint: "policy",
		AuthoredConnections: []CandidateAuthoredConnection{{
			LogicalConnection: "public_http", ConnectorKind: "http",
		}},
	}
	require.NoError(t, registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: CandidateRegistration{
			CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "public",
			ExpiresAt: now.Add(time.Hour), Compatibility: compatibility,
		},
		ServingStateID: "authored_refresh",
	}))
}

func TestCandidateRuntimeRefreshRejectsManagedDataConnectionDrift(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "managed_refresh", "sales", "refresh", 0)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		ManagedData: fakeManagedDataResolver{resolution: ManagedDataResolution{
			RevisionID: "sha256:" + strings.Repeat("a", 64),
			Roots:      map[string]string{"olist": "/managed/olist"},
		}},
		Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = registry.Close() })

	_, err := registry.PrepareCandidate(t.Context(), CandidatePreparation{
		Registration: CandidateRegistration{
			CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales",
			ExpiresAt: now.Add(time.Hour),
			Compatibility: CandidateCompatibility{
				ArtifactDigest: "artifact", DataRevision: "data",
				DataMode:       CandidateDataRefreshSources,
				RuntimeVersion: "runtime", AuthorizationFingerprint: "policy",
				ManagedDataConnections: []string{"other"},
			},
		},
		ServingStateID: "managed_refresh",
	})
	if !errors.Is(err, ErrCandidateRuntimeIncompatible) {
		t.Fatalf("PrepareCandidate() error = %v, want incompatible", err)
	}
}

func TestRegistryCloseBoundsHeldCandidateLeaseDrain(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "candidate_sales_1", "sales", "candidate-1", 21)
	factory := &recordingRegistryFactory{}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: factory, Now: func() time.Time { return now },
		CleanupDrainTimeout: 25 * time.Millisecond,
	})
	registration := CandidateRegistration{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales", ExpiresAt: now.Add(time.Hour),
		Compatibility: candidateCompatibility("one"),
	}
	registerCandidateRuntime(t, registry, registration, "candidate_sales_1")
	lease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: "cand_1", OwnerID: "author_1", WorkspaceID: "sales", Compatibility: registration.Compatibility,
	})
	require.NoError(t, err)

	err = registry.Close()
	require.ErrorContains(t, err, "candidate runtime cleanup did not drain")
	lease.Release()
	require.Eventually(t, func() bool {
		return len(factory.runtimes) == 1 && factory.runtimes[0].closed.Load()
	}, time.Second, 10*time.Millisecond)
}

func candidateTestRegistry(t *testing.T, now func() time.Time) *Registry {
	t.Helper()
	repo := newFakeRegistryRepo()
	addCandidateServingState(repo, "candidate_sales_1", "sales", "candidate-1", 21)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, Environment: "prod", Factory: &recordingRegistryFactory{},
		Now: now,
	})
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func addCandidateServingState(
	repo *fakeRegistryRepo,
	id servingstate.ID,
	workspace servingstate.WorkspaceID,
	digest string,
	snapshotID int64,
) {
	repo.deployments[id] = servingstate.State{
		ID: id, WorkspaceID: workspace, Environment: "prod",
		Status: servingstate.StatusValidated, DuckLakeSnapshotID: snapshotID,
	}
	repo.artifacts[id] = servingstate.Artifact{
		ServingStateID: id, WorkspaceID: workspace, Environment: "prod", Digest: digest,
	}
}

func registerCandidateRuntime(
	t *testing.T,
	registry *Registry,
	registration CandidateRegistration,
	servingStateID string,
) {
	t.Helper()
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: registration, ServingStateID: servingStateID,
	}); err != nil {
		t.Fatal(err)
	}
}

func candidateCompatibility(suffix string) CandidateCompatibility {
	return CandidateCompatibility{
		ArtifactDigest:           "artifact-" + suffix,
		DataRevision:             "data-" + suffix,
		DataMode:                 CandidateDataReuseSnapshot,
		RuntimeVersion:           "runtime-v1",
		AuthorizationFingerprint: "policy-" + suffix,
	}
}

func TestCandidateSnapshotReuseAllowsRetainedBindingEvidence(t *testing.T) {
	err := validateCandidateDataMode(servingstate.State{DuckLakeSnapshotID: 42}, CandidateCompatibility{
		DataMode: CandidateDataReuseSnapshot,
		Bindings: []CandidateBindingVersion{{
			BindingID: "binding_warehouse", LogicalConnection: "warehouse",
			ConnectorKind: "postgres", Revision: 7, ProviderVersion: "provider:v3",
			EndpointConfigHash: "sha256:" + strings.Repeat("a", 64),
		}},
	}, ManagedDataResolution{})
	require.NoError(t, err)
}

type candidateTestLifetime struct {
	closes atomic.Int32
}

func (lifetime *candidateTestLifetime) Close() error {
	lifetime.closes.Add(1)
	return nil
}
