package cli

import (
	"context"
	"errors"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

type semanticFakeClient struct {
	transport semanticFakeTransport
}

type semanticFakeTransport struct {
	requests []apigenclient.Request
	err      error
}

func (client *semanticFakeClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}

func (client *semanticFakeClient) Environment(_ context.Context, _ cliapi.Credentials, asserted string) (string, error) {
	return asserted, nil
}

func (client *semanticFakeClient) Transport(_ context.Context, _ cliapi.Credentials) (apigenclient.Transport, error) {
	return &client.transport, nil
}

func (transport *semanticFakeTransport) DoAPIGen(_ context.Context, request apigenclient.Request, _ any) (apigenclient.Response, error) {
	transport.requests = append(transport.requests, request)
	return apigenclient.Response{}, transport.err
}

func TestSemanticModelsCommandOwnsSemanticQuery(t *testing.T) {
	stop := errors.New("stop after request")
	client := &semanticFakeClient{transport: semanticFakeTransport{err: stop}}
	command := SemanticModelsCommand(context.Background(), client)
	command.SetArgs([]string{
		"query", "orders",
		"--target", "https://example.test", "--token", "secret",
		"--body-json", `{"dimensions":[{"field":"state"}]}`,
	})
	if err := command.Execute(); !errors.Is(err, stop) {
		t.Fatalf("execute error = %v", err)
	}
	if len(client.transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(client.transport.requests))
	}
	request := client.transport.requests[0]
	if request.OperationID != dashboardgen.GenOperationQuerySemanticModel ||
		request.PathParams["model"] != "orders" {
		t.Fatalf("request = %#v", request)
	}
	body := request.Body.(*dashboardgen.GenSchemaSemanticQueryRequest)
	if body.Dimensions == nil || len(*body.Dimensions) != 1 || (*body.Dimensions)[0].Field != "state" {
		t.Fatalf("body = %#v", body)
	}
}

func TestSemanticModelsCommandRejectsRemovedWorkspaceFlag(t *testing.T) {
	command := SemanticModelsCommand(context.Background(), &semanticFakeClient{})
	command.SetArgs([]string{"list", "--workspace", "sales"})
	if err := command.Execute(); err == nil {
		t.Fatal("semantic-model command accepted removed --workspace flag")
	}
}
