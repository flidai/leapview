//go:build linux

package hostinstall

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/outbound"
)

// The disposable containers run the compiled package's actual network helpers,
// not a second proof implementation. No provider key is needed or supplied.
func TestAgentRelayDockerHelper(t *testing.T) {
	mode := os.Getenv("LEAPVIEW_AGENT_RELAY_TEST_MODE")
	if mode == "" {
		t.Skip("container helper")
	}
	if mode == "server" {
		raw, err := os.ReadFile("/run/relay.json")
		if err != nil {
			t.Fatal("test config unavailable")
		}
		var config agentRelayConfig
		if json.Unmarshal(raw, &config) != nil || config.validate() != nil {
			t.Fatal("test config invalid")
		}
		app, err := net.Listen("tcp", net.JoinHostPort(config.SidecarIP, "443"))
		if err != nil {
			t.Fatal("test listener unavailable")
		}
		control, err := net.Listen("tcp", net.JoinHostPort(config.SidecarIP, "4443"))
		if err != nil {
			app.Close()
			t.Fatal("test listener unavailable")
		}
		fmt.Println("AGENT_RELAY_READY")
		if serveAgentRelay(t.Context(), config, app, control) != nil {
			t.Fatal("test relay failed")
		}
		return
	}
	address := "example.com:443"
	dial := func(config *tls.Config) (*tls.Conn, error) {
		raw, err := (&net.Dialer{Timeout: 3 * time.Second}).Dial("tcp", address)
		if err != nil {
			return nil, err
		}
		_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
		c := tls.Client(raw, config)
		if err = c.Handshake(); err != nil {
			raw.Close()
			return nil, err
		}
		return c, nil
	}
	if mode == "closed" || mode == "unauthorized" {
		if c, err := dial(&tls.Config{ServerName: "example.com"}); err == nil {
			c.Close()
			t.Fatal("forbidden relay route succeeded")
		}
		return
	}
	if mode != "client" {
		t.Fatal("unknown test mode")
	}
	c, err := dial(&tls.Config{ServerName: "example.com"})
	if err != nil {
		t.Fatal("original provider TLS verification failed")
	}
	request, _ := http.NewRequest("GET", "https://example.com/", nil)
	request.Close = true
	if request.Write(c) != nil {
		c.Close()
		t.Fatal("provider request failed")
	}
	response, err := http.ReadResponse(bufio.NewReader(c), request)
	if err != nil || response.StatusCode != 200 {
		c.Close()
		t.Fatal("provider response failed")
	}
	response.Body.Close()
	c.Close()
	for _, config := range []*tls.Config{{ServerName: "wrong.example"}, {ServerName: "example.com", RootCAs: x509.NewCertPool()}} {
		if c, err := dial(config); err == nil {
			c.Close()
			t.Fatal("negative certificate verification succeeded")
		}
	}
	for _, destination := range []string{"iana.org:443", net.JoinHostPort(os.Getenv("LEAPVIEW_AGENT_RELAY_TEST_PUBLIC_IP"), "443"), "[2606:4700:4700::1111]:443"} {
		c, err := (&net.Dialer{Timeout: 2 * time.Second}).Dial("tcp", destination)
		if err == nil {
			c.Close()
			t.Fatal("unrelated/default egress succeeded")
		}
	}
}

