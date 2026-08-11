package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
	workspacegen "github.com/flidai/leapview/internal/workspace/api/gen"
)

type failingAPIGenTransport struct{ err error }

func (transport failingAPIGenTransport) DoAPIGen(context.Context, apigenclient.Request, any) (apigenclient.Response, error) {
	return apigenclient.Response{}, transport.err
}

func TestCapabilityAPITransportExecutesGeneratedTypedClient(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/workspaces" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("limit") != "7" || request.URL.Query().Get("pageToken") != "cursor" {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Fatalf("accept = %q", request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "request-1")
		_, _ = writer.Write([]byte(`{"items":[],"page":{"nextCursor":""}}`))
	}))
	defer server.Close()

	limit := int32(7)
	pageToken := "cursor"
	client := workspacegen.NewGenClient(capabilityAPITransport{
		target: server.URL,
		token:  "secret",
		client: server.Client(),
	})
	response, err := client.ListWorkspaces(context.Background(), workspacegen.GenListWorkspacesClientRequest{
		Params: workspacegen.GenListWorkspacesClientParams{
			Limit:     &limit,
			PageToken: &pageToken,
		},
	})
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if response.StatusCode != http.StatusOK || response.Headers.Get("X-Request-ID") != "request-1" {
		t.Fatalf("response metadata = %#v", response)
	}
	if len(response.Body.Items) != 0 {
		t.Fatalf("items = %#v", response.Body.Items)
	}
}

func TestFinalizeReleaseGeneratedClientReturnsDeclaredFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/projects/project-1/releases/missing/finalize" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Idempotency-Key") != "finalize-1" || request.Header.Get("X-LeapView-Invocation-Surface") != "cli" {
			t.Fatalf("headers = %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"type":"https://leapview.dev/problems/release_not_found","title":"Not Found","status":404,"detail":"Release not found.","instance":"/api/v1/projects/project-1/releases/missing/finalize","code":"RELEASE_NOT_FOUND","requestId":"request-1","errors":[]}`))
	}))
	defer server.Close()

	client := releasegen.NewGenClient(capabilityAPITransport{target: server.URL, token: "secret", client: server.Client()})
	_, err := client.FinalizeRelease(context.Background(), releasegen.GenFinalizeReleaseClientRequest{
		Project: "project-1", Release: "missing",
		Headers: releasegen.GenFinalizeReleaseClientHeaders{IdempotencyKey: "finalize-1"},
	})
	var failure releasegen.GenFinalizeReleaseFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v", err, err)
	}
	matched := releasegen.MatchGenFinalizeReleaseFailure(
		failure,
		func(apigenclient.ProblemDetails) string { return "conflict" },
		func(apigenclient.ProblemDetails) string { return "immutable" },
		func(apigenclient.ProblemDetails) string { return "incomplete" },
		func(apigenclient.ProblemDetails) string { return "not_found" },
		func(apigenclient.ProblemDetails) string { return "queue_unavailable" },
	)
	if matched != "not_found" || failure.Problem().RequestID != "request-1" {
		t.Fatalf("matched = %q problem = %#v", matched, failure.Problem())
	}
}

func TestDeploymentCLIClientExhaustivelyMapsFinalizeReleaseFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"type":"https://leapview.dev/problems/release_not_found","title":"Not Found","status":404,"detail":"Release not found.","instance":"/finalize","code":"RELEASE_NOT_FOUND","requestId":"request-2","errors":[]}`))
	}))
	defer server.Close()

	_, err := newDeploymentCLIClient(server.Client(), server.URL, "secret").finalizeRelease(
		context.Background(), "project-1", "missing", "finalize-2",
	)
	if err == nil || err.Error() != "release not found: Release not found. (RELEASE_NOT_FOUND)" {
		t.Fatalf("error = %v", err)
	}
}

func TestFinalizeReleaseGeneratedClientRejectsUnexpectedProblems(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		bodyStatus int
		code       string
	}{
		{name: "undeclared code", statusCode: http.StatusNotFound, bodyStatus: http.StatusNotFound, code: "SOMETHING_NEW"},
		{name: "status mismatch", statusCode: http.StatusConflict, bodyStatus: http.StatusConflict, code: "RELEASE_NOT_FOUND"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/problem+json")
				writer.WriteHeader(test.statusCode)
				_, _ = writer.Write([]byte(`{"type":"about:blank","title":"Failure","status":` + fmt.Sprint(test.bodyStatus) + `,"detail":"Request failed.","instance":"/finalize","code":"` + test.code + `","requestId":"request-3","errors":[]}`))
			}))
			defer server.Close()

			client := releasegen.NewGenClient(capabilityAPITransport{target: server.URL, client: server.Client()})
			_, err := client.FinalizeRelease(context.Background(), releasegen.GenFinalizeReleaseClientRequest{
				Project: "project-1", Release: "missing",
				Headers: releasegen.GenFinalizeReleaseClientHeaders{IdempotencyKey: "finalize-3"},
			})
			var unexpected *apigenclient.UnexpectedProblemError
			var declared releasegen.GenFinalizeReleaseFailure
			if !errors.As(err, &unexpected) || errors.As(err, &declared) {
				t.Fatalf("error = %T %v", err, err)
			}
		})
	}
}

func TestFinalizeReleaseGeneratedClientPreservesTransportFailure(t *testing.T) {
	sentinel := errors.New("network unavailable")
	client := releasegen.NewGenClient(failingAPIGenTransport{err: sentinel})
	_, err := client.FinalizeRelease(context.Background(), releasegen.GenFinalizeReleaseClientRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %T %v", err, err)
	}
}
