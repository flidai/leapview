package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

func TestGenerationTargetsPartitionCapabilityRoots(t *testing.T) {
	root := repositoryRoot()
	doc, err := ir.Load(filepath.Join(root, "api/gen/ui-signals-ir.json"))
	if err != nil {
		t.Fatal(err)
	}

	wantRoots := map[string][]string{
		"access":    {"LoginPageEnvelope", "WorkspaceAccessSignal"},
		"admin":     {"AdminPageEnvelope", "AdminQueryHistoryCommand"},
		"agent":     {"AgentContextSignal", "ChatEnvelope"},
		"dashboard": {"DashboardEnvelope", "DashboardVisualizationSignal"},
	}
	for _, target := range generationTargets {
		contracts := contractsForTarget(doc, target)
		names := make(map[string]bool, len(contracts))
		for _, contract := range contracts {
			names[contract.Name] = true
		}
		for _, name := range wantRoots[target.name] {
			if !names[name] {
				t.Errorf("%s target is missing %s", target.name, name)
			}
		}
	}
}

func TestGeneratedOutputsContainOnlyNeededCapabilityModels(t *testing.T) {
	root := repositoryRoot()
	doc, err := ir.Load(filepath.Join(root, "api/gen/ui-signals-ir.json"))
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := generatedOutputs(root, doc)
	if err != nil {
		t.Fatal(err)
	}

	assertContains := func(path, model string) {
		t.Helper()
		source := string(outputs[filepath.Join(root, path)])
		if !strings.Contains(source, "type "+model+" ") {
			t.Errorf("%s is missing %s", path, model)
		}
	}
	assertOmits := func(path, model string) {
		t.Helper()
		source := string(outputs[filepath.Join(root, path)])
		if strings.Contains(source, "type "+model+" ") {
			t.Errorf("%s unexpectedly contains %s", path, model)
		}
	}

	assertContains("internal/access/ui/signals/models.gen.go", "LoginPageSignal")
	assertOmits("internal/access/ui/signals/models.gen.go", "AdminPageSignal")
	assertContains("internal/admin/ui/signals/models.gen.go", "AdminPageSignal")
	assertOmits("internal/admin/ui/signals/models.gen.go", "WorkspacePageSignal")
	assertContains("internal/agent/ui/signals/models.gen.go", "ChatSignal")
	assertOmits("internal/agent/ui/signals/models.gen.go", "AdminPageSignal")
	assertContains("internal/dashboard/ui/signals/models.gen.go", "DashboardPageSignal")
	assertOmits("internal/dashboard/ui/signals/models.gen.go", "AdminPageSignal")
}