func TestAgentRelayDockerIsolation(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_AGENT_RELAY_DOCKER") != "1" {
		t.Skip("explicit disposable Docker regression")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	docker := "/run/current-system/sw/bin/docker"
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, docker, args...)
		raw, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(raw)), err
	}
	image := "public.ecr.aws/docker/library/golang@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61"
	prefix := fmt.Sprintf("leapview-agent-relay-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	containers := []string{prefix + "-provider", prefix + "-app", prefix + "-other"}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, name := range containers {
			_ = exec.CommandContext(cleanupCtx, docker, "rm", "-f", "-v", name).Run()
		}
		if err := exec.CommandContext(cleanupCtx, docker, "network", "rm", prefix).Run(); err != nil {
			t.Error("disposable network cleanup failed")
		}
	}()
	if _, err := run("network", "create", "--internal", prefix); err != nil {
		t.Fatal("disposable network creation failed")
	}
	raw, err := run("network", "inspect", prefix, "--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}")
	if err != nil {
		t.Fatal("disposable gateway unavailable")
	}
	hostIP := net.ParseIP(raw).To4()
	if hostIP == nil {
		t.Fatal("disposable gateway invalid")
	}
	ip := func(last byte) string {
		address := append(net.IP(nil), hostIP...)
		address[3] = last
		return address.String()
	}
	config := agentRelayConfig{OperationDigest: "sha256:" + strings.Repeat("b", 64), Nonce: strings.Repeat("a", 64), AppIP: ip(10), SidecarIP: ip(11), HostIP: raw}
	directory := t.TempDir()
	configPath := filepath.Join(directory, "relay.json")
	data, _ := json.Marshal(config)
	if os.WriteFile(configPath, data, 0600) != nil {
		t.Fatal("disposable config failed")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("compiled helper unavailable")
	}
	base := func(name, ip, mode string) []string {
		return []string{"run", "--name", name, "--network", prefix, "--ip", ip, "--read-only", "--cap-drop=ALL", "--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), "--security-opt=no-new-privileges", "--sysctl", "net.ipv4.ip_unprivileged_port_start=0", "--mount", "type=bind,src=" + binary + ",dst=/test,readonly", "--env", "LEAPVIEW_AGENT_RELAY_TEST_MODE=" + mode}
	}
	server := append(base(containers[0], config.SidecarIP, "server"), "-d", "--mount", "type=bind,src="+configPath+",dst=/run/relay.json,readonly", image, "/test", "-test.run=^TestAgentRelayDockerHelper$", "-test.v")
	if _, err := run(server...); err != nil {
		t.Fatal("compiled relay container failed to start")
	}
	ready := false
	startupLogs := ""
	for range 50 {
		logs, _ := run("logs", containers[0])
		startupLogs = logs
		if strings.Contains(logs, "AGENT_RELAY_READY") {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Logf("synthetic startup diagnostics: %.2048s", startupLogs)
		state, _ := run("inspect", containers[0], "--format", "{{.State.Running}} {{.State.ExitCode}} {{.Config.User}}")
		t.Logf("synthetic container state: %.128s", state)
		t.Fatal("compiled relay listener startup failed")
	}
	inspection, inspectErr := run("inspect", containers[0])
	if inspectErr != nil {
		t.Fatal("owned container inspection failed")
	}
	var inspected []dockerInspection
	if json.Unmarshal([]byte(inspection), &inspected) != nil || len(inspected) != 1 || inspected[0].Config.Image != image || !inspected[0].State.Running {
		t.Fatal("owned container image/state mismatch")
	}
	if address, err := agentEndpoint(inspected[0], prefix); err != nil || address != config.SidecarIP {
		t.Fatal("owned container network/IP mismatch")
	}
	addresses, err := outbound.New(outbound.PublicOnly, outbound.Options{}).ResolveHost(ctx, "example.com")
	if err != nil {
		t.Fatal("public test destination unavailable")
	}
	publicIP := ""
	for _, address := range addresses {
		if address.Is4() {
			publicIP = address.String()
			break
		}
	}
	if publicIP == "" {
		t.Fatal("public IPv4 test destination unavailable")
	}
	wrong, err := net.DialTimeout("tcp", net.JoinHostPort(config.SidecarIP, "4443"), time.Second)
	if err != nil {
		t.Fatal("reverse control unavailable")
	}
	wrong.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(wrong, "wrong nonce\n")
	if _, err = wrong.Read(make([]byte, 1)); err == nil {
		wrong.Close()
		t.Fatal("wrong nonce accepted")
	}
	wrong.Close()
	closeChannels, err := startReverseAgentChannels(ctx, config, net.JoinHostPort(config.SidecarIP, "4443"), net.JoinHostPort(publicIP, "443"), 4)
	if err != nil {
		t.Fatal("authenticated reverse channels failed")
	}
	defer closeChannels()
	client := func(name, ip, mode string) error {
		args := append(base(name, ip, mode), "--add-host", "example.com:"+config.SidecarIP, "--env", "LEAPVIEW_AGENT_RELAY_TEST_PUBLIC_IP="+publicIP, image, "/test", "-test.run=^TestAgentRelayDockerHelper$", "-test.v")
		_, err := run(args...)
		return err
	}
	if err = client(containers[2], ip(12), "unauthorized"); err != nil {
		t.Fatal("unauthorized caller regression failed")
	}
	if err = client(containers[1], config.AppIP, "client"); err != nil {
		t.Fatal("compiled relay TLS/isolation regression failed")
	}
	if err = closeChannels(); err != nil {
		t.Fatal("channel cleanup barrier failed")
	}
	if _, err = run("rm", "-f", "-v", containers[0]); err != nil {
		t.Fatal("sidecar cleanup barrier failed")
	}
	if _, err = run("rm", "-f", "-v", containers[1]); err != nil {
		t.Fatal("client cleanup failed")
	}
	if err = client(containers[1], config.AppIP, "closed"); err != nil {
		t.Fatal("provider route survived cleanup")
	}
}
