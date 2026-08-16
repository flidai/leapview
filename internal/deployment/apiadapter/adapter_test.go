package apiadapter

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestCreateProducesStableIDAndRequestDigestForIdempotentReplay(t *testing.T) {
	service := &fakeService{}
	adapter, err := New(service)
	require.NoError(t, err)
	request := CreateRequest{Project: "project", Environment: "prod", GenerationID: "generation_2", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Actor: "principal", IdempotencyKey: "deploy-1"}
	first, err := adapter.Create(t.Context(), request)
	require.NoError(t, err)
	second, err := adapter.Create(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, first.RequestDigest, second.RequestDigest)
	require.Equal(t, StatusPending, first.Status)
	require.Equal(t, "project", service.created.ServingIdentity.ProjectID.String())
}

func TestCreateRequestDigestBindsImmutablePublishEvidence(t *testing.T) {
	request := CreateRequest{Project: "project", Environment: "prod", GenerationID: "generation_2", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Actor: "principal", IdempotencyKey: "publish-1", ReleaseID: "release_1", Evidence: PublishEvidence{ReleaseDigest: "sha256:" + strings.Repeat("e", 64), ArtifactContentDigest: "sha256:" + strings.Repeat("a", 64), ArtifactProvenanceDigest: "sha256:" + strings.Repeat("f", 64), PlanDigest: "sha256:" + strings.Repeat("b", 64), CandidateID: "candidate_1", CandidateRevision: 7, TargetID: "target_prod", Environment: "prod", GenerationID: "generation_2", RuntimeVersion: "v1.2.3", PolicyDigest: "sha256:" + strings.Repeat("c", 64)}}
	firstService, secondService := &fakeService{}, &fakeService{}
	first, err := New(firstService)
	require.NoError(t, err)
	second, err := New(secondService)
	require.NoError(t, err)
	require.NoError(t, func() error { _, err := first.Create(t.Context(), request); return err }())
	request.Evidence.PlanDigest = "sha256:" + strings.Repeat("d", 64)
	require.NoError(t, func() error { _, err := second.Create(t.Context(), request); return err }())
	require.NotEqual(t, firstService.created.RequestDigest, secondService.created.RequestDigest)
}

func TestCreateRejectsIncompleteImmutablePublishEvidence(t *testing.T) {
	adapter, err := New(&fakeService{})
	require.NoError(t, err)
	_, err = adapter.Create(t.Context(), CreateRequest{Project: "project", Environment: "prod", GenerationID: "generation_2", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Actor: "principal", IdempotencyKey: "publish-1", ReleaseID: "release_1", Evidence: PublishEvidence{TargetID: "target_prod"}})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestCreateRejectsMissingGenerationAsInvalidRequest(t *testing.T) {
	adapter, err := New(&fakeService{})
	require.NoError(t, err)
	_, err = adapter.Create(t.Context(), CreateRequest{Project: "project", Environment: "prod", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Actor: "principal", IdempotencyKey: "deploy-1"})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestMapResponseExposesGenerationIdentityAndActivationEvidence(t *testing.T) {
	service := &fakeService{row: deployment.Deployment{ID: "deployment_1", ServingIdentity: servingIdentity("project", "generation_2"), RequestDigest: "sha256:" + strings.Repeat("a", 64), Status: deployment.StatusActive, CreatedBy: "publisher", ActivationPrincipal: "activator", VerificationDigest: "sha256:" + strings.Repeat("f", 64), VerifiedAt: "2026-07-30T09:00:00Z"}}
	adapter, err := New(service)
	require.NoError(t, err)
	got, err := adapter.Get(t.Context(), Scope{Project: "project", DeploymentID: "deployment_1"})
	require.NoError(t, err)
	require.Equal(t, "project", got.Project)
	require.Equal(t, "generation_2", got.GenerationID)
	require.Equal(t, StatusActive, got.Status)
	require.Equal(t, "activator", got.ActivationPrincipal)
	require.Equal(t, service.row.VerificationDigest, got.VerificationDigest)
}

func TestActivateForwardsTheBoundedActivationPrincipal(t *testing.T) {
	service := &fakeService{row: deployment.Deployment{ID: "deployment_1", ServingIdentity: servingIdentity("project", "generation_2"), Status: deployment.StatusActive}}
	adapter, err := New(service)
	require.NoError(t, err)
	_, err = adapter.Activate(t.Context(), ActivateRequest{Scope: Scope{Project: "project", DeploymentID: "deployment_1"}, Actor: "principal_activator", IdempotencyKey: "activation-1"})
	require.NoError(t, err)
	require.Equal(t, "project", service.activation.ProjectID.String())
	require.Equal(t, "deployment_1", service.activation.DeploymentID)
	require.Equal(t, "principal_activator", service.activation.ActorID)
}

func servingIdentity(project, generation string) (identity projectgraph.ServingIdentity) {
	return projectgraph.ServingIdentity{ProjectID: project, Environment: "prod", GenerationID: generation}
}

type fakeService struct {
	row        deployment.Deployment
	created    deployment.CreateInput
	activation deployment.ActivationRequest
}

func (s *fakeService) Create(_ context.Context, input deployment.CreateInput) (deployment.Deployment, error) {
	s.created = input
	if s.row.ID == "" {
		s.row = deployment.Deployment{ID: input.ID, ServingIdentity: input.ServingIdentity, ArtifactDigest: input.ArtifactDigest, RequestDigest: input.RequestDigest, Status: deployment.StatusPending, CreatedBy: input.CreatedBy}
	}
	return s.row, nil
}
func (s *fakeService) Get(context.Context, deployment.Scope) (deployment.Deployment, error) {
	if s.row.ID == "" {
		return deployment.Deployment{}, deployment.ErrNotFound
	}
	return s.row, nil
}
func (s *fakeService) Activate(_ context.Context, request deployment.ActivationRequest) (deployment.Deployment, error) {
	s.activation = request
	return s.row, nil
}
func (s *fakeService) Cancel(context.Context, deployment.Scope) (deployment.Deployment, error) {
	s.row.Status = deployment.StatusCancelled
	return s.row, nil
}
