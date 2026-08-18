package module

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestPublishEvidenceAcceptsExactTargetRelease(t *testing.T) {
	targetRelease := publishTestRelease(t)

	evidence, err := publishEvidence(
		targetRelease,
		"lvinst_prod",
		"prod",
	)
	require.NoError(t, err)
	if evidence.ArtifactContentDigest != targetRelease.Provenance.Artifact.ContentDigest ||
		evidence.PlanDigest != targetRelease.Provenance.PlanDigest ||
		evidence.ReleaseDigest != targetRelease.Provenance.Digest ||
		evidence.CandidateID != "candidate_1" ||
		evidence.CandidateRevision != 4 ||
		evidence.TargetID != "lvinst_prod" {
		t.Fatalf("publish evidence = %#v", evidence)
	}
	response := publishEvidenceResponse(targetRelease)
	if response.ArtifactContentDigest != targetRelease.ArtifactDigest ||
		response.GenerationID != targetRelease.ServingIdentity.GenerationID ||
		response.SourceRevision == nil ||
		response.SourceRevision.Revision != "commit-a" {
		t.Fatalf("redacted publish evidence response = %#v", response)
	}
}

func TestPublishEvidenceRejectsCrossTargetAndIncompleteRelease(t *testing.T) {
	tests := map[string]func(*release.Release){
		"missing provenance": func(value *release.Release) {
			value.Provenance = nil
		},
		"cross target": func(value *release.Release) {
			value.Provenance.Plan.TargetID = "lvinst_other"
		},
		"environment drift": func(value *release.Release) {
			value.Provenance.Plan.Identity.Environment = "staging"
		},
		"generation drift": func(value *release.Release) {
			value.ServingIdentity.GenerationID = "generation_other"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			targetRelease := publishTestRelease(t)
			mutate(&targetRelease)
			_, err := publishEvidence(targetRelease, "lvinst_prod", "prod")
			if !errors.Is(err, deployment.ErrConflict) {
				t.Fatalf("error = %v, want deployment conflict", err)
			}
		})
	}
}

func TestDeploymentResponseExposesOnlyBoundedActivationEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("f", 64)
	response := deploymentResponse(apiadapter.Deployment{
		ID: "deployment_1", Project: "project", Environment: "prod",
		Status: apiadapter.StatusActive, CreatedBy: "publisher",
		ActivationPrincipal: "activator",
		VerificationDigest:  digest,
		VerifiedAt:          "2026-07-30T09:00:00Z",
	}, publishTestRelease(t))
	if response.ActivationPrincipal == nil ||
		*response.ActivationPrincipal != "activator" ||
		response.Verification == nil ||
		response.Verification.Digest != digest ||
		response.Verification.VerifiedAt != "2026-07-30T09:00:00Z" {
		t.Fatalf("activation response = %#v", response)
	}
}

func TestRetryCreatesOneNewRequestForTheSameImmutableRelease(t *testing.T) {
	targetRelease := publishTestRelease(t)
	coordinator := &publishCoordinatorStub{
		rows: map[string]apiadapter.Deployment{
			"deployment_failed": {
				ID: "deployment_failed", Project: "project", Environment: "prod",
				Status: apiadapter.StatusFailed,
			},
		},
	}
	releases := &publishReleaseStub{
		targetRelease: targetRelease,
		deployments:   map[string]string{"deployment_failed": targetRelease.ID},
	}
	module := &Module{
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			Coordinator: coordinator, InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "publisher"}, true
			},
		}),
		instanceID: "lvinst_prod",
		jobs:       JobConfig{Coordinator: coordinator},
		api:        APIConfig{Releases: releases},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/project/deployments/deployment_failed/retry",
		nil,
	)

	module.RetryDeployment(
		recorder,
		request,
		"project",
		"deployment_failed",
		"retry-1",
	)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if coordinator.created.ReleaseID != targetRelease.ID ||
		coordinator.created.IdempotencyKey != "retry-1" ||
		coordinator.created.Evidence.PlanDigest != targetRelease.Provenance.PlanDigest ||
		coordinator.created.Evidence.ReleaseDigest != targetRelease.Provenance.Digest {
		t.Fatalf("retry request = %#v", coordinator.created)
	}
}

