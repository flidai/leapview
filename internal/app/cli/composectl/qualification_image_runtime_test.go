package composectl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	var requests [][]string
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		arguments := append([]string(nil), request.Arguments...)
		requests = append(requests, arguments)
		joined := strings.Join(arguments, " ")
		switch {
		case strings.Contains(joined, "--entrypoint id") && slices.Contains(arguments, "-u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "--entrypoint id") && slices.Contains(arguments, "-g"):
			return []byte("1000\n"), nil
		case len(arguments) > 0 && arguments[0] == "inspect" && slices.Contains(arguments, "{{.State.Status}}"):
			return []byte("exited\n"), nil
		case len(arguments) > 0 && arguments[0] == "inspect" && slices.Contains(arguments, "{{.State.ExitCode}}"):
			return []byte("1\n"), nil
		case len(arguments) > 0 && arguments[0] == "logs":
			return []byte("production serve requires LEAPVIEW_POSTGRES_CONTROL_URL\n"), nil
		default:
			return []byte("ok\n"), nil
		}
	})
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: executor,
	})
	require.NoError(t, err)

	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	require.NoError(t, controller.qualifyProductionImageRuntime(t.Context(), image))
	assertQualificationDockerRun(t, requests, image, "", []string{
		"--read-only",
		"--network", "none",
		"--tmpfs", "/var/lib/leapview:rw,exec,nosuid,nodev,mode=0700,uid=1000,gid=1000,size=128m",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
		"--env", "LEAPVIEW_POSTGRES_CONTROL_URL=",
		"--env", "LEAPVIEW_API_TOKEN_ONLY_AUTH=1",
	})
	for _, request := range requests {
		require.NotContains(t, request, "127.0.0.1::8080")
	}
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

func TestProductionImageQualificationRunsPolicyPerformanceAfterAuthoring(t *testing.T) {
	imageQualification, err := os.ReadFile("qualification_image.go")
	require.NoError(t, err)
	source := string(imageQualification)
	authoring := strings.Index(source, "authoringReport, err := c.runQualificationAuthoring")
	performance := strings.Index(source, "instanceController.runQualificationPerformance")
	nativeReads := strings.LastIndex(source, "nativeTopology.AssertNativeDeliveryReads(ctx)")
	if authoring < 0 || performance < 0 || nativeReads < 0 {
		t.Fatalf("production qualification is missing authoring, performance, or native-read phase")
	}
	if !(authoring < performance && performance < nativeReads) {
		t.Fatalf("production qualification phase order is authoring=%d performance=%d native-reads=%d", authoring, performance, nativeReads)
	}

	policyPath := filepath.Join("..", "..", "..", "..", "deploy", "compose", "qualification", "performance-policy.json")
	policyJSON, err := os.ReadFile(policyPath)
	require.NoError(t, err)
	var policy struct {
		Assumptions struct {
			Samples struct {
				RefreshRuns int `json:"refreshRuns"`
			} `json:"samples"`
		} `json:"assumptions"`
	}
	require.NoError(t, json.Unmarshal(policyJSON, &policy))
	require.Equal(t, 3, policy.Assumptions.Samples.RefreshRuns)

	performanceScript, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "compose", "qualification", "performance.mjs"))
	require.NoError(t, err)
	require.Contains(t, string(performanceScript), "for (let index = 0; index < policy.assumptions.samples.refreshRuns; index += 1)")
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
		if len(request) == 0 || request[0] != "run" || !slices.Contains(request, "--detach") || !slices.Contains(request, image) || (port != "" && !slices.Contains(request, "127.0.0.1::"+port)) {
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
