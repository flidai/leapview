package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestDoctorRejectsRemoteLocalSelectorsAndLocalTokens(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "source root on remote", args: []string{"--target", "https://example.com", "--source-root", "dashboards"}},
		{name: "profile on remote", args: []string{"--target", "https://example.com", "--profile", "local"}},
		{name: "docker context on remote", args: []string{"--target", "https://example.com", "--docker-context", "desktop"}},
		{name: "token local", args: []string{"--token", "token-value"}},
		{name: "empty remote target", args: []string{"--target="}},
		{name: "timeout above maximum", args: []string{"--timeout=31s"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := doctorCommand(context.Background())
			command.SetArgs(test.args)
			command.SilenceErrors, command.SilenceUsage = true, true
			command.RunE = func(command *cobra.Command, _ []string) error {
				return validateDoctorInvocation(command, flagsFromCommand(command))
			}
			err := command.Execute()
			require.Error(t, err)
		})
	}
}

func TestDoctorMissingDockerIsReportedAsUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	_, err := localdocker.Resolve(context.Background(), localdocker.Options{Environment: []string{"PATH=" + dir}})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "docker") || errors.Is(err, os.ErrNotExist), "%v", err)
}

func TestDoctorLocalProjectChecksRedactCredentialValues(t *testing.T) {
	checkout, sourceRoot := writeLocalProfileProject(t)
	t.Chdir(checkout)
	secret := "credential-value-that-must-not-appear"
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", secret)
	command := projectDoctorCommand(sourceRoot, "", "")
	var report doctorReport
	runLocalProjectChecks(context.Background(), command, doctorFlags{sourceRoot: sourceRoot}, &report)
	result := report.finish()
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.Contains(t, string(encoded), "LEAPVIEW_DEV_CONNECTION_WAREHOUSE")
	credentials := findDoctorCheck(t, report.Checks, "project.credentials")
	require.Equal(t, "fail", credentials.Status)
}

func TestDoctorTimeoutCheckOverridesCompletedReport(t *testing.T) {
	report := newDoctorReport()
	report.add("local.identity", "pass", "CLI identity is available.", "")
	addDoctorTimeoutCheck(&report, context.DeadlineExceeded)
	require.Equal(t, "fail", report.finish().Status)
	require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "doctor.timeout").Status)
}

func TestDoctorProjectChecksDoNotPassPhasesAfterTheBudgetExpires(t *testing.T) {
	for _, test := range []struct {
		name          string
		expireOnCheck int
		wantProfile   string
		wantCreds     string
	}{
		{name: "profile phase", expireOnCheck: 5, wantProfile: "fail", wantCreds: "skip"},
		{name: "credential phase", expireOnCheck: 7, wantProfile: "pass", wantCreds: "fail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout, _ := writeLocalProfileProject(t)
			t.Chdir(checkout)
			command := projectDoctorCommand("", "", "")
			var report doctorReport
			ctx := doctorContextExpiringAfterChecks{Context: context.Background(), expireOnCheck: test.expireOnCheck}
			runLocalProjectChecks(&ctx, command, doctorFlags{}, &report)
			require.Equal(t, "pass", findDoctorCheck(t, report.Checks, "project.compiler").Status)
			require.Equal(t, test.wantProfile, findDoctorCheck(t, report.Checks, "project.profile").Status)
			require.Equal(t, test.wantCreds, findDoctorCheck(t, report.Checks, "project.credentials").Status)
		})
	}
}

type doctorContextExpiringAfterChecks struct {
	context.Context
	expireOnCheck int
	checks        int
}

func (ctx *doctorContextExpiringAfterChecks) Err() error {
	ctx.checks++
	if ctx.checks >= ctx.expireOnCheck {
		return context.DeadlineExceeded
	}
	return nil
}

func TestDoctorInvalidProfileFailsWithoutPrintingProfileContents(t *testing.T) {
	checkout, sourceRoot := writeLocalProfileProject(t)
	t.Chdir(checkout)
	secret := "profile-secret-never-print"
	profilePath := filepath.Join(checkout, ".leapview", "profiles.local.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("not: [valid: "+secret), 0o600))
	command := projectDoctorCommand(sourceRoot, "", "local")
	var report doctorReport
	runLocalProjectChecks(context.Background(), command, doctorFlags{sourceRoot: sourceRoot, profile: "local"}, &report)
	encoded, err := json.Marshal(report.finish())
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "project.profile").Status)
}

