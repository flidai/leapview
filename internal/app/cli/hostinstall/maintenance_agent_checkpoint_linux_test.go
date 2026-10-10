//go:build linux

package hostinstall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

type agentAckWriter struct {
	events *[]string
	input  *io.PipeWriter
	digest string
}

func (w agentAckWriter) Write(raw []byte) (int, error) {
	phase, _, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
	*w.events = append(*w.events, phase)
	go func() { _, _ = io.WriteString(w.input, "commit "+w.digest+" "+phase+"\n") }()
	return len(raw), nil
}

func TestAgentCheckpointCleanupBarrierBeforeSave(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup-failure"}[failed], func(t *testing.T) {
			root, intent := agentTransitionFixture(t)
			r := nativeRequestFixture(t)
			r.Profile.StateRoot = root
			r.AgentCredentialTransition = &intent
			id, err := r.Identity()
			if err != nil {
				t.Fatal(err)
			}
			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()
			events := []string{}
			e := &NativeEffects{request: r, id: id, reader: bufio.NewReader(input), stdout: agentAckWriter{&events, writer, id.ArtifactAdmissionDigest}}
			err = e.agentCredentialCheckpoint(t.Context(), true, func() error {
				events = append(events, "provider-closed-and-joined")
				if failed {
					return errors.New("fixture cleanup failure")
				}
				return nil
			})
			if (err != nil) != failed {
				t.Fatalf("unexpected checkpoint outcome: %v", err)
			}
			want := []string{"AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST", "provider-closed-and-joined"}
			if !failed {
				want = append(want, "AWAITING_REHEARSAL_AGENT_CREDENTIAL_SAVE")
			}
			if strings.Join(events, "|") != strings.Join(want, "|") {
				t.Fatal("Save was not fenced by completed provider cleanup")
			}
			if _, err = os.Stat(agentCheckpointPath(r, id)); !os.IsNotExist(err) {
				t.Fatal("checkpoint retained after completion/failure")
			}
		})
	}
}

func TestAgentCheckpointNoOptInAndEOFFailClosed(t *testing.T) {
	e := &NativeEffects{}
	if err := e.agentCredentialCheckpoint(t.Context(), true, func() error { t.Fatal("no-intent checkpoint changed egress"); return nil }); err != nil {
		t.Fatal(err)
	}
	root, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	r.Profile.StateRoot = root
	r.AgentCredentialTransition = &intent
	id, _ := r.Identity()
	var out bytes.Buffer
	e = &NativeEffects{request: r, id: id, stdout: &out, reader: bufio.NewReader(strings.NewReader(""))}
	if err := e.agentCredentialCheckpoint(t.Context(), true, func() error { t.Fatal("EOF permitted Save"); return nil }); err == nil {
		t.Fatal("EOF approved checkpoint")
	}
	if strings.Contains(out.String(), "_SAVE") {
		t.Fatal("Save marker after EOF")
	}
	if _, err := os.Stat(agentCheckpointPath(r, id)); !os.IsNotExist(err) {
		t.Fatal("EOF retained checkpoint")
	}
}

func TestAgentInventoryRetainsSettingsAndDistinctAuthorities(t *testing.T) {
	_, intent := agentTransitionFixture(t)
	row := map[string]any{"instanceId": intent.InstanceID, "customerOwnerId": intent.CustomerOwnerID, "revision": intent.ExpectedRevision, "legacy": true, "enabled": true, "model": intent.Provider.Model, "baseUrl": intent.Provider.BaseURL, "apiMode": intent.Provider.APIMode, "reasoningEffort": intent.Provider.ReasoningEffort}
	raw, _ := json.Marshal(row)
	if err := validateAgentInventory(string(raw), intent); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"instanceId": intent.InstallationID, "customerOwnerId": "other-customer", "revision": intent.ExpectedRevision + 1, "legacy": false, "model": "changed-model", "baseUrl": "https://different.example/v1"} {
		next := map[string]any{}
		for k, v := range row {
			next[k] = v
		}
		next[key] = value
		raw, _ := json.Marshal(next)
		if validateAgentInventory(string(raw), intent) == nil {
			t.Fatalf("accepted altered %s", key)
		}
	}
	if strings.Contains(agentTransitionInventorySQL, "credential_version_id") || strings.Contains(agentTransitionInventorySQL, "SELECT *") {
		t.Fatal("inventory requires unavailable or unrestricted schema fields")
	}
}

