package composectl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/installationstate"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	"github.com/stretchr/testify/require"
)

func TestFirstInstallProbesKeepLoopbackConnectionWithConfiguredAuthority(t *testing.T) {
	for _, operation := range []string{"startup", "activation", "activation rollback"} {
		t.Run(operation, func(t *testing.T) {
			controller, root := bootstrapLifecycleController(t, true)
			controller.dockerBin = writeBootstrapDockerProbe(t, root, operation == "activation rollback")
			var mu sync.Mutex
			var paths []string
			allowed := httpmiddleware.AllowedHosts([]string{"dash.example.com"})(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				mu.Lock()
				paths = append(paths, request.URL.Path)
				mu.Unlock()
				response.WriteHeader(http.StatusOK)
			}))
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				// Docker forwards the host's loopback publication over its bridge;
				// the application sees a non-loopback peer and enforces Host.
				request.RemoteAddr = "172.18.0.1:49152"
				allowed.ServeHTTP(response, request)
			}))
			defer server.Close()
			var err error
			if operation == "startup" {
				err = controller.startFirstInstallBootstrapAt(t.Context(), server.URL)
			} else {
				err = controller.activateFirstInstallAt(t.Context(), server.URL)
			}
			if operation == "activation rollback" {
				require.ErrorContains(t, err, "activate public first-install configuration")
				require.NotContains(t, err.Error(), "restore private first-install configuration")
			} else {
				require.NoError(t, err)
			}
			mu.Lock()
			defer mu.Unlock()
			switch operation {
			case "startup":
				require.Equal(t, []string{"/healthz"}, paths)
			case "activation":
				require.Equal(t, []string{"/readyz", "/readyz"}, paths)
			case "activation rollback":
				require.Equal(t, []string{"/readyz", "/healthz"}, paths)
			}
		})
	}
}

func TestPrivateComposeArgumentsAndEnvironmentOverrideHostBindings(t *testing.T) {
	root := t.TempDir()
	image := bootstrapLifecycleImage('a')
	contents := "COMPOSE_PROJECT_NAME=host-private-test\nCOMPOSE_HTTPS=1\nLEAPVIEW_IMAGE=" + image +
		"\nCOMPOSE_APP_BIND=0.0.0.0:8080\nCADDY_HTTP_BIND=0.0.0.0:80\nCADDY_HTTPS_BIND=0.0.0.0:443\nCADDY_HTTPS_UDP_BIND=0.0.0.0:443\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(contents), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "first-install.env"), []byte("COMPOSE_APP_BIND=127.0.0.1:8080\n"), 0o600))
	for name, value := range map[string]string{
		"COMPOSE_APP_BIND": "192.0.2.20:8080", "CADDY_HTTP_BIND": "192.0.2.20:80",
		"CADDY_HTTPS_BIND": "192.0.2.20:443", "CADDY_HTTPS_UDP_BIND": "192.0.2.20:443",
	} {
		t.Setenv(name, value)
	}

	arguments, err := composeArgumentsForPhase(root, installationstate.PhasePrivate, "up", "-d")
	require.NoError(t, err)
	joined := strings.Join(arguments, " ")
	require.Contains(t, joined, "--env-file "+filepath.Join(root, "first-install.env"))
	require.Contains(t, joined, "--file "+filepath.Join(root, "compose.first-install-bootstrap.yaml"))
	require.True(t, strings.Index(joined, "first-install.env") < strings.Index(joined, "compose.first-install-bootstrap.yaml"))
	require.True(t, strings.HasSuffix(joined, "up -d"))

	processEnvironment, err := composeProcessEnvironment(root, privateBootstrapEnvironment)
	require.NoError(t, err)
	values := environmentValues(strings.Join(processEnvironment, "\n"))
	for name, want := range privateBootstrapEnvironment {
		require.Equal(t, want, values[name], "host process must not override %s", name)
	}
}

