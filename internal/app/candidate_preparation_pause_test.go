package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/workload"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

const candidatePreparationPauseDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type candidatePreparationCleanupGate struct {
	release chan struct{}
	once    sync.Once
}

func newCandidatePreparationCleanupGate() *candidatePreparationCleanupGate {
	return &candidatePreparationCleanupGate{release: make(chan struct{})}
}

func (g *candidatePreparationCleanupGate) open() {
	if g != nil {
		g.once.Do(func() { close(g.release) })
	}
}

type candidatePreparationConnectionLease struct {
	evidence []deployment.CandidateConnectionEvidence
	gate     *candidatePreparationCleanupGate
	started  chan struct{}
	close    sync.Once
	closes   atomic.Int32
}

func (l *candidatePreparationConnectionLease) Evidence() []deployment.CandidateConnectionEvidence {
	return append([]deployment.CandidateConnectionEvidence(nil), l.evidence...)
}

func (l *candidatePreparationConnectionLease) Close() error {
	l.close.Do(func() {
		close(l.started)
		if l.gate != nil {
			<-l.gate.release
		}
		l.closes.Add(1)
	})
	return nil
}

type candidatePreparationConnectionLeaser struct {
	calls   atomic.Int32
	started chan struct{}
	lease   *candidatePreparationConnectionLease
}

func (l *candidatePreparationConnectionLeaser) Acquire(context.Context, deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
	if l.calls.Add(1) == 1 {
		close(l.started)
	}
	return l.lease, nil
}

type candidatePreparationRuntimeHost struct {
	calls           atomic.Int32
	started         chan struct{}
	mu              sync.Mutex
	retained        []runtimehost.RuntimeLifetime
	registrationErr error
}

func (h *candidatePreparationRuntimeHost) PrepareAndRegisterCandidateSet(_ context.Context, candidates []runtimehost.CandidatePreparation) error {
	h.calls.Add(1)
	close(h.started)
	if h.registrationErr != nil {
		for _, candidate := range candidates {
			if candidate.Lifetime != nil {
				_ = candidate.Lifetime.Close()
			}
		}
		return h.registrationErr
	}
	h.mu.Lock()
	for _, candidate := range candidates {
		if candidate.Lifetime != nil {
			h.retained = append(h.retained, candidate.Lifetime)
		}
	}
	h.mu.Unlock()
	return nil
}

func (h *candidatePreparationRuntimeHost) closeRetained() {
	h.mu.Lock()
	retained := append([]runtimehost.RuntimeLifetime(nil), h.retained...)
	h.retained = nil
	h.mu.Unlock()
	for _, lifetime := range retained {
		_ = lifetime.Close()
	}
}

type candidatePreparationServiceFixture struct {
	service     *deployment.CandidateRuntimeService
	request     deployment.CandidateRuntimeRequest
	connections *candidatePreparationConnectionLeaser
	host        *candidatePreparationRuntimeHost
}

func newCandidatePreparationServiceFixture(t *testing.T, cleanupGate *candidatePreparationCleanupGate, registrationErr error) candidatePreparationServiceFixture {
	t.Helper()
	now := time.Now().UTC()
	evidence := []deployment.CandidateConnectionEvidence{{
		BindingID: "binding_warehouse", ConnectionID: projectgraph.ResourceID("warehouse"), ConnectorKind: "postgres",
		Revision: 7, ProviderVersion: "provider:v3", EndpointConfigHash: "sha256:" + strings.Repeat("9", 64),
	}}
	bindingFingerprint, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		t.Fatal(err)
	}
	gateEvidence, err := (release.GateEvidence{
		Version: 1, CandidateID: "candidate_pause", SourceDigest: candidatePreparationPauseDigest,
		BindingGeneration: bindingFingerprint, RuntimeVersion: "leapview:test", DuckDBVersion: "duckdb:test",
		Outcome: release.GateSuccess, EvaluatedAt: now,
		Bounds: release.GateBounds{MaxRows: 1, MaxQueries: 1, MaxMillis: 1},
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_candidate_pause")
	if err != nil {
		t.Fatal(err)
	}
	connectionLease := &candidatePreparationConnectionLease{evidence: evidence, gate: cleanupGate, started: make(chan struct{})}
	connections := &candidatePreparationConnectionLeaser{started: make(chan struct{}), lease: connectionLease}
	host := &candidatePreparationRuntimeHost{started: make(chan struct{}), registrationErr: registrationErr}
	service, err := deployment.NewCandidateRuntimeService(deployment.CandidateRuntimeServiceConfig{
		Connections: connections, Runtime: host, RuntimeVersion: "leapview:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupGate != nil {
			cleanupGate.open()
		}
		host.closeRetained()
	})
	candidate := deployment.Candidate{
		ID: "candidate_pause", TargetID: "target_demo", OwnerID: "principal_demo",
		Scope:          deployment.CandidateScope{ProjectID: "project_demo", Environment: "prod", BaseGenerationID: "generation_base"},
		ArtifactDigest: candidatePreparationPauseDigest, Status: deployment.CandidatePreparing,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Revision: 1,
	}
	return candidatePreparationServiceFixture{
		service: service,
		request: deployment.CandidateRuntimeRequest{
			Candidate: candidate, AuthorizationFingerprint: "authorization:v1",
			Generation: deployment.CandidateGenerationRuntime{
				Identity: identity, ArtifactDigest: candidatePreparationPauseDigest, DataRevision: "sources:42",
				DataMode:           deployment.CandidateDataRefreshSources,
				Connections:        []deployment.CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}},
				BindingFingerprint: bindingFingerprint, GateEvidence: &gateEvidence,
			},
		},
		connections: connections, host: host,
	}
}

