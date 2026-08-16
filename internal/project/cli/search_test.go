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
	return apigenclient.Response{StatusCode: 200}, json.Unmarshal([]byte(`{"items":[{"reference":{"id":"dash_1","kind":"dashboard"},"name":"Revenue","displayName":"Revenue dashboard","description":"Sales","domain":"sales","owner":"owner_1","tags":["finance"]}],"page":{"nextCursor":"next-page"}}`), out)
}

func TestSearchCommandUsesCanonicalFiltersAndCursor(t *testing.T) {
	client := &searchTestClient{}
	command := SearchCommand(context.Background(), client)
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"orders", "--kind", "dashboard", "--kind", "semantic_model", "--domain", "sales", "--page-token", "cursor-1", "--target", "https://example.test"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	request := client.transport.requests[0]
	if request.OperationID != projectgen.GenOperationSearch || request.Query.Get("q") != "orders" || request.Query.Get("domain") != "sales" || request.Query.Get("cursor") != "cursor-1" {
		t.Fatalf("request = %#v", request)
	}
	if got := request.Query["kind"]; len(got) != 2 || got[0] != "dashboard" || got[1] != "semantic_model" {
		t.Fatalf("kind query = %#v", got)
	}
	if request.Query.Get("project") != "" || request.Query.Get("workspace") != "" {
		t.Fatalf("project/workspace selector leaked into query: %#v", request.Query)
	}
	if !strings.Contains(output.String(), "dashboard") || !strings.Contains(output.String(), "dash_1") {
		t.Fatalf("rendered output = %q", output.String())
	}
}

func TestSearchCommandRejectsLegacyTypeFlag(t *testing.T) {
	command := SearchCommand(context.Background(), &searchTestClient{})
	command.SetArgs([]string{"orders", "--type", "dashboard"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --type") {
		t.Fatalf("error = %v, want unknown --type flag", err)
	}
}
