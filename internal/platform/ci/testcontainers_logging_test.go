package ci

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

const syntheticRegistrySentinel = "synthetic-registry-credential-must-never-be-printed"

func TestTestcontainersRateLimitDoesNotPrintRegistryCredentials(t *testing.T) {
	if os.Getenv("LEAPVIEW_TESTCONTAINERS_LOGGING_CHILD") == "1" {
		if os.Getenv("LEAPVIEW_SYNTHETIC_PARENT_CREDENTIAL") != "" {
			t.Fatal("unexpected inherited parent credential")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{Image: "example.invalid/synthetic/no-pull:latest"},
		})
		if err == nil || !strings.Contains(err.Error(), "toomanyrequests") {
			t.Fatal("synthetic rate-limit path was not reached")
		}
		fmt.Println("SYNTHETIC_RATE_LIMIT_REACHED")
		return
	}

	var rateLimits atomic.Int32
	versionPrefix := regexp.MustCompile(`^/v[0-9.]+`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch versionPrefix.ReplaceAllString(r.URL.Path, "") {
		case "/_ping":
			w.Header().Set("API-Version", "1.54")
			_, _ = w.Write([]byte("OK"))
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.54","MinAPIVersion":"1.44","Version":"29.8.0"}`))
		case "/info":
			_, _ = w.Write([]byte(`{"ServerVersion":"29.8.0","OperatingSystem":"synthetic","OSType":"linux","Architecture":"x86_64","DockerRootDir":"/synthetic","IndexServerAddress":"https://index.docker.io/v1/"}`))
		case "/networks":
			rateLimits.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"toomanyrequests: synthetic test limit"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"unsupported synthetic endpoint"}`))
		}
	}))
	defer server.Close()

	privateRoot := t.TempDir()
	if err := os.Chmod(privateRoot, 0700); err != nil {
		t.Fatal("prepare private synthetic home")
	}
	dockerConfig := filepath.Join(privateRoot, "docker")
	if err := os.Mkdir(dockerConfig, 0700); err != nil {
		t.Fatal("prepare private synthetic Docker configuration")
	}
	syntheticAuth := `{"auths":{"synthetic.invalid":{"username":"synthetic-user","password":"` + syntheticRegistrySentinel + `"}}}`
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte(syntheticAuth), 0600); err != nil {
		t.Fatal("write private synthetic Docker configuration")
	}
	t.Setenv("LEAPVIEW_SYNTHETIC_PARENT_CREDENTIAL", "must-not-reach-child")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTestcontainersRateLimitDoesNotPrintRegistryCredentials$", "-test.count=1")
	// An explicit environment prevents registry credentials, credential helpers,
	// Docker socket selection and user configuration from the runner reaching
	// this deliberately failing dependency path.
	child.Env = []string{
		"HOME=" + privateRoot,
		"TMPDIR=" + privateRoot,
		"DOCKER_CONFIG=" + dockerConfig,
		"DOCKER_AUTH_CONFIG=" + syntheticAuth,
		"DOCKER_HOST=" + strings.Replace(server.URL, "http://", "tcp://", 1),
		"DOCKER_API_VERSION=1.54",
		"LEAPVIEW_TESTCONTAINERS_LOGGING_CHILD=1",
	}
	output, err := child.CombinedOutput()
	// Never include captured dependency output in a failing assertion.
	if err != nil {
		t.Fatal("synthetic dependency subprocess failed")
	}
	if rateLimits.Load() == 0 || !strings.Contains(string(output), "SYNTHETIC_RATE_LIMIT_REACHED") {
		t.Fatal("synthetic rate-limit path was not reached")
	}
	for _, forbidden := range []string{syntheticRegistrySentinel, "synthetic-user", "XXX: too many requests:", "AuthConfigs:", "Auths:map"} {
		if strings.Contains(string(output), forbidden) {
			t.Fatal("dependency rate-limit output disclosed synthetic registry configuration")
		}
	}
}
