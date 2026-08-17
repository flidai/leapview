package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func testDeployment(id, project, generation string, status Status) Deployment {
	return Deployment{ID: id, ServingIdentity: projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(project), Environment: "prod", GenerationID: generation}, ArtifactDigest: "sha256:" + strings.Repeat("b", 64), RequestDigest: "sha256:" + strings.Repeat("c", 64), Status: status}
}

func TestActivateResolvesPersistedBindingsPreparesAndAtomicallyCommits(t *testing.T) {
	repo := &fakeRepository{deployment: testDeployment("deployment_1", "project", "generation_2", StatusPending)}
	resolver := &fakeResolver{resolution: runtimehost.ManagedDataResolution{RevisionID: "sha256:" + strings.Repeat("a", 64), Roots: map[string]string{"orders": "/cache/orders"}}}
	runtime := &fakeRuntime{prepared: &fakePrepared{snapshot: 41}}
	states := &fakeServingStates{}
	service := mustService(t, repo, states, runtime, resolver)

	got, err := service.Activate(t.Context(), ActivationRequest{Scope: Scope{ProjectID: "project", DeploymentID: "deployment_1"}, ActorID: "principal_activator"})
	require.NoError(t, err)
	require.Equal(t, StatusActive, got.Status)
	require.Equal(t, []runtimehost.ServingStateCandidate{{Identity: repo.deployment.ServingIdentity, ManagedData: resolver.resolution}}, runtime.candidates)
	require.Equal(t, map[servingstate.ID]int64{"generation_2": 41}, states.recorded)
	require.Equal(t, 1, repo.activateCalls)
	require.True(t, runtime.committed)
	require.Equal(t, 1, runtime.verifyCalls)
	require.Equal(t, "principal_activator", repo.activationInput.ActivationPrincipal)
}

func TestActivateSupportsDeploymentWithoutManagedConnections(t *testing.T) {
	repo := &fakeRepository{deployment: testDeployment("deployment_empty", "project", "generation_2", StatusPending)}
	runtime := &fakeRuntime{prepared: &fakePrepared{}}
	service := mustService(t, repo, &fakeServingStates{}, runtime, &fakeResolver{resolution: runtimehost.ManagedDataResolution{Roots: map[string]string{}}})
	_, err := service.Activate(t.Context(), ActivationRequest{Scope: Scope{ProjectID: "project", DeploymentID: "deployment_empty"}, ActorID: "principal_activator"})
	require.NoError(t, err)
	require.Len(t, runtime.candidates, 1)
	require.Empty(t, runtime.candidates[0].ManagedData.Roots)
}

func TestActivateObserverFailureDoesNotRollBackActivation(t *testing.T) {
	repo := &fakeRepository{deployment: testDeployment("deployment_observer", "project", "generation_2", StatusPending)}
	runtime := &fakeRuntime{prepared: &fakePrepared{}}
	service := mustService(t, repo, &fakeServingStates{}, runtime, &fakeResolver{})
	observed := false
	service.SetAfterActivated(func(context.Context, Deployment) { observed = true })

	got, err := service.Activate(t.Context(), ActivationRequest{Scope: Scope{ProjectID: "project", DeploymentID: "deployment_observer"}, ActorID: "principal_activator"})
	require.NoError(t, err)
	require.Equal(t, StatusActive, got.Status)
	require.True(t, runtime.committed)
	require.True(t, observed)
}

func TestActivatePreparationFailureLeavesDeploymentPending(t *testing.T) {
	wantErr := errors.New("duckdb preparation failed")
	repo := &fakeRepository{deployment: testDeployment("deployment_1", "project", "generation_2", StatusPending)}
	runtime := &fakeRuntime{prepareErr: wantErr}
	service := mustService(t, repo, &fakeServingStates{}, runtime, &fakeResolver{})
	_, err := service.Activate(t.Context(), ActivationRequest{Scope: Scope{ProjectID: "project", DeploymentID: "deployment_1"}, ActorID: "principal_activator"})
	require.ErrorIs(t, err, wantErr)
	require.Zero(t, repo.activateCalls)
	require.Equal(t, "deployment_1", repo.failedID)
}

func TestActivateVerificationFailureLeavesPriorGenerationActive(t *testing.T) {
	wantErr := errors.New("representative query failed")
	repo := &fakeRepository{deployment: func() Deployment {
		d := testDeployment("deployment_1", "project", "generation_2", StatusPending)
		d.PriorGenerationID = "generation_1"
		return d
	}()}
	runtime := &fakeRuntime{prepared: &fakePrepared{}, verifyErr: wantErr}
	service := mustService(t, repo, &fakeServingStates{}, runtime, &fakeResolver{})
	_, err := service.Activate(t.Context(), ActivationRequest{Scope: Scope{ProjectID: "project", DeploymentID: "deployment_1"}, ActorID: "principal_activator"})
	require.ErrorIs(t, err, wantErr)
	require.Zero(t, repo.activateCalls)
	require.Equal(t, "deployment_1", repo.failedID)
}

