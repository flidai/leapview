//go:build linux

package hostinstall

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

type agentCheckpoint struct {
	OperationDigest string `json:"operationDigest"`
	Phase           string `json:"phase"`
}

func agentCheckpointPath(r NativeRequest, id Identity) string {
	return filepath.Join(r.Profile.StateRoot, "upgrade-operations", strings.TrimPrefix(id.ArtifactAdmissionDigest, "sha256:"), "agent-credential-checkpoint.json")
}
func validateAgentExportPhase(r NativeRequest, id Identity) error {
	raw, err := securefs.ReadPrivateFile(agentCheckpointPath(r, id))
	if err != nil || len(raw) > 1024 {
		return errAgentTransition
	}
	var checkpoint agentCheckpoint
	if json.Unmarshal(raw, &checkpoint) != nil || checkpoint.OperationDigest != id.ArtifactAdmissionDigest || (checkpoint.Phase != "AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST" && checkpoint.Phase != "AWAITING_CANDIDATE_AGENT_CREDENTIAL_TEST") {
		return errAgentTransition
	}
	// The controller writes this checkpoint only while its original durable
	// operation is active; terminal journals can never export credentials.
	if checkpoint.Phase == "AWAITING_CANDIDATE_AGENT_CREDENTIAL_TEST" {
		raw, err = securefs.ReadPrivateFile(filepath.Join(r.Profile.Root, "upgrade-operation.json"))
		if err != nil {
			return errAgentTransition
		}
		var envelope struct {
			State State `json:"state"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.State.validate() != nil || envelope.State.Identity != id || envelope.State.Phase != Starting {
			return errAgentTransition
		}
	} else {
		operation := filepath.Dir(agentCheckpointPath(r, id))
		if state, err := readDetachedState(operation); err == nil {
			if state.Identity != id || state.Phase != DetachedRunning {
				return errAgentTransition
			}
		} else {
			if !errors.Is(err, os.ErrNotExist) {
				return errAgentTransition
			}
			raw, err = securefs.ReadPrivateFile(filepath.Join(r.Profile.Root, "upgrade-operation.json"))
			if err != nil {
				return errAgentTransition
			}
			var envelope struct {
				State State `json:"state"`
			}
			if json.Unmarshal(raw, &envelope) != nil || envelope.State.validate() != nil || envelope.State.Identity != id || envelope.State.Phase != Capturing {
				return errAgentTransition
			}
		}
	}
	return nil
}

func (e *NativeEffects) agentCredentialCheckpoint(ctx context.Context, clone bool, closeProvider func() error) (err error) {
	if e.request.AgentCredentialTransition == nil {
		return nil
	}
	if _, err = readBoundAgentTransition(e.request.Profile.StateRoot, *e.request.AgentCredentialTransition); err != nil {
		return err
	}
	path := agentCheckpointPath(e.request, e.id)
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, errAgentTransition)
		}
	}()
	prefix := "AWAITING_CANDIDATE_AGENT_CREDENTIAL_"
	if clone {
		prefix = "AWAITING_REHEARSAL_AGENT_CREDENTIAL_"
	}
	write := func(phase string) error {
		raw, _ := json.Marshal(agentCheckpoint{OperationDigest: e.id.ArtifactAdmissionDigest, Phase: phase})
		return securefs.WritePrivateFileAtomic(path, raw)
	}
	if err = write(prefix + "TEST"); err != nil {
		return errAgentTransition
	}
	if err = e.awaitBrowser(ctx, prefix+"TEST"); err != nil {
		return err
	}
	// Native cleanup is the authorization barrier: all channels are canceled and
	// joined and the sidecar removed before Save may reopen provider admission.
	if closeProvider != nil {
		if err = closeProvider(); err != nil {
			return errAgentTransition
		}
	}
	if err = write(prefix + "SAVE"); err != nil {
		return errAgentTransition
	}
	return e.awaitBrowser(ctx, prefix+"SAVE")
}

func (e *NativeEffects) waitAgentHTTP(ctx context.Context, name string) error {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		info, err := e.inspect(ctx, name)
		if err == nil && info.State.Running && info.Config.Image == e.id.Candidate {
			addr := containerEnv(info)["LEAPVIEW_ADDR"]
			port := "8080"
			if addr != "" {
				_, p, err := net.SplitHostPort(addr)
				if err != nil {
					return errAgentTransition
				}
				port = p
			}
			if _, err = e.docker(ctx, "exec", name, "leapview", "healthcheck", "--url", "http://"+net.JoinHostPort("127.0.0.1", port)+"/healthz"); err == nil {
				version, err := e.appVersion(ctx, name)
				if err == nil && version.Revision == e.request.CandidateRevision {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return errAgentTransition
		case <-timer.C:
			return errAgentTransition
		case <-time.After(time.Second):
		}
	}
}

func agentEndpoint(info dockerInspection, network string) (string, error) {
	var endpoint struct{ IPAddress string }
	if len(info.NetworkSettings.Networks) != 1 || json.Unmarshal(info.NetworkSettings.Networks[network], &endpoint) != nil {
		return "", errAgentTransition
	}
	ip, err := netip.ParseAddr(endpoint.IPAddress)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return "", errAgentTransition
	}
	return ip.String(), nil
}

func agentUsableCloneAddress(subnet netip.Prefix, raw string) bool {
	ip, err := netip.ParseAddr(raw)
	if err != nil || !ip.Is4() || ip.String() != raw || !ip.IsPrivate() || ip.IsLoopback() || !subnet.Contains(ip) || ip == subnet.Masked().Addr() {
		return false
	}
	network := subnet.Masked().Addr().As4()
	last := binary.BigEndian.Uint32(network[:]) | (^uint32(0) >> subnet.Bits())
	address := ip.As4()
	return binary.BigEndian.Uint32(address[:]) != last
}

func (e *NativeEffects) prepareAgentClone(ctx context.Context, candidate dockerInspection) (dockerInspection, error) {
	if e.request.AgentCredentialTransition == nil {
		return candidate, nil
	}
	u, _ := url.Parse(e.request.AgentCredentialTransition.Provider.BaseURL)
	for _, alias := range candidate.HostConfig.ExtraHosts {
		host, _, _ := strings.Cut(alias, ":")
		if strings.EqualFold(host, u.Hostname()) {
			return candidate, errAgentTransition
		}
	}
	raw, err := e.docker(ctx, "network", "inspect", e.clonePrefix())
	if err != nil || len(raw) > 65536 {
		return candidate, errAgentTransition
	}
	var networks []struct {
		Internal bool
		IPAM     struct {
			Config []struct{ Subnet, Gateway string }
		}
		Containers map[string]struct{ Name, IPv4Address string }
	}
	if json.Unmarshal([]byte(raw), &networks) != nil || len(networks) != 1 || !networks[0].Internal || len(networks[0].IPAM.Config) != 1 {
		return candidate, errAgentTransition
	}
	subnet, err := netip.ParsePrefix(networks[0].IPAM.Config[0].Subnet)
	if err != nil || !subnet.Addr().Is4() || !subnet.Addr().IsPrivate() || subnet != subnet.Masked() || subnet.Bits() > 29 {
		return candidate, errAgentTransition
	}
	if len(networks[0].Containers) != 2 {
		return candidate, errAgentTransition
	}
	e.agentCloneAppIP = ""
	e.agentCloneProxyIP = ""
	gateway := networks[0].IPAM.Config[0].Gateway
	if !agentUsableCloneAddress(subnet, gateway) {
		return candidate, errAgentTransition
	}
	used := map[string]bool{gateway: true}
	seen := map[string]bool{}
	for _, container := range networks[0].Containers {
		address, _, _ := strings.Cut(container.IPv4Address, "/")
		if !agentUsableCloneAddress(subnet, address) || used[address] || seen[container.Name] {
			return candidate, errAgentTransition
		}
		if container.Name == e.clonePrefix()+"-caddy" {
			e.agentCloneProxyIP = address
		} else if container.Name != e.clonePrefix()+"-pg" {
			return candidate, errAgentTransition
		}
		used[address] = true
		seen[container.Name] = true
	}
	if e.agentCloneProxyIP == "" || !seen[e.clonePrefix()+"-pg"] {
		return candidate, errAgentTransition
	}
	ip := subnet.Masked().Addr().Next()
	for range 64 {
		if !subnet.Contains(ip) {
			break
		}
		if !used[ip.String()] && agentUsableCloneAddress(subnet, ip.String()) {
			if e.agentCloneAppIP == "" {
				e.agentCloneAppIP = ip.String()
			} else {
				candidate.HostConfig.ExtraHosts = append(candidate.HostConfig.ExtraHosts, u.Hostname()+":"+ip.String())
				return candidate, nil
			}
		}
		ip = ip.Next()
	}
	return candidate, errAgentTransition
}

func (e *NativeEffects) startAgentCloneProvider(ctx context.Context, candidate dockerInspection) (func() error, error) {
	intent := e.request.AgentCredentialTransition
	if intent == nil {
		return nil, nil
	}
	u, _ := url.Parse(intent.Provider.BaseURL)
	sidecarIP := ""
	for _, alias := range candidate.HostConfig.ExtraHosts {
		host, ip, ok := strings.Cut(alias, ":")
		if ok && host == u.Hostname() {
			sidecarIP = ip
		}
	}
	app, err := e.inspect(ctx, e.clonePrefix()+"-app")
	if err != nil {
		return nil, errAgentTransition
	}
	appIP, err := agentEndpoint(app, e.clonePrefix())
	if err != nil || appIP == sidecarIP || appIP != e.agentCloneAppIP {
		return nil, errAgentTransition
	}
	raw, err := e.docker(ctx, "network", "inspect", e.clonePrefix(), "--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}")
	if err != nil {
		return nil, errAgentTransition
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return nil, errAgentTransition
	}
	config := agentRelayConfig{OperationDigest: e.id.ArtifactAdmissionDigest, Nonce: hex.EncodeToString(nonce), AppIP: appIP, HostIP: raw, SidecarIP: sidecarIP}
	if config.validate() != nil {
		return nil, errAgentTransition
	}
	path := filepath.Join(e.operation, "agent-provider-relay.json")
	name := e.clonePrefix() + "-provider"
	data, _ := json.Marshal(config)
	if err = securefs.WritePrivateFileAtomic(path, data); err != nil {
		return nil, errAgentTransition
	}
	var closeChannels func() error
	stopped := false
	cleanup := func() error {
		if stopped {
			return nil
		}
		if closeChannels != nil {
			if err := closeChannels(); err != nil {
				return errAgentTransition
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := e.docker(cleanupCtx, "rm", "-f", "-v", name)
		removeErr := os.Remove(path)
		if err != nil || (removeErr != nil && !os.IsNotExist(removeErr)) {
			return errAgentTransition
		}
		stopped = true
		return nil
	}
	// Only this isolated helper needs the controller's root-owned 0600 config;
	// the application continues to use the candidate image's unprivileged user.
	_, err = e.docker(ctx, "run", "-d", "--name", name, "--user", "0:0", "--network", e.clonePrefix(), "--ip", sidecarIP, "--restart=no", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--sysctl", "net.ipv4.ip_unprivileged_port_start=0", "--mount", "type=bind,src="+path+",dst=/run/agent-relay.json,readonly", "--entrypoint", "/usr/local/libexec/leapviewctl", e.id.Candidate, "host", "upgrade", "agent-relay", "--config", "/run/agent-relay.json")
	if err != nil {
		_ = cleanup()
		return nil, errAgentTransition
	}
	sidecar, err := e.inspect(ctx, name)
	if err != nil {
		_ = cleanup()
		return nil, errAgentTransition
	}
	if actual, err := agentEndpoint(sidecar, e.clonePrefix()); err != nil || actual != sidecarIP || sidecar.Config.Image != e.id.Candidate {
		_ = cleanup()
		return nil, errAgentTransition
	}
	// Wait for listener startup separately from authenticated channel admission.
	ready := false
	for range 50 {
		c, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort(sidecarIP, "4443"))
		if err == nil {
			c.Close()
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			_ = cleanup()
			return nil, errAgentTransition
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		_ = cleanup()
		return nil, errAgentTransition
	}
	closeChannels, err = startReverseAgentChannels(ctx, config, net.JoinHostPort(sidecarIP, "4443"), net.JoinHostPort(intent.ProviderAddress, "443"), agentRelayStreamBudget)
	if err != nil {
		_ = cleanup()
		return nil, errAgentTransition
	}
	return cleanup, nil
}
