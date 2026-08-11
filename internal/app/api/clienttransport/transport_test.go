package clienttransport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
)

func TestTransportPreservesStructuredProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"type":"https://leapview.dev/problems/release_not_found","title":"Not Found","status":404,"detail":"Release not found.","instance":"/releases/missing/finalize","code":"RELEASE_NOT_FOUND","requestId":"request-1","errors":[]}`))
	}))
	defer server.Close()

	metadata, err := (Transport{Target: server.URL, Client: server.Client()}).DoAPIGen(
		context.Background(),
		apigenclient.Request{OperationID: "finalizeRelease", Method: http.MethodPost, Path: "/releases/missing/finalize"},
		nil,
	)
	if metadata.StatusCode != http.StatusNotFound {
		t.Fatalf("metadata = %#v", metadata)
	}
	var problem *apigenclient.ProblemError
	if !errors.As(err, &problem) {
		t.Fatalf("error = %T %v", err, err)
	}
	if problem.OperationID != "finalizeRelease" || problem.Problem.Code != "RELEASE_NOT_FOUND" || problem.Problem.RequestID != "request-1" {
		t.Fatalf("problem = %#v", problem)
	}
}