func TestPrivateFirstInstallStartRemovesOldProxyAndWaitsForLivenessBeforeStartingProxy(t *testing.T) {
	controller, root := bootstrapLifecycleController(t, true)
	var mu sync.Mutex
	var calls []string
	controller.composeOverride = func(_ context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) error {
		mu.Lock()
		calls = append(calls, strings.Join(args, " "))
		mu.Unlock()
		if strings.Join(args, " ") == "ps --quiet caddy" {
			_, err := fmt.Fprintln(stdout, "old-caddy")
			return err
		}
		return nil
	}
	livenessObserved := make(chan []string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			http.NotFound(response, request)
			return
		}
		mu.Lock()
		livenessObserved <- append([]string(nil), calls...)
		mu.Unlock()
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, controller.startFirstInstallBootstrapAt(t.Context(), server.URL))
	beforeProxy := <-livenessObserved
	require.Contains(t, beforeProxy, "rm --stop --force caddy")
	require.Contains(t, beforeProxy, "up -d leapview")
	require.NotContains(t, beforeProxy, "up -d caddy")
	mu.Lock()
	require.Equal(t, []string{"ps --quiet caddy", "rm --stop --force caddy", "up -d leapview", "up -d caddy"}, calls)
	mu.Unlock()

	marker, present, err := installationstate.ReadMarker(root)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
}

func TestPrivateFirstInstallStartWithoutHTTPSDoesNotStartCaddy(t *testing.T) {
	controller, root := bootstrapLifecycleController(t, false)
	var calls []string
	controller.composeOverride = func(_ context.Context, _ io.Reader, _ io.Writer, _ io.Writer, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/healthz", request.URL.Path)
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, controller.startFirstInstallBootstrapAt(t.Context(), server.URL))
	require.Equal(t, []string{"up -d leapview"}, calls)
	marker, present, err := installationstate.ReadMarker(root)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
}

func TestFirstInstallActivationRequiresExactReadinessBeforeCompose(t *testing.T) {
	controller, root := bootstrapLifecycleController(t, true)
	var calls [][]string
	controller.composeOverride = func(_ context.Context, _ io.Reader, _ io.Writer, _ io.Writer, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(response, request, "/readyz", http.StatusFound)
			return
		}
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := controller.activateFirstInstallAt(t.Context(), server.URL)
	require.ErrorContains(t, err, "requires HTTP 200")
	require.Empty(t, calls, "public Compose configuration must not start before readiness")
	marker, present, readErr := installationstate.ReadMarker(root)
	require.NoError(t, readErr)
	require.True(t, present)
	require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)

	status, err := hostHTTPStatus(t.Context(), server.URL+"/redirect", "dash.example.com")
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, status, "readiness redirects are not followed")
}

func TestFirstInstallActivationFailureRestoresPrivatePhase(t *testing.T) {
	controller, root := bootstrapLifecycleController(t, true)
	logPath := filepath.Join(root, "docker-commands.log")
	docker := filepath.Join(root, "docker-probe")
	contents := `#!/bin/sh
printf '%s\n' "$*" >> "$LEAPVIEW_TEST_DOCKER_LOG"
case "$*" in
  *"ps -q leapview"*) printf 'leapview-container\n'; exit 0 ;;
  *"inspect -f {{.State.Health.Status}} leapview-container"*) printf 'healthy\n'; exit 0 ;;
esac
if [ "$LEAPVIEW_FAIL_PUBLIC_UP" = "1" ] && [ "$1" = "compose" ] &&
   echo "$*" | grep -q ' up -d' && ! echo "$*" | grep -q 'compose.first-install-bootstrap.yaml'; then
  exit 17
fi
exit 0
`
	require.NoError(t, os.WriteFile(docker, []byte(contents), 0o700))
	controller.dockerBin = docker
	t.Setenv("LEAPVIEW_TEST_DOCKER_LOG", logPath)
	t.Setenv("LEAPVIEW_FAIL_PUBLIC_UP", "1")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := controller.activateFirstInstallAt(t.Context(), server.URL)
	require.ErrorContains(t, err, "activate public first-install configuration")
	require.NotContains(t, err.Error(), "restore private first-install configuration")
	marker, present, readErr := installationstate.ReadMarker(root)
	require.NoError(t, readErr)
	require.True(t, present)
	require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
	commands, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	require.Contains(t, string(commands), "compose.first-install-bootstrap.yaml up -d")
	require.NotContains(t, string(commands), "first-install public activation completed")
}

