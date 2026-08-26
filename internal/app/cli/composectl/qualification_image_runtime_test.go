package composectl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type qualificationExecutorFunc func(context.Context, qualificationCommandRequest) ([]byte, error)

func (f qualificationExecutorFunc) Execute(ctx context.Context, request qualificationCommandRequest) ([]byte, error) {
	return f(ctx, request)
}

func TestProductionImageRuntimeQualificationIsOwnedByGo(t *testing.T) {
	metricsToken := ""
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz":
			response.WriteHeader(http.StatusOK)
		case "/readyz":
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(response, `{"checks":{"deliveryStartup":"missing_physical_pool_admission,target_revision_missing"},"status":"not_ready"}`)
		case "/metrics":
			authorization := request.Header.Get("Authorization")
			if authorization == "" {
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			metricsToken = strings.TrimPrefix(authorization, "Bearer ")
			_, _ = io.WriteString(response, "# HELP leapview_http_request_duration_seconds duration\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	var requests [][]string
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		arguments := append([]string(nil), request.Arguments...)
		requests = append(requests, arguments)
		switch {
		case len(arguments) > 1 && arguments[0] == "image" && arguments[1] == "inspect" && slices.Contains(arguments, "{{.Config.User}}"):
			return []byte("65532:65532\n"), nil
		case len(arguments) > 0 && arguments[0] == "port":
			return []byte(strings.TrimPrefix(server.URL, "http://") + "\n"), nil
		case len(arguments) > 0 && arguments[0] == "inspect" && slices.Contains(arguments, "{{.State.Health.Status}}"):
			return []byte("unhealthy\n"), nil
		default:
			return []byte("ok\n"), nil
		}
	})
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: executor,
	})
	require.NoError(t, err)

	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	result, err := controller.qualifyProductionImageRuntime(t.Context(), image)
	require.NoError(t, err)
	if result.metricsToken == "" || metricsToken != result.metricsToken {
		t.Fatal("runtime qualification did not generate a metrics token")
	}
	assertQualificationDockerRun(t, requests, image, "8080", []string{
		"--read-only",
		"--tmpfs", "/var/lib/leapview:rw,exec,nosuid,nodev,mode=0700,uid=65532,gid=65532,size=128m",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
		"--env", "LEAPVIEW_API_TOKEN_ONLY_AUTH=1",
	})
	assertQualificationCleanup(t, requests)
}

func TestProductionImageQualificationCanRequireImmutableDigest(t *testing.T) {
	called := false
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe",
		qualificationExecutor: qualificationExecutorFunc(func(context.Context, qualificationCommandRequest) ([]byte, error) {
			called = true
			return nil, nil
		}),
	})
	require.NoError(t, err)

	err = controller.QualifyImage(t.Context(), QualificationImageOptions{
		Image:            "ghcr.io/flidai/leapview:mutable",
		RequireImmutable: true,
	})
	require.ErrorContains(t, err, "immutable repository@sha256 digest")
	if called {
		t.Fatal("mutable image validation must fail before invoking Docker")
	}
}

func TestSiteImageQualificationIsOwnedByGo(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen[request.URL.Path] = true
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var requests [][]string
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		arguments := append([]string(nil), request.Arguments...)
		requests = append(requests, arguments)
		switch {
		case len(arguments) > 1 && arguments[0] == "image" && arguments[1] == "inspect":
			return []byte("65532:65532\n"), nil
		case len(arguments) > 0 && arguments[0] == "port":
			return []byte(strings.TrimPrefix(server.URL, "http://") + "\n"), nil
		case len(arguments) > 0 && arguments[0] == "inspect" && slices.Contains(arguments, "{{.State.Running}}"):
			return []byte("true\n"), nil
		default:
			return []byte("ok\n"), nil
		}
	})
	var output strings.Builder
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", Stdout: &output,
		qualificationExecutor: executor,
	})
	require.NoError(t, err)

	image := "ghcr.io/flidai/leapview-site@sha256:" + strings.Repeat("b", 64)
	require.NoError(t, controller.QualifySiteImage(t.Context(), QualificationSiteImageOptions{Image: image}))
	for _, endpoint := range []string{"/healthz", "/readyz", "/docs"} {
		if !seen[endpoint] {
			t.Errorf("site qualification did not request %s", endpoint)
		}
	}
	assertQualificationDockerRun(t, requests, image, "8081", []string{
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=32m",
	})
	assertQualificationCleanup(t, requests)
	if !strings.Contains(output.String(), "public site image passed qualification") {
		t.Fatalf("qualification output = %q", output.String())
	}
}

func assertQualificationDockerRun(t *testing.T, requests [][]string, image, port string, required []string) {
	t.Helper()
	for _, request := range requests {
		if len(request) == 0 || request[0] != "run" || !slices.Contains(request, image) || !slices.Contains(request, "127.0.0.1::"+port) {
			continue
		}
		joined := strings.Join(request, "\x00")
		for _, fragment := range required {
			if !strings.Contains(joined, fragment) {
				t.Errorf("docker run %q missing %q", request, fragment)
			}
		}
		return
	}
	t.Fatalf("no Docker run request found for %s on %s: %v", image, port, requests)
}

func assertQualificationCleanup(t *testing.T, requests [][]string) {
	t.Helper()
	for _, request := range requests {
		if len(request) >= 3 && request[0] == "rm" && request[1] == "--force" {
			return
		}
	}
	t.Fatal(fmt.Sprintf("qualification did not remove its container: %v", requests))
}
