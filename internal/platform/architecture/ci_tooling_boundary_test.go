package architecture

import (
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const (
	ciPlanningPackage   = modulePath + "/internal/platform/ci"
	ciAdapterPackage    = modulePath + "/internal/app/tools/ciadapter"
	ciAdapterPackageDir = "internal/app/tools/ciadapter"
	ciPlanCommand       = "internal/app/tools/ciplan"
	ciReportCommand     = "internal/app/tools/cireport"
)

var ciCommandPackages = map[string]struct{}{
	ciPlanCommand:   {},
	ciReportCommand: {},
}

// TestCIPlanningAndAdapterImportsStayAtTheToolBoundary keeps the CI planner
// and its wire compatibility adapter out of the application runtime. The
// production file inventory is the same one used by the other architecture
// checks, so adding a new importer cannot be hidden behind a package listing
// or a test-only fixture.
func TestCIPlanningAndAdapterImportsStayAtTheToolBoundary(t *testing.T) {
	files := productionGoFiles(t)
	seen := map[string]bool{}
	for _, file := range files {
		for _, imported := range file.imports {
			if imported != ciPlanningPackage && imported != ciAdapterPackage {
				continue
			}
			seen[imported] = true
		}
		for _, violation := range ciToolingImportViolations(file) {
			t.Errorf("%s", violation)
		}
	}
	for _, imported := range []string{ciPlanningPackage, ciAdapterPackage} {
		if !seen[imported] {
			t.Errorf("no production package imports CI boundary package %q", imported)
		}
	}

	for _, violation := range ciCommandPackageViolations(files) {
		t.Errorf("%s", violation)
	}
}

// TestCIImportBoundaryRejectsRuntimeFixtures makes the importer allowlist
// executable as a regression test. These fixtures represent the failure mode
// this test protects against: a runtime package taking a direct dependency on
// either CI package.
func TestCIImportBoundaryRejectsRuntimeFixtures(t *testing.T) {
	for _, imported := range []string{ciPlanningPackage, ciAdapterPackage} {
		t.Run(imported, func(t *testing.T) {
			file := goFile{
				path:    "internal/app/runtime/ci.go",
				pkgDir:  "internal/app/runtime",
				imports: []string{imported},
			}
			if violations := ciToolingImportViolations(file); len(violations) == 0 {
				t.Fatalf("runtime importer fixture for %q was accepted", imported)
			}
		})
	}
}

// TestCICommandPackageRejectsLibraryFixtures protects the second half of the
// boundary. A command directory must remain an executable package even when
// its helper imports CI tooling; otherwise a runtime package could import the
// command as an ordinary library and bypass the intended composition root.
func TestCICommandPackageRejectsLibraryFixtures(t *testing.T) {
	valid := []goFile{
		{path: ciPlanCommand + "/main.go", pkgDir: ciPlanCommand, body: "package main\n\nfunc main() {}\n"},
		{path: ciReportCommand + "/main.go", pkgDir: ciReportCommand, body: "package main\n\nfunc main() {}\n"},
	}
	if violations := ciCommandPackageViolations(valid); len(violations) != 0 {
		t.Fatalf("valid command fixture rejected: %v", violations)
	}

	for _, command := range []string{ciPlanCommand, ciReportCommand} {
		t.Run(command, func(t *testing.T) {
			files := append([]goFile(nil), valid...)
			for index := range files {
				if files[index].pkgDir == command {
					files[index].body = "package library\n\nfunc Helper() {}\n"
				}
			}
			violations := ciCommandPackageViolations(files)
			want := "CI command package " + command + "/main.go uses package library"
			for _, violation := range violations {
				if strings.Contains(violation, want) {
					return
				}
			}
			t.Fatalf("library command fixture for %q did not produce %q: %v", command, want, violations)
		})
	}
}

func ciToolingImportViolations(file goFile) []string {
	violations := []string{}
	for _, imported := range file.imports {
		switch imported {
		case ciPlanningPackage:
			if _, allowed := ciCommandPackages[file.pkgDir]; !allowed && file.pkgDir != ciAdapterPackageDir {
				violations = append(violations, fmt.Sprintf("%s imports CI planning package outside the tooling boundary (%s)", file.path, file.pkgDir))
			}
		case ciAdapterPackage:
			if _, allowed := ciCommandPackages[file.pkgDir]; !allowed {
				violations = append(violations, fmt.Sprintf("%s imports CI compatibility adapter outside the command boundary (%s)", file.path, file.pkgDir))
			}
		}
	}
	return violations
}

func ciCommandPackageViolations(files []goFile) []string {
	violations := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		if _, command := ciCommandPackages[file.pkgDir]; !command {
			continue
		}
		seen[file.pkgDir] = true
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			violations = append(violations, fmt.Sprintf("parse command file %s: %v", file.path, err))
			continue
		}
		if parsed.Name.Name != "main" {
			violations = append(violations, fmt.Sprintf("CI command package %s uses package %s; commands must remain package main", file.path, parsed.Name.Name))
		}
	}
	for command := range ciCommandPackages {
		if !seen[command] {
			violations = append(violations, fmt.Sprintf("CI command package %s has no production Go files", command))
		}
	}
	return violations
}
