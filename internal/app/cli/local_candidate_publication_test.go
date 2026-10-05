package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	"github.com/stretchr/testify/require"
)

const (
	localTestCandidateID   = "0198f2c0-7c7a-7f00-8a11-000000000103"
	localTestPublicationID = "0198f2c0-7c7a-7f00-8a11-000000000105"
	localTestGenerationID  = "0198f2c0-7c7a-7f00-8a11-000000000106"
	localTestPlanID        = "0198f2c0-7c7a-7f00-8a11-000000000101"
	localTestPlanDigest    = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestLocalCandidatePublicationWaitsForExactActivatedGeneration(t *testing.T) {
	local := localPublicationTestSession(t)
	candidate := localPublicationTestCandidate()
	transport := &localPublicationTransport{project: candidate.ProjectID.String(), target: candidate.TargetID, environment: candidate.Environment}
	result, err := publishAndActivateLocalCandidate(t.Context(), deploymentgen.NewGenClient(transport), local, candidate)
	require.NoError(t, err)
	require.Equal(t, localTestGenerationID, result.GenerationID)
	require.Equal(t, 2, transport.publicationReads)
	require.Equal(t, 1, transport.operatorReads)
	require.NotEmpty(t, transport.publishKey)

	second := &localPublicationTransport{project: candidate.ProjectID.String(), target: candidate.TargetID, environment: candidate.Environment}
	_, err = publishAndActivateLocalCandidate(t.Context(), deploymentgen.NewGenClient(second), local, candidate)
	require.NoError(t, err)
	require.Equal(t, transport.publishKey, second.publishKey, "the exact candidate must replay the publication")
}

func TestLocalCandidatePublicationRejectsNonlocalTargetBeforeMutation(t *testing.T) {
	local := localPublicationTestSession(t)
	candidate := localPublicationTestCandidate()
	candidate.TargetID = "remote-target"
	transport := &localPublicationTransport{}
	_, err := publishAndActivateLocalCandidate(t.Context(), deploymentgen.NewGenClient(transport), local, candidate)
	require.ErrorContains(t, err, "local")
	require.Zero(t, transport.calls)
}

func TestLocalCandidatePublicationRejectsWrongActiveGeneration(t *testing.T) {
	local := localPublicationTestSession(t)
	candidate := localPublicationTestCandidate()
	transport := &localPublicationTransport{project: candidate.ProjectID.String(), target: candidate.TargetID, environment: candidate.Environment, wrongActive: true}
	_, err := publishAndActivateLocalCandidate(t.Context(), deploymentgen.NewGenClient(transport), local, candidate)
	require.ErrorContains(t, err, "active generation")
}

func TestLocalCandidatePublicationWaitsThroughBootstrapUnavailable(t *testing.T) {
	local := localPublicationTestSession(t)
	candidate := localPublicationTestCandidate()
	transport := &localPublicationTransport{project: candidate.ProjectID.String(), target: candidate.TargetID, environment: candidate.Environment, bootstrapUnavailable: true}
	result, err := publishAndActivateLocalCandidate(t.Context(), deploymentgen.NewGenClient(transport), local, candidate)
	require.NoError(t, err)
	require.Equal(t, localTestGenerationID, result.GenerationID)
	require.Equal(t, 3, transport.publicationReads)
}

func localPublicationTestSession(t *testing.T) localDevelopmentSession {
	t.Helper()
	var state localruntime.State
	require.NoError(t, json.Unmarshal([]byte(`{"authority":{"instanceId":"local-target","projectUid":"project:test","environment":"dev"},"network":{"url":"http://127.0.0.1:49916"}}`), &state))
	return localDevelopmentSession{state: state}
}

func localPublicationTestCandidate() projectdevloop.Candidate {
	return projectdevloop.Candidate{ID: localTestCandidateID, ProjectID: "project:test", TargetID: "local-target", Environment: "dev", PlanID: localTestPlanID, PlanDigest: localTestPlanDigest}
}

type localPublicationTransport struct {
	project, target, environment           string
	calls, publicationReads, operatorReads int
	publishKey                             string
	wrongActive                            bool
	bootstrapUnavailable                   bool
}

func (transport *localPublicationTransport) DoAPIGen(_ context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	transport.calls++
	status := http.StatusOK
	var body any
	switch request.OperationID {
	case deploymentgen.GenOperationPublishDeliveryCandidate:
		transport.publishKey = request.Headers.Get("Idempotency-Key")
		status = http.StatusAccepted
		body = deploymentgen.DeliveryPublicationEvidenceResponse{Id: localTestPublicationID, CandidateId: localTestCandidateID, GenerationId: localTestGenerationID, PlanId: localTestPlanID, PlanDigest: localTestPlanDigest, ProjectId: transport.project, TargetId: transport.target, Environment: transport.environment, Status: deploymentgen.DeliveryPublicationStatusPending}
	case deploymentgen.GenOperationGetDeliveryPublicationEvidence:
		transport.publicationReads++
		if transport.bootstrapUnavailable && transport.publicationReads == 1 {
			return apigenclient.Response{}, fmt.Errorf("GET local publication: %s", http.StatusText(http.StatusServiceUnavailable))
		}
		publicationStatus := deploymentgen.DeliveryPublicationStatusPending
		if transport.publicationReads > 1 && (!transport.bootstrapUnavailable || transport.publicationReads > 2) {
			publicationStatus = deploymentgen.DeliveryPublicationStatusCommitted
		}
		body = deploymentgen.DeliveryPublicationEvidenceResponse{Id: localTestPublicationID, CandidateId: localTestCandidateID, GenerationId: localTestGenerationID, PlanId: localTestPlanID, PlanDigest: localTestPlanDigest, ProjectId: transport.project, TargetId: transport.target, Environment: transport.environment, Status: publicationStatus, ResultTargetRevision: 2}
	case deploymentgen.GenOperationGetDeliveryOperatorSnapshot:
		transport.operatorReads++
		active := localTestGenerationID
		if transport.wrongActive {
			active = "other-generation"
		}
		body = deploymentgen.DeliveryOperatorSnapshotResponse{ProjectId: transport.project, TargetId: transport.target, Environment: transport.environment, ActiveGeneration: &active, TargetRevision: 2}
	default:
		return apigenclient.Response{}, fmt.Errorf("unexpected operation %s", request.OperationID)
	}
	encoded, err := json.Marshal(body)
	if err == nil {
		err = json.Unmarshal(encoded, out)
	}
	return apigenclient.Response{StatusCode: status, Headers: make(http.Header), ContentType: "application/json"}, err
}