func TestFirstInstallActivationPersistsPublicPhaseOnlyAfterHealthAndReady(t *testing.T) {
	controller, root := bootstrapLifecycleController(t, true)
	docker := writeBootstrapDockerProbe(t, root, false)
	controller.dockerBin = docker
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, controller.activateFirstInstallAt(t.Context(), server.URL))
	marker, present, err := installationstate.ReadMarker(root)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, installationstate.PhasePublic, marker.BootstrapPhase)
	commands, err := os.ReadFile(filepath.Join(root, "docker-commands.log"))
	require.NoError(t, err)
	commandLog := string(commands)
	require.Contains(t, commandLog, "compose.https.yaml up -d")
	require.Contains(t, commandLog, "inspect -f {{.State.Health.Status}} leapview-container")
}

func writeBootstrapDockerProbe(t *testing.T, root string, failPublicUp bool) string {
	t.Helper()
	path := filepath.Join(root, "docker-probe")
	failure := "0"
	if failPublicUp {
		failure = "1"
	}
	contents := `#!/bin/sh
printf '%s\n' "$*" >> "$LEAPVIEW_TEST_DOCKER_LOG"
case "$*" in
  *"ps -q leapview"*) printf 'leapview-container\n'; exit 0 ;;
  *"inspect -f {{.State.Health.Status}} leapview-container"*) printf 'healthy\n'; exit 0 ;;
esac
if [ "$LEAPVIEW_FAIL_PUBLIC_UP" = "1" ] && [ "$1" = "compose" ] &&
   echo "$*" | grep -q ' up -d' && ! echo "$*" | grep -q 'compose.first-install-bootstrap.yaml'; then
  exit 17
fi
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o700))
	t.Setenv("LEAPVIEW_TEST_DOCKER_LOG", filepath.Join(root, "docker-commands.log"))
	t.Setenv("LEAPVIEW_FAIL_PUBLIC_UP", failure)
	return path
}

func bootstrapLifecycleController(t *testing.T, https bool) (*Controller, string) {
	t.Helper()
	root := t.TempDir()
	image := bootstrapLifecycleImage('a')
	caddy := bootstrapLifecycleImage('b')
	httpsValue := "0"
	if https {
		httpsValue = "1"
	}
	contents := "COMPOSE_PROJECT_NAME=host-bootstrap-test\nCOMPOSE_HTTPS=" + httpsValue + "\nLEAPVIEW_IMAGE=" + image +
		"\nCADDY_IMAGE=" + caddy + "\nCADDY_DOMAIN=dash.example.com\nCOMPOSE_APP_BIND=127.0.0.1:8080\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(contents), 0o600))
	generation := "sha256-" + strings.Repeat("a", 64)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "releases", generation), 0o700))
	require.NoError(t, os.Symlink(filepath.Join("releases", generation), filepath.Join(root, "current")))
	marker, err := installationstate.NewMarker(installationstate.Config{
		SchemaVersion: installationstate.SchemaVersion, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: image, HTTPS: &https,
	}, installationstate.PhasePrivate)
	require.NoError(t, err)
	require.NoError(t, installationstate.WriteMarker(root, marker))
	controller, err := New(Options{Root: root, Sleep: func(context.Context, time.Duration) error { return nil }})
	require.NoError(t, err)
	return controller, root
}

func bootstrapLifecycleImage(hash byte) string {
	return "ghcr.io/flidai/leapview@sha256:" + strings.Repeat(string(hash), 64)
}
