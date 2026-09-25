//go:build linux

package hostinstall

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Exercise Docker's actual named-volume inspection and read-only file mounts,
// including replacement of the live source by a restored rehearsal directory.
func TestVolumeTLSMountQualification(t *testing.T) {
	if os.Getenv("LEAPVIEW_HOST_UPGRADE_QUALIFICATION") != "1" {
		t.Skip("set LEAPVIEW_HOST_UPGRADE_QUALIFICATION=1 for isolated Docker TLS mount qualification")
	}
	if os.Geteuid() != 0 {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("sudo", "-n", "env", "LEAPVIEW_HOST_UPGRADE_QUALIFICATION=1", executable, "-test.run=^TestVolumeTLSMountQualification$", "-test.v", "-test.timeout=2m")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("root TLS mount qualification: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
		return
	}
	docker := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Docker %s: %v\n%s", args[0], err, output)
		}
		return strings.TrimSpace(string(output))
	}
	name := "leapview-tls-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	volume := name + "-home"
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "-v", name).Run()
		_ = exec.Command("docker", "volume", "rm", volume).Run()
	})
	docker("volume", "create", volume)
	docker("create", "--name", name, "--network", "none", "--mount", "type=volume,src="+volume+",dst=/state", "--entrypoint", "/bin/sh", nativePGImage, "-c", "printf live-ca > /state/ca.pem")
	docker("start", "-a", name)
	var records []dockerInspection
	if err := json.Unmarshal([]byte(docker("inspect", name)), &records); err != nil || len(records) != 1 {
		t.Fatalf("container inspection: %v", err)
	}
	app := records[0]
	live := ""
	for _, mount := range app.Mounts {
		if mount.Name == volume && mount.Destination == "/state" {
			live = mount.Source
		}
	}
	if live == "" {
		t.Fatal("named application volume missing")
	}
	restored := t.TempDir()
	if err := os.WriteFile(filepath.Join(restored, "ca.pem"), []byte("restored-ca"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{live, restored} {
		mounts, err := migrationTLSMounts("postgresql://migrator@postgres/control?sslmode=verify-full&sslrootcert=/state/ca.pem", app, map[string]string{live: source})
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{"run", "--rm", "--network", "none", "--read-only"}, mounts...)
		args = append(args, "--entrypoint", "/bin/sh", nativePGImage, "-c", "cat /state/ca.pem; if printf corrupted > /state/ca.pem 2>/dev/null; then exit 1; fi")
		output := docker(args...)
		want := "live-ca"
		if source == restored {
			want = "restored-ca"
		}
		// Docker combines stdout and stderr without preserving their relative
		// order. The expected certificate must be an exact output line even if
		// the rejected write's shell diagnostic arrives first.
		if !strings.Contains("\n"+output+"\n", "\n"+want+"\n") {
			t.Fatalf("wrong TLS source: got %q, want %q", output, want)
		}
		data, err := os.ReadFile(filepath.Join(source, "ca.pem"))
		if err != nil || string(data) != want {
			t.Fatalf("TLS material changed: %q %v", data, err)
		}
	}
}
