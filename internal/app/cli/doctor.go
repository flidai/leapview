package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	accessapigen "github.com/flidai/leapview/internal/access/api/gen"
	analyticsenvironment "github.com/flidai/leapview/internal/analytics/environment"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	developmentinput "github.com/flidai/leapview/internal/project/developmentinput"
	developmentprofile "github.com/flidai/leapview/internal/project/developmentprofile"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

const (
	doctorDefaultTimeout = 30 * time.Second
	doctorMaxTimeout     = 30 * time.Second
	doctorProbeTimeout   = 5 * time.Second
	doctorSchemaVersion  = 1
)

type doctorFlags struct {
	sourceRoot    string
	profileFile   string
	profile       string
	dockerContext string
	dockerHost    string
	target        string
	token         string
	format        string
	timeout       time.Duration
}

type doctorCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Remedy  string `json:"remedy,omitempty"`
}

type doctorReport struct {
	SchemaVersion int           `json:"schemaVersion"`
	Status        string        `json:"status"`
	Checks        []doctorCheck `json:"checks"`
	Summary       string        `json:"summary"`
}

func doctorCommand(ctx context.Context) *cobra.Command {
	flags := doctorFlags{format: "text", timeout: doctorDefaultTimeout}
	command := &cobra.Command{
		Use:     "doctor",
		Short:   "Inspect local development prerequisites or an explicitly selected target",
		Long:    "Inspect local authoring prerequisites without starting containers, repairing state, or connecting to upstream data. Add --target for remote readiness and identity checks; authentication is checked only with an explicit API token or LEAPVIEW_API_TOKEN. Skipped checks remain unverified.",
		Example: "  leapview doctor\n  leapview doctor --source-root ./dashboards --format json\n  leapview doctor --target production --format json",
		Args:    cobra.NoArgs,
		Annotations: map[string]string{
			documentationEffectAnnotation:       "read",
			documentationConfirmationAnnotation: "never",
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateDoctorInvocation(command, flags); err != nil {
				return cliapi.NewUsageError(err)
			}
			budget, cancel := context.WithTimeout(ctx, flags.timeout)
			defer cancel()
			var report doctorReport
			if command.Flags().Changed("target") {
				report = runRemoteDoctor(budget, flags)
			} else {
				report = runLocalDoctor(budget, command, flags)
			}
			if err := budget.Err(); err != nil {
				addDoctorTimeoutCheck(&report, err)
			}
			if err := writeDoctorReport(command.OutOrStdout(), flags.format, report); err != nil {
				return err
			}
			if report.Status == "fail" {
				return cliapi.NewReportedError(errors.New("doctor found required checks that failed"))
			}
			return nil
		},
	}
	command.Flags().StringVar(&flags.sourceRoot, "source-root", "", "analytics source root to compile")
	command.Flags().StringVar(&flags.profileFile, "profile-file", "", "local development profile file")
	command.Flags().StringVar(&flags.profile, "profile", "", "local development profile name")
	command.Flags().StringVar(&flags.dockerContext, "docker-context", "", "explicit local Docker context")
	command.Flags().StringVar(&flags.dockerHost, "docker-host", "", "explicit local Docker Unix socket")
	command.Flags().StringVar(&flags.target, "target", "", "explicit remote target URL or saved profile name")
	command.Flags().StringVar(&flags.token, "token", "", "API token for the explicitly selected remote target")
	command.Flags().StringVar(&flags.format, "format", flags.format, "report format: text or json")
	command.Flags().DurationVar(&flags.timeout, "timeout", flags.timeout, "overall diagnostic time budget (maximum 30s)")
	return command
}

