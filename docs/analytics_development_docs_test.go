package docs

import (
	"os"
	"strings"
	"testing"
)

func TestAnalyticsDevelopmentGuideAndEvidenceMatrixStayLinked(t *testing.T) {
	guideBytes, err := Files.ReadFile("guides/cli/analytics-development.md")
	if err != nil {
		t.Fatalf("read analytics development guide: %v", err)
	}
	guide := string(guideBytes)
	for _, phrase := range []string{
		"current branch",
		"released authoring package",
		"task dev",
		"leapview init",
		"stages them to the internally resolved local target and Project",
		"a restart reuses the retained revision",
		"ordinary YAML edits never refresh mutable inputs",
		"profiles.local.yaml",
		"--allow-upstream-read",
		"leapview dev --target staging",
		"leapview dev status",
		"leapview dev reset",
		"--new --operation release-42",
		"--resume --operation release-42",
		"credential-free, versioned",
		"Production owns the target's bindings",
		"dbt profile importer is deferred",
		"Milestone 5 conformance evidence matrix",
	} {
		if !strings.Contains(guide, phrase) {
			t.Errorf("analytics development guide does not cover %q", phrase)
		}
	}

	matrixBytes, err := os.ReadFile("../adr/specifications/adr-0021-milestone-5-conformance-evidence.md")
	if err != nil {
		t.Fatalf("read Milestone 5 evidence matrix: %v", err)
	}
	matrix := string(matrixBytes)
	for _, phrase := range []string{
		"## ADR-0021 Confirmation groups",
		"## CLI contract rows",
		"Automated/local",
		"External platform/manual",
		"Release-blocked",
		"[FAI-778]",
		"[FAI-788]",
	} {
		if !strings.Contains(matrix, phrase) {
			t.Errorf("Milestone 5 evidence matrix does not contain %q", phrase)
		}
	}

	// Keep this list synchronized with the Required conformance evidence table
	// in analytics-development-cli-contract.md. Each scenario must have a
	// corresponding evidence row, even when its status is not released.
	for _, scenario := range []string{
		"Remote active Docker context",
		"Misleading context name",
		"Supported local Engine",
		"Docker context changes during startup",
		"PostgreSQL and object-storage sources",
		"Profile-file/name selection",
		"Invalid schema",
		"Missing credential",
		"Different profiles attach",
		"A covers commerce/inventory",
		"Graph gains a required external connection",
		"Second binding fails",
		"Crash after all changes",
		"Recovery sees edited YAML",
		"Same profile and variable name",
		"Explicit secret rotation",
		"Switch away from a credential-bearing connection",
		"Connection works on host",
		"Guided setup",
		"Approved remote reads",
		"Local profile followed by production deployment",
		"Two sessions",
		"Crash or concurrent join",
		"Stop/reset with attachments",
		"Several dashboard queries",
		"loses publication acknowledgement",
		"Multiple checkpoints",
		"Headless missing/unknown handle",
		"Resume from retained CI artifact",
		"Pending approval",
	} {
		if !strings.Contains(matrix, scenario) {
			t.Errorf("evidence matrix is missing CLI scenario %q", scenario)
		}
	}

	navigation, err := Files.ReadFile("navigation.yaml")
	if err != nil {
		t.Fatalf("read documentation navigation: %v", err)
	}
	for _, fragment := range []string{
		"slug: cli/analytics-development",
		"source: guides/cli/analytics-development.md",
	} {
		if !strings.Contains(string(navigation), fragment) {
			t.Errorf("documentation navigation is missing %q", fragment)
		}
	}
}
