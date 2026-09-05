package module

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestCandidateSourcePlanUsesNativeClaimAuthority(t *testing.T) {
	module := nativeSourcePlanModule("principal_1")
	claims := &candidateProjectClaimRepositoryStub{}
	claimService, err := deployment.NewProjectClaimService(claims)
	require.NoError(t, err)
	module.projectClaims = claimService
	module.instanceEnvironment = "prod"
	var boundProject projectgraph.ResourceID
	module.bindClaimedProject = func(_ context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) error {
		boundProject = projectID
		require.Equal(t, servingstate.Environment("prod"), environment)
		return nil
	}
	module.candidateSourceAudit = func(context.Context, CandidateSourceAuditEvent) error { return nil }
	digest := "sha256:" + strings.Repeat("a", 64)
	module.candidateSources = &candidateSourceSynchronizerStub{}

	response := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/plan", `{"projectFile":"leapview.yaml","artifactDigest":"`+digest+`","artifacts":[]}`, func(w http.ResponseWriter, r *http.Request) {
		module.PlanProjectCandidateSynchronization(w, r, "finance", "plan-idem")
	})

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"planId":"plan-test"`)
	require.Equal(t, projectgraph.ResourceID("finance"), claims.input.ProjectID)
	require.Equal(t, "principal_1", claims.input.ClaimedBy)
	require.Equal(t, projectgraph.ResourceID("finance"), boundProject)
}

func TestNativeCandidateSourcePlanRequiresProjectClaimAuthority(t *testing.T) {
	module := nativeSourcePlanModule("principal_1")
	sources := &candidateSourceSynchronizerStub{}
	module.candidateSources = sources
	digest := "sha256:" + strings.Repeat("a", 64)

	response := callCandidateAPI(t, http.MethodPost, "/api/v1/projects/finance/candidate-sync/plan", `{"projectFile":"leapview.yaml","artifactDigest":"`+digest+`","artifacts":[]}`, func(w http.ResponseWriter, r *http.Request) {
		module.PlanProjectCandidateSynchronization(w, r, "finance", "plan-idem")
	})

	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "CANDIDATE_UNAVAILABLE")
	require.Zero(t, sources.plans)
}

func nativeSourcePlanModule(principalID string) *Module {
	return &Module{
		candidateSourceBlobAudit: func(context.Context, CandidateSourceAuditEvent) error { return nil },
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: principalID}, true
			},
			InstanceEnvironment: "prod",
		}),
	}
}

func callCandidateAPI(t *testing.T, method, target, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

type candidateSourceSynchronizerStub struct {
	missing []string
	plans   int
}

func (stub *candidateSourceSynchronizerStub) Plan(_ context.Context, _ deployment.CandidateSourceScope, request deployment.CandidateSynchronizationRequest) (project.CandidateSynchronizationPlan, error) {
	stub.plans++
	return project.CandidateSynchronizationPlan{PlanID: "plan-test", ArtifactDigest: request.ArtifactDigest, MissingDigests: append([]string(nil), stub.missing...)}, nil
}

func (*candidateSourceSynchronizerStub) Upload(_ context.Context, _ deployment.CandidateSourceScope, _, _ string, source io.Reader) error {
	_, err := io.Copy(io.Discard, source)
	return err
}

func (*candidateSourceSynchronizerStub) Commit(_ context.Context, _ deployment.CandidateSourceScope, request deployment.CandidateSynchronizationRequest) (project.CandidateSourceSnapshot, error) {
	return project.CandidateSourceSnapshot{ProjectID: "finance", ArtifactDigest: request.ArtifactDigest}, nil
}

type candidateProjectClaimRepositoryStub struct {
	input deployment.ProjectClaimInput
}

func (stub *candidateProjectClaimRepositoryStub) ClaimProject(_ context.Context, input deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	stub.input = input
	return deployment.ProjectClaim{ProjectID: input.ProjectID, Environment: input.Environment, ClaimedBy: input.ClaimedBy, ClaimedAt: input.ClaimedAt}, nil
}

func (*candidateProjectClaimRepositoryStub) GetProjectClaim(context.Context) (deployment.ProjectClaim, error) {
	return deployment.ProjectClaim{}, deployment.ErrProjectClaimNotFound
}