func validateDoctorInvocation(command *cobra.Command, flags doctorFlags) error {
	if flags.format != "text" && flags.format != "json" {
		return fmt.Errorf("--format must be text or json")
	}
	if flags.timeout <= 0 || flags.timeout > doctorMaxTimeout {
		return fmt.Errorf("--timeout must be greater than zero and no more than %s", doctorMaxTimeout)
	}
	remote := command.Flags().Changed("target")
	for _, name := range []string{"source-root", "profile-file", "profile", "docker-context", "docker-host"} {
		if remote && command.Flags().Changed(name) {
			return fmt.Errorf("--%s is a local selector and cannot be combined with remote --target", name)
		}
	}
	if command.Flags().Changed("token") && (!remote || strings.TrimSpace(flags.token) == "") {
		return errors.New("--token requires a non-empty explicit remote --target")
	}
	if remote {
		if strings.TrimSpace(flags.target) == "" {
			return errors.New("explicit --target must not be empty")
		}
		return nil
	}
	for _, selector := range []struct{ name, value string }{
		{"source-root", flags.sourceRoot}, {"profile-file", flags.profileFile}, {"profile", flags.profile},
		{"docker-context", flags.dockerContext}, {"docker-host", flags.dockerHost},
	} {
		if command.Flags().Changed(selector.name) && strings.TrimSpace(selector.value) == "" {
			return fmt.Errorf("--%s must not be empty", selector.name)
		}
	}
	if command.Flags().Changed("docker-context") && command.Flags().Changed("docker-host") {
		return errors.New("choose either --docker-context or --docker-host, not both")
	}
	return nil
}

func runLocalDoctor(ctx context.Context, command *cobra.Command, flags doctorFlags) doctorReport {
	report := newDoctorReport()
	identity := buildinfo.Current()
	report.add("local.identity", "pass", fmt.Sprintf("LeapView %s is running.", identity.Version), "")
	platformOK := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	if platformOK {
		report.add("local.platform", "pass", fmt.Sprintf("%s/%s supports local Docker development.", runtime.GOOS, runtime.GOARCH), "")
	} else {
		report.add("local.platform", "fail", fmt.Sprintf("Local Docker development is unsupported on %s/%s.", runtime.GOOS, runtime.GOARCH), "Run local development on Linux or macOS.")
	}

	packageInfo, packageErr := localruntime.InspectRuntimePackage("", identity)
	if packageErr == nil {
		report.add("local.runtime_package", "pass", fmt.Sprintf("Runtime package matches LeapView %s.", packageInfo.Version), "")
	} else {
		report.add("local.runtime_package", "fail", "The installed local runtime package is missing, invalid, or does not match this CLI.", "Install a clean LeapView release with its matching local runtime package.")
	}

	var endpoint localdocker.Endpoint
	var endpointErr error
	if !platformOK {
		report.add("local.docker_endpoint", "skip", "Docker endpoint verification requires a supported local platform.", "")
	} else {
		probe, cancel := probeContext(ctx)
		endpoint, endpointErr = localdocker.Resolve(probe, localdocker.Options{
			ExplicitContext: flags.dockerContext,
			ExplicitHost:    flags.dockerHost,
		})
		cancel()
		addDockerEndpointCheck(&report, endpoint, endpointErr)
	}

	composeOK := false
	if !platformOK || endpointErr != nil {
		report.add("local.docker_compose", "skip", "Compose verification requires a verified local Docker endpoint.", "")
	} else if packageErr != nil {
		report.add("local.docker_compose", "skip", "Compose verification requires a valid matching runtime package.", "")
	} else if composeVersion, err := inspectComposeVersion(ctx, endpoint, packageInfo.ComposeMinimumVersion); err != nil {
		report.add("local.docker_compose", "fail", "Docker Compose is unavailable or below the runtime package minimum.", "Install or upgrade the Docker Compose plugin for the selected local endpoint.")
	} else {
		composeOK = true
		report.add("local.docker_compose", "pass", fmt.Sprintf("Docker Compose %s meets the required version.", composeVersion), "")
	}

	runLocalProjectChecks(ctx, command, flags, &report)

	if !platformOK || endpointErr != nil || packageErr != nil || !composeOK {
		report.add("local.runtime_state", "skip", "Saved local runtime inspection requires a supported platform, matching runtime package, Docker endpoint, and Compose.", "")
	} else {
		status, err := localruntime.Inspect(ctx, localruntime.InspectionOptions{
			Endpoint: endpoint, BuildIdentity: identity,
		})
		if ctx.Err() != nil {
			report.add("local.runtime_state", "fail", "Saved local runtime inspection was cancelled or timed out.", "Check the Docker and state filesystem latency, then rerun leapview doctor with --timeout up to 30s.")
		} else if err != nil {
			report.add("local.runtime_state", "fail", "Saved local runtime state, attachments, or services could not be safely inspected.", "Review the local runtime state and rerun leapview doctor.")
		} else if !status.Exists {
			report.add("local.runtime_state", "skip", "No saved local runtime exists for this checkout.", "Run leapview dev to create one when local development is needed.")
		} else if status.Phase == "reset" {
			report.add("local.runtime_state", "warn", "A local runtime reset is in progress.", "Resume the reset with leapview dev reset.")
		} else if status.RuntimeStatus != "applied" || !allServicesRunning(status.Services) {
			report.add("local.runtime_state", "warn", "The saved local runtime is incomplete or has services that are not running.", "Run leapview dev to resume or start the local runtime.")
		} else {
			report.add("local.runtime_state", "pass", fmt.Sprintf("Saved runtime is applied with %d service(s) and %d active attachment(s).", len(status.Services), len(status.Attachments)), "")
		}
	}
	return report.finish()
}

