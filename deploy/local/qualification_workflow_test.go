package local

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleasedAuthoringQualificationWorkflowArguments(t *testing.T) {
	root := repositoryRoot(t)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join("..", "..", ".github", "workflows", "authoring-package-qualification.yml"))), &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	var keyringSetup bool
	for _, step := range workflow.Jobs["qualify"].Steps {
		if step.Name == "Install optional lifecycle keyring dependencies" {
			keyringSetup = step.If == "inputs.run_lifecycle == true"
		}
		if step.Name == "Run required package qualification and optional lifecycle" {
			script = step.Run
		}
	}
	if !keyringSetup {
		t.Fatal("native keyring dependencies must be installed only for requested lifecycle runs")
	}
	if script == "" {
		t.Fatal("released authoring qualification step is missing")
	}

	// Execute the workflow's shell arguments against the real parser, without
	// downloading an archive or provisioning the optional Docker runtime.
	parserProbe := `import importlib.util
import json
import os
import sys

spec = importlib.util.spec_from_file_location("leapview_qualify", os.environ["QUALIFICATION_MODULE"])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
print(json.dumps(vars(module.parse_args(sys.argv[1:]))))
`
	for _, lifecycle := range []bool{false, true} {
		t.Run("lifecycle="+strconv.FormatBool(lifecycle), func(t *testing.T) {
			directory := t.TempDir()
			entrypoint := filepath.Join(directory, "deploy", "local", "qualification", "qualify.sh")
			if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
				t.Fatal(err)
			}
			requireWriteFile(t, filepath.Join(filepath.Dir(entrypoint), "parse.py"), parserProbe, 0o600)
			requireWriteFile(t, entrypoint, "#!/usr/bin/env bash\nexec python3 \"$(dirname \"$0\")/parse.py\" \"$@\"\n", 0o755)
			wrapper := filepath.Join(filepath.Dir(entrypoint), "with_keyring.py")
			requireWriteFile(t, wrapper, "import os, sys\nassert os.environ['RUN_LIFECYCLE'] == 'true'\nopen(os.environ['KEYRING_MARKER'], 'w').close()\nos.execvp(sys.argv[1], sys.argv[1:])\n", 0o600)
			marker := filepath.Join(directory, "keyring-used")
			archive := filepath.Join(directory, "archive with spaces.tar.gz")
			evidence := filepath.Join(directory, "evidence with spaces")
			command := exec.Command("bash", "-c", script)
			command.Dir = directory
			command.Env = append(os.Environ(),
				"ARCHIVE="+archive,
				"EVIDENCE_DIR="+evidence,
				"RUN_LIFECYCLE="+strconv.FormatBool(lifecycle),
				"KEYRING_MARKER="+marker,
				"QUALIFICATION_MODULE="+filepath.Join(root, "deploy", "local", "qualification", "qualify.py"),
				"PYTHONDONTWRITEBYTECODE=1",
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("workflow arguments rejected by qualification parser: %v\n%s", err, output)
			}
			var arguments struct {
				Archive      string `json:"archive"`
				Checksum     string `json:"checksum"`
				EvidenceDir  string `json:"evidence_dir"`
				Required     bool   `json:"required"`
				RunLifecycle bool   `json:"run_lifecycle"`
				DockerHost   string `json:"docker_host"`
			}
			if err := json.Unmarshal(output, &arguments); err != nil {
				t.Fatalf("parse workflow arguments: %v\n%s", err, output)
			}
			if arguments.Archive != archive || arguments.Checksum != archive+".sha256" || arguments.EvidenceDir != evidence || !arguments.Required || arguments.RunLifecycle != lifecycle {
				t.Fatalf("workflow arguments = %+v", arguments)
			}
			wantDockerHost := ""
			if lifecycle {
				wantDockerHost = "unix:///var/run/docker.sock"
			}
			if arguments.DockerHost != wantDockerHost {
				t.Fatalf("workflow Docker host = %q, want %q", arguments.DockerHost, wantDockerHost)
			}
			_, markerErr := os.Stat(marker)
			if (markerErr == nil) != lifecycle {
				t.Fatalf("isolated keyring usage does not match lifecycle=%t", lifecycle)
			}
		})
	}
}

func TestNativeAuthoringBuildRunsRequiredStaticQualification(t *testing.T) {
	root := repositoryRoot(t)
	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []struct{ OS, Arch string } `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))), &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["authoring-cli"]
	var script string
	var durableEvidence bool
	for _, step := range job.Steps {
		if step.Name == "Qualify installed discovery and diagnostics" {
			script = step.Run
		}
		if step.Name == "Upload authoring qualification evidence" {
			durableEvidence = step.If == "always()" && strings.Contains(step.With["path"], "qualification-report.json") && strings.Contains(step.With["path"], "raw-results.json")
		}
	}
	if script == "" || !durableEvidence {
		t.Fatal("native archive builds must run static qualification and retain success/failure evidence")
	}
	if len(job.Strategy.Matrix.Include) != 4 {
		t.Fatal("expected the four existing native authoring targets")
	}
	for _, target := range job.Strategy.Matrix.Include {
		t.Run(target.OS+"/"+target.Arch, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "checkout with spaces")
			entrypoint := filepath.Join(directory, "deploy/local/qualification/qualify.sh")
			if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
				t.Fatal(err)
			}
			// Bash reports the physical working directory on macOS, where
			// the temporary root may be reached through /var's symlink.
			var err error
			directory, err = filepath.EvalSymlinks(directory)
			if err != nil {
				t.Fatal(err)
			}
			entrypoint = filepath.Join(directory, "deploy/local/qualification/qualify.sh")
			requireWriteFile(t, filepath.Join(filepath.Dir(entrypoint), "parse.py"), `import importlib.util, json, os, sys
spec = importlib.util.spec_from_file_location("qualification", os.environ["QUALIFICATION_MODULE"])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
print(json.dumps(vars(module.parse_args(sys.argv[1:]))))
`, 0o600)
			requireWriteFile(t, entrypoint, "#!/usr/bin/env bash\nexec python3 \"$(dirname \"$0\")/parse.py\" \"$@\"\n", 0o755)
			command := exec.Command("bash", "-c", script)
			command.Dir = directory
			command.Env = append(os.Environ(), "RELEASE_TAG=candidate-123-1", "TARGET_OS="+target.OS, "TARGET_ARCH="+target.Arch,
				"QUALIFICATION_MODULE="+filepath.Join(root, "deploy/local/qualification/qualify.py"), "PYTHONDONTWRITEBYTECODE=1")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("native qualification arguments rejected: %v\n%s", err, output)
			}
			var arguments struct {
				Archive      string `json:"archive"`
				EvidenceDir  string `json:"evidence_dir"`
				Required     bool   `json:"required"`
				RunLifecycle bool   `json:"run_lifecycle"`
			}
			if err := json.Unmarshal(output, &arguments); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(directory, "dist", "leapview-cli-candidate-123-1-"+target.OS+"-"+target.Arch+".tar.gz")
			evidence := filepath.Join(directory, "dist/evidence", "qualification-"+target.OS+"-"+target.Arch)
			if arguments.Archive != archive || arguments.EvidenceDir != evidence || !arguments.Required || arguments.RunLifecycle {
				t.Fatalf("native static qualification arguments = %+v", arguments)
			}
		})
	}
}
