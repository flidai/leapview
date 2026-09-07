package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestBootstrapProjectClaimRejectsUnauthenticatedWithoutSideEffects(t *testing.T) {
	fake := &projectClaimBootstrapFake{}
	m := projectClaimBootstrapTestModule(fake, "")
	response := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")

	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	require.Zero(t, fake.calls)
	require.Empty(t, fake.claim.ProjectID)
	require.Empty(t, fake.audits)
}

func TestBootstrapProjectClaimRepeatingTupleIsIdempotent(t *testing.T) {
	fake := &projectClaimBootstrapFake{}
	m := projectClaimBootstrapTestModule(fake, "instance-admin")
	first := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")
	second := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, first.Body.String(), second.Body.String())
	require.Equal(t, 2, fake.calls)
	require.Len(t, fake.audits, 1)
	require.Equal(t, "project:one", fake.claim.ProjectID.String())
	require.Equal(t, "instance-admin", fake.claim.ClaimedBy)
}

func TestBootstrapProjectClaimConflictAuditsAndPreservesState(t *testing.T) {
	fake := &projectClaimBootstrapFake{claim: deployment.ProjectClaim{
		ProjectID: "project:one", Environment: "prod", ClaimedBy: "original-admin", ClaimedAt: time.Unix(10, 0).UTC(),
	}}
	m := projectClaimBootstrapTestModule(fake, "instance-admin")
	response := callProjectClaimBootstrap(t, m, "project:two", "issuer:two", "prod", "key-2")

	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Equal(t, "project:one", fake.claim.ProjectID.String())
	require.Equal(t, "original-admin", fake.claim.ClaimedBy)
	require.Len(t, fake.audits, 1)
	require.Equal(t, "failure", fake.audits[0].Outcome)
}

func TestBootstrapProjectClaimConflictingEnvironmentIsAuditedWithoutMutation(t *testing.T) {
	fake := &projectClaimBootstrapFake{}
	m := projectClaimBootstrapTestModule(fake, "instance-admin")
	response := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "dev", "key-1")

	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Equal(t, 1, fake.calls)
	require.Len(t, fake.audits, 1)
	require.Equal(t, "failure", fake.audits[0].Outcome)
	require.Empty(t, fake.claim.ProjectID)
}

func TestBootstrapProjectClaimAuditFailureRollsBackClaim(t *testing.T) {
	fake := &projectClaimBootstrapFake{failAudit: true}
	m := projectClaimBootstrapTestModule(fake, "instance-admin")
	response := callProjectClaimBootstrap(t, m, "project:one", "issuer:one", "prod", "key-1")

	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Equal(t, 1, fake.calls)
	require.Empty(t, fake.claim.ProjectID)
	require.Empty(t, fake.audits)
}

func projectClaimBootstrapTestModule(fake *projectClaimBootstrapFake, principalID string) *Module {
	return &Module{
		instanceEnvironment: servingstate.Environment("prod"),
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
			return deploymenthttp.Principal{ID: principalID}, principalID != ""
		}}),
		projectClaimBootstrap: fake.execute,
	}
}

func callProjectClaimBootstrap(t *testing.T, module *Module, projectUID, issuerID, environment, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/instance/project-claim", strings.NewReader(`{"projectUid":"`+projectUID+`","issuerId":"`+issuerID+`","environment":"`+environment+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	module.BootstrapProjectClaim(response, request, idempotencyKey)
	return response
}

type projectClaimBootstrapFake struct {
	claim     deployment.ProjectClaim
	audits    []ProjectClaimAuditInput
	requests  map[string]ProjectClaimBootstrapResult
	calls     int
	failAudit bool
}

func (fake *projectClaimBootstrapFake) execute(_ context.Context, input ProjectClaimBootstrapInput) (ProjectClaimBootstrapResult, error) {
	fake.calls++
	if fake.requests == nil {
		fake.requests = map[string]ProjectClaimBootstrapResult{}
	}
	if result, ok := fake.requests[input.IdempotencyKey+"/"+input.RequestDigest]; ok {
		return result, nil
	}
	prior := fake.claim
	conflict := input.Environment != "prod" || prior.ProjectID != "" && (prior.ProjectID.String() != input.ProjectUID || string(prior.Environment) != input.Environment)
	if !conflict && prior.ProjectID == "" {
		projectID, _ := projectgraph.NewResourceID(input.ProjectUID)
		fake.claim = deployment.ProjectClaim{ProjectID: projectID, Environment: servingstate.Environment(input.Environment), ClaimedBy: input.PrincipalID, ClaimedAt: time.Unix(20, 0).UTC()}
	}
	audit := ProjectClaimAuditInput{AuditID: input.IdempotencyKey, ActorID: input.PrincipalID, ProjectUID: input.ProjectUID, Outcome: "success"}
	if conflict {
		audit.Outcome = "failure"
	}
	if fake.failAudit {
		fake.claim = prior
		return ProjectClaimBootstrapResult{}, errors.New("audit append failed")
	}
	fake.audits = append(fake.audits, audit)
	result := ProjectClaimBootstrapResult{Claim: fake.claim, Conflict: conflict, AuditInput: audit}
	fake.requests[input.IdempotencyKey+"/"+input.RequestDigest] = result
	return result, nil
}