func TestDoctorMissingExplicitProfileAndSourceFail(t *testing.T) {
	checkout, sourceRoot := writeLocalProfileProject(t)
	t.Chdir(checkout)
	t.Run("missing profile", func(t *testing.T) {
		missing := filepath.Join(checkout, ".leapview", "missing-profile.yaml")
		command := projectDoctorCommand(sourceRoot, missing, "")
		var report doctorReport
		runLocalProjectChecks(context.Background(), command, doctorFlags{sourceRoot: sourceRoot, profileFile: missing}, &report)
		require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "project.profile").Status)
	})
	t.Run("missing source", func(t *testing.T) {
		missing := filepath.Join(checkout, "missing-dashboards")
		command := projectDoctorCommand(missing, "", "")
		var report doctorReport
		runLocalProjectChecks(context.Background(), command, doctorFlags{sourceRoot: missing}, &report)
		require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "project.compiler").Status)
	})
}

func TestDoctorOutsideProjectSkipsProjectChecksByDefault(t *testing.T) {
	outside := t.TempDir()
	t.Chdir(outside)
	command := projectDoctorCommand("", "", "")
	var report doctorReport
	runLocalProjectChecks(context.Background(), command, doctorFlags{}, &report)
	require.Equal(t, "skip", findDoctorCheck(t, report.Checks, "project.compiler").Status)
	require.Equal(t, "skip", findDoctorCheck(t, report.Checks, "project.profile").Status)
	require.Equal(t, "skip", findDoctorCheck(t, report.Checks, "project.credentials").Status)
}

func TestDoctorImplicitProjectInspectionFailureDoesNotSkipChecks(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("dashboards", []byte("not a directory"), 0o600))
	command := projectDoctorCommand("", "", "")
	var report doctorReport
	runLocalProjectChecks(t.Context(), command, doctorFlags{}, &report)
	require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "project.compiler").Status)
}

func TestDoctorDetectsAuthoredDashboardProjectWithoutInitializationMarker(t *testing.T) {
	checkout, _ := writeLocalProfileProject(t)
	t.Chdir(checkout)
	command := &cobra.Command{Use: "doctor"}
	var report doctorReport
	runLocalProjectChecks(context.Background(), command, doctorFlags{}, &report)
	require.Equal(t, "pass", findDoctorCheck(t, report.Checks, "project.compiler").Status)
	require.Equal(t, "pass", findDoctorCheck(t, report.Checks, "project.profile").Status)
	require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "project.credentials").Status)
}

func TestDoctorRemoteIdentityMustMatchSavedProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/instance" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"canonicalOrigin":"` + "http://" + request.Host + `","environment":"prod","id":"instance-live"}`))
	}))
	defer server.Close()
	profile := cliapi.TargetProfile{Origin: server.URL, InstanceID: "instance-stale", Environment: "prod"}
	_, err := checkRemoteIdentity(context.Background(), server.Client(), selectedDoctorTarget{origin: server.URL, profile: &profile})
	require.Error(t, err)
}

