package securitycontracts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRequiredSecurityWorkflowAggregatesEveryFailClosedLane(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	for _, fragment := range []string{
		"pull_request:",
		"push:",
		"branches: [main]",
		"merge_group:",
		"policy-validation:",
		"dependency-validation:",
		"source-validation:",
		"uses: ./.github/actions/setup-ci",
		"task security:source",
		"sast-validation:",
		"build-mode: autobuild",
		"build-mode: ${{ matrix.build-mode }}",
		"name: Security gate",
		"if: ${{ always() }}",
		"needs: [policy-validation, dependency-validation, source-validation, sast-validation]",
		"go run ./internal/app/tools/securityresults",
	} {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("security workflow is missing %q", fragment)
		}
	}
}

func TestOCIAdmissionActionUsesRepositoryOwnedGoContract(t *testing.T) {
	action := repositoryYAML(t, ".github/actions/oci-admission/action.yml")
	for _, fragment := range []string{
		"go-version-file: go.mod",
		"go run ./internal/app/tools/ociadmission",
		"--image \"$IMAGE\"",
		"--output \"$output_path\"",
	} {
		if !strings.Contains(action, fragment) {
			t.Errorf("OCI admission action is missing %q", fragment)
		}
	}
	if !containsPinnedAction(action, "actions/setup-go") {
		t.Fatal("OCI admission action does not use a commit-pinned setup-go action")
	}
	if strings.Contains(action, "scripts/admit_oci_artifact.sh") {
		t.Fatal("OCI admission action still invokes the legacy shell implementation")
	}
}

func TestAggregateJobChecksOutCandidateOwnedGoContract(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	start := strings.Index(workflow, "  security-gate:")
	if start < 0 {
		t.Fatal("security-gate job is missing")
	}
	aggregate := workflow[start:]
	if !containsPinnedAction(aggregate, "actions/checkout") {
		t.Fatal("aggregate job does not use a commit-pinned checkout action")
	}
	if !strings.Contains(aggregate, "go run ./internal/app/tools/securityresults") {
		t.Fatal("aggregate job does not invoke the candidate-owned Go result contract")
	}
}

func TestScannerJobsInvokeTheirRepositoryOwnedGoGates(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	var document struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatal(err)
	}
	for job, command := range map[string]string{
		"dependency-validation": "task security:dependencies",
		"source-validation":     "task security:source",
	} {
		definition, ok := document.Jobs[job]
		if !ok {
			t.Errorf("security workflow is missing %s", job)
			continue
		}
		found := false
		for _, step := range definition.Steps {
			if strings.TrimSpace(step.Run) == command {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s does not run %q", job, command)
		}
	}
}

func TestNativeOCIRefsAreAdmittedBeforeManifestAssembly(t *testing.T) {
	for _, path := range []string{".github/workflows/release.yml", ".github/workflows/site-image.yml"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			workflow := repositoryYAML(t, path)
			attestation := strings.Index(workflow, "Attest native")
			admission := strings.Index(workflow, "      - name: Admit exact native")
			record := strings.Index(workflow, "Record admitted native")
			assembly := strings.Index(workflow, "docker buildx imagetools create")
			topLevelAdmission := strings.LastIndex(workflow, "uses: ./.github/actions/oci-admission")
			if !(attestation >= 0 && admission > attestation && record > admission && assembly > record && topLevelAdmission > assembly) {
				t.Fatalf("native admission order is invalid in %s", path)
			}
			admittedBlock := workflow[admission:record]
			for _, fragment := range []string{
				"uses: ./.github/actions/oci-admission",
				"image: ${{ env.IMAGE_NAME }}@${{ steps.publish.outputs.digest }}",
				"repository: ${{ env.IMAGE_NAME }}",
				"source-revision: ${{ needs.identity.outputs.revision }}",
			} {
				if !strings.Contains(admittedBlock, fragment) {
					t.Errorf("native admission block in %s is missing %q", path, fragment)
				}
			}
			if !strings.Contains(admittedBlock, "expected-workflow: flidai/leapview/.github/workflows/") {
				t.Errorf("native admission block in %s has no trusted workflow identity", path)
			}
			recordedBlock := workflow[record:assembly]
			if !strings.Contains(recordedBlock, "IMAGE_REFERENCE: ${{ steps.admission.outputs.image }}") ||
				!strings.Contains(recordedBlock, `printf '%s\n' "$IMAGE_REFERENCE"`) {
				t.Errorf("manifest assembly in %s does not consume the admitted reference", path)
			}
			if !strings.Contains(workflow[assembly:], `image_references[@]`) {
				t.Errorf("manifest assembly in %s does not use the admitted reference list", path)
			}
		})
	}
}

func repositoryYAML(t *testing.T, relative string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test location")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../.."))
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		t.Fatalf("%s must contain one YAML document", relative)
	}
	return string(data)
}

func containsPinnedAction(workflow, action string) bool {
	needle := "uses: " + action + "@"
	start := strings.Index(workflow, needle)
	if start < 0 {
		return false
	}
	reference := workflow[start+len(needle):]
	end := strings.IndexAny(reference, " \t\r\n#")
	if end >= 0 {
		reference = reference[:end]
	}
	if len(reference) != 40 {
		return false
	}
	for _, character := range reference {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