func inspectComposeVersion(ctx context.Context, endpoint localdocker.Endpoint, minimum string) (string, error) {
	probe, cancel := probeContext(ctx)
	defer cancel()
	if err := endpoint.Verify(probe); err != nil {
		return "", err
	}
	command := exec.CommandContext(probe, "docker", endpoint.DockerArguments("compose", "version", "--short")...)
	command.Env = endpoint.Environment(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	actual := strings.TrimPrefix(strings.TrimSpace(string(output)), "v")
	if !semver.IsValid("v"+actual) || !semver.IsValid("v"+minimum) || semver.Compare("v"+actual, "v"+minimum) < 0 {
		return "", fmt.Errorf("Docker Compose version %q does not meet minimum %q", actual, minimum)
	}
	return actual, nil
}

func addDockerEndpointCheck(report *doctorReport, endpoint localdocker.Endpoint, err error) {
	if err != nil {
		report.add("local.docker_endpoint", "fail", "A verified local Docker Engine endpoint is unavailable.", "Start Docker Engine or select a supported local endpoint with --docker-context or --docker-host.")
		return
	}
	report.add("local.docker_endpoint", "pass", fmt.Sprintf("Verified local Docker endpoint (%s).", endpoint.Kind()), "")
}

func runLocalProjectChecks(ctx context.Context, command *cobra.Command, flags doctorFlags, report *doctorReport) {
	invocation, err := os.Getwd()
	if err != nil {
		addSkippedProjectChecks(report, "The current project directory could not be resolved.")
		return
	}
	checkout, err := discoverLocalCheckout(invocation)
	if err != nil {
		addSkippedProjectChecks(report, "The current checkout could not be resolved.")
		return
	}
	explicit := command.Flags().Changed("source-root") || command.Flags().Changed("profile-file") || command.Flags().Changed("profile")
	marker := filepath.Join(checkout, filepath.FromSlash(developmentinput.DefaultRelativePath))
	if !explicit {
		info, statErr := os.Lstat(marker)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) || statErr == nil && !info.Mode().IsRegular() {
			addFailProjectChecks(report, "The project initialization marker could not be safely inspected.", "Check the project marker's file type and permissions, then rerun leapview doctor.")
			return
		}
		if statErr != nil {
			found, err := hasAuthoredDashboardSources(filepath.Join(checkout, "dashboards"))
			if err != nil {
				addFailProjectChecks(report, "The analytics source tree could not be inspected.", "Check the dashboards directory's file type and permissions, then rerun leapview doctor.")
				return
			}
			if !found {
				addSkippedProjectChecks(report, "No initialized LeapView project is present in this checkout.")
				return
			}
		}
	}

	sourceRoot := flags.sourceRoot
	if sourceRoot == "" {
		sourceRoot = filepath.Join(checkout, "dashboards")
	} else if !filepath.IsAbs(sourceRoot) {
		sourceRoot = filepath.Join(invocation, sourceRoot)
	}
	sourceRoot, err = filepath.Abs(sourceRoot)
	if err != nil {
		addFailProjectChecks(report, "The selected analytics source root is invalid.", "Pass an existing analytics source root with --source-root.")
		return
	}
	if err := ctx.Err(); err != nil {
		addFailProjectChecks(report, "Analytics source compilation was cancelled or timed out.", "Check the source filesystem and rerun leapview doctor with a time budget up to --timeout 30s.")
		return
	}
	bundle, err := compileDoctorGraph(ctx, func() (projectartifact.SourceBundle, error) {
		return compileStableProfileGraph(filepath.Clean(sourceRoot))
	})
	if err := ctx.Err(); err != nil {
		addFailProjectChecks(report, "Analytics source compilation was cancelled or timed out.", "Check the source filesystem and rerun leapview doctor with a time budget up to --timeout 30s.")
		return
	}
	if err != nil {
		addFailProjectChecks(report, "The selected analytics source root does not compile.", "Fix the authored analytics resources and rerun leapview doctor.")
		return
	}
	report.add("project.compiler", "pass", "The analytics source root compiles.", "")
	catalog, err := developmentprofile.CatalogFromManifest(bundle.Manifest())
	if ctx.Err() != nil {
		addProfileTimeoutChecks(report)
		return
	}
	if err != nil {
		report.add("project.profile", "fail", "The compiled connection catalog is invalid for development profile selection.", "Fix the analytics connection declarations and rerun leapview doctor.")
		report.add("project.credentials", "skip", "Credential checks require a valid selected development profile.", "")
		return
	}
	selected, err := developmentprofile.Load(developmentprofile.LoadOptions{
		CheckoutRoot: checkout, InvocationDirectory: invocation, SourceRoot: sourceRoot,
		ProfileFile: flags.profileFile, ProfileName: flags.profile, Connections: catalog,
	})
	if ctx.Err() != nil {
		addProfileTimeoutChecks(report)
		return
	}
	if err != nil {
		report.add("project.profile", "fail", "The selected development profile is missing or invalid.", "Correct --profile-file or --profile and rerun leapview doctor.")
		report.add("project.credentials", "skip", "Credential checks require a valid selected development profile.", "")
		return
	}
	report.add("project.profile", "pass", "The selected development profile is valid.", "")
	for _, connection := range selected.Connections {
		name := connection.Credentials.EnvironmentVariable
		if name == "" {
			continue
		}
		value, exists := os.LookupEnv(name)
		validationErr := error(nil)
		if exists {
			validationErr = analyticsenvironment.ValidateCredentialBundle(value)
		}
		if ctx.Err() != nil {
			addCredentialTimeoutCheck(report)
			return
		}
		if !exists || validationErr != nil {
			report.add("project.credentials", "fail", "A required development credential bundle is missing or has an invalid shape; values are never displayed.", fmt.Sprintf("Set %s to a valid credential bundle; its value is never displayed.", name))
			return
		}
	}
	if ctx.Err() != nil {
		addCredentialTimeoutCheck(report)
		return
	}
	report.add("project.credentials", "pass", "Required development credential bundles are present and valid; values are never displayed.", "")
}