func runCandidatePreparationThroughAdmission(ctx context.Context, admitter deploymentmodule.CandidatePreparationAdmitter, service *deployment.CandidateRuntimeService, request deployment.CandidateRuntimeRequest) error {
	preparation, err := admitter.AcquireCandidatePreparation(ctx)
	if err != nil {
		return err
	}
	if preparation == nil {
		return deployment.ErrCandidateUnavailable
	}
	defer preparation.Release()
	if preparation.Context() != nil {
		ctx = preparation.Context()
	}
	_, err = service.Prepare(ctx, request)
	return err
}

func newCandidatePreparationWorkload(t *testing.T) *workload.Controller {
	t.Helper()
	controller, err := workload.New(workload.Config{MaxRunning: 2, Classes: map[workload.Class]workload.Policy{
		workload.Control: {MaximumRunning: 1}, workload.Refresh: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return controller
}

func waitCandidatePreparationSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitCandidatePreparationDrained(t *testing.T, pause interface{ WaitDrained(context.Context) error }, description string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := pause.WaitDrained(ctx); err != nil {
		t.Fatalf("%s: %v", description, err)
	}
}

func TestPausedCandidatePreparationBlocksBeforeWorkloadAndConnectionAcquisition(t *testing.T) {
	controller := newCandidatePreparationWorkload(t)
	fixture := newCandidatePreparationServiceFixture(t, nil, nil)
	admitter := candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))
	pause, err := admitter.Pause()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- runCandidatePreparationThroughAdmission(t.Context(), admitter, fixture.service, fixture.request)
	}()
	select {
	case err := <-result:
		t.Fatalf("candidate preparation returned while paused: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	stats := controller.Stats()
	if stats.Running != 0 || stats.Classes[workload.Control].Running != 0 {
		t.Fatalf("workload was charged before lifecycle admission resumed: %#v", stats)
	}
	if got := fixture.connections.calls.Load(); got != 0 {
		t.Fatalf("connection acquisition calls while paused = %d, want 0", got)
	}
	waitCandidatePreparationDrained(t, pause, "drain paused idle gate")
	if err := pause.Resume(); err != nil {
		t.Fatal(err)
	}
	waitCandidatePreparationSignal(t, fixture.connections.started, "connection acquisition after resume")
	waitCandidatePreparationSignal(t, fixture.host.started, "candidate runtime registration")
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("candidate preparation after resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate preparation did not complete after resume")
	}
	if got := controller.Stats().Running; got != 0 {
		t.Fatalf("workload leases after preparation = %d, want 0", got)
	}
	fixture.host.closeRetained()
}

func TestPausedCandidatePreparationDoesNotBypassInheritedRefreshGate(t *testing.T) {
	controller := newCandidatePreparationWorkload(t)
	outer, err := controller.Acquire(t.Context(), workload.Request{
		Class: workload.Refresh, PrincipalID: "refresh_actor", Operation: "materialization.refresh", EstimatedMemoryBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Release()
	fixture := newCandidatePreparationServiceFixture(t, nil, nil)
	admitter := candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))
	pause, err := admitter.Pause()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- runCandidatePreparationThroughAdmission(outer.Context(), admitter, fixture.service, fixture.request)
	}()
	select {
	case err := <-result:
		t.Fatalf("inherited refresh candidate preparation returned while paused: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	stats := controller.Stats()
	if stats.Running != 1 || stats.Classes[workload.Refresh].Running != 1 || stats.Classes[workload.Control].Running != 0 {
		t.Fatalf("paused inherited refresh admission = %#v, want only the outer refresh lease", stats)
	}
	if got := fixture.connections.calls.Load(); got != 0 {
		t.Fatalf("connection acquisition bypassed the preparation gate: %d calls", got)
	}
	waitCandidatePreparationDrained(t, pause, "drain paused idle candidate gate")
	if err := pause.Resume(); err != nil {
		t.Fatal(err)
	}
	waitCandidatePreparationSignal(t, fixture.connections.started, "inherited refresh connection acquisition")
	waitCandidatePreparationSignal(t, fixture.host.started, "inherited refresh runtime registration")
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("candidate preparation under outer refresh lease: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("inherited refresh candidate preparation did not finish")
	}
	stats = controller.Stats()
	if stats.Running != 1 || stats.Classes[workload.Refresh].Running != 1 || stats.Classes[workload.Control].Running != 0 {
		t.Fatalf("candidate preparation changed outer refresh admission: %#v", stats)
	}
	fixture.host.closeRetained()
}

func TestCanceledCandidatePreparationRemainsAdmittedThroughFailureCleanup(t *testing.T) {
	controller := newCandidatePreparationWorkload(t)
	cleanupGate := newCandidatePreparationCleanupGate()
	cleanupErr := errors.New("candidate registration failed")
	fixture := newCandidatePreparationServiceFixture(t, cleanupGate, cleanupErr)
	admitter := candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- runCandidatePreparationThroughAdmission(ctx, admitter, fixture.service, fixture.request)
	}()
	waitCandidatePreparationSignal(t, fixture.connections.started, "candidate connection acquisition")
	waitCandidatePreparationSignal(t, fixture.host.started, "candidate runtime registration")
	waitCandidatePreparationSignal(t, fixture.connections.lease.started, "failure cleanup of acquired connections")

	pause, err := admitter.Pause()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("candidate preparation context did not cancel")
	}
	drainCtx, drainCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	err = pause.WaitDrained(drainCtx)
	drainCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Pause.WaitDrained during blocked failure cleanup = %v, want deadline", err)
	}
	if got := fixture.connections.lease.closes.Load(); got != 0 {
		t.Fatalf("connection lifetime closed before cleanup gate release: %d", got)
	}
	select {
	case err := <-result:
		t.Fatalf("candidate preparation returned before cleanup completed: %v", err)
	default:
	}

	cleanupGate.open()
	select {
	case err := <-result:
		if !errors.Is(err, deployment.ErrCandidateUnavailable) {
			t.Fatalf("candidate preparation failure = %v, want unavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate preparation did not return after cleanup completed")
	}
	waitCandidatePreparationDrained(t, pause, "Pause.WaitDrained after real cleanup")
	if got := fixture.connections.lease.closes.Load(); got != 1 {
		t.Fatalf("connection lifetime Close() calls = %d, want 1", got)
	}
	stats := controller.Stats()
	if stats.Running != 0 {
		t.Fatalf("workload admission remained active after failure cleanup: %#v", stats)
	}
	if err := pause.Resume(); err != nil {
		t.Fatal(err)
	}
}

