package architecture

import (
	"strings"
	"testing"
)

func TestSavedExplorationSQLiteIsOnlyTheNarrowPersistenceAdapter(t *testing.T) {
	const sqlitePath = "internal/analytics/exploration/saved/sqlite"
	for _, path := range []string{sqlitePath, sqlitePath + "/repository"} {
		rule, ok := ClassifyPackage(path)
		if !ok {
			t.Fatalf("%s is not classified", path)
		}
		if rule.Prefix != sqlitePath || rule.Capability != "analytics" || rule.Layer != LayerAdapter {
			t.Fatalf("%s classification = %#v, want exact analytics adapter rule", path, rule)
		}
	}

	for _, path := range []string{
		"internal/analytics/exploration",
		"internal/analytics/exploration/saved",
		"internal/analytics/exploration/saved/application",
	} {
		rule, ok := ClassifyPackage(path)
		if !ok {
			t.Fatalf("%s is not classified", path)
		}
		if rule.Capability != "analytics" || rule.Layer != LayerContract {
			t.Fatalf("%s classification = %#v, want analytics contract", path, rule)
		}
	}
}

func TestAnalyticsMayImportOnlyThePublishedProjectRuntimeContract(t *testing.T) {
	const sourcePath = "internal/analytics/exploration/saved/application"
	source, ok := ClassifyPackage(sourcePath)
	if !ok {
		t.Fatalf("%s is not classified", sourcePath)
	}
	runtimePath := "internal/project/runtime"
	runtime, ok := ClassifyPackage(runtimePath)
	if !ok || runtime.Capability != "project" || runtime.Layer != LayerContract {
		t.Fatalf("%s classification = %#v, want project contract", runtimePath, runtime)
	}
	if !IsSharedContractImport("analytics", runtimePath) {
		t.Fatalf("%s is not published as an analytics shared contract", runtimePath)
	}
	if IsSharedContractImport("analytics", runtimePath+"/cache") {
		t.Fatalf("analytics runtime contract widened beyond the exact package")
	}
	if violation := CapabilityImportViolation(sourcePath, source, runtimePath, runtime); violation != "" {
		t.Fatalf("%s -> %s violation = %q, want allowed shared contract", sourcePath, runtimePath, violation)
	}

	compilerPath := "internal/project/compiler"
	compiler, ok := ClassifyPackage(compilerPath)
	if !ok {
		t.Fatalf("%s is not classified", compilerPath)
	}
	if violation := CapabilityImportViolation(sourcePath, source, compilerPath, compiler); !strings.Contains(violation, "undeclared capability edge") {
		t.Fatalf("%s -> %s violation = %q, want undeclared capability edge", sourcePath, compilerPath, violation)
	}
}
