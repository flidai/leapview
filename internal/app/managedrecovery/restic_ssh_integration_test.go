package managedrecovery

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/google/uuid"
)

func TestActualPinnedResticRestoresThroughAuthenticatedOffHostSFTP(t *testing.T) {
	restic, ssh := os.Getenv("LEAPVIEW_TEST_MANAGED_RESTIC"), os.Getenv("LEAPVIEW_TEST_MANAGED_SSH")
	if restic == "" || ssh == "" {
		t.Skip("explicit pinned Restic/OpenSSH required")
	}
	base := t.TempDir()
	inputs := filepath.Join(base, "inputs")
	if err := os.Mkdir(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	keygen := filepath.Join(filepath.Dir(ssh), "ssh-keygen")
	sshd := filepath.Join(filepath.Dir(ssh), "sshd")
	generate := func(path string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), keygen, "-q", "-t", "ed25519", "-N", "", "-f", path)
		if err := command.Run(); err != nil {
			t.Fatal("pinned SSH key generation failed")
		}
	}
	clientKey, hostKey := filepath.Join(inputs, "client"), filepath.Join(inputs, "host")
	generate(clientKey)
	generate(hostKey)
	config := strings.Join([]string{"Port 22", "ListenAddress 0.0.0.0", "HostKey /inputs/host", "PidFile /run/sshd/sshd.pid", "AuthorizedKeysFile /root/.ssh/authorized_keys", "PasswordAuthentication no", "KbdInteractiveAuthentication no", "PermitRootLogin prohibit-password", "UsePAM no", "UseDNS no", "AllowUsers root", "Subsystem sftp internal-sftp", "LoginGraceTime 10"}, "\n")
	if err := os.WriteFile(filepath.Join(inputs, "sshd_config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	name := "managed-sftp-" + uuid.NewString()
	docker := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), "docker", args...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("disposable SFTP container failed: %s", output)
		}
		return bytes.TrimSpace(output)
	}
	docker("run", "--detach", "--name", name, "--user", "root", "--publish", "127.0.0.1::22", "--volume", "/nix/store:/nix/store:ro", "--volume", inputs+":/inputs:ro", "--entrypoint", "/bin/sh", postgrestest.PostgreSQL18Image, "-c", "mkdir -p /run/sshd /var/empty /root/.ssh; chmod 700 /root/.ssh; cp /inputs/client.pub /root/.ssh/authorized_keys; chmod 600 /root/.ssh/authorized_keys; sed -i 's/^root:[^:]*:/root:publickey-only:/' /etc/shadow; adduser -D -H -h /var/empty -s /sbin/nologin sshd; exec "+sshd+" -D -e -f /inputs/sshd_config")
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", name).Run() })
	hostPort := string(docker("port", name, "22/tcp"))
	address, portValue, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", hostPort, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SFTP host failed startup: %s", docker("logs", name))
		}
		time.Sleep(20 * time.Millisecond)
	}
	public, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(public))
	known := filepath.Join(base, "known-hosts")
	if err := os.WriteFile(known, []byte("["+address+"]:"+portValue+" "+fields[0]+" "+fields[1]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := ResticSSHConfig{SSH: ssh, Address: address, Port: port, User: "root", IdentityFile: clientKey, KnownHostsFile: known}
	repository := "sftp://root@" + hostPort + "/repository"
	sshCommand, err := managedResticSSHCommand(repository, &transport)
	if err != nil {
		t.Fatal(err)
	}
	password := filepath.Join(base, "password")
	if err := os.WriteFile(password, []byte("disposable encrypted SFTP password"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "acknowledged"), []byte("exact retained remote bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), restic, append([]string{"--no-cache", "--repo", repository, "--password-file", password, "--option", "sftp.command=" + sshCommand}, args...)...)
		command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
		if err := configureRestoreProcess(command); err != nil {
			t.Fatal(err)
		}
		output, err := command.Output()
		if err != nil {
			t.Fatalf("actual encrypted SFTP operation failed: %v", err)
		}
		return output
	}
	run("init")
	snapshot := ""
	for _, line := range bytes.Split(run("backup", "--json", source), []byte("\n")) {
		var value struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &value) == nil && value.Type == "summary" {
			snapshot = value.ID
		}
	}
	if len(snapshot) != 64 {
		t.Fatal("actual remote immutable snapshot missing")
	}
	manifest, err := CaptureFiles(source)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := recoveryset.ObjectRoot{Kind: recoveryset.ObjectRootDuckLake, URI: source, Digest: digestBytes([]byte(source)), VersionID: snapshot, ProviderRecoveryFrontier: "restic:" + snapshot}
	configInput := ResticConfig{TargetID: "target", RecoverySetID: "set", Root: root, Restic: restic, Repository: repository, PasswordFile: password, Destination: filepath.Join(base, "restored"), Manifest: manifest, ManifestDigest: digest, SSH: &transport}
	restorer, err := NewRestic(configInput)
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.ObjectRequest{TargetID: "target", RecoverySetID: "set", Root: root, IdempotencyKey: "exact-off-host-operation"}
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(configInput.Destination); err != nil {
		t.Fatal(err)
	}
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	// Strict host trust must reject a different enrolled host key before exposure.
	other := filepath.Join(inputs, "foreign-host")
	generate(other)
	otherPublic, err := os.ReadFile(other + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	otherFields := strings.Fields(string(otherPublic))
	if err := os.WriteFile(known, []byte("["+address+"]:"+portValue+" "+otherFields[0]+" "+otherFields[1]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configInput.Destination = filepath.Join(base, "denied")
	denied, err := NewRestic(configInput)
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "foreign-host-attempt"
	if _, err := denied.RestoreObject(t.Context(), request); err == nil {
		t.Fatal("foreign host key accepted")
	}
	if _, err := os.Lstat(configInput.Destination); !os.IsNotExist(err) {
		t.Fatal("foreign host exposed restore content")
	}
}
