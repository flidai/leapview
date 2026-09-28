package host_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const revision019Image = "ghcr.io/flidai/leapview@sha256:4a4455ff0048704acf0df1a9308a39a09b4c786f801fe7f3a383ada089d21368"

func revision019Config() map[string]any {
	return map[string]any{
		"schemaVersion": 1, "domain": "dash.example.com", "adminEmail": "admin@example.com",
		"environment": "prod", "image": revision019Image, "targetId": "deployment-target-019", "https": true,
	}
}

func writePrivateJSON(t *testing.T, path string, value map[string]any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func runRevision019Compat(t *testing.T, wantSuccess bool, args ...string) {
	t.Helper()
	command := exec.Command("python3", append([]string{"revision019-config-compat.py"}, args...)...)
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("compatibility command failed: %v: %s", err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatal("compatibility command accepted invalid input")
	}
}

func TestRevision019TranslationAndProvisionedBinding(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "bootstrap.json")
	translated := filepath.Join(directory, "revision019.json")
	binding := filepath.Join(directory, "installed", ".host-target-binding.json")
	marker := filepath.Join(directory, "installed", ".host-install.json")
	writePrivateJSON(t, config, revision019Config())
	prepare := []string{"prepare", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker}
	runRevision019Compat(t, true, prepare...)
	legacy := readJSON(t, translated)
	if _, ok := legacy["targetId"]; ok || len(legacy) != 6 || legacy["image"] != revision019Image {
		t.Fatalf("revision-019 would receive unsupported fields: %v", legacy)
	}
	bound := readJSON(t, binding)
	if bound["targetId"] != "deployment-target-019" || bound["image"] != revision019Image ||
		!reflect.DeepEqual(bound["legacyConfig"], legacy) {
		t.Fatalf("provisioned identity differs from installer input: %v", bound)
	}
	for _, path := range []string{translated, binding} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s must be private: %v, %v", path, info, err)
		}
	}
	// The target binding exists before the immutable predecessor writes its marker.
	writePrivateJSON(t, marker, legacy)
	verify := []string{"verify", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker}
	runRevision019Compat(t, true, verify...)
	runRevision019Compat(t, true, prepare...)
	runRevision019Compat(t, true, verify...)
	if !reflect.DeepEqual(readJSON(t, marker), legacy) {
		t.Fatal("compatibility layer modified the predecessor-owned marker")
	}
}

func TestRevision019TranslationCanonicalizesPredecessorMarkerDomain(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "bootstrap.json")
	translated := filepath.Join(directory, "revision019.json")
	binding := filepath.Join(directory, ".host-target-binding.json")
	marker := filepath.Join(directory, ".host-install.json")
	current := revision019Config()
	current["domain"] = "Dash.Example.Com."
	writePrivateJSON(t, config, current)
	runRevision019Compat(t, true, "prepare", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker)
	legacy := readJSON(t, translated)
	if legacy["domain"] != "dash.example.com" {
		t.Fatalf("old installer would write a different marker: %v", legacy["domain"])
	}
	writePrivateJSON(t, marker, legacy)
	runRevision019Compat(t, true, "verify", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker)
}

func TestRevision019TranslationRejectsUnsupportedFields(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"unknown field":  func(config map[string]any) { config["candidateImage"] = "unexpected" },
		"missing target": func(config map[string]any) { delete(config, "targetId") },
		"empty target":   func(config map[string]any) { config["targetId"] = "" },
		"wrong image": func(config map[string]any) {
			config["image"] = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			current := revision019Config()
			change(current)
			config := filepath.Join(directory, "bootstrap.json")
			translated := filepath.Join(directory, "revision019.json")
			binding := filepath.Join(directory, ".host-target-binding.json")
			marker := filepath.Join(directory, ".host-install.json")
			writePrivateJSON(t, config, current)
			runRevision019Compat(t, false, "prepare", "--config", config, "--image", revision019Image,
				"--translated", translated, "--binding", binding, "--marker", marker)
			for _, path := range []string{translated, binding} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid input created %s: %v", path, err)
				}
			}
		})
	}
}

func TestRevision019BindingRejectsForeignState(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "bootstrap.json")
	translated := filepath.Join(directory, "revision019.json")
	binding := filepath.Join(directory, "installed", ".host-target-binding.json")
	marker := filepath.Join(directory, "installed", ".host-install.json")
	writePrivateJSON(t, config, revision019Config())
	prepare := []string{"prepare", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker}
	runRevision019Compat(t, true, prepare...)
	foreign := readJSON(t, translated)
	foreign["image"] = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	writePrivateJSON(t, marker, foreign)
	runRevision019Compat(t, false, "verify", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker)
	current := revision019Config()
	current["targetId"] = "another-target"
	writePrivateJSON(t, config, current)
	runRevision019Compat(t, false, prepare...)
}

func TestRevision019CannotRebindExistingUnboundInstallation(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "bootstrap.json")
	translated := filepath.Join(directory, "revision019.json")
	binding := filepath.Join(directory, ".host-target-binding.json")
	marker := filepath.Join(directory, ".host-install.json")
	current := revision019Config()
	writePrivateJSON(t, config, current)
	legacy := revision019Config()
	delete(legacy, "targetId")
	writePrivateJSON(t, marker, legacy)
	runRevision019Compat(t, false, "prepare", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", marker)
	if _, err := os.Stat(binding); !os.IsNotExist(err) {
		t.Fatalf("existing unbound installation was given a target binding: %v", err)
	}
}

func TestCurrentInstallerKeepsItsConfiguration(t *testing.T) {
	bootstrap := read(t, "bootstrap-linux.sh")
	if !strings.Contains(bootstrap, `install_config="$config_file"`) ||
		!strings.Contains(bootstrap, `installer="$payload_dir/leapviewctl"`) ||
		!strings.Contains(bootstrap, `--config "$install_config"`) ||
		!strings.Contains(bootstrap, `installer="$controller_dir/leapviewctl"`) {
		t.Fatal("current installers must receive the unmodified current configuration")
	}
}

func TestActualRevision019ExecutableAcceptsTranslatedConfiguration(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_REV019_EXECUTABLE") != "1" {
		t.Skip("set LEAPVIEW_TEST_REV019_EXECUTABLE=1 to exercise the immutable predecessor image")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is unavailable")
	}
	directory := t.TempDir()
	config := filepath.Join(directory, "current.json")
	translated := filepath.Join(directory, "revision019.json")
	binding := filepath.Join(directory, "binding.json")
	writePrivateJSON(t, config, revision019Config())
	runRevision019Compat(t, true, "prepare", "--config", config, "--image", revision019Image,
		"--translated", translated, "--binding", binding, "--marker", filepath.Join(directory, "marker.json"))
	command := exec.Command("docker", "run", "--rm", "--user", "0",
		"--entrypoint", "/usr/local/share/leapview/deployment/leapviewctl",
		"--mount", "type=bind,src="+translated+",dst=/run/revision019.json,readonly",
		revision019Image, "host", "install", "--config", "/run/revision019.json",
		"--payload", "/absent-payload", "--source-image", revision019Image)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "validate host installation payload") {
		t.Fatalf("revision-019 executable did not accept translated config before rejecting absent payload: %v: %s", err, output)
	}
}
