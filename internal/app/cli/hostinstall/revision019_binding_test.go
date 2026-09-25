package hostinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevision019ProvisionedTargetBinding(t *testing.T) {
	root := t.TempDir()
	legacy := Config{SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: revision019PredecessorImage, HTTPS: boolPointer(true)}
	writeConfig(t, filepath.Join(root, installMarkerName), legacy)
	if _, _, err := readUpgradeInstallation(root); err == nil {
		t.Fatal("unbound revision-019 installation was accepted")
	}
	binding := revision019Binding{SchemaVersion: 1, Image: revision019PredecessorImage,
		TargetID: "provisioned-target-a", LegacyConfig: legacy}
	writeBinding := func() {
		t.Helper()
		contents, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, revision019BindingName), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeBinding()
	installed, _, err := readUpgradeInstallation(root)
	if err != nil || installed.TargetID != binding.TargetID || installed.Image != revision019PredecessorImage {
		t.Fatalf("provisioned predecessor identity not available to upgrade: %+v, %v", installed, err)
	}
	for name, change := range map[string]func(){
		"wrong target":   func() { binding.TargetID = "" },
		"wrong image":    func() { binding.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64) },
		"changed marker": func() { binding.LegacyConfig.Domain = "foreign.example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			original := binding
			change()
			writeBinding()
			if _, _, err := readUpgradeInstallation(root); err == nil {
				t.Fatal("foreign target binding was accepted")
			}
			binding = original
		})
	}
	current := legacy
	current.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	current.TargetID = "current-target"
	writeConfig(t, filepath.Join(root, installMarkerName), current)
	installed, _, err = readUpgradeInstallation(root)
	if err != nil || installed.TargetID != current.TargetID {
		t.Fatalf("current installer marker changed behavior: %+v, %v", installed, err)
	}
}