func addProfileTimeoutChecks(report *doctorReport) {
	report.add("project.profile", "fail", "Development profile validation was cancelled or timed out.", "Check the profile filesystem latency and rerun leapview doctor with --timeout up to 30s.")
	report.add("project.credentials", "skip", "Credential checks require a valid selected development profile.", "")
}

func addCredentialTimeoutCheck(report *doctorReport) {
	report.add("project.credentials", "fail", "Development credential validation was cancelled or timed out; values are never displayed.", "Check the environment source and rerun leapview doctor with --timeout up to 30s.")
}

func addDoctorTimeoutCheck(report *doctorReport, cause error) {
	for _, check := range report.Checks {
		if check.ID == "doctor.timeout" {
			return
		}
	}
	summary := "The overall doctor time budget expired before all checks completed."
	remedy := "Check Docker or source filesystem latency, then rerun leapview doctor with --timeout up to 30s."
	if !errors.Is(cause, context.DeadlineExceeded) {
		summary = "Doctor checks were cancelled before completion."
		remedy = "Rerun leapview doctor when the cancellation condition has passed."
	}
	report.add("doctor.timeout", "fail", summary, remedy)
	*report = report.finish()
}

func addSkippedProjectChecks(report *doctorReport, summary string) {
	report.add("project.compiler", "skip", summary, "")
	report.add("project.profile", "skip", "Profile validation requires an initialized project or an explicit local selector.", "")
	report.add("project.credentials", "skip", "Credential validation requires a selected development profile.", "")
}