func TestRollbackCreatesFreshPlanFromTheRetainedPriorRelease(t *testing.T) {
	targetRelease := publishTestRelease(t)
	coordinator := &publishCoordinatorStub{}
	releases := &publishReleaseStub{
		targetRelease:  targetRelease,
		priorReleaseID: targetRelease.ID,
	}
	module := &Module{
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			Coordinator: coordinator, InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, true
			},
		}),
		instanceID: "lvinst_prod",
		jobs:       JobConfig{Coordinator: coordinator},
		api:        APIConfig{Releases: releases},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/project/deployments/deployment_active/rollback",
		nil,
	)

	module.RollbackDeployment(
		recorder,
		request,
		"project",
		"deployment_active",
		"rollback-1",
	)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if coordinator.created.ReleaseID != targetRelease.ID ||
		coordinator.created.RollbackOf != "deployment_active" ||
		coordinator.created.IdempotencyKey != "rollback-1" ||
		coordinator.created.Evidence.PlanDigest != targetRelease.Provenance.PlanDigest ||
		coordinator.created.GenerationID != targetRelease.ServingIdentity.GenerationID {
		t.Fatalf("rollback request = %#v", coordinator.created)
	}
}

func TestPublishProjectCandidatePromotesAndRequestsTheExactReadyCandidate(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	lifecycle := &candidateRuntimeLifecycleRecorder{}
	module.candidateRuntimeLifecycle = lifecycle
	module.instanceID = "lvinst_prod"
	artifactDigest := "sha256:" + strings.Repeat("8", 64)
	started, err := module.candidates.Start(
		t.Context(),
		deployment.StartCandidateRequest{
			ProjectID: "project", OwnerID: "principal_1",
			ArtifactDigest: artifactDigest,
		},
	)
	require.NoError(t, err)
	targetRelease := publishTestRelease(t)
	input := release.ProvenanceInput{
		Artifact: targetRelease.Provenance.Artifact,
		Candidate: release.CandidateProvenance{
			ID: started.Candidate.ID, Revision: started.Candidate.Revision + 1,
			OwnerID: "principal_1",
		},
		Plan: targetRelease.Provenance.Plan,
	}
	gateEvidence, err := (&release.GateEvidence{Version: 1, CandidateID: input.Candidate.ID, SourceDigest: input.Artifact.SourceDigest, BindingGeneration: release.BindingFingerprint(input.Plan.Bindings), RuntimeVersion: input.Plan.RuntimeVersion, DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	require.NoError(t, err)
	input.Plan.GateEvidence = &gateEvidence
	provenance, err := release.NewProvenance(input)
	require.NoError(t, err)
	targetRelease.Provenance = &provenance
	ready, err := module.candidates.MarkReady(
		t.Context(),
		deployment.CandidateAccessScope{
			ProjectID: "project", CandidateID: started.Candidate.ID,
			OwnerID: "principal_1",
		},
		artifactDigest,
		provenance.Digest,
	)
	require.NoError(t, err)
	coordinator := &publishCoordinatorStub{}
	releases := &publishReleaseStub{targetRelease: targetRelease}
	module.jobs = JobConfig{Coordinator: coordinator}
	module.api = APIConfig{Releases: releases}
	body := fmt.Sprintf(
		`{"expectedRevision":%d,"provenanceDigest":%q,"targetId":%q}`,
		ready.Revision,
		ready.ProvenanceDigest,
		ready.TargetID,
	)
	response := callCandidateAPI(
		t,
		http.MethodPost,
		"/api/v1/projects/project/candidates/"+ready.ID+"/publish",
		body,
		func(w http.ResponseWriter, r *http.Request) {
			module.PublishProjectCandidate(
				w,
				r,
				"project",
				ready.ID,
				"publish-1",
			)
		},
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if releases.published.CandidateID != ready.ID ||
		releases.published.CandidateRevision != ready.Revision ||
		releases.published.ProvenanceDigest != ready.ProvenanceDigest ||
		releases.published.TargetID != ready.TargetID ||
		releases.published.IdempotencyKey != "publish-1" {
		t.Fatalf("candidate publication = %#v", releases.published)
	}
	if coordinator.created.ReleaseID != targetRelease.ID ||
		coordinator.created.Evidence.CandidateID != ready.ID ||
		coordinator.created.Evidence.CandidateRevision != ready.Revision {
		t.Fatalf("deployment request = %#v", coordinator.created)
	}
	workflow, err := coordinator.created.Workflow("deployment_1")
	require.NoError(t, err)
	if workflow.Job.PrincipalID != "principal_1" {
		t.Fatalf("activation job principal = %q, want principal_1", workflow.Job.PrincipalID)
	}
	if len(lifecycle.retired) != 1 || lifecycle.retired[0] != ready.ID {
		t.Fatalf("retired candidate runtimes = %#v, want %q", lifecycle.retired, ready.ID)
	}
}

func TestPublishProjectCandidateRejectsStaleClientRevision(t *testing.T) {
	module := testCandidateModule(t, "principal_1")
	module.instanceID = "lvinst_prod"
	digest := "sha256:" + strings.Repeat("8", 64)
	started, err := module.candidates.Start(
		t.Context(),
		deployment.StartCandidateRequest{
			ProjectID: "project", OwnerID: "principal_1",
			ArtifactDigest: digest,
		},
	)
	require.NoError(t, err)
	ready, err := module.candidates.MarkReady(
		t.Context(),
		deployment.CandidateAccessScope{
			ProjectID: "project", CandidateID: started.Candidate.ID,
			OwnerID: "principal_1",
		},
		digest,
		"sha256:"+strings.Repeat("9", 64),
	)
	require.NoError(t, err)
	releases := &publishReleaseStub{targetRelease: publishTestRelease(t)}
	module.api = APIConfig{Releases: releases}
	response := callCandidateAPI(
		t,
		http.MethodPost,
		"/api/v1/projects/project/candidates/"+ready.ID+"/publish",
		fmt.Sprintf(
			`{"expectedRevision":%d,"provenanceDigest":%q,"targetId":%q}`,
			ready.Revision-1,
			ready.ProvenanceDigest,
			ready.TargetID,
		),
		func(w http.ResponseWriter, r *http.Request) {
			module.PublishProjectCandidate(
				w,
				r,
				"project",
				ready.ID,
				"publish-stale",
			)
		},
	)
	if response.Code != http.StatusConflict || releases.published.CandidateID != "" {
		t.Fatalf("stale publication = %d %s %#v", response.Code, response.Body.String(), releases.published)
	}
}

func publishTestRelease(t *testing.T) release.Release {
	t.Helper()
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	projectDigest := "sha256:" + strings.Repeat("b", 64)
	policyDigest := "sha256:" + strings.Repeat("c", 64)
	identity, err := projectgraph.NewServingIdentity("project", "prod", "generation_4")
	require.NoError(t, err)
	baseIdentity, err := projectgraph.NewServingIdentity("project", "prod", "generation_3")
	require.NoError(t, err)
	input := release.ProvenanceInput{
		Artifact: release.ProjectArtifactProvenance{
			SourceDigest: "sha256:" + strings.Repeat("d", 64), ProjectDigest: projectDigest,
			ContentDigest: artifactDigest, CompilerVersion: "test", SchemaVersion: 1,
		},
		Candidate: release.CandidateProvenance{
			ID: "candidate_1", Revision: 4, OwnerID: "author_1",
		},
		SourceRevision: &release.SourceRevisionProvenance{
			Revision:   "commit-a",
			Repository: "https://code.example/acme/analytics",
			Ref:        "refs/heads/main", ChangeID: "github:branch/main",
		},
		Plan: release.GenerationPlanProvenance{
			Identity: identity, BaseIdentity: &baseIdentity, TargetID: "lvinst_prod", RuntimeVersion: "test",
			PolicyDigest: policyDigest, DataRevision: "snapshot_4", DataMode: release.GenerationDataRefreshSources,
			ManagedDataPins: []release.ManagedDataPin{{ConnectionID: "orders", RevisionID: "revision_4"}},
			Bindings:        []release.BindingEvidence{{BindingID: "warehouse", ConnectionID: "warehouse", ConnectorKind: "postgres", Revision: 7, ValidatedVersion: "version_7", EndpointConfigHash: "sha256:" + strings.Repeat("9", 64)}},
		},
	}
	gateEvidence, err := (&release.GateEvidence{Version: 1, CandidateID: input.Candidate.ID, SourceDigest: input.Artifact.SourceDigest, BindingGeneration: release.BindingFingerprint(input.Plan.Bindings), RuntimeVersion: input.Plan.RuntimeVersion, DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	require.NoError(t, err)
	input.Plan.GateEvidence = &gateEvidence
	provenance, err := release.NewProvenance(input)
	require.NoError(t, err)
	return release.Release{
		ID: "release_1", ServingIdentity: identity, ProjectDigest: provenance.Artifact.ProjectDigest,
		ArtifactDigest: artifactDigest, ActualDigest: artifactDigest, Status: release.StatusReady, Provenance: &provenance,
	}
}

type publishCoordinatorStub struct {
	rows    map[string]apiadapter.Deployment
	created apiadapter.CreateRequest
}

func (stub *publishCoordinatorStub) Create(
	_ context.Context,
	request apiadapter.CreateRequest,
) (apiadapter.Deployment, error) {
	stub.created = request
	return apiadapter.Deployment{
		ID: "deployment_retry", Project: request.Project,
		Environment: request.Environment, RequestDigest: request.Evidence.PlanDigest,
		Status: apiadapter.StatusPending,
	}, nil
}

func (stub *publishCoordinatorStub) Get(
	_ context.Context,
	scope apiadapter.Scope,
) (apiadapter.Deployment, error) {
	row, ok := stub.rows[scope.DeploymentID]
	if !ok {
		return apiadapter.Deployment{}, deployment.ErrNotFound
	}
	return row, nil
}

func (*publishCoordinatorStub) Activate(
	context.Context,
	apiadapter.ActivateRequest,
) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, nil
}

func (*publishCoordinatorStub) Cancel(
	context.Context,
	apiadapter.Scope,
) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, nil
}

type publishReleaseStub struct {
	targetRelease  release.Release
	deployments    map[string]string
	published      release.PublishCandidateInput
	priorReleaseID string
}

func (stub *publishReleaseStub) Get(
	_ context.Context,
	projectID projectgraph.ResourceID,
	releaseID string,
) (release.Release, error) {
	if projectID != stub.targetRelease.ServingIdentity.ProjectID ||
		releaseID != stub.targetRelease.ID {
		return release.Release{}, release.ErrNotFound
	}
	return stub.targetRelease, nil
}

func (stub *publishReleaseStub) PublishCandidate(
	_ context.Context,
	input release.PublishCandidateInput,
) (release.Release, error) {
	stub.published = input
	return stub.targetRelease, nil
}

func (*publishReleaseStub) LinkDeployment(
	context.Context,
	string,
	string,
	string,
	string,
) error {
	return nil
}

func (*publishReleaseStub) LinkDeploymentTx(
	context.Context,
	transaction.Transaction,
	string,
	string,
	string,
	string,
) error {
	return nil
}

func (stub *publishReleaseStub) DeploymentRelease(
	_ context.Context,
	projectID,
	deploymentID string,
) (string, string, error) {
	if projectID != stub.targetRelease.ServingIdentity.ProjectID.String() {
		return "", "", release.ErrNotFound
	}
	releaseID, ok := stub.deployments[deploymentID]
	if !ok {
		return "", "", release.ErrNotFound
	}
	return releaseID, "", nil
}

func (*publishReleaseStub) ListDeploymentIDs(
	context.Context,
	string,
) ([]string, error) {
	return nil, nil
}

func (stub *publishReleaseStub) PriorDeploymentRelease(
	_ context.Context,
	projectID,
	_ string,
) (string, error) {
	if projectID != stub.targetRelease.ServingIdentity.ProjectID.String() ||
		stub.priorReleaseID == "" {
		return "", release.ErrNotFound
	}
	return stub.priorReleaseID, nil
}
