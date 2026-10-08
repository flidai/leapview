package architecture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSourceTimingPreservesExecution(t *testing.T) {
	commands := []string{
		"run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote",
		"run ./internal/app/tools/configgen",
		"run ./internal/app/tools/layoutcontractgen",
		"-C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target leapview-v1",
		"-C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target leapview-v1",
		"run ./internal/app/tools/apigenpatch",
		"-C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target ui-signals",
		"-C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target ui-signals",
		"run ./internal/app/tools/signalcontracts",
		"-C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target desktop-discovery-contracts",
		"-C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target desktop-discovery-contracts",
		"-C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target data-resource-contracts",
		"-C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target data-resource-contracts",
		"run ./internal/project/contracts/generate",
		"-C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target visualization-ir",
		"-C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target visualization-ir",
		"run ./cmd/leapview schema export --format json-schema --out schemas/json",
	}
	phases := []string{"sqlc", "config", "layout-contract", "leapview-v1-typespec", "leapview-v1", "api-patch", "ui-signals-typespec", "ui-signals", "signal-contracts", "desktop-discovery-typespec", "desktop-discovery", "data-resource-typespec", "data-resource", "project-contracts", "visualization-ir-typespec", "visualization-ir", "json-schema"}
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			fixture := newBuildTimingFixture(t)
			count, status := len(commands), 0
			if fail {
				count, status = 6, 23
			}
			cmd := exec.Command(fixture.shell, "scripts/generate_build_sources.sh")
			cmd.Dir = repoRoot(t)
			cmd.Env = append(fixture.env, fmt.Sprintf("FAIL_API_PATCH=%t", fail))
			output, err := cmd.CombinedOutput()
			if got := commandExitCode(err); got != status {
				t.Fatalf("exit code = %d, want %d: %s", got, status, output)
			}
			var wantCalls, wantOutput strings.Builder
			for i := range count {
				debug, toolchain := "caller-debug", "caller-toolchain"
				if i == 0 {
					debug, toolchain = "http2client=0", "go1.26.7"
				}
				fmt.Fprintf(&wantCalls, "%s|%s|", debug, toolchain)
				for _, arg := range strings.Fields(commands[i]) {
					fmt.Fprintf(&wantCalls, "<%s>", arg)
				}
				wantCalls.WriteByte('\n')
				exitCode := 0
				if i == count-1 {
					exitCode = status
				}
				fmt.Fprintf(&wantOutput, "command stdout\ncommand stderr\nbuild_phase=%s elapsed_seconds=2 exit_code=%d\n", phases[i], exitCode)
			}
			calls, err := os.ReadFile(fixture.calls)
			if err != nil {
				t.Fatal(err)
			}
			if string(calls) != wantCalls.String() {
				t.Errorf("command arguments/environment/order changed:\ngot %s\nwant %s", calls, wantCalls.String())
			}
			if string(output) != wantOutput.String() {
				t.Errorf("output = %q, want %q", output, wantOutput.String())
			}
		})
	}
}

func TestBuildTimingPreservesArgumentBoundariesAndOutputStreams(t *testing.T) {
	fixture := newBuildTimingFixture(t)
	cmd := exec.Command(fixture.shell, "scripts/time_build_phase.sh", "argument-check", "go", "two words", "", "literal *")
	cmd.Dir = repoRoot(t)
	cmd.Env = fixture.env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("timed command: %v: %s", err, stderr.String())
	}
	calls, err := os.ReadFile(fixture.calls)
	if err != nil {
		t.Fatal(err)
	}
	if want := "caller-debug|caller-toolchain|<two words><><literal *>\n"; string(calls) != want {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	if string(stdout) != "command stdout\n" || stderr.String() != "command stderr\nbuild_phase=argument-check elapsed_seconds=2 exit_code=0\n" {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr.String())
	}
}

func TestBuildTimingClockFailurePreservesCommandFailure(t *testing.T) {
	fixture := newBuildTimingFixture(t)
	cmd := exec.Command(fixture.shell, "scripts/time_build_phase.sh", "clock-failure", "go", "run", "./internal/app/tools/apigenpatch")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(fixture.env, "FAIL_API_PATCH=true", "FAIL_CLOCK=true")
	output, err := cmd.CombinedOutput()
	if got := commandExitCode(err); got != 23 {
		t.Fatalf("exit code = %d, want command status 23: %s", got, output)
	}
	if want := "command stdout\ncommand stderr\nbuild_phase=clock-failure elapsed_seconds=unavailable exit_code=23\n"; string(output) != want {
		t.Errorf("output = %q, want %q", output, want)
	}
}

type buildTimingFixture struct {
	shell string
	calls string
	env   []string
}

func newBuildTimingFixture(t *testing.T) buildTimingFixture {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go": `printf '%s|%s|' "$GODEBUG" "$GOTOOLCHAIN" >> "$CALLS"
printf '<%s>' "$@" >> "$CALLS"
printf '\n' >> "$CALLS"
printf 'command stdout\n'
printf 'command stderr\n' >&2
if [ "${FAIL_API_PATCH:-false}" = true ] && [ "$*" = 'run ./internal/app/tools/apigenpatch' ]; then exit 23; fi
`,
		"date": `if [ "${FAIL_CLOCK:-false}" = true ]; then exit 42; fi
read -r counter < "$CLOCK_FILE"
printf '%s\n' "$counter"
printf '%s\n' "$((counter + 2))" > "$CLOCK_FILE"
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+shell+"\nset -eu\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	clock := filepath.Join(dir, "clock")
	if err := os.WriteFile(clock, []byte("100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	env := append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "CALLS="+calls, "CLOCK_FILE="+clock, "GODEBUG=caller-debug", "GOTOOLCHAIN=caller-toolchain")
	return buildTimingFixture{shell: shell, calls: calls, env: env}
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return -1
}