func TestAgentExportPhaseRejectsPostCommitEvenWithCheckpoint(t *testing.T) {
	root, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	r.Profile.StateRoot = root
	r.Profile.Root = t.TempDir()
	r.AgentCredentialTransition = &intent
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := json.Marshal(agentCheckpoint{id.ArtifactAdmissionDigest, "AWAITING_CANDIDATE_AGENT_CREDENTIAL_TEST"})
	if err = securefs.WritePrivateFileAtomic(agentCheckpointPath(r, id), checkpoint); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{Starting, Committed, Succeeded, Recovered} {
		raw, _ := json.Marshal(map[string]any{"state": State{Identity: id, Phase: phase, RecoveryDigest: "sha256:" + strings.Repeat("c", 64), RestoreRequired: phase != Recovered}})
		if err = securefs.WritePrivateFileAtomic(filepath.Join(r.Profile.Root, "upgrade-operation.json"), raw); err != nil {
			t.Fatal(err)
		}
		if got := validateAgentExportPhase(r, id); (got == nil) != (phase == Starting) {
			t.Fatalf("phase %s export admission: %v", phase, got)
		}
	}
	// Recover/plan identity remains valid after the custodian withdraws plaintext;
	// neither immutable request validation nor terminal recovery reads its row.
	if err = os.Remove(filepath.Join(root, "agent-credential-transitions", "fixture.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Identity(); err != nil {
		t.Fatal("terminal request identity depends on plaintext source")
	}
}

func TestAgentRelayDestinationBinding(t *testing.T) {
	cfg := agentRelayConfig{OperationDigest: "sha256:" + strings.Repeat("b", 64), Nonce: strings.Repeat("a", 64), AppIP: "172.18.0.2", HostIP: "172.18.0.1", SidecarIP: "172.18.0.3"}
	for _, test := range []struct{ control, destination string }{
		{"172.18.0.4:4443", "93.184.215.14:443"}, {"172.18.0.3:443", "93.184.215.14:443"},
		{"sidecar.example:4443", "93.184.215.14:443"}, {"172.18.0.3:4443", "provider.example:443"},
		{"172.18.0.3:4443", "127.0.0.1:443"}, {"172.18.0.3:4443", "10.0.0.1:443"},
		{"172.18.0.3:4443", "93.184.215.14:8443"}, {"172.18.0.3:4443", "[2606:4700::1111]:443"},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if close, err := startReverseAgentChannels(ctx, cfg, test.control, test.destination, 4); err == nil {
			close()
			t.Fatal("mismatched relay destination admitted")
		}
	}
}

func TestAgentClonePinsDistinctAddressesAndPreservesHostAliases(t *testing.T) {
	root, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	r.Profile.StateRoot = root
	r.AgentCredentialTransition = &intent
	id, _ := r.Identity()
	e := &NativeEffects{request: r, id: id, operation: t.TempDir()}
	calls := [][]string{}
	e.execute = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 1 && args[0] == "network" && args[1] == "inspect" {
			return fmt.Sprintf(`[{"Internal":true,"IPAM":{"Config":[{"Subnet":"172.18.0.0/16","Gateway":"172.18.0.1"}]},"Containers":{"pg":{"Name":"%s-pg","IPv4Address":"172.18.0.2/16"},"proxy":{"Name":"%s-caddy","IPv4Address":"172.18.0.4/16"}}}]`, e.clonePrefix(), e.clonePrefix()), nil
		}
		return "", nil
	}
	candidate := dockerInspection{}
	candidate.Config.Image = id.Candidate
	candidate.HostConfig.ExtraHosts = []string{"retained.example:192.168.1.4"}
	configured, err := e.prepareAgentClone(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if e.agentCloneAppIP != "172.18.0.3" || configured.HostConfig.ExtraHosts[len(configured.HostConfig.ExtraHosts)-1] != "provider.example:172.18.0.5" {
		t.Fatal("app and sidecar address allocation collided")
	}
	if err = e.clone(t.Context(), e.clonePrefix()+"-app", r.Profile.AppService, configured, nil); err != nil {
		t.Fatal(err)
	}
	command := strings.Join(calls[len(calls)-1], " ")
	if !strings.Contains(command, "--ip 172.18.0.3") || !strings.Contains(command, "--add-host retained.example:192.168.1.4") || !strings.Contains(command, "--add-host provider.example:172.18.0.5") {
		t.Fatal("owned addresses/retained host aliases were not applied")
	}
	if err = e.clone(t.Context(), e.clonePrefix()+"-app", r.Profile.AppService, candidate, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls[len(calls)-1], " "), "--add-host provider.example:") {
		t.Fatal("post-Save restart retained provider alias")
	}
	if err = e.clone(t.Context(), e.clonePrefix()+"-caddy", r.Profile.ProxyService, dockerInspection{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls[len(calls)-1], " "), "--ip 172.18.0.4") {
		t.Fatal("recreated proxy could consume reserved sidecar address")
	}
	candidate.HostConfig.ExtraHosts = append(candidate.HostConfig.ExtraHosts, "provider.example:1.1.1.1")
	if _, err = e.prepareAgentClone(t.Context(), candidate); err == nil {
		t.Fatal("existing provider alias overridden")
	}
}