func TestActivateIsIdempotentAfterSuccess(t *testing.T) {
	repo := &fakeRepository{deployment: testDeployment("deployment_1", "project", "generation_2", StatusActive)}
	runtime := &fakeRuntime{}
	service := mustService(t, repo, &fakeServingStates{}, runtime, &fakeResolver{})
	got, err := service.Activate(t.Context(), ActivationRequest{Scope: Scope{ProjectID: "project", DeploymentID: "deployment_1"}, ActorID: "principal_activator"})
	require.NoError(t, err)
	require.Equal(t, StatusActive, got.Status)
	require.Zero(t, runtime.prepareCalls)
}

func TestCancelIsIdempotentAfterCancellation(t *testing.T) {
	repo := &fakeRepository{deployment: testDeployment("deployment_1", "project", "generation_2", StatusCancelled)}
	service := mustService(t, repo, &fakeServingStates{}, &fakeRuntime{}, &fakeResolver{})
	got, err := service.Cancel(t.Context(), Scope{ProjectID: "project", DeploymentID: "deployment_1"})
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, got.Status)
	require.Zero(t, repo.cancelCalls)
}

func mustService(t *testing.T, repo Repository, states ServingStateRepository, runtime Runtime, resolver ManagedDataResolver) *Service {
	t.Helper()
	activation, ok := repo.(ActivationUnitOfWork)
	require.True(t, ok)
	service, err := New(repo, activation, states, runtime, resolver)
	require.NoError(t, err)
	return service
}

type fakeRepository struct {
	deployment                 Deployment
	activateCalls, cancelCalls int
	failedID                   string
	activationInput            ActivationInput
}

func (r *fakeRepository) CreateDeployment(context.Context, CreateInput) (Deployment, error) {
	return r.deployment, nil
}
func (r *fakeRepository) DeploymentByID(context.Context, string) (Deployment, error) {
	return r.deployment, nil
}
func (r *fakeRepository) ActivateDeployment(_ context.Context, input ActivationInput) (Deployment, error) {
	r.activateCalls++
	r.activationInput = input
	r.deployment.Status = StatusActive
	r.deployment.ActivationPrincipal = input.ActivationPrincipal
	r.deployment.VerificationDigest = input.VerificationDigest
	return r.deployment, nil
}
func (r *fakeRepository) CancelDeployment(context.Context, string) (Deployment, error) {
	r.cancelCalls++
	r.deployment.Status = StatusCancelled
	return r.deployment, nil
}
func (r *fakeRepository) FailDeployment(_ context.Context, id string, _ error) error {
	r.failedID = id
	return nil
}

type fakeResolver struct {
	resolution runtimehost.ManagedDataResolution
	err        error
}

func (r *fakeResolver) ResolveManagedDataForIdentity(context.Context, projectgraph.ServingIdentity) (runtimehost.ManagedDataResolution, error) {
	return r.resolution, r.err
}

type fakePrepared struct{ snapshot int64 }

func (p *fakePrepared) DuckLakeSnapshotID() int64 { return p.snapshot }
func (p *fakePrepared) Close() error              { return nil }

type fakeRuntime struct {
	prepared                  Prepared
	prepareErr                error
	prepareCalls, verifyCalls int
	candidates                []runtimehost.ServingStateCandidate
	committed                 bool
	verification              Verification
	verifyErr                 error
}

func (r *fakeRuntime) Prepare(_ context.Context, candidate runtimehost.ServingStateCandidate) (Prepared, error) {
	r.prepareCalls++
	r.candidates = append(r.candidates, candidate)
	return r.prepared, r.prepareErr
}
func (r *fakeRuntime) Verify(_ context.Context, _ Prepared) (Verification, error) {
	r.verifyCalls++
	if r.verification.Digest == "" {
		r.verification.Digest = "sha256:" + strings.Repeat("a", 64)
	}
	return r.verification, r.verifyErr
}
func (r *fakeRuntime) Activate(_ Prepared, activate func() error) error {
	if err := activate(); err != nil {
		return err
	}
	r.committed = true
	return nil
}

type fakeServingStates struct{ recorded map[servingstate.ID]int64 }

func (s *fakeServingStates) RecordDuckLakeSnapshot(_ context.Context, id servingstate.ID, snapshotID int64) error {
	if s.recorded == nil {
		s.recorded = map[servingstate.ID]int64{}
	}
	s.recorded[id] = snapshotID
	return nil
}
