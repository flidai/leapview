package providerrestore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func sshFenceFixture(t *testing.T) (*SSHPrimaryFence, *coordinatorFixture) {
	t.Helper()
	x := newCoordinatorFixture(t)
	fence, err := NewSSHPrimaryFence(PrimaryFenceSSHConfig{
		TargetID: x.set.Delivery.TargetID, SSH: "/nix/store/test/bin/ssh",
		IdentityFile: "/private/identity", KnownHostsFile: "/private/known_hosts",
		Primaries: []PrimaryEnrollment{
			{ClusterIdentity: "postgres-provider-a", Address: "192.0.2.1", MachineID: strings.Repeat("a", 32), SystemIdentifier: "12345"},
			{ClusterIdentity: "postgres-provider-b", Address: "192.0.2.2", MachineID: strings.Repeat("b", 32), SystemIdentifier: "54321"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fence, x
}

func successfulFenceResponse(input []byte) []byte {
	var identity fenceIdentity
	if err := json.Unmarshal(input, &identity); err != nil {
		panic(err)
	}
	output, err := json.Marshal(fenceReceipt{Identity: identity, PostgreSQLStopped: true, RestartFenced: true})
	if err != nil {
		panic(err)
	}
	return output
}

func TestSSHPrimaryFenceUsesExplicitTrustAndObservesEveryOriginal(t *testing.T) {
	fence, x := sshFenceFixture(t)
	calls := 0
	fence.execute = func(ctx context.Context, executable string, args []string, input []byte) ([]byte, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded original-host observation")
		}
		if executable != fence.config.SSH || args[0] != "-F" || args[1] != "/dev/null" {
			t.Fatal("ambient SSH configuration")
		}
		for _, required := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null", "UserKnownHostsFile=/private/known_hosts", "IdentityAgent=none", "IdentitiesOnly=yes", "ForwardAgent=no"} {
			if !slices.Contains(args, required) {
				t.Fatalf("missing %s", required)
			}
		}
		if args[len(args)-1] != "--check" {
			t.Fatal("verification attempted to create a fence")
		}
		return successfulFenceResponse(input), nil
	}
	if err := fence.Verify(t.Context(), x.set); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("observed %d original clusters", calls)
	}
}

func TestSSHPrimaryFenceRejectsIncompleteEnrollmentBeforeContactingAnyHost(t *testing.T) {
	fence, x := sshFenceFixture(t)
	fence.config.Primaries = fence.config.Primaries[:1]
	fence.execute = func(context.Context, string, []string, []byte) ([]byte, error) {
		t.Fatal("partial fence mutation")
		return nil, nil
	}
	if err := fence.FenceOriginals(t.Context(), x.set); err == nil {
		t.Fatal("partial cluster coverage accepted")
	}
}

func TestSSHPrimaryFenceDoesNotTreatTimeoutOrStaleReceiptAsFence(t *testing.T) {
	for _, mutation := range []string{"timeout", "frontier", "host", "running", "restart", "duplicate", "unknown", "oversize"} {
		t.Run(mutation, func(t *testing.T) {
			fence, x := sshFenceFixture(t)
			fence.execute = func(_ context.Context, _ string, _ []string, input []byte) ([]byte, error) {
				if mutation == "timeout" {
					return nil, errors.New("private SSH diagnostics")
				}
				var value fenceReceipt
				if err := json.Unmarshal(successfulFenceResponse(input), &value); err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "frontier":
					value.Identity.FrontierDigest = "sha256:" + strings.Repeat("0", 64)
				case "host":
					value.Identity.MachineID = strings.Repeat("0", 32)
				case "running":
					value.PostgreSQLStopped = false
				case "restart":
					value.RestartFenced = false
				}
				raw, _ := json.Marshal(value)
				if mutation == "duplicate" {
					raw = append([]byte(`{"postgresqlStopped":false,`), raw[1:]...)
				}
				if mutation == "unknown" {
					raw = append([]byte(`{"unverified":true,`), raw[1:]...)
				}
				if mutation == "oversize" {
					raw = []byte(strings.Repeat(" ", 16385))
				}
				return raw, nil
			}
			err := fence.Verify(t.Context(), x.set)
			if err == nil {
				t.Fatal("unconfirmed fence accepted")
			}
			if strings.Contains(err.Error(), "private SSH diagnostics") {
				t.Fatal("private transport diagnostics leaked")
			}
		})
	}
}

func TestSSHPrimaryFenceApplyIsExplicitAndCoordinatorRechecks(t *testing.T) {
	fence, x := sshFenceFixture(t)
	applied, checked := 0, 0
	fence.execute = func(_ context.Context, _ string, args []string, input []byte) ([]byte, error) {
		if args[len(args)-1] == "--check" {
			checked++
		} else {
			applied++
		}
		return successfulFenceResponse(input), nil
	}
	if err := fence.FenceOriginals(t.Context(), x.set); err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewManaged(x.coordinator.dependencies, fence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Run(t.Context(), x.request); err != nil {
		t.Fatal(err)
	}
	if applied != 2 || checked < 4 {
		t.Fatalf("apply=%d checks=%d", applied, checked)
	}
}

func TestSSHPrimaryFenceEnrollmentRejectsShellAndAmbiguousIdentity(t *testing.T) {
	for _, mutation := range []string{"relative", "trust-path", "trust-token", "host", "duplicate", "address", "cluster", "systemid", "machine"} {
		t.Run(mutation, func(t *testing.T) {
			fence, _ := sshFenceFixture(t)
			config := fence.config
			switch mutation {
			case "relative":
				config.SSH = "ssh"
			case "trust-path":
				config.KnownHostsFile = "/private/trust /another/trust"
			case "trust-token":
				config.KnownHostsFile = "/private/%h"
			case "host":
				config.Primaries[0].Address = "host;false"
			case "duplicate":
				config.Primaries[1] = config.Primaries[0]
			case "address":
				config.Primaries[1].Address = config.Primaries[0].Address
			case "cluster":
				config.Primaries[1].ClusterIdentity = "cluster\x00invalid"
			case "systemid":
				config.Primaries[0].SystemIdentifier = "18446744073709551616"
			case "machine":
				config.Primaries[0].MachineID = "not-enrolled"
			}
			if _, err := NewSSHPrimaryFence(config); err == nil {
				t.Fatal("invalid enrollment accepted")
			}
		})
	}
}