type candidatePreparationCountingAdmitter struct {
	lease *candidatePreparationCountingLease
	err   error
}

func (a *candidatePreparationCountingAdmitter) Acquire(context.Context, workload.Request) (workload.Lease, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.lease, nil
}

type candidatePreparationCountingLease struct {
	ctx      context.Context
	releases atomic.Int32
}

func (l *candidatePreparationCountingLease) Context() context.Context { return l.ctx }
func (*candidatePreparationCountingLease) QueueWait() time.Duration   { return 0 }
func (l *candidatePreparationCountingLease) Release()                 { l.releases.Add(1) }

func TestCandidatePreparationAdmissionReleasesGateOnWorkloadFailure(t *testing.T) {
	workloadErr := errors.New("workload controller unavailable")
	admitter := candidatePreparationAdmitter(&candidatePreparationCountingAdmitter{err: workloadErr}, workloadmodule.ControlRequest("candidate.prepare"))
	if _, err := admitter.AcquireCandidatePreparation(t.Context()); !errors.Is(err, workloadErr) {
		t.Fatalf("candidate admission failure = %v, want %v", err, workloadErr)
	}
	pause, err := admitter.Pause()
	if err != nil {
		t.Fatal(err)
	}
	waitCandidatePreparationDrained(t, pause, "gate remained active after workload admission failure")
	if err := pause.Resume(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidatePreparationAdmissionReleaseIsConcurrentAndIdempotent(t *testing.T) {
	workloadLease := &candidatePreparationCountingLease{ctx: t.Context()}
	admitter := candidatePreparationAdmitter(&candidatePreparationCountingAdmitter{lease: workloadLease}, workloadmodule.ControlRequest("candidate.prepare"))
	preparation, err := admitter.AcquireCandidatePreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const releasers = 12
	var wg sync.WaitGroup
	wg.Add(releasers)
	for i := 0; i < releasers; i++ {
		go func() {
			defer wg.Done()
			preparation.Release()
		}()
	}
	wg.Wait()
	if got := workloadLease.releases.Load(); got != 1 {
		t.Fatalf("underlying workload lease Release() calls = %d, want 1", got)
	}
	pause, err := admitter.Pause()
	if err != nil {
		t.Fatal(err)
	}
	waitCandidatePreparationDrained(t, pause, "sourcework gate did not drain after concurrent Release()")
	if err := pause.Resume(); err != nil {
		t.Fatal(err)
	}
}
