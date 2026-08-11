package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	manageddataapi "github.com/flidai/leapview/internal/manageddata/api"
)

func TestManagedDataCLIClientGeneratedCommandFailureVariants(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       string
		detail     string
		call       func(context.Context, *managedDataCLIClient) error
		want       []string
	}{
		{
			name:       "create too large",
			statusCode: http.StatusRequestEntityTooLarge,
			code:       "MANAGED_DATA_UPLOAD_TOO_LARGE",
			detail:     "upload manifest is too large",
			call: func(ctx context.Context, client *managedDataCLIClient) error {
				_, err := client.createUploadSession(ctx, "project-1", "orders", "create-1", manageddataapi.ManagedDataUploadSessionCreateRequest{})
				return err
			},
			want: []string{"create managed-data upload session too large", "upload manifest is too large", "MANAGED_DATA_UPLOAD_TOO_LARGE"},
		},
		{
			name:       "finalize not found",
			statusCode: http.StatusNotFound,
			code:       "MANAGED_DATA_UPLOAD_NOT_FOUND",
			detail:     "upload session was not found",
			call: func(ctx context.Context, client *managedDataCLIClient) error {
				_, err := client.finalizeUploadSession(ctx, "project-1", "orders", "missing", "finalize-1")
				return err
			},
			want: []string{"finalize managed-data upload session not found", "upload session was not found", "MANAGED_DATA_UPLOAD_NOT_FOUND"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("authorization = %q", request.Header.Get("Authorization"))
				}
				if request.Header.Get("X-LeapView-Invocation-Surface") != "cli" {
					t.Errorf("invocation surface = %q", request.Header.Get("X-LeapView-Invocation-Surface"))
				}
				writer.Header().Set("Content-Type", "application/problem+json")
				writer.WriteHeader(test.statusCode)
				_, _ = writer.Write([]byte(`{"type":"about:blank","title":"Failure","status":` +
					strconv.Itoa(test.statusCode) + `,"detail":"` + test.detail + `","code":"` + test.code + `"}`))
			}))
			defer server.Close()

			err := test.call(context.Background(), newManagedDataCLIClient(server.Client(), server.URL, "secret"))
			if err == nil {
				t.Fatal("error = nil")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestManagedDataCLIClientGeneratedCommandPreservesUnexpectedProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"type":"about:blank","title":"Failure","status":404,"detail":"new problem","code":"MANAGED_DATA_UPLOAD_NEW_VARIANT"}`))
	}))
	defer server.Close()

	_, err := newManagedDataCLIClient(server.Client(), server.URL, "secret").finalizeUploadSession(
		context.Background(), "project-1", "orders", "missing", "finalize-2",
	)
	var unexpected *apigenclient.UnexpectedProblemError
	if !errors.As(err, &unexpected) {
		t.Fatalf("error = %T %v, want UnexpectedProblemError", err, err)
	}
}

func TestManagedDataCLIClientGeneratedCommandPreservesTransportFailure(t *testing.T) {
	sentinel := errors.New("network unavailable")
	client := newManagedDataCLIClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	})}, "http://managed-data.invalid", "secret")

	_, err := client.finalizeUploadSession(context.Background(), "project-1", "orders", "upload-1", "finalize-3")
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %T %v, want transport sentinel", err, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