func addFailProjectChecks(report *doctorReport, summary, remedy string) {
	report.add("project.compiler", "fail", summary, remedy)
	report.add("project.profile", "skip", "Profile validation requires a compiled analytics source root.", "")
	report.add("project.credentials", "skip", "Credential validation requires a valid selected development profile.", "")
}

func allServicesRunning(services map[string]string) bool {
	if len(services) == 0 {
		return false
	}
	for _, status := range services {
		if status != "running" {
			return false
		}
	}
	return true
}

type selectedDoctorTarget struct {
	origin  string
	profile *cliapi.TargetProfile
}

func runRemoteDoctor(ctx context.Context, flags doctorFlags) doctorReport {
	report := newDoctorReport()
	identity := buildinfo.Current()
	report.add("remote.client_identity", "pass", fmt.Sprintf("LeapView %s is running.", identity.Version), "")
	report.add("remote.client_platform", "pass", fmt.Sprintf("Remote checks can run from %s/%s.", runtime.GOOS, runtime.GOARCH), "")
	target, err := resolveDoctorTarget(flags.target)
	if err != nil {
		report.add("remote.target", "fail", "The explicit remote target URL or saved profile could not be resolved.", "Pass an absolute HTTPS URL, a loopback HTTP URL, or an existing saved target profile name.")
		report.add("remote.health", "skip", "Remote probes require a valid explicit target.", "")
		addSkippedRemoteChecks(&report)
		return report.finish()
	}
	report.add("remote.target", "pass", "Using the explicitly selected remote target.", "")
	client := &http.Client{Timeout: doctorProbeTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	if err := checkRemoteHealth(ctx, client, target.origin); err != nil {
		report.add("remote.health", "fail", "The remote health or readiness endpoint did not return success.", "Check the target address and wait for the LeapView server to become ready.")
		report.add("remote.identity", "skip", "Identity checks require a healthy target.", "")
		addSkippedRemoteChecks(&report)
		return report.finish()
	}
	report.add("remote.health", "pass", "The remote health and readiness endpoints are responding.", "")
	instance, err := checkRemoteIdentity(ctx, client, target)
	if err != nil {
		report.add("remote.identity", "fail", "The remote public instance identity is invalid or does not match the selected target profile.", "Verify that the selected target profile still names the intended LeapView instance and environment.")
		addSkippedRemoteChecks(&report)
		return report.finish()
	}
	identityStatus := "pass"
	identitySummary := "The remote instance identity matches the selected target."
	if target.profile != nil && strings.TrimSpace(target.profile.Environment) == "" {
		identityStatus = "warn"
		identitySummary = "The saved target profile matches the remote instance, but has no saved environment to compare."
	}
	report.add("remote.identity", identityStatus, identitySummary, "")
	token := flags.token
	if token == "" {
		token = strings.TrimSpace(os.Getenv("LEAPVIEW_API_TOKEN"))
	}
	if token == "" {
		report.add("remote.capabilities", "skip", "Capability checks require an explicit --token or LEAPVIEW_API_TOKEN.", "Pass a scoped API token to check authenticated target capabilities.")
		report.add("remote.me", "skip", "Current-principal checks require an explicit --token or LEAPVIEW_API_TOKEN.", "Pass a scoped API token to check the authenticated principal.")
		return report.finish()
	}
	capabilities, err := checkRemoteCapabilities(ctx, client, target.origin, token)
	capabilitiesCompatible := err == nil && strings.TrimSpace(capabilities.Environment) == strings.TrimSpace(instance.Environment)
	if err != nil {
		report.add("remote.capabilities", "fail", "The API token could not read compatible target capabilities.", "Check the token and target API version and environment.")
	} else if !capabilitiesCompatible {
		report.add("remote.capabilities", "fail", "Target capability and instance environments do not match.", "Verify the selected target and its deployment configuration.")
	} else {
		report.add("remote.capabilities", "pass", "The API token can read capabilities for the target environment.", "")
	}
	if !capabilitiesCompatible {
		report.add("remote.me", "skip", "Current-principal checks require compatible target capabilities.", "")
		return report.finish()
	}
	if err := checkRemoteMe(ctx, client, target.origin, token); err != nil {
		report.add("remote.me", "fail", "The API token could not read the current-principal endpoint.", "Check the token's authentication status and retry.")
	} else {
		report.add("remote.me", "pass", "The API token can read the current-principal endpoint.", "")
	}
	return report.finish()
}

func hasAuthoredDashboardSources(sourceRoot string) (bool, error) {
	for _, section := range []string{"connections", "sources", "models", "semantic-models", "pipelines", "dashboards"} {
		root := filepath.Join(sourceRoot, section)
		found := false
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, fs.ErrNotExist) && path == root {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(path))
			if extension != ".yaml" && extension != ".yml" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				found = true
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func resolveDoctorTarget(value string) (selectedDoctorTarget, error) {
	if origin, err := canonicalDoctorOrigin(value); err == nil {
		return selectedDoctorTarget{origin: origin}, nil
	}
	path := strings.TrimSpace(os.Getenv("LEAPVIEW_CLI_CONFIG"))
	if path == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return selectedDoctorTarget{}, err
		}
		path = filepath.Join(configDir, "leapview", "cli.json")
	}
	profile, err := cliapi.NewProfileStore(path).Get(value)
	if err != nil {
		return selectedDoctorTarget{}, err
	}
	origin, err := canonicalDoctorOrigin(profile.Origin)
	if err != nil {
		return selectedDoctorTarget{}, err
	}
	return selectedDoctorTarget{origin: origin, profile: &profile}, nil
}

func canonicalDoctorOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("target origin must be an absolute URL without credentials, path, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isDoctorLoopback(parsed.Hostname())) {
		return "", errors.New("target origin must use HTTPS; HTTP is allowed only for loopback")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isDoctorLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func checkRemoteHealth(ctx context.Context, client *http.Client, origin string) error {
	for _, path := range []string{"/healthz", "/readyz"} {
		status, _, err := remoteGet(ctx, client, origin+path, "")
		if err != nil || status < http.StatusOK || status >= http.StatusMultipleChoices {
			return errors.New("remote health probe failed")
		}
	}
	return nil
}

func checkRemoteIdentity(ctx context.Context, client *http.Client, target selectedDoctorTarget) (apigenapi.InstanceResponse, error) {
	var instance apigenapi.InstanceResponse
	if _, err := remoteJSON(ctx, client, target.origin+"/api/v1/instance", "", &instance); err != nil {
		return apigenapi.InstanceResponse{}, err
	}
	canonical, err := canonicalDoctorOrigin(instance.CanonicalOrigin)
	if err != nil || canonical != target.origin || strings.TrimSpace(instance.Id) == "" || instance.Id != strings.TrimSpace(instance.Id) || instance.Environment == "" || instance.Environment != strings.TrimSpace(instance.Environment) {
		return apigenapi.InstanceResponse{}, errors.New("remote instance identity is incomplete or inconsistent")
	}
	if target.profile != nil && (instance.Id != strings.TrimSpace(target.profile.InstanceID) ||
		canonical != strings.TrimRight(strings.TrimSpace(target.profile.Origin), "/") ||
		(strings.TrimSpace(target.profile.Environment) != "" && instance.Environment != strings.TrimSpace(target.profile.Environment))) {
		return apigenapi.InstanceResponse{}, errors.New("remote instance identity differs from saved target profile")
	}
	return instance, nil
}

func checkRemoteCapabilities(ctx context.Context, client *http.Client, origin, token string) (apigenapi.CapabilitiesResponse, error) {
	var capabilities apigenapi.CapabilitiesResponse
	if _, err := remoteJSON(ctx, client, origin+"/api/v1/capabilities", token, &capabilities); err != nil {
		return apigenapi.CapabilitiesResponse{}, err
	}
	if strings.TrimSpace(capabilities.ApiVersion) != "v1" {
		return apigenapi.CapabilitiesResponse{}, errors.New("target API version is incompatible")
	}
	return capabilities, nil
}

func checkRemoteMe(ctx context.Context, client *http.Client, origin, token string) error {
	var principal accessapigen.CurrentPrincipalResponse
	if _, err := remoteJSON(ctx, client, origin+"/api/v1/me", token, &principal); err != nil || strings.TrimSpace(principal.Id) == "" {
		return errors.New("current-principal request failed")
	}
	return nil
}

func remoteJSON(ctx context.Context, client *http.Client, address, token string, out any) (int, error) {
	status, body, err := remoteGet(ctx, client, address, token)
	if err != nil {
		return 0, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return status, fmt.Errorf("remote endpoint returned HTTP %d", status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return status, errors.New("remote endpoint returned invalid JSON")
	}
	return status, nil
}

func remoteGet(ctx context.Context, client *http.Client, address, token string) (int, []byte, error) {
	probe, cancel := probeContext(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(probe, http.MethodGet, address, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, errors.New("remote endpoint could not be reached")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, errors.New("remote endpoint response could not be read")
	}
	return response.StatusCode, body, nil
}

func addSkippedRemoteChecks(report *doctorReport) {
	report.add("remote.capabilities", "skip", "Capability checks require a verified target identity and API token.", "")
	report.add("remote.me", "skip", "Current-principal checks require a verified target identity and API token.", "")
}

func probeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, doctorProbeTimeout)
}

func newDoctorReport() doctorReport {
	return doctorReport{SchemaVersion: doctorSchemaVersion, Checks: []doctorCheck{}}
}

func (report *doctorReport) add(id, status, summary, remedy string) {
	report.Checks = append(report.Checks, doctorCheck{ID: id, Status: status, Summary: summary, Remedy: remedy})
}

func (report doctorReport) finish() doctorReport {
	failures, warnings := 0, 0
	for _, check := range report.Checks {
		switch check.Status {
		case "fail":
			failures++
		case "warn":
			warnings++
		}
	}
	switch {
	case failures > 0:
		report.Status = "fail"
		report.Summary = fmt.Sprintf("%d required check(s) failed.", failures)
	case warnings > 0:
		report.Status = "warn"
		report.Summary = "Checks completed with warnings."
	default:
		report.Status = "pass"
		report.Summary = "All required checks passed."
	}
	return report
}

func writeDoctorReport(out io.Writer, format string, report doctorReport) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	for _, check := range report.Checks {
		label := strings.ToUpper(check.Status)
		if _, err := fmt.Fprintf(out, "%s %-27s %s\n", label, check.ID, check.Summary); err != nil {
			return err
		}
		if check.Remedy != "" && (check.Status == "fail" || check.Status == "warn" || check.Status == "skip") {
			if _, err := fmt.Fprintf(out, "  Remedy: %s\n", check.Remedy); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(out, "\n%s\n", report.Summary)
	return err
}
