package managedrecovery

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestActualPinnedResticRestoresSelectedSnapshot(t *testing.T) {
	program := os.Getenv("LEAPVIEW_TEST_MANAGED_RESTIC")
	if program == "" {
		t.Skip("explicit pinned Restic integration inputs required")
	}
	restorer, request := resticFixture(t)
	restorer.config.Restic = program
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), program, append([]string{"--no-cache", "--repo", restorer.config.Repository, "--password-file", restorer.config.PasswordFile}, args...)...)
		command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
		output, err := command.Output()
		if err != nil {
			t.Fatal("actual pinned Restic operation failed")
		}
		return output
	}
	run("init")
	output := run("backup", "--json", restorer.source)
	var summary struct {
		Message  string `json:"message_type"`
		Snapshot string `json:"snapshot_id"`
	}
	for _, line := range bytes.Split(output, []byte("\n")) {
		if json.Unmarshal(line, &summary) == nil && summary.Message == "summary" {
			break
		}
	}
	if len(summary.Snapshot) != 64 {
		t.Fatal("actual snapshot identity missing")
	}
	request.Root.VersionID = summary.Snapshot
	request.Root.ProviderRecoveryFrontier = "restic:" + summary.Snapshot
	config := restorer.config
	config.Root = request.Root
	restorer, err := NewRestic(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restorer.source, "data"), []byte("advanced-after-frontier"), 0600); err != nil {
		t.Fatal(err)
	}
	run("backup", restorer.source)
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(config.Destination, "data"))
	if err != nil || string(actual) != "acknowledged" {
		t.Fatal("selected snapshot bytes not recovered")
	}
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}
