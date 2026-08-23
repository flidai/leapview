package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCoverageDiscoversOmissionsAndDuplicates(t *testing.T) {
	root := fixtureRepository(t)
	coverage := mustReadCoverage(t, root)
	coverage.Surfaces = coverage.Surfaces[:1]
	writeCoverage(t, root, coverage)
	assertValidationError(t, root, "missing from coverage")

	coverage = fixtureCoverage()
	coverage.Surfaces = append(coverage.Surfaces, coverage.Surfaces[0])
	writeCoverage(t, root, coverage)
	assertValidationError(t, root, "duplicate coverage path")

	coverage = fixtureCoverage()
	coverage.Surfaces = append(coverage.Surfaces, Surface{
		Path: "does-not-exist", Kind: "go-module", Updater: Updater{Ecosystem: "gomod", Directory: "/"}, Scanners: []string{"osv-scanner"},
	})
	writeCoverage(t, root, coverage)
	assertValidationError(t, root, "not a maintained security surface")

	root = fixtureRepository(t)
	if err := os.Remove(filepath.Join(root, "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	assertValidationError(t, root, "not a maintained security surface")
}

func TestExceptionsRejectUnsafeEntries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Exception)
		want   string
	}{
		{"malformed date", func(e *Exception) { e.Expires = "tomorrow" }, "ISO date"},
		{"expired", func(e *Exception) { e.Expires = "2026-08-22" }, "expired"},
		{"over 90 days", func(e *Exception) { e.Expires = "2026-11-01" }, "90-day"},
		{"ownerless", func(e *Exception) { e.Owner = "  " }, "owner is required"},
		{"rationaleless", func(e *Exception) { e.Rationale = "" }, "rationale is required"},
		{"overbroad", func(e *Exception) { e.Resource = "*" }, "overbroad"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := fixtureRepository(t)
			exception := fixtureException()
			test.mutate(&exception)
			writeExceptions(t, root, Exceptions{Version: 1, Exceptions: []Exception{exception}})
			assertValidationError(t, root, test.want)
		})
	}

	t.Run("duplicate ids", func(t *testing.T) {
		root := fixtureRepository(t)
		exception := fixtureException()
		writeExceptions(t, root, Exceptions{Version: 1, Exceptions: []Exception{exception, exception}})
		assertValidationError(t, root, "duplicate exception id")
	})
}

func TestUpdaterCoverageAndBounds(t *testing.T) {
	root := fixtureRepository(t)
	path := filepath.Join(root, dependabotFile)
	config, err := readYAML[dependabotConfig](path)
	if err != nil {
		t.Fatal(err)
	}
	config.Updates = config.Updates[:len(config.Updates)-1]
	writeYAML(t, path, config)
	assertValidationError(t, root, "missing Dependabot updater")

	root = fixtureRepository(t)
	config, err = readYAML[dependabotConfig](filepath.Join(root, dependabotFile))
	if err != nil {
		t.Fatal(err)
	}
	config.Updates[0].OpenPullRequestsLimit = intPointer(11)
	writeYAML(t, filepath.Join(root, dependabotFile), config)
	assertValidationError(t, root, "between 1 and 10")
}

func TestGitHubActionsRequireImmutableThirdPartyRefs(t *testing.T) {
	root := fixtureRepository(t)
	workflow := filepath.Join(root, ".github/workflows/test.yml")
	if err := os.WriteFile(workflow, []byte("steps:\n  - uses: actions/checkout@v4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertValidationError(t, root, "full commit SHA")

	if err := os.WriteFile(workflow, []byte("steps:\n  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n  - uses: ./.github/actions/local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepository(root, time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ValidateRepository() error = %v", err)
	}
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range []string{"go.mod", "Dockerfile", ".github/workflows/test.yml"} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		contents := "module example\n\ngo 1.25\n"
		if file == "Dockerfile" {
			contents = "FROM alpine:3.20\n"
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".security"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCoverage(t, root, fixtureCoverage())
	writeExceptions(t, root, Exceptions{Version: 1, Exceptions: []Exception{}})
	dependabot := `version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule: {interval: weekly}
    open-pull-requests-limit: 5
    groups: {go: {patterns: ["*"]}}
  - package-ecosystem: docker
    directory: /
    schedule: {interval: weekly}
    open-pull-requests-limit: 5
    groups: {docker: {patterns: ["*"]}}
  - package-ecosystem: github-actions
    directory: /
    schedule: {interval: weekly}
    open-pull-requests-limit: 5
    groups: {actions: {patterns: ["*"]}}
`
	if err := os.WriteFile(filepath.Join(root, dependabotFile), []byte(dependabot), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func fixtureCoverage() Coverage {
	return Coverage{Version: 1, Surfaces: []Surface{
		{Path: "go.mod", Kind: "go-module", Updater: Updater{Ecosystem: "gomod", Directory: "/"}, Scanners: []string{"osv-scanner"}},
		{Path: "Dockerfile", Kind: "dockerfile", Updater: Updater{Ecosystem: "docker", Directory: "/"}, Scanners: []string{"trivy"}, Images: []string{"alpine:3.20"}},
		{Path: ".github/workflows/test.yml", Kind: "github-actions", Updater: Updater{Ecosystem: "github-actions", Directory: "/"}, Scanners: []string{"actionlint"}},
	}}
}

func fixtureException() Exception {
	return Exception{ID: "scan-001", Scanner: "osv-scanner", Rule: "CVE-2026-0001", Resource: "example/module", Owner: "security@example.com", Rationale: "Upstream fix is queued.", Created: "2026-08-01", Expires: "2026-09-01"}
}

func mustReadCoverage(t *testing.T, root string) Coverage {
	t.Helper()
	coverage, err := readYAML[Coverage](filepath.Join(root, coverageFile))
	if err != nil {
		t.Fatal(err)
	}
	return coverage
}

func writeCoverage(t *testing.T, root string, coverage Coverage) {
	writeYAML(t, filepath.Join(root, coverageFile), coverage)
}
func writeExceptions(t *testing.T, root string, exceptions Exceptions) {
	writeYAML(t, filepath.Join(root, exceptionsFile), exceptions)
}

func writeYAML(t *testing.T, path string, value any) {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertValidationError(t *testing.T, root, want string) {
	t.Helper()
	err := ValidateRepository(root, time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ValidateRepository error = %v, want substring %q", err, want)
	}
}

func intPointer(value int) *int { return &value }
