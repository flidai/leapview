package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectgen "github.com/flidai/leapview/internal/project/api/gen"
)

type searchTestClient struct{ transport searchTestTransport }
type searchTestTransport struct {
	requests []apigenclient.Request
}

func (client *searchTestClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}
func (client *searchTestClient) Environment(_ context.Context, _ cliapi.Credentials, asserted string) (string, error) {
	return asserted, nil
}
func (client *searchTestClient) Transport(_ context.Context, _ cliapi.Credentials) (apigenclient.Transport, error) {
	return &client.transport, nil
}
func (transport *searchTestTransport) DoAPIGen(_ context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	transport.requests = append(transport.requests, request)
	return apigenclient.Response{StatusCode: 200}, json.Unmarshal([]byte(`{"items":[],"page":{"nextCursor":""}}`), out)
}

func TestSearchCommandIsProjectWide(t *testing.T) {
	client := &searchTestClient{}
	command := SearchCommand(context.Background(), client)
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"orders", "--type", "dashboard", "--target", "https://example.test"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	request := client.transport.requests[0]
	if request.OperationID != projectgen.GenOperationSearch || request.Query.Get("q") != "orders" {
		t.Fatalf("request = %#v", request)
	}
	if request.Query.Get("project") != "" || request.Query.Get("workspace") != "" {
		t.Fatalf("project/workspace selector leaked into query: %#v", request.Query)
	}
}
