package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
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

func TestDeploymentCLIClientCreateReleaseUsesGeneratedCommandContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/projects/project-1/releases" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Idempotency-Key") != "create-1" ||
			request.Header.Get("X-LeapView-Invocation-Surface") != "cli" {
			t.Fatalf("headers = %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":"release-1","projectId":"project-1","status":"draft"}`))
	}))
	defer server.Close()

	response, err := newDeploymentCLIClient(server.Client(), server.URL, "secret").createRelease(
		context.Background(), "project-1", "create-1", releasegen.ReleaseCreateRequest{ProjectDigest: "sha256:project"},
	)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if response.Id != "release-1" || response.ProjectId != "project-1" {
		t.Fatalf("response = %#v", response)
	}
}

func TestDeploymentCLIClientStreamsReleaseArtifactThroughGeneratedCommandContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/api/v1/projects/project-1/releases/release-1/workspaces/sales/artifact" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		if string(payload) != "artifact bytes" || request.Header.Get("Content-Digest") != "sha256:digest" ||
			request.Header.Get("X-LeapView-Invocation-Surface") != "cli" {
			t.Fatalf("payload = %q headers = %#v", payload, request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Location", "/artifact")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"digest":"sha256:digest","releaseId":"release-1","sizeBytes":14,"workspaceId":"sales"}`))
	}))
	defer server.Close()

	response, err := newDeploymentCLIClient(server.Client(), server.URL, "secret").uploadReleaseArtifact(
		context.Background(), "project-1", "release-1", "sales", "sha256:digest", strings.NewReader("artifact bytes"),
	)
	if err != nil {
		t.Fatalf("upload release artifact: %v", err)
	}
	if response.Digest != "sha256:digest" || response.SizeBytes != 14 {
		t.Fatalf("response = %#v", response)
	}
}

func TestDeploymentCLIClientCreateReleaseMapsDeclaredFailuresExhaustively(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       string
	}{
		{name: "conflict", statusCode: http.StatusConflict, code: "RELEASE_CONFLICT"},
		{name: "invalid", statusCode: http.StatusUnprocessableEntity, code: "INVALID_RELEASE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/problem+json")
				writer.WriteHeader(test.statusCode)
				_, _ = fmt.Fprintf(writer, `{"status":%d,"detail":"declared failure","code":"%s"}`, test.statusCode, test.code)
			}))
			defer server.Close()

			_, err := newDeploymentCLIClient(server.Client(), server.URL, "secret").createRelease(
				context.Background(), "project-1", "create-2", releasegen.ReleaseCreateRequest{},
			)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error = %v", err)
			}
			var declared releasegen.GenCreateReleaseFailure
			if errors.As(err, &declared) {
				t.Fatalf("declared failure escaped CLI mapping: %T", err)
			}
		})
	}
}

func TestDeploymentCLIClientCreateDeploymentPreservesUnexpectedAndTransportFailures(t *testing.T) {
	t.Run("unexpected problem", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/problem+json")
			writer.WriteHeader(http.StatusConflict)
			_, _ = writer.Write([]byte(`{"status":409,"detail":"new failure","code":"NEW_DEPLOYMENT_FAILURE"}`))
		}))
		defer server.Close()

		_, err := newDeploymentCLIClient(server.Client(), server.URL, "secret").createDeployment(
			context.Background(), "project-1", "create-3", deploymentgen.DeploymentCreateRequest{ReleaseId: "release-1"},
		)
		var unexpected *apigenclient.UnexpectedProblemError
		if !errors.As(err, &unexpected) {
			t.Fatalf("error = %T %v", err, err)
		}
		var declared deploymentgen.GenCreateDeploymentFailure
		if errors.As(err, &declared) {
			t.Fatalf("unexpected problem classified as declared failure: %T", err)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		sentinel := errors.New("network unavailable")
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, sentinel
		})}
		_, err := newDeploymentCLIClient(client, "https://target.example", "secret").createDeployment(
			context.Background(), "project-1", "create-4", deploymentgen.DeploymentCreateRequest{ReleaseId: "release-1"},
		)
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %T %v", err, err)
		}
		var unexpected *apigenclient.UnexpectedProblemError
		if errors.As(err, &unexpected) {
			t.Fatalf("transport failure classified as unexpected problem: %T", err)
		}
	})
}
