package cli

import (
	"context"
	"errors"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

type fakeClient struct {
	transport fakeTransport
}

type fakeTransport struct {
	requests []apigenclient.Request
	err      error
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

func (transport *fakeTransport) DoAPIGen(_ context.Context, request apigenclient.Request, _ any) (apigenclient.Response, error) {
	transport.requests = append(transport.requests, request)
	return apigenclient.Response{}, transport.err
}

func TestCommandOwnsDashboardVisualQuery(t *testing.T) {
	stop := errors.New("stop after request")
	client := &fakeClient{transport: fakeTransport{err: stop}}
	command := Command(context.Background(), client)
	command.SetArgs([]string{
		"visual-data", "executive", "overview", "orders",
		"--target", "https://example.test", "--token", "secret",
		"--count", "7", "--filter-state-json", `{"version":"typed_v1"}`,
	})
	if err := command.Execute(); !errors.Is(err, stop) {
		t.Fatalf("execute error = %v", err)
	}
	if len(client.transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(client.transport.requests))
	}
	request := client.transport.requests[0]
	if request.OperationID != dashboardgen.GenOperationQueryDashboardVisualData {
		t.Fatalf("operation = %q", request.OperationID)
	}
	if request.PathParams["dashboard"] != "executive" ||
		request.PathParams["page"] != "overview" || request.PathParams["visual"] != "orders" {
		t.Fatalf("path params = %#v", request.PathParams)
	}
	body := request.Body.(*dashboardgen.GenSchemaDashboardVisualQueryRequest)
	if body.Limit == nil || *body.Limit != 7 {
		t.Fatalf("body = %#v", body)
	}
	if body.FilterState == nil || body.FilterState.Version != "typed_v1" {
		t.Fatalf("filter state = %#v", body.FilterState)
	}
}

func TestCommandRejectsRemovedWorkspaceFlag(t *testing.T) {
	command := Command(context.Background(), &fakeClient{})
	command.SetArgs([]string{"list", "--workspace", "sales"})
	if err := command.Execute(); err == nil {
		t.Fatal("dashboard command accepted removed --workspace flag")
	}
}
