//go:build linux

package hostinstall

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func agentProviderLaunchFixture(t *testing.T) (*NativeEffects, dockerInspection) {
	t.Helper()
	_, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	r.AgentCredentialTransition = &intent
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	e := &NativeEffects{request: r, id: id, operation: t.TempDir(), agentCloneAppIP: "172.25.0.2"}
	var candidate dockerInspection
	candidate.HostConfig.ExtraHosts = []string{"provider.example:172.25.0.3"}
	return e, candidate
}

func TestAgentProviderLaunchReadsPrivateConfigWithoutRelaxingIsolation(t *testing.T) {
	e, candidate := agentProviderLaunchFixture(t)
	launched, removed := false, false
	e.execute = func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "inspect":
			return fmt.Sprintf(`[{"NetworkSettings":{"Networks":{%q:{"IPAddress":"172.25.0.2"}}}}]`, e.clonePrefix()), nil
		case "network":
			return "172.25.0.1", nil
		case "run":
			launched = true
			path := filepath.Join(e.operation, "agent-provider-relay.json")
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("relay configuration lost private permissions")
			}
			raw, err := os.ReadFile(path)
			var config agentRelayConfig
			if err != nil || json.Unmarshal(raw, &config) != nil || config.validate() != nil {
				t.Fatal("production launcher did not write valid configuration")
			}
			joined := " " + strings.Join(args, " ") + " "
			for _, required := range []string{" --user 0:0 ", " --read-only ", " --cap-drop=ALL ", " --security-opt=no-new-privileges ", " --restart=no ", " --network " + e.clonePrefix() + " ", " --ip 172.25.0.3 ", "type=bind,src=" + path + ",dst=/run/agent-relay.json,readonly", " --entrypoint /usr/local/libexec/leapviewctl " + e.id.Candidate + " host upgrade agent-relay --config /run/agent-relay.json "} {
				if !strings.Contains(joined, required) {
					t.Errorf("production relay launch missing required boundary: %s", required)
				}
			}
			return "", errors.New("stop at admitted launch boundary")
		case "rm":
			removed = true
			return "", nil
		default:
			t.Fatal("unexpected Docker operation")
			return "", nil
		}
	}
	if cleanup, err := e.startAgentCloneProvider(t.Context(), candidate); err == nil || cleanup != nil {
		t.Fatal("failed launch was admitted")
	}
	if !launched || !removed {
		t.Fatal("production launch or cleanup was not exercised")
	}
	if _, err := os.Stat(filepath.Join(e.operation, "agent-provider-relay.json")); !os.IsNotExist(err) {
		t.Fatal("failed launch retained private configuration")
	}
}

// The container wrapper forwards the production argv to the actual hidden CLI
// command, including its secure private-file reader. It supplies no credentials.
func TestAgentRelayProductionCommandHelper(t *testing.T) {
	if os.Getenv("LEAPVIEW_AGENT_RELAY_COMMAND_HELPER") != "1" {
		t.Skip("disposable container helper")
	}
	root := &cobra.Command{Use: "leapviewctl", SilenceErrors: true, SilenceUsage: true}
	host := &cobra.Command{Use: "host"}
	upgrade := &cobra.Command{Use: "upgrade"}
	root.AddCommand(host)
	host.AddCommand(upgrade)
	addAgentRelayCommand(t.Context(), upgrade, io.Discard)
	for i, arg := range os.Args {
		if arg == "--" {
			root.SetArgs(os.Args[i+1:])
			if root.Execute() != nil {
				t.Fatal("production relay command unavailable")
			}
			return
		}
	}
	t.Fatal("production command arguments missing")
}

// Engine 28 requires an explicitly configured subnet for static container IPs.
// Let Docker choose an available pool, then make only this empty owned network's
// exact allocation explicit before attaching any fixture containers.
func agentProviderFixtureNetwork(ctx context.Context, run func(context.Context, ...string) (string, error), name string) (netip.Addr, error) {
	if _, err := run(ctx, "network", "create", "--internal", name); err != nil {
		return netip.Addr{}, errors.New("private fixture network creation failed")
	}
	raw, err := run(ctx, "network", "inspect", name, "--format", "{{json .IPAM.Config}}")
	var config []struct{ Subnet, Gateway string }
	if err != nil || len(raw) > 2048 || json.Unmarshal([]byte(raw), &config) != nil || len(config) != 1 {
		return netip.Addr{}, errors.New("private fixture network allocation unavailable")
	}
	subnet, subnetErr := netip.ParsePrefix(config[0].Subnet)
	gateway, gatewayErr := netip.ParseAddr(config[0].Gateway)
	if subnetErr != nil || gatewayErr != nil || !subnet.Addr().Is4() || !subnet.Addr().IsPrivate() || subnet != subnet.Masked() || subnet.Bits() > 29 || !agentUsableCloneAddress(subnet, gateway.String()) || !agentUsableCloneAddress(subnet, gateway.Next().String()) || !agentUsableCloneAddress(subnet, gateway.Next().Next().String()) {
		return netip.Addr{}, errors.New("private fixture network allocation invalid")
	}
	if _, err := run(ctx, "network", "rm", name); err != nil {
		return netip.Addr{}, errors.New("empty fixture network removal failed")
	}
	if _, err := run(ctx, "network", "create", "--internal", "--subnet", subnet.String(), "--gateway", gateway.String(), name); err != nil {
		return netip.Addr{}, errors.New("explicit private fixture network creation failed")
	}
	return gateway, nil
}

