package managedrecovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResticSSHRequiresExactRepositoryAndExplicitPrivateTrust(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key with 'quote")
	known := filepath.Join(root, "known hosts")
	for _, path := range []string{key, known} {
		if err := os.WriteFile(path, []byte("private test trust"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	transport := ResticSSHConfig{SSH: "/nix/store/pinned/bin/ssh", Address: "192.0.2.10", Port: 2222, User: "backup", IdentityFile: key, KnownHostsFile: known}
	repository := "sftp://backup@192.0.2.10:2222/retained/repository"
	command, err := managedResticSSHCommand(repository, &transport)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "IdentityAgent=none", "PasswordAuthentication=no", "GlobalKnownHostsFile=/dev/null", "-F", "/dev/null", "'\\''"} {
		if !strings.Contains(command, required) {
			t.Fatalf("missing explicit trust option %s", required)
		}
	}
	for _, foreign := range []string{"sftp://other@192.0.2.10:2222/retained/repository", "sftp://backup:password@192.0.2.10:2222/retained/repository", "sftp://backup@192.0.2.11:2222/retained/repository", "sftp://backup@192.0.2.10:22/retained/repository", "sftp://backup@192.0.2.10:2222/retained/../repository", "sftp://backup@192.0.2.10:2222/retained/repository?command=anything", "sftp:backup@192.0.2.10:/retained/repository"} {
		if _, err := managedResticSSHCommand(foreign, &transport); err == nil {
			t.Fatalf("foreign repository accepted %q", foreign)
		}
	}
	if _, err := managedResticSSHCommand(repository, nil); err == nil {
		t.Fatal("ambient SFTP authentication accepted")
	}
	if _, err := managedResticSSHCommand("s3:https://example.invalid/bucket", nil); err == nil {
		t.Fatal("unsupported remote fallback accepted")
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := managedResticSSHCommand(repository, &transport); err == nil {
		t.Fatal("public identity file accepted")
	}
}
