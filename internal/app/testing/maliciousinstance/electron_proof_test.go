package maliciousinstance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

const maximumElectronProofDiagnosticBytes = 8 * 1024

type electronProofResult struct {
	Passed          bool          `json:"passed"`
	Framework       string        `json:"framework"`
	Chromium        string        `json:"chromium"`
	Phase           string        `json:"phase"`
	ManifestVersion string        `json:"manifestVersion"`
	Observations    []Observation `json:"observations"`
	Error           string        `json:"error"`
}

func TestElectronPolicyIntegrationPreservesBrowserEquivalentAuthority(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
	default:
		t.Skipf("Electron policy integration is unsupported on %s", runtime.GOOS)
	}
	electronBinary := os.Getenv("LEAPVIEW_ELECTRON_BINARY")
	if electronBinary == "" {
		t.Skip("set LEAPVIEW_ELECTRON_BINARY to run the pinned Electron policy integration")
	}

	externalServer := newExternalTargetServer(t)
	harness, err := New(Config{ExternalOrigin: externalServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	server := newHarnessServer(t, harness)

	resultPath := filepath.Join(t.TempDir(), "electron-proof.json")
	runnerPath := filepath.Join("electron", "runner.mjs")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, electronBinary, runnerPath) //nolint:gosec // The explicit test-only binary is provided by the caller.
	command.Env = append(os.Environ(),
		"LEAPVIEW_PROOF_ORIGIN="+server.URL,
		"LEAPVIEW_PROOF_RESULT="+resultPath,
	)
	output, runErr := command.CombinedOutput()

	result, payload, err := readElectronProofResult(resultPath, runErr, output)
	if err != nil {
		t.Fatal(err)
	}
	if err := electronProofProcessFailure(result, payload, runErr, output); err != nil {
		t.Fatal(err)
	}
	manifest := harness.Manifest()
	if result.ManifestVersion != manifest.Version {
		t.Fatalf("Electron proof manifest version = %q, want %q", result.ManifestVersion, manifest.Version)
	}
	if len(result.Observations) != len(manifest.Attacks) {
		t.Fatalf("Electron proof observations = %v, want exactly %d", result.Observations, len(manifest.Attacks))
	}
	seen := make(map[string]Outcome, len(result.Observations))
	for _, observation := range result.Observations {
		if _, duplicate := seen[observation.AttackID]; duplicate {
			t.Fatalf("Electron proof contains duplicate observation %q", observation.AttackID)
		}
		seen[observation.AttackID] = observation.Outcome
	}
	for _, attack := range manifest.Attacks {
		if got, ok := seen[attack.ID]; !ok || got != attack.Expected {
			t.Fatalf("Electron proof observation %q = %q, present=%v, want %q", attack.ID, got, ok, attack.Expected)
		}
	}
	t.Logf("%s / Chromium %s satisfied all %d security invariants", result.Framework, result.Chromium, len(result.Observations))
}

func readElectronProofResult(
	resultPath string,
	processErr error,
	output []byte,
) (electronProofResult, []byte, error) {
	payload, err := os.ReadFile(resultPath)
	if err != nil {
		classification := "could not be read"
		if os.IsNotExist(err) {
			classification = "is missing"
		}
		return electronProofResult{}, nil, fmt.Errorf(
			"Electron proof result %s: %v\n%s",
			classification,
			err,
			electronProofProcessDiagnostics(processErr, output),
		)
	}
	if len(payload) == 0 {
		return electronProofResult{}, payload, fmt.Errorf(
			"Electron proof result is empty (size=0 bytes)\nraw result: %s\n%s",
			formatElectronProofPayload(payload),
			electronProofProcessDiagnostics(processErr, output),
		)
	}

	var result electronProofResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return electronProofResult{}, payload, fmt.Errorf(
			"Electron proof result is malformed (size=%d bytes): %w\nraw result: %s\n%s",
			len(payload),
			err,
			formatElectronProofPayload(payload),
			electronProofProcessDiagnostics(processErr, output),
		)
	}
	return result, payload, nil
}

func electronProofProcessFailure(
	result electronProofResult,
	payload []byte,
	processErr error,
	output []byte,
) error {
	if processErr == nil && result.Passed {
		return nil
	}
	return fmt.Errorf(
		"Electron proof returned a valid failure result (size=%d bytes, phase=%q, error=%q)\nraw result: %s\n%s",
		len(payload),
		result.Phase,
		result.Error,
		formatElectronProofPayload(payload),
		electronProofProcessDiagnostics(processErr, output),
	)
}

func electronProofProcessDiagnostics(processErr error, output []byte) string {
	return fmt.Sprintf("process error: %v\noutput:\n%s", processErr, output)
}

func formatElectronProofPayload(payload []byte) string {
	display := payload
	suffix := ""
	if len(display) > maximumElectronProofDiagnosticBytes {
		display = display[:maximumElectronProofDiagnosticBytes]
		suffix = fmt.Sprintf(" (truncated; total size=%d bytes)", len(payload))
	}
	return strconv.QuoteToASCII(string(display)) + suffix
}