func TestAgentProviderFixtureUsesExplicitPrivateAllocation(t *testing.T) {
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "inspect" {
			return `[{"Subnet":"172.25.0.0/16","Gateway":"172.25.0.1"}]`, nil
		}
		return "", nil
	}
	gateway, err := agentProviderFixtureNetwork(t.Context(), run, "owned-fixture")
	if err != nil || gateway.String() != "172.25.0.1" {
		t.Fatal("valid private allocation rejected")
	}
	want := []string{"network create --internal owned-fixture", "network inspect owned-fixture --format {{json .IPAM.Config}}", "network rm owned-fixture", "network create --internal --subnet 172.25.0.0/16 --gateway 172.25.0.1 owned-fixture"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatal("fixture did not explicitly preserve its owned subnet and gateway")
	}
	for _, invalid := range []string{`[]`, `[{},{}]`, `[{"Subnet":"1.1.1.0/24","Gateway":"1.1.1.1"}]`, `[{"Subnet":"172.25.0.0/16","Gateway":"172.26.0.1"}]`, `[{"Subnet":"172.25.0.0/31","Gateway":"172.25.0.1"}]`, `[{"Subnet":"172.25.0.0/16","Gateway":"172.25.0.0"}]`, `[{"Subnet":"172.25.0.0/16","Gateway":"172.25.255.253"}]`, `[{"Subnet":"172.25.0.0/16","Gateway":"172.25.255.255"}]`, `[{"Subnet":"fd00::/64","Gateway":"fd00::1"}]`} {
		mutated := false
		run := func(_ context.Context, args ...string) (string, error) {
			if args[1] == "inspect" {
				return invalid, nil
			}
			if args[1] == "rm" || len(args) > 4 {
				mutated = true
			}
			return "", nil
		}
		if _, err := agentProviderFixtureNetwork(t.Context(), run, "owned-fixture"); err == nil || mutated {
			t.Fatal("invalid fixture allocation reached network recreation")
		}
	}
}

func agentProviderFixtureFailure(output string) string {
	switch {
	case strings.Contains(output, "user configured subnets"):
		return "static_ip_requires_explicit_subnet"
	case strings.Contains(output, "no such file or directory"), strings.Contains(output, "exec format error"):
		return "executable_unavailable"
	case strings.Contains(output, "permission denied"):
		return "permission_denied"
	default:
		return "other_setup_failure"
	}
}

func TestAgentProviderFixtureFailureIsSanitized(t *testing.T) {
	for _, tc := range []struct{ output, want string }{
		{"user configured subnets: private-canary", "static_ip_requires_explicit_subnet"},
		{"no such file or directory: private-canary", "executable_unavailable"},
		{"permission denied: private-canary", "permission_denied"},
		{"arbitrary private-canary", "other_setup_failure"},
	} {
		if agentProviderFixtureFailure(tc.output) != tc.want {
			t.Fatal("fixture setup diagnostic escaped its fixed classification")
		}
	}
}

