package providerrestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/pkg/strictjson"
)

// PrimaryEnrollment binds a RecoverySet cluster identity to its independently
// enrolled original host. Never derive these identities from a replacement.
type PrimaryEnrollment struct {
	ClusterIdentity  string `json:"clusterIdentity"`
	Address          string `json:"address"`
	MachineID        string `json:"machineID"`
	SystemIdentifier string `json:"systemIdentifier"`
}

type PrimaryFenceSSHConfig struct {
	TargetID       string              `json:"targetID"`
	SSH            string              `json:"ssh"`
	IdentityFile   string              `json:"identityFile"`
	KnownHostsFile string              `json:"knownHostsFile"`
	Primaries      []PrimaryEnrollment `json:"primaries"`
}

type fenceIdentity struct {
	SchemaVersion    int    `json:"schemaVersion"`
	TargetID         string `json:"targetID"`
	RecoverySetID    string `json:"recoverySetID"`
	FrontierDigest   string `json:"frontierDigest"`
	ClusterIdentity  string `json:"clusterIdentity"`
	MachineID        string `json:"machineID"`
	SystemIdentifier string `json:"systemIdentifier"`
}

type fenceReceipt struct {
	Identity          fenceIdentity `json:"identity"`
	PostgreSQLStopped bool          `json:"postgresqlStopped"`
	RestartFenced     bool          `json:"restartFenced"`
}

// SSHPrimaryFence uses the installed NixOS helper and explicit pinned SSH trust.
// It never interprets SSH failure or an unreachable host as proof of fencing.
type SSHPrimaryFence struct {
	config  PrimaryFenceSSHConfig
	execute func(context.Context, string, []string, []byte) ([]byte, error)
}

func NewSSHPrimaryFence(config PrimaryFenceSSHConfig) (*SSHPrimaryFence, error) {
	if !validFenceName(config.TargetID) || len(config.Primaries) == 0 {
		return nil, fmt.Errorf("%w: missing primary enrollment", ErrInvalid)
	}
	for _, path := range []string{config.SSH, config.IdentityFile, config.KnownHostsFile} {
		// OpenSSH expands percent tokens and parses the known-hosts option as
		// config syntax. Require one literal path, never a second trust source.
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, " \t\r\n\x00%~\"'\\") {
			return nil, fmt.Errorf("%w: primary fence requires explicit absolute SSH paths", ErrInvalid)
		}
	}
	clusters, machines, addresses := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, primary := range config.Primaries {
		_, err := strconv.ParseUint(primary.SystemIdentifier, 10, 64)
		if !validFenceName(primary.ClusterIdentity) ||
			net.ParseIP(primary.Address) == nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(primary.MachineID) ||
			err != nil || !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(primary.SystemIdentifier) ||
			clusters[primary.ClusterIdentity] || machines[primary.MachineID] || addresses[primary.Address] {
			return nil, fmt.Errorf("%w: ambiguous or invalid original-primary enrollment", ErrInvalid)
		}
		clusters[primary.ClusterIdentity], machines[primary.MachineID] = true, true
		addresses[primary.Address] = true
	}
	config.Primaries = slices.Clone(config.Primaries)
	return &SSHPrimaryFence{config: config, execute: executeFenceSSH}, nil
}

func validFenceName(value string) bool {
	return value != "" && len(value) <= 255 && value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// FenceOriginals is an explicit operator action. A partial failure leaves every
// already-created fence in place; retry the same exact frontier to finish it.
func (f *SSHPrimaryFence) FenceOriginals(ctx context.Context, set recoveryset.RecoverySet) error {
	return f.observe(ctx, set, false)
}

func (f *SSHPrimaryFence) Verify(ctx context.Context, set recoveryset.RecoverySet) error {
	return f.observe(ctx, set, true)
}

func (f *SSHPrimaryFence) observe(ctx context.Context, set recoveryset.RecoverySet, check bool) error {
	digest, err := set.Digest()
	if err != nil || digest != set.FrontierDigest || set.Delivery.TargetID != f.config.TargetID {
		return fmt.Errorf("%w: primary fence frontier mismatch", ErrInvalid)
	}
	// Validate the complete enrollment before touching even the first host.
	clusters := map[string]bool{}
	for _, point := range set.ClusterPoints {
		clusters[point.ClusterIdentity] = true
	}
	if len(clusters) != len(f.config.Primaries) {
		return fmt.Errorf("%w: primary enrollment does not cover recovery set", ErrInvalid)
	}
	for _, primary := range f.config.Primaries {
		if !clusters[primary.ClusterIdentity] {
			return fmt.Errorf("%w: primary enrollment does not cover recovery set", ErrInvalid)
		}
	}
	for _, primary := range f.config.Primaries {
		wanted := fenceIdentity{1, f.config.TargetID, set.ID, digest, primary.ClusterIdentity, primary.MachineID, primary.SystemIdentifier}
		input, err := json.Marshal(wanted)
		if err != nil {
			return err
		}
		args := []string{"-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
			"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UserKnownHostsFile=" + f.config.KnownHostsFile,
			"-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "ForwardAgent=no",
			"-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=10", "-o", "ServerAliveCountMax=2",
			"-i", f.config.IdentityFile, "root@" + primary.Address, "/run/current-system/sw/bin/leapview-postgres-fence"}
		if check {
			args = append(args, "--check")
		}
		bounded, cancel := context.WithTimeout(ctx, 150*time.Second)
		output, err := f.execute(bounded, f.config.SSH, args, input)
		cancel()
		if err != nil {
			return fmt.Errorf("%w: original-primary fence is unconfirmed", ErrIndeterminate)
		}
		var receipt fenceReceipt
		if strictjson.DecodeWithOptions(output, &receipt, strictjson.Options{MaxBytes: 16384}) != nil ||
			receipt.Identity != wanted || !receipt.PostgreSQLStopped || !receipt.RestartFenced {
			return fmt.Errorf("%w: original-primary fence response does not bind the requested frontier", ErrInconsistent)
		}
	}
	return nil
}

type fenceOutput struct{ bytes.Buffer }

func (b *fenceOutput) Write(value []byte) (int, error) {
	if b.Len()+len(value) > 16384 {
		return 0, errors.New("fence response too large")
	}
	return b.Buffer.Write(value)
}

func executeFenceSSH(ctx context.Context, executable string, args []string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = []string{"LC_ALL=C"}
	command.Stdin = bytes.NewReader(input)
	var output fenceOutput
	command.Stdout, command.Stderr = &output, io.Discard
	err := command.Run()
	return output.Bytes(), err
}
