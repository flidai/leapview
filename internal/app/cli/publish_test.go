package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
)

func TestProjectPublishOperationsUseCanonicalDeliveryPublication(t *testing.T) {
	transport := &publishTransportStub{}
	var output strings.Builder
	operations := projectPublishOperations{client: fixedTransportClient{transport: transport}}
	checkpoint := projectcli.CandidateCheckpoint{
		ProjectPath: "/work/leapview.yaml", TargetOrigin: "https://target.example",
		TargetID: "target_1", Environment: "production", ProjectID: "finance",
		CandidateID: "cand_1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		PlanID:         "plan_1", PlanDigest: "sha256:" + strings.Repeat("b", 64),
	}

	if err := operations.Publish(t.Context(), projectcli.PublishOptions{
		ProjectPath: checkpoint.ProjectPath,
		Credentials: cliapi.Credentials{Target: checkpoint.TargetOrigin, Token: "token"},
		Checkpoint:  checkpoint,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if transport.request.OperationID != deploymentgen.GenOperationPublishDeliveryCandidate ||
		transport.request.PathParams["project"] != checkpoint.ProjectID ||
		transport.request.PathParams["candidate"] != checkpoint.CandidateID ||
		transport.request.Headers.Get("Idempotency-Key") == "" {
		t.Fatalf("request = %#v", transport.request)
	}
	if transport.request.Body != nil {
		t.Fatalf("canonical publication unexpectedly supplied a legacy request body: %#v", transport.request.Body)
	}
	if !strings.Contains(output.String(), "publication publication_1 candidate cand_1 generation generation_1 status pending") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestProjectPublishOperationsEmitVersionedAcceptedJSON(t *testing.T) {
	transport := &publishTransportStub{}
	var output strings.Builder
	operations := projectPublishOperations{client: fixedTransportClient{transport: transport}}
	checkpoint := projectcli.CandidateCheckpoint{
		TargetOrigin: "https://target.example", ProjectID: "finance", CandidateID: "cand_1",
		PlanID: "plan_1", PlanDigest: "sha256:" + strings.Repeat("b", 64),
	}
	if err := operations.Publish(t.Context(), projectcli.PublishOptions{
		Credentials: cliapi.Credentials{Target: checkpoint.TargetOrigin, Token: "token"},
		Checkpoint:  checkpoint, Format: "json",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result projectcli.PublishResult
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.PublicationID != "publication_1" ||
		result.CandidateID != "cand_1" || result.GenerationID != "generation_1" ||
		result.PlanID != "plan_1" || result.TargetRevision != 12 {
		t.Fatalf("result = %#v", result)
	}
}

type fixedTransportClient struct {
	transport apigenclient.Transport
}

func (client fixedTransportClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}

func (client fixedTransportClient) Environment(context.Context, cliapi.Credentials, string) (string, error) {
	return "", nil
}

func (client fixedTransportClient) Transport(context.Context, cliapi.Credentials) (apigenclient.Transport, error) {
	return client.transport, nil
}

type publishTransportStub struct {
	request apigenclient.Request
}

func (stub *publishTransportStub) DoAPIGen(
	_ context.Context,
	request apigenclient.Request,
	out any,
) (apigenclient.Response, error) {
	stub.request = request
	response := deploymentgen.DeliveryPublicationEvidenceResponse{
		Id: "publication_1", ProjectId: "finance", TargetId: "target_1", Environment: "production",
		PlanId: "plan_1", PlanDigest: "sha256:" + strings.Repeat("b", 64), CandidateId: "cand_1",
		GenerationId: "generation_1", Status: "pending", ResultTargetRevision: 12,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return apigenclient.Response{}, err
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return apigenclient.Response{}, err
	}
	return apigenclient.Response{StatusCode: http.StatusAccepted, Headers: http.Header{}, ContentType: "application/json"}, nil
}
