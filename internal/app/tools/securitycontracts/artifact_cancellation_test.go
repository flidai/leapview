package securitycontracts

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type artifactJob struct {
	If    string `yaml:"if"`
	Steps []struct {
		Name string            `yaml:"name"`
		Run  string            `yaml:"run"`
		Env  map[string]string `yaml:"env"`
	} `yaml:"steps"`
}

func artifactJobs(t *testing.T) map[string]artifactJob {
	t.Helper()
	var workflow struct {
		Jobs map[string]artifactJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(repositoryYAML(t, ".github/workflows/artifacts.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

func TestProductionImageJobGuardsStopCancelledWorkAndPreserveAdmission(t *testing.T) {
	jobs := artifactJobs(t)
	for _, test := range []struct {
		name          string
		repository    string
		event         string
		ref           string
		authorization string
		want          bool
	}{
		{"main with skipped authorization", "flidai/leapview", "push", "refs/heads/main", "skipped", true},
		{"authorized candidate", "flidai/leapview", "workflow_dispatch", "refs/heads/main", "success", true},
		{"failed candidate authorization", "flidai/leapview", "workflow_dispatch", "refs/heads/main", "failure", false},
		{"skipped candidate authorization", "flidai/leapview", "workflow_dispatch", "refs/heads/main", "skipped", false},
		{"cancelled candidate authorization", "flidai/leapview", "workflow_dispatch", "refs/heads/main", "cancelled", false},
		{"other push branch", "flidai/leapview", "push", "refs/heads/feature", "skipped", false},
		{"other repository", "other/leapview", "push", "refs/heads/main", "skipped", false},
		{"unauthorized event", "flidai/leapview", "pull_request", "refs/heads/main", "success", false},
	} {
		for _, cancelled := range []bool{false, true} {
			t.Run(test.name+"/cancelled="+strconv.FormatBool(cancelled), func(t *testing.T) {
				context := map[string]string{
					"github.repository":                test.repository,
					"github.event_name":                test.event,
					"github.ref":                       test.ref,
					"needs.authorize-candidate.result": test.authorization,
				}
				got := artifactGuard(t, jobs["build-production-image"].If, cancelled, context)
				if want := test.want && !cancelled; got != want {
					t.Fatalf("build admission = %t, want %t", got, want)
				}
			})
		}
	}
	for _, job := range []string{"qualify-production-image", "qualify-historical-transition"} {
		for _, result := range []string{"success", "failure", "skipped", "cancelled"} {
			for _, cancelled := range []bool{false, true} {
				t.Run(job+"/build="+result+"/cancelled="+strconv.FormatBool(cancelled), func(t *testing.T) {
					got := artifactGuard(t, jobs[job].If, cancelled, map[string]string{"needs.build-production-image.result": result})
					if want := result == "success" && !cancelled; got != want {
						t.Fatalf("qualification admission = %t, want %t", got, want)
					}
				})
			}
		}
	}
	for _, cancelled := range []bool{false, true} {
		t.Run("record/cancelled="+strconv.FormatBool(cancelled), func(t *testing.T) {
			if got := artifactGuard(t, jobs["record-production-image-qualification"].If, cancelled, nil); got == cancelled {
				t.Fatalf("record guard = %t, want %t", got, !cancelled)
			}
		})
	}
}

// These guards share Bash's Boolean/comparison syntax. Substitute the finite
// context and status operands, then use Bash rather than a custom expression
// interpreter. An explicit status function avoids Actions' implicit success()
// check after main's skipped authorization or a failed qualification.
func artifactGuard(t *testing.T, condition string, cancelled bool, context map[string]string) bool {
	t.Helper()
	if !strings.HasPrefix(condition, "${{") || !strings.HasSuffix(strings.TrimSpace(condition), "}}") ||
		!strings.Contains(condition, "always()") && !strings.Contains(condition, "cancelled()") {
		t.Fatalf("producer guard must declare an explicit status check: %q", condition)
	}
	condition = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(condition), "${{"), "}}"))
	condition = strings.ReplaceAll(condition, "\n", " ")
	for name, value := range context {
		condition = strings.ReplaceAll(condition, name, strconv.Quote(value))
	}
	condition = strings.NewReplacer("always()", "( 1 == 1 )", "cancelled()", "( 1 == "+strconv.Itoa(boolInt(cancelled))+" )").Replace(condition)
	condition = strings.NewReplacer("(", " ( ", ")", " ) ", "!", " ! ").Replace(condition)
	if strings.Contains(condition, "github.") || strings.Contains(condition, "needs.") || strings.ContainsAny(condition, "$`;\\\n") {
		t.Fatalf("unsupported producer guard: %q", condition)
	}
	output, err := exec.Command("bash", "-c", "if [[ "+condition+" ]]; then exit 0; else exit 1; fi").CombinedOutput()
	if err == nil {
		return true
	}
	if code, ok := err.(*exec.ExitError); ok && code.ExitCode() == 1 {
		return false
	}
	t.Fatalf("evaluate producer guard: %v\n%s", err, output)
	return false
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestProductionImageReceiptRequiresEverySuccessfulDependency(t *testing.T) {
	job := artifactJobs(t)["record-production-image-qualification"]
	if len(job.Steps) == 0 || job.Steps[0].Name != "Require both exact-image qualifications" {
		t.Fatal("receipt recording must check dependency results before downloading or writing receipts")
	}
	step := job.Steps[0]
	dependencies := map[string]string{
		"IMAGE_BUILD_RESULT":           "needs.build-production-image.result",
		"IMAGE_QUALIFICATION_RESULT":   "needs.qualify-production-image.result",
		"HISTORICAL_TRANSITION_RESULT": "needs.qualify-historical-transition.result",
	}
	for name, dependency := range dependencies {
		if step.Env[name] != "${{ "+dependency+" }}" {
			t.Fatalf("receipt result %s is not bound to %s", name, dependency)
		}
	}
	for _, failed := range []string{"", "IMAGE_BUILD_RESULT", "IMAGE_QUALIFICATION_RESULT", "HISTORICAL_TRANSITION_RESULT"} {
		t.Run("failed="+failed, func(t *testing.T) {
			if !artifactGuard(t, job.If, false, nil) {
				t.Fatal("non-cancelled failures must reach the receipt result check")
			}
			command := exec.Command("bash", "-c", step.Run)
			command.Env = os.Environ()
			for name := range dependencies {
				result := "success"
				if name == failed {
					result = "failure"
				}
				command.Env = append(command.Env, name+"="+result)
			}
			output, err := command.CombinedOutput()
			if (err == nil) != (failed == "") {
				t.Fatalf("receipt precondition error = %v, failed dependency = %q\n%s", err, failed, output)
			}
		})
	}
}
