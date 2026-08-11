package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

type fakeClient struct {
	transport fakeTransport
}

type fakeTransport struct {
	requests []apigenclient.Request
	do       func(apigenclient.Request, any) error
}

func (client *fakeClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}

func (client *fakeClient) Environment(_ context.Context, _ cliapi.Credentials, asserted string) (string, error) {
	return asserted, nil
}

func (client *fakeClient) Transport(_ context.Context, _ cliapi.Credentials) (apigenclient.Transport, error) {
	return &client.transport, nil
}

func (transport *fakeTransport) DoAPIGen(_ context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	transport.requests = append(transport.requests, request)
	if err := transport.do(request, out); err != nil {
		return apigenclient.Response{}, err
	}
	return apigenclient.Response{StatusCode: 200}, nil
}

func TestCommandRunsAgentConversationWithoutApplicationProcess(t *testing.T) {
	client := &fakeClient{}
	client.transport.do = func(request apigenclient.Request, out any) error {
		switch request.OperationID {
		case agentgen.GenOperationCreateAgentConversation:
			return json.Unmarshal([]byte(`{"id":"conv_1","createdAt":"","principalId":"principal","status":"active","title":"","updatedAt":""}`), out)
		case agentgen.GenOperationCreateAgentRun:
			return json.Unmarshal([]byte(`{"id":"run_1","conversationId":"conv_1","createdAt":"","principalId":"principal","status":"completed","stopReason":"complete"}`), out)
		case agentgen.GenOperationListAgentMessages:
			return json.Unmarshal([]byte(`{"items":[{"id":"message_1","contentText":"Answer","createdAt":"","role":"assistant","runId":"run_1","seq":1}],"page":{}}`), out)
		default:
			t.Fatalf("unexpected operation %q", request.OperationID)
		}
		return nil
	}
	command := Command(context.Background(), Dependencies{Client: client})
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"ask", "Question", "--target", "https://example.test", "--token", "secret"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Answer") || !strings.Contains(got, "conversation=conv_1 run=run_1") {
		t.Fatalf("output = %q", got)
	}
	if len(client.transport.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(client.transport.requests))
	}
	if client.transport.requests[0].Headers.Get("Idempotency-Key") == "" || client.transport.requests[1].Headers.Get("Idempotency-Key") == "" {
		t.Fatal("mutating Agent requests must carry idempotency keys")
	}
}

func TestCommandOwnsConversationEnvelopePresentation(t *testing.T) {
	client := &fakeClient{}
	client.transport.do = func(request apigenclient.Request, out any) error {
		if request.OperationID != agentgen.GenOperationListAgentConversations {
			t.Fatalf("operation = %q", request.OperationID)
		}
		return json.Unmarshal([]byte(`{"items":[{"id":"conv_1","createdAt":"","principalId":"principal","status":"active","title":"Ask","updatedAt":""}],"page":{}}`), out)
	}
	command := Command(context.Background(), Dependencies{Client: client})
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"conversations", "--json", "--target", "https://example.test", "--token", "secret", "--limit", "7", "--page-token", "cursor"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []agentgen.GenSchemaAgentConversationResponse
	if err := json.Unmarshal([]byte(output.String()), &rows); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(rows) != 1 || rows[0].Id != "conv_1" {
		t.Fatalf("rows = %#v", rows)
	}
	query := client.transport.requests[0].Query
	if query.Get("limit") != "7" || query.Get("pageToken") != "cursor" {
		t.Fatalf("query = %s", query.Encode())
	}
}

func TestCommandMatchesCreateAgentRunFailure(t *testing.T) {
	client := &fakeClient{}
	client.transport.do = func(request apigenclient.Request, out any) error {
		switch request.OperationID {
		case agentgen.GenOperationCreateAgentConversation:
			return json.Unmarshal([]byte(`{"id":"conv_1","createdAt":"","principalId":"principal","status":"active","title":"","updatedAt":""}`), out)
		case agentgen.GenOperationCreateAgentRun:
			return &apigenclient.ProblemError{
				OperationID: request.OperationID,
				Response:    apigenclient.Response{StatusCode: 503},
				Problem: apigenclient.ProblemDetails{
					Code: "AGENT_SERVICE_UNAVAILABLE", Detail: "agent service is unavailable",
				},
			}
		default:
			t.Fatalf("unexpected operation %q", request.OperationID)
		}
		return nil
	}
	command := Command(context.Background(), Dependencies{Client: client})
	command.SetArgs([]string{"ask", "Question", "--target", "https://example.test", "--token", "secret"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "create agent run failed (AGENT_SERVICE_UNAVAILABLE)") {
		t.Fatalf("error = %v", err)
	}
	var failure agentgen.GenCreateAgentRunFailure
	if errors.As(err, &failure) {
		t.Fatalf("matched CLI error still exposes generated failure: %v", err)
	}
}
