package managedrecovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPGRestoreRejectsConfigurationOverridesAndMissingSandbox(t *testing.T) {
	restorer, _ := pgBackRestFixture(t)
	for _, override := range []string{"link-all=y", "link-map=pg_wal=/outside", "tablespace-map=custom=/outside", "tablespace-map-all=/outside", "recovery-option=restore_command=uncontrolled", "config-include-path=/outside"} {
		t.Run(override, func(t *testing.T) {
			config := restorer.config
			contents := []byte("[global]\nrepo1-path=/retained/repository\n" + override + "\n")
			if err := os.WriteFile(config.ConfigFile, contents, 0600); err != nil {
				t.Fatal(err)
			}
			config.ConfigDigest = digestBytes(contents)
			if _, err := NewPGBackRest(config); err == nil {
				t.Fatal("retained config escaped managed restore constraints")
			}
		})
	}
	config := restorer.config
	config.Bubblewrap = ""
	if _, err := NewPGBackRest(config); err == nil {
		t.Fatal("missing kernel confinement admitted")
	}
}

func TestActualKernelConfinementDeniesOutsideWritesAndAllowsOnlyStage(t *testing.T) {
	program := os.Getenv("LEAPVIEW_TEST_MANAGED_BWRAP")
	if program == "" {
		t.Skip("explicit pinned Bubblewrap integration input required")
	}
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	outside := filepath.Join(parent, "outside-canary")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	cluster := &PGStagingCluster{directory: stage, bubblewrap: program}
	write := func(target string) error {
		args := append(cluster.arguments(), "/run/current-system/sw/bin/sh", "-c", `printf changed > "$1"`, "--", target)
		command := exec.CommandContext(t.Context(), program, args...)
		command.Env = []string{"PATH=/nonexistent", "LANG=C"}
		return command.Run()
	}
	if err := write(outside); err == nil {
		t.Fatal("kernel allowed write outside exact staging")
	}
	if contents, err := os.ReadFile(outside); err != nil || string(contents) != "untouched" {
		t.Fatal("outside canary changed")
	}
	if err := write(filepath.Join(stage, "allowed")); err != nil {
		t.Fatal("kernel denied confined staging write")
	}
	if contents, err := os.ReadFile(filepath.Join(stage, "allowed")); err != nil || string(contents) != "changed" {
		t.Fatal("staging write missing")
	}
}