func TestAgentProviderPrivateConfigContainer(t *testing.T) {
	if os.Getenv("LEAPVIEW_HOST_UPGRADE_QUALIFICATION") != "1" {
		t.Skip("explicit disposable host recovery qualification")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("Docker executable unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	run := func(ctx context.Context, args ...string) (string, error) {
		raw, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		return strings.TrimSpace(string(raw)), err
	}
	e, candidate := agentProviderLaunchFixture(t)
	var operationNonce [32]byte
	if _, err := rand.Read(operationNonce[:]); err != nil {
		t.Fatal("unique fixture operation unavailable")
	}
	e.id.ArtifactAdmissionDigest = fmt.Sprintf("sha256:%x", operationNonce[:])
	prefix := e.clonePrefix()
	image := prefix + ":fixture"
	e.id.Candidate = image
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, name := range []string{prefix + "-provider", prefix + "-app"} {
			_, _ = run(cleanupCtx, "rm", "-f", "-v", name)
		}
		_, _ = run(cleanupCtx, "network", "rm", prefix)
		_, _ = run(cleanupCtx, "image", "rm", "-f", image)
	}()
	build := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("compiled test executable unavailable")
	}
	in, err := os.Open(binary)
	if err != nil {
		t.Fatal("compiled test executable unavailable")
	}
	out, err := os.OpenFile(filepath.Join(build, "fixture"), os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		in.Close()
		t.Fatal("fixture build unavailable")
	}
	_, copyErr := io.Copy(out, in)
	in.Close()
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("fixture executable copy failed")
	}
	const base = "public.ecr.aws/docker/library/golang@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61"
	dockerfile := "FROM " + base + "\nCOPY fixture /fixture\nCOPY leapviewctl /usr/local/libexec/leapviewctl\nENV LEAPVIEW_AGENT_RELAY_COMMAND_HELPER=1\nUSER 999:999\n"
	if os.WriteFile(filepath.Join(build, "Dockerfile"), []byte(dockerfile), 0600) != nil || os.WriteFile(filepath.Join(build, "leapviewctl"), []byte("#!/bin/sh\nexec /fixture -test.run=^TestAgentRelayProductionCommandHelper$ -- \"$@\"\n"), 0755) != nil {
		t.Fatal("fixture image setup failed")
	}
	if _, err := run(ctx, "build", "--network=none", "-t", image, build); err != nil {
		t.Fatal("fixture image build failed")
	}
	if user, err := run(ctx, "image", "inspect", image, "--format", "{{.Config.User}}"); err != nil || user != "999:999" {
		t.Fatal("fixture does not retain production default UID")
	}
	address, err := agentProviderFixtureNetwork(ctx, run, prefix)
	if err != nil {
		t.Fatal("private fixture network setup failed")
	}
	e.agentCloneAppIP = address.Next().String()
	sidecarIP := address.Next().Next().String()
	candidate.HostConfig.ExtraHosts = []string{"provider.example:" + sidecarIP}
	if output, err := run(ctx, "run", "-d", "--name", prefix+"-app", "--network", prefix, "--ip", e.agentCloneAppIP, "--entrypoint", "/bin/sh", image, "-c", "sleep 180"); err != nil {
		t.Fatal("default-UID app fixture failed: " + agentProviderFixtureFailure(output))
	}
	e.execute = func(commandCtx context.Context, args ...string) (string, error) {
		if args[0] == "run" {
			path := filepath.Join(e.operation, "agent-provider-relay.json")
			// Native maintenance runs as root. Reproduce that exact ownership even
			// when the qualification worker itself has an unprivileged host UID.
			if _, err := run(commandCtx, "run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL", "--cap-add=CHOWN", "--security-opt=no-new-privileges", "--user", "0:0", "--mount", "type=bind,src="+path+",dst=/config", "--entrypoint", "/bin/chown", image, "0:0", "/config"); err != nil {
				return "", errors.New("root-owned fixture setup failed")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0 {
				return "", errors.New("root-private fixture admission failed")
			}
			// The same actual command must fail under the image's default UID.
			negative := append([]string{"run", "--rm", "--network", prefix, "--ip", sidecarIP, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--sysctl", "net.ipv4.ip_unprivileged_port_start=0", "--mount", "type=bind,src=" + path + ",dst=/run/agent-relay.json,readonly", "--entrypoint", "/usr/local/libexec/leapviewctl", image}, "host", "upgrade", "agent-relay", "--config", "/run/agent-relay.json")
			if output, err := run(commandCtx, negative...); err == nil || !strings.Contains(output, "production relay command unavailable") {
				return "", errors.New("default UID did not fail in the actual relay command")
			}
		}
		return run(commandCtx, args...)
	}
	cleanup, err := e.startAgentCloneProvider(ctx, candidate)
	if err != nil {
		t.Fatal("production launch cannot read root-private configuration")
	}
	if user, err := run(ctx, "inspect", prefix+"-provider", "--format", "{{.Config.User}}"); err != nil || user != "0:0" {
		t.Error("relay did not use explicit private-config identity")
	}
	if user, err := run(ctx, "inspect", prefix+"-app", "--format", "{{.Config.User}}"); err != nil || user != "999:999" {
		t.Error("app default identity changed")
	}
	if err := cleanup(); err != nil {
		t.Fatal("relay cleanup did not join")
	}
	if _, err := os.Stat(filepath.Join(e.operation, "agent-provider-relay.json")); !os.IsNotExist(err) {
		t.Fatal("relay cleanup retained private configuration")
	}
	if _, err := run(ctx, "inspect", prefix+"-provider"); err == nil {
		t.Fatal("relay cleanup retained container")
	}
}