func TestDoctorRemoteWithoutTokenSkipsAuthenticatedChecks(t *testing.T) {
	t.Setenv("LEAPVIEW_API_TOKEN", "")
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/healthz", "/readyz":
			response.WriteHeader(http.StatusOK)
		case "/api/v1/instance":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"canonicalOrigin":"http://` + request.Host + `","environment":"dev","id":"instance-local"}`))
		default:
			t.Errorf("unexpected unauthenticated request to %s", request.URL.Path)
			http.NotFound(response, request)
		}
		if request.Header.Get("Authorization") != "" {
			t.Errorf("doctor sent credentials without a token: %q", request.Header.Get("Authorization"))
		}
	}))
	defer server.Close()
	report := runRemoteDoctor(context.Background(), doctorFlags{target: server.URL}).finish()
	require.Equal(t, "pass", report.Status)
	require.Equal(t, "skip", findDoctorCheck(t, report.Checks, "remote.capabilities").Status)
	require.Equal(t, "skip", findDoctorCheck(t, report.Checks, "remote.me").Status)
	mu.Lock()
	defer mu.Unlock()
	require.ElementsMatch(t, []string{"/healthz", "/readyz", "/api/v1/instance"}, paths)
}

func TestDoctorRemoteMeRequiresAPrincipalIdentity(t *testing.T) {
	for _, body := range []string{"{}", "<html>signed in</html>"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(body))
			}))
			defer server.Close()
			require.Error(t, checkRemoteMe(context.Background(), server.Client(), server.URL, "explicit-test-token"))
		})
	}
}

func TestDoctorRemoteMeSkipsWhenCapabilitiesAreIncompatible(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/healthz", "/readyz":
			response.WriteHeader(http.StatusOK)
		case "/api/v1/instance":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"canonicalOrigin":"http://` + request.Host + `","environment":"dev","id":"instance-local"}`))
		case "/api/v1/capabilities":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"apiVersion":"v2","environment":"dev"}`))
		default:
			t.Errorf("unexpected request to %s", request.URL.Path)
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	report := runRemoteDoctor(context.Background(), doctorFlags{target: server.URL, token: "explicit-test-token"}).finish()
	require.Equal(t, "fail", findDoctorCheck(t, report.Checks, "remote.capabilities").Status)
	require.Equal(t, "skip", findDoctorCheck(t, report.Checks, "remote.me").Status)
	mu.Lock()
	defer mu.Unlock()
	require.NotContains(t, paths, "/api/v1/me")
}

func TestDoctorRemoteProbeHonorsContextTimeout(t *testing.T) {
	entered := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(entered)
		<-request.Context().Done()
		close(finished)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client := &http.Client{Timeout: doctorProbeTimeout}
	_, _, err := remoteGet(ctx, client, server.URL+"/healthz", "")
	require.Error(t, err)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the test server")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("server handler did not stop after request cancellation")
	}
}

func TestDoctorExternalProbeDeadlineIsAtMostFiveSeconds(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		require.True(t, ok, "external probe has no deadline")
		require.Positive(t, time.Until(deadline))
		require.LessOrEqual(t, time.Until(deadline), doctorProbeTimeout)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	status, _, err := remoteGet(context.Background(), client, "https://target.example/healthz", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}

func TestDoctorDockerFailureDoesNotEchoCommandOutput(t *testing.T) {
	secret := "docker-output-secret-value"
	report := newDoctorReport()
	addDockerEndpointCheck(&report, localdocker.Endpoint{}, errors.New("Docker returned "+secret))
	encoded, err := json.Marshal(report.finish())
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
}

func TestDoctorNamedTargetLookupDoesNotCreateProfileStore(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "not-created", "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	_, err := resolveDoctorTarget("missing")
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Dir(configPath))
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

func projectDoctorCommand(sourceRoot, profileFile, profile string) *cobra.Command {
	command := &cobra.Command{Use: "doctor"}
	command.Flags().String("source-root", "", "source root")
	command.Flags().String("profile-file", "", "profile file")
	command.Flags().String("profile", "", "profile")
	if sourceRoot != "" {
		_ = command.Flags().Set("source-root", sourceRoot)
	}
	if profileFile != "" {
		_ = command.Flags().Set("profile-file", profileFile)
	}
	if profile != "" {
		_ = command.Flags().Set("profile", profile)
	}
	return command
}

func flagsFromCommand(command *cobra.Command) doctorFlags {
	flags := doctorFlags{format: "text", timeout: doctorDefaultTimeout}
	flags.sourceRoot, _ = command.Flags().GetString("source-root")
	flags.profileFile, _ = command.Flags().GetString("profile-file")
	flags.profile, _ = command.Flags().GetString("profile")
	flags.dockerContext, _ = command.Flags().GetString("docker-context")
	flags.dockerHost, _ = command.Flags().GetString("docker-host")
	flags.target, _ = command.Flags().GetString("target")
	flags.token, _ = command.Flags().GetString("token")
	flags.format, _ = command.Flags().GetString("format")
	flags.timeout, _ = command.Flags().GetDuration("timeout")
	return flags
}

func findDoctorCheck(t *testing.T, checks []doctorCheck, id string) doctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("doctor check %q was not emitted", id)
	return doctorCheck{}
}
