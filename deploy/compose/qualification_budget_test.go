package compose

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestInstalledQualificationBudgetAndWorkflowTimeout(t *testing.T) {
	root := filepath.Join("..", "..")
	installedSource := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_installed.go"))
	start := strings.Index(installedSource, "func (c *Controller) QualifyInstalledCandidate")
	if start < 0 {
		t.Fatal("installed qualification entrypoint is missing")
	}
	end := strings.Index(installedSource[start:], "\nfunc isQualificationLowerHex")
	if end < 0 {
		t.Fatal("installed qualification entrypoint boundary is missing")
	}
	entrypoint := installedSource[start : start+end]

	// Keep this map in the test as the phase-admission contract. It makes a
	// removed or shortened evidence phase fail review instead of silently
	// making the workflow timeout look generous.
	wantPhases := map[string]int{
		"preflight":             15,
		"target bootstrap":      20,
		"enterprise authoring":  30,
		"application upgrade":   15,
		"performance":           45,
		"governance":            10,
		"interruption recovery": 60,
		"restart persistence":   15,
		"multi-node process":    20,
	}
	phasePattern := regexp.MustCompile(`phases\.Begin\(rootContext,\s*"([^"]+)",\s*(\d+)\*time\.Minute\)`)
	phaseMatches := phasePattern.FindAllStringSubmatch(entrypoint, -1)
	if len(phaseMatches) != len(wantPhases) {
		t.Fatalf("installed qualification has %d sequential phases, want %d", len(phaseMatches), len(wantPhases))
	}
	totalMinutes := 0
	for _, match := range phaseMatches {
		minutes, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("parse %s phase budget: %v", match[1], err)
		}
		want, ok := wantPhases[match[1]]
		if !ok {
			t.Errorf("unexpected installed qualification phase %q", match[1])
			continue
		}
		if minutes != want {
			t.Errorf("%s phase budget = %d minutes, want %d", match[1], minutes, want)
		}
		totalMinutes += minutes
	}
	if totalMinutes != 230 {
		t.Fatalf("installed qualification sequential phase budget = %d minutes, want 230", totalMinutes)
	}

	// Setup (toolchain/download), cleanup (Compose teardown), and bounded
	// evidence upload get an explicit 20-minute allowance. The workflow job
	// must exceed the phase budget plus that allowance; phase evidence remains
	// the complete nine-phase journey above.
	const setupCleanupArtifactHeadroomMinutes = 20
	minimumTimeout := totalMinutes + setupCleanupArtifactHeadroomMinutes
	for _, workflowName := range []string{"release.yml", "installed-candidate.yml"} {
		workflow := read(t, filepath.Join(root, ".github", "workflows", workflowName))
		jobStart := strings.Index(workflow, "\n  qualify:")
		if jobStart < 0 {
			t.Fatalf("%s is missing qualify job", workflowName)
		}
		job := workflow[jobStart:]
		timeoutMatch := regexp.MustCompile(`(?m)^    timeout-minutes:\s*(\d+)\s*$`).FindStringSubmatch(job)
		if len(timeoutMatch) != 2 {
			t.Fatalf("%s qualify job is missing timeout-minutes", workflowName)
		}
		timeoutMinutes, err := strconv.Atoi(timeoutMatch[1])
		if err != nil {
			t.Fatalf("parse %s qualify timeout: %v", workflowName, err)
		}
		if timeoutMinutes <= minimumTimeout {
			t.Errorf("%s qualify timeout = %d minutes, want > %d (phases %d + headroom %d)", workflowName, timeoutMinutes, minimumTimeout, totalMinutes, setupCleanupArtifactHeadroomMinutes)
		}
	}
}