func TestAgentExportDoesNotFallbackFromMalformedDetachedReceipt(t *testing.T) {
	root, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	r.Profile.StateRoot = root
	r.Profile.Root = t.TempDir()
	r.AgentCredentialTransition = &intent
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := json.Marshal(agentCheckpoint{id.ArtifactAdmissionDigest, "AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST"})
	if err = securefs.WritePrivateFileAtomic(agentCheckpointPath(r, id), checkpoint); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"state": State{Identity: id, Phase: Capturing}})
	if err = securefs.WritePrivateFileAtomic(filepath.Join(r.Profile.Root, "upgrade-operation.json"), raw); err != nil {
		t.Fatal(err)
	}
	if err = validateAgentExportPhase(r, id); err != nil {
		t.Fatal("fresh capturing export rejected")
	}
	path := filepath.Join(filepath.Dir(agentCheckpointPath(r, id)), detachedStateName)
	detached := DetachedRehearsalState{Version: 1, Identity: id, RecoveryDigest: "sha256:" + strings.Repeat("c", 64), Phase: DetachedRunning}
	valid, _ := json.Marshal(detached)
	if err = securefs.WritePrivateFileAtomic(path, valid); err != nil {
		t.Fatal(err)
	}
	if err = validateAgentExportPhase(r, id); err != nil {
		t.Fatal("active exact detached export rejected")
	}
	detached.Identity.ArtifactAdmissionDigest = "sha256:" + strings.Repeat("d", 64)
	wrong, _ := json.Marshal(detached)
	for _, invalid := range [][]byte{[]byte(`{"phase":"malformed"}`), []byte(`{"phase":`), []byte{}, wrong} {
		if err = securefs.WritePrivateFileAtomic(path, invalid); err != nil {
			t.Fatal(err)
		}
		if validateAgentExportPhase(r, id) == nil {
			t.Fatal("invalid detached receipt fell back to live journal")
		}
	}
}

func TestAgentCloneRejectsUnexpectedParticipantsAndUnusableAddresses(t *testing.T) {
	root, intent := agentTransitionFixture(t)
	r := nativeRequestFixture(t)
	r.Profile.StateRoot = root
	r.AgentCredentialTransition = &intent
	id, _ := r.Identity()
	e := &NativeEffects{request: r, id: id}
	template := func(pgName, proxyName, pgIP, proxyIP, gateway, subnet string) string {
		return fmt.Sprintf(`[{"Internal":true,"IPAM":{"Config":[{"Subnet":"%s","Gateway":"%s"}]},"Containers":{"pg":{"Name":"%s","IPv4Address":"%s"},"proxy":{"Name":"%s","IPv4Address":"%s"}}}]`, subnet, gateway, pgName, pgIP, proxyName, proxyIP)
	}
	pg, proxy := e.clonePrefix()+"-pg", e.clonePrefix()+"-caddy"
	for _, raw := range []string{
		template("foreign", proxy, "172.18.0.2/16", "172.18.0.4/16", "172.18.0.1", "172.18.0.0/16"),
		template(proxy, proxy, "172.18.0.2/16", "172.18.0.4/16", "172.18.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.2/16", "172.18.0.2/16", "172.18.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.0/16", "172.18.0.4/16", "172.18.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.2/16", "172.18.255.255/16", "172.18.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.19.0.2/16", "172.18.0.4/16", "172.18.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.2/16", "172.18.0.4/16", "172.19.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.2/16", "172.18.0.4/16", "172.18.0.0", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.1/16", "172.18.0.4/16", "172.18.0.1", "172.18.0.0/16"),
		template(pg, proxy, "172.18.0.2/30", "172.18.0.3/30", "172.18.0.1", "172.18.0.0/30"),
	} {
		e.execute = func(context.Context, ...string) (string, error) { return raw, nil }
		if _, err := e.prepareAgentClone(t.Context(), dockerInspection{}); err == nil {
			t.Fatal("unsupported clone participants/address admitted")
		}
	}
}
